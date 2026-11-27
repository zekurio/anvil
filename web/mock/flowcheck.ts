// What POST /flows/check and POST /flows/test compute: node summaries,
// validation issues, and a dry run against one file. The real daemon is the
// authority; this is plausible enough to build the editor against.

import type { Field, FileFacts, FlowCheck, FlowTest, Graph, GraphNode, Issue, NodeType, OptionValue, Outcome } from "../src/api/types.ts";
import { NODE_TYPE_BY_ID } from "./fixtures/catalog.ts";

const DOT = " · ";

function fields(type: NodeType): Field[] {
  return type.sections.flatMap((s) => s.fields);
}

/** The effective value of an option: the node's, or the field's default. */
function option(node: GraphNode, type: NodeType, key: string): OptionValue {
  const own = node.options?.[key];
  if (own !== undefined) return own;
  return fields(type).find((f) => f.key === key)?.default ?? null;
}

function reader(node: GraphNode, type: NodeType) {
  return {
    str: (key: string) => String(option(node, type, key) ?? ""),
    num: (key: string) => Number(option(node, type, key) ?? 0),
    bool: (key: string) => option(node, type, key) === true,
    list: (key: string) => {
      const value = option(node, type, key);
      return Array.isArray(value) ? value : [];
    },
  };
}

function title(node: GraphNode, type: NodeType): string {
  return node.label || type.label;
}

const sign = (op: string) => (op === "gt" ? ">" : "<");

function summarize(node: GraphNode, type: NodeType): string {
  const o = reader(node, type);
  const join = (parts: (string | false)[]) => parts.filter(Boolean).join(DOT);
  switch (type.id) {
    case "input":
      return o.list("extensions").map((e) => `*.${e}`).join(", ") || "every file";
    case "check.video_codec":
      return `video codec in ${o.list("codecs").join(", ") || "nothing"}`;
    case "check.resolution":
      return `height ${sign(o.str("op"))} ${o.num("height")} px`;
    case "check.hdr":
      return join([`hdr in ${o.list("formats").join(", ") || "nothing"}`, o.list("formats").includes("dolby_vision") && o.list("dv_profiles").length > 0 && `profile ${o.list("dv_profiles").join(", ")}`]);
    case "check.bitrate":
      return `bitrate ${sign(o.str("op"))} ${o.num("kbps")} kb/s`;
    case "check.file_size":
      return `size ${sign(o.str("op"))} ${o.num("gigabytes")} GB`;
    case "check.audio_language":
      return `audio has ${o.list("languages").join(", ") || "nothing"}`;
    case "video.encode": {
      const quality = { search: `vmaf ${o.num("target_vmaf")}`, crf: `crf ${o.num("crf")}`, bitrate: `${o.num("bitrate")} kb/s` }[o.str("rate_control")];
      return join([o.str("encoder"), quality ?? "", `preset ${o.num("preset")}`]);
    }
    case "video.copy":
      return "copy";
    case "video.crop":
      return o.str("mode") === "manual" ? o.str("filter") || "no filter set" : join(["detect", `keep ≥ ${o.num("min_retained_percent")}%`]);
    case "video.scale":
      return join([`≤ ${o.num("height")} px`, o.str("algorithm")]);
    case "audio.keep":
      return join([o.list("languages").join(", ") || "no language", o.bool("keep_original") && "original", o.bool("drop_commentary") && "no commentary"]);
    case "audio.encode":
      return join([`${o.list("from_codecs").join(", ") || "nothing"} → ${o.str("codec")}`, `${o.num("bitrate_per_channel")} kb/s per channel`]);
    case "audio.downmix":
      return join([`${o.str("mode")} ${o.str("codec")} 2.0`, `${o.num("bitrate")} kb/s`]);
    case "subtitles.keep":
      return join([o.list("languages").join(", ") || "no language", o.bool("keep_forced") && "forced", o.bool("drop_sdh") && "no sdh"]);
    case "subtitles.drop_bitmap":
      return `drop ${o.list("codecs").join(", ") || "nothing"}`;
    case "subtitles.remove_all":
      return "drop all";
    case "container.metadata":
      return join([o.bool("strip_title") && "title", o.bool("strip_chapters") && "chapters", o.bool("strip_attachments") && "attachments", o.list("tags").length > 0 && `${o.list("tags").length} tags`]) || "nothing removed";
    case "container.remux":
      return join([o.str("container"), o.str("container") === "mp4" && o.bool("faststart") && "faststart"]);
    case "output.publish":
      return o.bool("only_if_smaller") ? join(["only if smaller", `save ≥ ${o.num("min_saving_percent")}%`]) : "always publish";
    case "output.skip":
      return o.str("reason") || "leave untouched";
    default:
      return "";
  }
}

function visible(field: Field, node: GraphNode, type: NodeType): boolean {
  const when = field.visible_when;
  if (!when) return true;
  const current = option(node, type, when.key);
  return when.in.some((allowed) => (Array.isArray(current) ? current.includes(String(allowed)) : current === allowed));
}

function optionIssues(node: GraphNode, type: NodeType): Issue[] {
  const issues: Issue[] = [];
  for (const field of fields(type)) {
    if (!visible(field, node, type)) continue;
    const value = option(node, type, field.key);
    if (field.type !== "int" && field.type !== "float") continue;
    const error = (message: string) => issues.push({ severity: "error", node_id: node.id, field: field.key, message });
    if (typeof value !== "number" || Number.isNaN(value)) {
      error(`${field.label} must be a number.`);
      continue;
    }
    if (field.type === "int" && !Number.isInteger(value)) error(`${field.label} must be a whole number.`);
    if (field.min !== undefined && field.max !== undefined && (value < field.min || value > field.max)) {
      error(`${field.label} must be between ${field.min} and ${field.max}.`);
    } else if (field.min !== undefined && value < field.min) {
      error(`${field.label} must be ${field.min} or more.`);
    } else if (field.max !== undefined && value > field.max) {
      error(`${field.label} must be ${field.max} or less.`);
    }
  }
  return issues;
}

export function checkGraph(graph: Graph): FlowCheck {
  const issues: Issue[] = [];
  const nodes: FlowCheck["nodes"] = {};
  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  const error = (message: string, node_id?: string) => issues.push({ severity: "error", ...(node_id ? { node_id } : {}), message });

  const inputs = graph.nodes.filter((n) => n.type === "input");
  if (inputs.length === 0) error("The flow has no Input file node. Add one from the palette.");
  for (const extra of inputs.slice(1)) error("A flow has exactly one Input file node. Remove this one.", extra.id);

  for (const e of graph.edges) {
    if (!byId.has(e.from) || !byId.has(e.to)) error("An edge points at a node that no longer exists.");
  }

  // Everything the input can reach; only those nodes must be fully wired.
  const reachable = new Set<string>();
  const queue = inputs.slice(0, 1).map((n) => n.id);
  for (let id = queue.shift(); id !== undefined; id = queue.shift()) {
    if (reachable.has(id)) continue;
    reachable.add(id);
    for (const e of graph.edges) if (e.from === id) queue.push(e.to);
  }

  for (const node of graph.nodes) {
    const type = NODE_TYPE_BY_ID.get(node.type);
    if (!type) {
      error(`"${node.type}" is not a node type this daemon knows.`, node.id);
      nodes[node.id] = { summary: "unknown node type", invalid: true };
      continue;
    }
    if (!reachable.has(node.id)) {
      issues.push({ severity: "warning", node_id: node.id, message: `"${title(node, type)}" is not connected to the input and never runs.` });
    } else {
      type.outputs.forEach((label, port) => {
        if (graph.edges.some((e) => e.from === node.id && e.port === port)) return;
        error(label ? `Output "${label}" of "${title(node, type)}" is not connected.` : `"${title(node, type)}" is not connected to a next step.`, node.id);
      });
    }
    issues.push(...optionIssues(node, type));
    if (type.id === "audio.keep" && reader(node, type).list("languages").length === 0 && !reader(node, type).bool("keep_original")) {
      issues.push({ severity: "warning", node_id: node.id, field: "languages", message: "No language is listed, so the fallback decides every file." });
    }
    nodes[node.id] = { summary: summarize(node, type), invalid: false };
  }

  for (const issue of issues) {
    const entry = issue.node_id ? nodes[issue.node_id] : undefined;
    if (!entry || issue.severity !== "error") continue;
    entry.invalid = true;
    if (!issue.field) entry.summary = issue.message;
  }
  return { valid: !issues.some((i) => i.severity === "error"), issues, nodes };
}

function decide(node: GraphNode, type: NodeType, file: FileFacts): { result: boolean; why: string } {
  const o = reader(node, type);
  const compare = (value: number, op: string, limit: number) => (op === "gt" ? value > limit : value < limit);
  switch (type.id) {
    case "check.video_codec":
      return { result: o.list("codecs").includes(file.video_codec), why: `video codec is ${file.video_codec}` };
    case "check.resolution":
      return { result: compare(file.height, o.str("op"), o.num("height")), why: `height is ${file.height} px` };
    case "check.hdr":
      return { result: file.hdr !== "" && o.list("formats").includes(file.hdr), why: file.hdr ? `hdr is ${file.hdr}` : "the video is sdr" };
    case "check.bitrate":
      return { result: compare(file.bitrate_kbps, o.str("op"), o.num("kbps")), why: `bitrate is ${file.bitrate_kbps} kb/s` };
    case "check.file_size":
      return { result: compare(file.size_bytes / 1e9, o.str("op"), o.num("gigabytes")), why: `size is ${(file.size_bytes / 1e9).toFixed(1)} GB` };
    default: {
      const wanted = o.list("languages");
      const found = file.audio.map((a) => a.language);
      return { result: wanted.some((l) => found.includes(l)), why: `audio languages are ${found.join(", ") || "unknown"}` };
    }
  }
}

function planAndCommand(path: { node: GraphNode; type: NodeType }[], file: FileFacts): { plan: string[]; command: string[] } {
  const plan: string[] = [];
  const video: string[] = ["-map", "0:v:0"];
  const audio: string[] = ["-map", "0:a", "-c:a", "copy"];
  const subtitles: string[] = ["-map", "0:s?", "-c:s", "copy"];
  const filters: string[] = [];
  let encoded = false;

  for (const { node, type } of path) {
    const o = reader(node, type);
    switch (type.id) {
      case "video.copy":
        plan.push("Video: copy the stream unchanged");
        video.push("-c:v", "copy");
        encoded = true;
        break;
      case "video.crop":
        plan.push(o.str("mode") === "manual" ? `Video: crop with ${o.str("filter")}` : "Video: crop the black bars crop detection found");
        filters.push(o.str("mode") === "manual" ? o.str("filter") : "crop={crop}");
        break;
      case "video.scale":
        if (file.height > o.num("height")) {
          plan.push(`Video: scale from ${file.height} px to ${o.num("height")} px`);
          filters.push(`scale=-2:${o.num("height")}:flags=${o.str("algorithm")}`);
        }
        break;
      case "video.encode": {
        const mode = o.str("rate_control");
        plan.push(`Video: encode ${file.video_codec} to ${o.str("codec")} with ${o.str("encoder")}, ${mode === "search" ? `target VMAF ${o.num("target_vmaf")}` : mode === "crf" ? `CRF ${o.num("crf")}` : `${o.num("bitrate")} kb/s`}`);
        video.push("-c:v", o.str("encoder"), "-preset", String(o.num("preset")));
        if (mode === "bitrate") video.push("-b:v", `${o.num("bitrate")}k`);
        else video.push("-crf", mode === "search" ? "{crf}" : String(o.num("crf")));
        video.push("-pix_fmt", o.str("pix_fmt"), ...o.list("extra_args").flatMap((a) => a.split(" ")));
        encoded = true;
        break;
      }
      case "audio.keep":
        plan.push(`Audio: keep ${o.list("languages").join(", ") || "no language"}${o.bool("keep_original") ? " and the original language" : ""}`);
        break;
      case "audio.encode": {
        const hit = file.audio.filter((a) => o.list("from_codecs").includes(a.codec));
        if (hit.length === 0) break;
        plan.push(`Audio: encode ${hit.map((a) => a.codec).join(", ")} to ${o.str("codec")} at ${o.num("bitrate_per_channel")} kb/s per channel`);
        audio.splice(2, 2, "-c:a", o.str("codec"), "-b:a", `${o.num("bitrate_per_channel") * (hit[0]?.channels ?? 2)}k`);
        break;
      }
      case "audio.downmix":
        plan.push(`Audio: ${o.str("mode")} a stereo ${o.str("codec")} track at ${o.num("bitrate")} kb/s`);
        audio.push("-ac", "2");
        break;
      case "subtitles.keep":
        plan.push(`Subtitles: keep ${o.list("languages").join(", ") || "no language"}`);
        break;
      case "subtitles.drop_bitmap":
        plan.push(`Subtitles: drop ${o.list("codecs").join(", ")}`);
        break;
      case "subtitles.remove_all":
        plan.push("Subtitles: drop every track");
        subtitles.length = 0;
        subtitles.push("-sn");
        break;
      case "container.metadata":
        plan.push("Container: remove the title and the listed tags");
        break;
      case "container.remux":
        plan.push(`Container: remux to ${o.str("container")}`);
        break;
      case "output.publish":
        plan.push(o.bool("only_if_smaller") ? "Output: publish only if the encode is smaller than the source" : "Output: publish the result");
        break;
    }
  }
  if (!encoded) video.push("-c:v", "copy");
  if (filters.length > 0) video.push("-vf", filters.join(","));
  return { plan, command: ["-i", "{input}", ...video, ...audio, ...subtitles, "{output}"] };
}

export function testGraph(graph: Graph, file: FileFacts): FlowTest {
  const check = checkGraph(graph);
  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  const walked: { node: GraphNode; type: NodeType }[] = [];
  const decisions: string[] = [];
  let outcome: Outcome = "skip";

  let current = graph.nodes.find((n) => n.type === "input");
  while (current && walked.length <= graph.nodes.length) {
    const type = NODE_TYPE_BY_ID.get(current.type);
    if (!type) break;
    walked.push({ node: current, type });
    let port = 0;
    if (type.kind === "condition") {
      const { result, why } = decide(current, type, file);
      decisions.push(`${title(current, type)} -> ${result} (${why})`);
      port = result ? 0 : 1;
    }
    if (type.id === "output.publish") outcome = "process";
    const from = current.id;
    const next = graph.edges.find((e) => e.from === from && e.port === port);
    current = next ? byId.get(next.to) : undefined;
  }

  const { plan, command } = planAndCommand(walked, file);
  return {
    check,
    outcome,
    path: walked.map((w) => w.node.id),
    decisions,
    file,
    plan: outcome === "skip" ? ["The file is left untouched."] : plan,
    command: outcome === "skip" ? [] : command,
  };
}
