// What POST /flows/check and POST /flows/test compute: a port of
// pkg/flow/validate.go, options.go and evaluate.go, with the same issue
// order and messages. The daemon is the authority; the ffmpeg command of a
// test is only shaped like the real one.

import type { Field, FileFacts, FlowCheck, FlowTest, Graph, GraphNode, Issue, NodeType, OptionValue, Outcome } from "../src/api/types.ts";
import { NODE_TYPE_BY_ID, RESOLUTION_TIERS } from "./fixtures/catalog.ts";

const DOT = " · ";

/* ---------- options (pkg/flow/options.go) ---------- */

interface Problem {
  field: string;
  message: string;
}

/** A node's settings after decoding: every field of the type, defaults filled in. Durations are seconds. */
class Options {
  readonly values = new Map<string, OptionValue>();
  text(key: string): string {
    const value = this.values.get(key);
    return typeof value === "string" ? value : "";
  }
  number(key: string): number {
    const value = this.values.get(key);
    return typeof value === "number" ? value : 0;
  }
  on(key: string): boolean {
    return this.values.get(key) === true;
  }
  list(key: string): string[] {
    const value = this.values.get(key);
    return Array.isArray(value) ? value : [];
  }
}

const UNITS: Record<string, number> = { ns: 1e-9, us: 1e-6, "µs": 1e-6, "μs": 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 };

/** time.ParseDuration, in seconds; undefined when the text is not a Go duration. */
export function parseDuration(text: string): number | undefined {
  let rest = text.trim();
  let sign = 1;
  if (rest.startsWith("-") || rest.startsWith("+")) {
    if (rest.startsWith("-")) sign = -1;
    rest = rest.slice(1);
  }
  if (rest === "0") return 0;
  if (rest === "") return undefined;
  let total = 0;
  while (rest !== "") {
    const part = /^(\d*\.?\d*)(ns|us|µs|μs|ms|s|m|h)/.exec(rest);
    if (!part || part[1] === "" || part[1] === ".") return undefined;
    total += Number(part[1]) * (UNITS[part[2] ?? ""] ?? 0);
    rest = rest.slice(part[0].length);
  }
  return sign * total;
}

const formatNumber = (value: number) => String(value);

function rangeMessage(field: Field, value: number): string {
  const below = field.min !== undefined && (value < field.min || (field.min_exclusive === true && value === field.min));
  const above = field.max !== undefined && value > field.max;
  if (!below && !above) return "";
  if (field.min_exclusive && field.max !== undefined) return `Must be greater than ${formatNumber(field.min ?? 0)} and at most ${formatNumber(field.max)}.`;
  if (field.min_exclusive) return `Must be greater than ${formatNumber(field.min ?? 0)}.`;
  if (field.min !== undefined && field.max !== undefined) return `Must be between ${formatNumber(field.min)} and ${formatNumber(field.max)}.`;
  if (field.min !== undefined) return `Must be at least ${formatNumber(field.min)}.`;
  return `Must be at most ${formatNumber(field.max ?? 0)}.`;
}

// An enum's message lists what its control shows: the labels.
const choiceLabels = (field: Field) => (field.choices ?? []).map((c) => c.label).join(", ");
const hasChoice = (field: Field, value: string) => (field.choices ?? []).some((c) => c.value === value);
const isStringList = (value: unknown): value is string[] => Array.isArray(value) && value.every((entry) => typeof entry === "string");

function decodeValue(field: Field, value: unknown): { value?: OptionValue; message?: string } {
  switch (field.type) {
    case "string":
      return typeof value === "string" ? { value } : { message: "Must be text." };
    case "bool":
      return typeof value === "boolean" ? { value } : { message: "Must be true or false." };
    case "int": {
      if (typeof value !== "number" || !Number.isFinite(value) || !Number.isInteger(value)) return { message: "Must be a whole number." };
      const message = rangeMessage(field, value);
      return message ? { message } : { value };
    }
    case "float": {
      if (typeof value !== "number" || !Number.isFinite(value)) return { message: "Must be a number." };
      const message = rangeMessage(field, value);
      return message ? { message } : { value };
    }
    case "duration": {
      const seconds = typeof value === "string" ? parseDuration(value) : undefined;
      return seconds === undefined ? { message: 'Must be a duration such as "20s".' } : { value: seconds };
    }
    case "enum":
      return typeof value === "string" && hasChoice(field, value) ? { value } : { message: `Must be one of: ${choiceLabels(field)}.` };
    case "enum_list": {
      if (!isStringList(value) || value.some((entry) => !hasChoice(field, entry))) return { message: `Must be a list of: ${choiceLabels(field)}.` };
      // A set: catalog order and no repeats.
      return { value: (field.choices ?? []).map((c) => c.value).filter((v) => value.includes(v)) };
    }
    case "string_list":
      return isStringList(value) ? { value: [...value] } : { message: "Must be a list of text values." };
    default:
      return { message: "Must be a value this field supports." };
  }
}

function visible(field: Field, decoded: Options): boolean {
  const when = field.visible_when;
  if (!when) return true;
  return when.in.includes(decoded.values.get(when.key) ?? null);
}

function decodeOptions(type: NodeType, raw: Record<string, OptionValue> = {}): { options: Options; problems: Problem[] } {
  const options = new Options();
  const problems: Problem[] = [];
  const flagged = new Set<string>();
  for (const field of type.sections.flatMap((s) => s.fields)) {
    options.values.set(field.key, decodeValue(field, field.default).value ?? null);
    if (!(field.key in raw) || !visible(field, options)) continue;
    const decoded = decodeValue(field, raw[field.key]);
    if (decoded.message) {
      problems.push({ field: field.key, message: decoded.message });
      flagged.add(field.key);
      continue;
    }
    options.values.set(field.key, decoded.value ?? null);
  }
  for (const problem of CHECKS[type.id]?.(options) ?? []) {
    if (!flagged.has(problem.field)) problems.push(problem);
  }
  for (const key of Object.keys(raw).sort()) {
    if (!options.values.has(key)) problems.push({ field: key, message: `"${key}" is not an option of this node. Remove it.` });
  }
  return { options, problems };
}

/* ---------- per-type rules (pkg/flow/conditions.go, actions.go, pkg/config/validate.go) ---------- */

const requireEntries = (o: Options, key: string, message: string): Problem[] => (o.list(key).length > 0 ? [] : [{ field: key, message }]);
const blankEntries = (o: Options, key: string): Problem[] => (o.list(key).some((entry) => entry.trim() === "") ? [{ field: key, message: "Entries must not be empty. Remove the blank one." }] : []);
const cleanList = (entries: string[]) => entries.map((entry) => entry.trim().toLowerCase()).filter(Boolean);
const listOr = (entries: string[], fallback: string) => (entries.length === 0 ? fallback : entries.join(", "));

const MANAGED_ARGS = new Set(
  "i f y n stdin nostdin vf af lavfi s video_size r vsync fps_mode t to ss sseof fs frames vframes stream_loop shortest copyts start_at_zero itsoffset itsscale c codec vcodec an vn sn dn crf qp q qscale global_quality rc qp_i qp_p qp_b b vb bit_rate pix_fmt pixel_format threads preset pass passlogfile progress stats_period".split(
    " ",
  ),
);

function encoderArgsRule(args: string[]): string {
  for (let i = 0; i < args.length; i += 2) {
    const key = args[i] ?? "";
    if (!key.startsWith("-") || key === "-" || i + 1 === args.length) return `Must contain encoder option/value pairs; invalid option "${key}".`;
    const name = key.replace(/^-+/, "").split(":")[0] ?? "";
    if (name.startsWith("filter") || name.startsWith("map") || MANAGED_ARGS.has(name)) return `Option "${key}" is managed by Anvil and cannot be used during CRF search.`;
  }
  return "";
}

const CHECKS: Record<string, (o: Options) => Problem[]> = {
  "check.video_codec": (o) => requireEntries(o, "codecs", "Choose at least one codec."),
  "check.hdr": (o) => requireEntries(o, "kinds", "Choose at least one HDR format."),
  "check.audio_language": (o) => {
    const missing = requireEntries(o, "languages", "Add at least one language.");
    return missing.length > 0 ? missing : blankEntries(o, "languages");
  },
  "check.audio_codec": (o) => {
    const missing = requireEntries(o, "codecs", "Add at least one codec.");
    return missing.length > 0 ? missing : blankEntries(o, "codecs");
  },
  "video.encode": (o) => {
    const problems: Problem[] = [];
    if (o.number("crf_min") > o.number("crf_max")) problems.push({ field: "crf_min", message: "Must be less than or equal to Highest CRF." });
    if (o.number("sample_duration") <= 0) problems.push({ field: "sample_duration", message: "Must be positive." });
    const args = encoderArgsRule(o.list("ffmpeg_args"));
    if (args) problems.push({ field: "ffmpeg_args", message: args });
    if (o.text("metric") === "xpsnr" && o.number("target") === 0) problems.push({ field: "target", message: 'Must be set when metric is "xpsnr" (typical targets are 35-50).' });
    return problems;
  },
  "video.crop": (o) => {
    const problems: Problem[] = [];
    const offsets = o.list("seek_offsets").map((entry) => ({ entry, seconds: parseDuration(entry) }));
    const bad = offsets.find((offset) => offset.seconds === undefined);
    if (bad) problems.push({ field: "seek_offsets", message: `Entries must be durations such as 5m or 1h10m. "${bad.entry}" is not one.` });
    if (o.number("samples") === 1) problems.push({ field: "samples", message: "Must be 0 (automatic) or at least 2." });
    if (offsets.some((offset) => (offset.seconds ?? 0) < 0)) problems.push({ field: "seek_offsets", message: "Must not contain negative durations." });
    return problems;
  },
  "audio.keep": (o) => blankEntries(o, "languages"),
  "audio.encode": (o) => blankEntries(o, "source_codecs"),
  "subtitles.keep": (o) => blankEntries(o, "languages"),
};

const ENCODERS: Record<string, Record<string, string>> = {
  software: { av1: "libsvtav1", hevc: "libx265", h264: "libx264" },
  qsv: { av1: "av1_qsv", hevc: "hevc_qsv", h264: "h264_qsv" },
  vaapi: { av1: "av1_vaapi", hevc: "hevc_vaapi", h264: "h264_vaapi" },
  amf: { av1: "av1_amf", hevc: "hevc_amf", h264: "h264_amf" },
};
const encoderOf = (o: Options) => ENCODERS[o.text("accelerator")]?.[o.text("codec")] ?? "libsvtav1";
const layoutName = (channels: string) => (channels === "2" ? "stereo" : "5.1");
const audioBitrate = (o: Options) => (o.text("codec") === "flac" ? 0 : o.number("bitrate_kbps_per_channel"));

const FALLBACK_WORDS: Record<string, string> = { keep_first: "else the first track", fail_job: "else fail the job" };

function keepSummary(o: Options): string {
  const languages = cleanList(o.list("languages"));
  return languages.length === 0 ? "every language" : `${languages.join(", ")}${DOT}${FALLBACK_WORDS[o.text("fallback")] ?? "else every track"}`;
}

const SUMMARIES: Record<string, (o: Options) => string> = {
  input: () => "every queued file",
  "check.video_codec": (o) => `video.codec in ${listOr(o.list("codecs"), "nothing")}`,
  "check.resolution": (o) => `${o.text("op").replaceAll("_", " ")} ${o.text("resolution")}`,
  "check.hdr": (o) => `video.hdr in ${listOr(o.list("kinds"), "nothing")}`,
  "check.bitrate": (o) => `${o.text("op")} ${o.number("kbps")} kb/s`,
  "check.file_size": (o) => `${o.text("op")} ${o.number("megabytes")} MiB`,
  "check.audio_language": (o) => `audio.language in ${listOr(cleanList(o.list("languages")), "nothing")}`,
  "check.audio_codec": (o) => `audio.codec in ${listOr(cleanList(o.list("codecs")), "nothing")}`,
  "check.audio_channels": (o) => `audio.channels above ${o.text("more_than")}`,
  "video.encode": (o) => {
    const parts = [encoderOf(o), o.text("quality_mode") === "fixed" ? `crf ${o.number("crf")}` : `${o.text("metric")} ${formatNumber(o.number("target"))}`];
    if (o.text("preset").trim() !== "") parts.push(`preset ${o.text("preset").trim()}`);
    return parts.join(DOT);
  },
  "video.copy": () => "keep the source video stream",
  "video.crop": (o) => `keep at least ${formatNumber(o.number("min_retained_area_percent"))}%${DOT}limit ${o.number("limit")}`,
  "audio.keep": keepSummary,
  "audio.encode": (o) => {
    const summary = `${listOr(cleanList(o.list("source_codecs")), "every track")} → ${o.text("codec")}`;
    if (o.text("codec") === "flac") return summary;
    return audioBitrate(o) === 0 ? `${summary}${DOT}default bitrate` : `${summary}${DOT}${audioBitrate(o)} kb/s per channel`;
  },
  "audio.downmix": (o) => `above ${layoutName(o.text("max_channels"))} → ${layoutName(o.text("max_channels"))}${DOT}${o.text("codec")}`,
  "subtitles.keep": keepSummary,
  "subtitles.drop_bitmap": () => "hdmv_pgs_subtitle, dvd_subtitle, dvb_subtitle",
  "subtitles.remove_all": () => "every subtitle track",
  "container.metadata": (o) => {
    const titles: Record<string, string> = { preserve: "keep titles", strip: "strip titles", standardize: "standardize titles" };
    const parts = [o.text("metadata") === "strip" ? "strip metadata" : "keep metadata", titles[o.text("track_titles")] ?? ""];
    for (const key of ["attachments", "chapters"]) if (o.text(key) === "strip") parts.push(`no ${key}`);
    return parts.join(DOT);
  },
  "output.publish": () => "replace in place, or hand off",
  "output.skip": () => "leave the file untouched",
};

/* ---------- analysis (pkg/flow/validate.go) ---------- */

interface Analysis {
  issues: Issue[];
  /** The first node of every non-empty id. */
  nodes: Map<string, GraphNode>;
  options: Map<string, Options>;
  /** Per node, the node each output port leads to; "" marks an unconnected port. */
  targets: Map<string, string[]>;
  input: string;
}

const quote = (text: string) => JSON.stringify(text);

function analyze(graph: Graph): Analysis {
  const a: Analysis = { issues: [], nodes: new Map(), options: new Map(), targets: new Map(), input: "" };
  const error = (nodeId: string, field: string, message: string) => a.issues.push({ severity: "error", ...(nodeId ? { node_id: nodeId } : {}), ...(field ? { field } : {}), message });
  const warn = (nodeId: string, message: string) => a.issues.push({ severity: "warning", node_id: nodeId, message });
  const label = (id: string) => {
    const node = a.nodes.get(id);
    return node?.label || NODE_TYPE_BY_ID.get(node?.type ?? "")?.label || id;
  };
  const name = (id: string) => quote(label(id));
  const port = (id: string, index: number) => {
    const output = NODE_TYPE_BY_ID.get(a.nodes.get(id)?.type ?? "")?.outputs[index] ?? "";
    return output === "" ? `Output of ${name(id)}` : `${quote(output)} output of ${name(id)}`;
  };

  // Nodes and their options, as the graph lists them.
  const inputs: string[] = [];
  for (const node of graph.nodes) {
    if (!node.id) {
      error("", "", "Every node needs an ID.");
      continue;
    }
    if (a.nodes.has(node.id)) {
      error(node.id, "", `Node ID ${quote(node.id)} is used more than once. Every node needs its own ID.`);
      continue;
    }
    a.nodes.set(node.id, node);
    const type = NODE_TYPE_BY_ID.get(node.type);
    if (!type) {
      error(node.id, "", `${quote(node.type)} is not a node type Anvil knows. Replace this node.`);
      continue;
    }
    const { options, problems } = decodeOptions(type, node.options);
    a.options.set(node.id, options);
    a.targets.set(
      node.id,
      type.outputs.map(() => ""),
    );
    for (const problem of problems) error(node.id, problem.field, problem.message);
    if (type.kind === "input") inputs.push(node.id);
  }
  if (inputs.length === 0) error("", "", "A flow needs an input node. Add one.");
  else {
    a.input = inputs[0] ?? "";
    for (const extra of inputs.slice(1)) error(extra, "", "A flow has exactly one input node. Remove this one.");
  }

  // Edges.
  for (const edge of graph.edges) {
    const from = a.nodes.get(edge.from);
    if (!from) {
      error("", "", `An edge starts at ${quote(edge.from)}, which is not a node of this flow. Remove the edge.`);
      continue;
    }
    const target = a.nodes.get(edge.to);
    if (!target) {
      error(edge.from, "", `An edge from ${name(edge.from)} leads to ${quote(edge.to)}, which is not a node of this flow. Remove the edge.`);
      continue;
    }
    if (!NODE_TYPE_BY_ID.has(from.type)) {
      a.targets.set(edge.from, [...(a.targets.get(edge.from) ?? []), edge.to]);
      continue;
    }
    const ports = a.targets.get(edge.from) ?? [];
    if (ports.length === 0) error(edge.from, "", `${name(edge.from)} ends the flow and has no outputs. Remove the edge that leaves it.`);
    else if (edge.port < 0 || edge.port >= ports.length) error(edge.from, "", `${name(edge.from)} has outputs 0 to ${ports.length - 1}, and an edge uses output ${edge.port}. Remove the edge.`);
    else if (NODE_TYPE_BY_ID.get(target.type)?.kind === "input") error(edge.to, "", `The input node starts the flow, so no edge can lead into it. Remove the edge from ${name(edge.from)}.`);
    else if (ports[edge.port] !== "") error(edge.from, "", `${port(edge.from, edge.port)} has more than one edge. An output leads to exactly one node.`);
    else ports[edge.port] = edge.to;
  }

  // Loops.
  const state = new Map<string, 1 | 2>();
  const reported = new Set<string>();
  const visit = (id: string) => {
    state.set(id, 1);
    for (const target of a.targets.get(id) ?? []) {
      if (target === "") continue;
      if (state.get(target) === 1 && !reported.has(target)) {
        reported.add(target);
        error(target, "", `The flow loops back to ${name(target)}. Every path has to end at an output without repeating a node.`);
      } else if (!state.has(target)) visit(target);
    }
    state.set(id, 2);
  };
  for (const node of graph.nodes) if (a.nodes.get(node.id) === node && !state.has(node.id)) visit(node.id);

  // Unconnected outputs on the way from the input.
  const reached = new Set<string>();
  if (a.input) {
    reached.add(a.input);
    const queue = [a.input];
    for (let id = queue.shift(); id !== undefined; id = queue.shift()) {
      (a.targets.get(id) ?? []).forEach((target, index) => {
        if (target === "") error(id as string, "", `${port(id as string, index)} is not connected.`);
        else if (!reached.has(target)) {
          reached.add(target);
          queue.push(target);
        }
      });
    }
    for (const node of graph.nodes) {
      if (a.nodes.get(node.id) !== node || reached.has(node.id)) continue;
      warn(node.id, `${name(node.id)} is not connected to the input, so no file reaches it.`);
    }
  }

  // Crop nodes on a path that ends up copying the video.
  if (!a.issues.some((issue) => issue.severity === "error")) {
    const useless = new Set<string>();
    const seen = new Set<string>();
    const stack = [{ node: a.input, encodes: false, crop: "" }];
    for (let current = stack.pop(); current; current = stack.pop()) {
      const key = `${current.node}|${current.encodes}|${current.crop}`;
      if (seen.has(key)) continue;
      seen.add(key);
      const type = a.nodes.get(current.node)?.type;
      if (type === "video.encode") current.encodes = true;
      else if (type === "video.copy") current.encodes = false;
      else if (type === "video.crop") current.crop = current.node;
      else if (type === "output.publish" && !current.encodes && current.crop) useless.add(current.crop);
      for (const target of a.targets.get(current.node) ?? []) stack.push({ node: target, encodes: current.encodes, crop: current.crop });
    }
    for (const node of graph.nodes) {
      if (useless.delete(node.id)) warn(node.id, `Cropping needs a re-encode, but a path through ${name(node.id)} copies the video, so nothing is cropped there.`);
    }
  }
  return a;
}

export function checkGraph(graph: Graph): FlowCheck {
  const { issues } = analyze(graph);
  const nodes: FlowCheck["nodes"] = {};
  for (const node of graph.nodes) {
    const type = NODE_TYPE_BY_ID.get(node.type);
    nodes[node.id] = { summary: type ? (SUMMARIES[type.id]?.(decodeOptions(type, node.options).options) ?? "") : "unknown node type", invalid: false };
  }
  for (const issue of issues) {
    const entry = issue.node_id ? nodes[issue.node_id] : undefined;
    if (entry && issue.severity === "error") entry.invalid = true;
  }
  return { valid: !issues.some((issue) => issue.severity === "error"), issues, nodes };
}

/* ---------- evaluation (pkg/flow/evaluate.go) ---------- */

function codecFamily(codec: string): string {
  return { mpeg1video: "mpeg1", mpeg2video: "mpeg2", h265: "hevc", avc: "h264" }[codec] ?? codec;
}

function resolutionRank(width: number, height: number): number {
  let rank = 0;
  RESOLUTION_TIERS.forEach(([, w, h], index) => {
    if (width >= w || height >= h) rank = index + 1;
  });
  return rank;
}

function unique(values: string[]): string[] {
  return [...new Set(values)];
}

function decide(type: string, o: Options, file: FileFacts): { ok: boolean; reason: string } {
  switch (type) {
    case "check.video_codec": {
      const family = codecFamily(file.video_codec);
      return family ? { ok: o.list("codecs").includes(family), reason: `video codec is ${family}` } : { ok: false, reason: "video codec is unknown" };
    }
    case "check.resolution": {
      const rank = resolutionRank(file.width, file.height);
      const wanted = 1 + RESOLUTION_TIERS.findIndex(([name]) => name === o.text("resolution"));
      const reason = `video is ${file.width}x${file.height}, ${rank > 0 ? RESOLUTION_TIERS[rank - 1]?.[0] : "below 480p"}`;
      return { ok: o.text("op") === "at_most" ? rank <= wanted : rank >= wanted, reason };
    }
    case "check.hdr":
      return file.hdr === "" ? { ok: false, reason: "video is sdr" } : { ok: o.list("kinds").includes(file.hdr), reason: `video is ${file.hdr}` };
    case "check.bitrate": {
      if (file.duration_seconds <= 0) return { ok: false, reason: "duration is unknown" };
      const reason = `overall bitrate is ${file.bitrate_kbps} kb/s`;
      return { ok: o.text("op") === "below" ? file.bitrate_kbps < o.number("kbps") : file.bitrate_kbps > o.number("kbps"), reason };
    }
    case "check.file_size": {
      const limit = o.number("megabytes") * 2 ** 20;
      const reason = `file is ${(file.size_bytes / 2 ** 20).toFixed(0)} MiB`;
      return { ok: o.text("op") === "below" ? file.size_bytes < limit : file.size_bytes > limit, reason };
    }
    case "check.audio_language": {
      if (file.audio.length === 0) return { ok: false, reason: "no audio tracks" };
      // The mock knows no original language, so "orig" never matches.
      const tokens = cleanList(o.list("languages"));
      const wanted = tokens.filter((token) => token !== "orig");
      const found = unique(file.audio.map((track) => track.language || "und"));
      const reason = `${found.length === 1 ? "audio language is" : "audio languages are"} ${found.join(", ")}${tokens.includes("orig") ? "; original language is unknown" : ""}`;
      return { ok: file.audio.some((track) => track.language !== "" && wanted.includes(track.language)), reason };
    }
    case "check.audio_codec": {
      if (file.audio.length === 0) return { ok: false, reason: "no audio tracks" };
      const wanted = cleanList(o.list("codecs"));
      const found = unique(file.audio.map((track) => track.codec || "unknown"));
      return { ok: file.audio.some((track) => wanted.includes(track.codec)), reason: `${found.length === 1 ? "audio codec is" : "audio codecs are"} ${found.join(", ")}` };
    }
    default: {
      if (file.audio.length === 0) return { ok: false, reason: "no audio tracks" };
      const most = Math.max(...file.audio.map((track) => track.channels ?? 0));
      return { ok: most > Number(o.text("more_than")), reason: `largest audio track has ${most} channels` };
    }
  }
}

/** The parts of the profile a walk sets; the last node on the path wins each. */
interface Plan {
  video?: Options;
  crop: boolean;
  audioKeep?: Options;
  audioEncode?: Options;
  audioDownmix?: Options;
  subtitlesKeep?: Options;
  dropBitmap: boolean;
  removeAll: boolean;
  metadata?: Options;
}

const audioTarget = (o: Options) => (o.text("codec") === "flac" ? "flac" : audioBitrate(o) === 0 ? `${o.text("codec")} at the encoder's default bitrate` : `${o.text("codec")} at ${audioBitrate(o)} kb/s per channel`);

function planLines(plan: Plan): string[] {
  let video = "Video: copy";
  if (plan.video) {
    const o = plan.video;
    const parts = [`encode to ${o.text("codec")} with ${encoderOf(o)}`];
    if (o.text("preset").trim() !== "") parts.push(`preset ${o.text("preset").trim()}`);
    parts.push(`${o.text("bit_depth")}-bit`);
    const fixed = o.text("quality_mode") === "fixed";
    parts.push(fixed ? `CRF ${o.number("crf")}` : `${o.text("metric").toUpperCase()} ${formatNumber(o.number("target"))} target (CRF ${o.number("crf_min")}–${o.number("crf_max")})`);
    video = `Video: ${parts.join(", ")}`;
    if (!fixed && o.text("on_no_fit") === "encode") video += "; encode anyway when no CRF fits";
    if (plan.crop) video += "; crop black bars";
  } else if (plan.crop) video = "Video: copy; black bars are not cropped, because cropping needs a re-encode";

  const audio = [`keep ${listOr(cleanList(plan.audioKeep?.list("languages") ?? []), "every track")}`];
  if (!plan.audioEncode && !plan.audioDownmix) audio.push("copy");
  if (plan.audioEncode) audio.push(`encode ${listOr(cleanList(plan.audioEncode.list("source_codecs")), "every track")} to ${audioTarget(plan.audioEncode)}`);
  if (plan.audioDownmix) audio.push(`downmix above ${layoutName(plan.audioDownmix.text("max_channels"))} to ${audioTarget(plan.audioDownmix)}`);

  let subtitles = "Subtitles: remove all";
  if (!plan.removeAll) {
    const keep = plan.subtitlesKeep;
    const languages = cleanList(keep?.list("languages") ?? []);
    subtitles = `Subtitles: keep ${listOr(languages, "every track")}`;
    const kinds = keep && languages.length > 0 ? [keep.on("keep_forced") && "forced", keep.on("keep_sdh") && "SDH", keep.on("keep_commentary") && "commentary"].filter(Boolean) : [];
    if (kinds.length > 0) subtitles += `, including ${kinds.join(", ")}`;
    if (plan.dropBitmap) subtitles += "; drop image-based";
  }

  const container = ["mkv"];
  const meta = plan.metadata;
  if (meta?.text("metadata") === "strip") container.push("strip metadata");
  if (meta?.text("track_titles") === "strip") container.push("strip track titles");
  if (meta?.text("track_titles") === "standardize") container.push("standardize track titles");
  if (meta?.text("attachments") === "strip") container.push("strip attachments");
  if (meta?.text("chapters") === "strip") container.push("strip chapters");
  if (container.length === 1) container.push("keep metadata, track titles, attachments, and chapters");

  return [video, `Audio: ${audio.join("; ")}`, subtitles, `Container: ${container.join("; ")}`];
}

const BITMAP_SUBTITLES = ["hdmv_pgs_subtitle", "dvd_subtitle", "dvb_subtitle"];

function keeps(o: Options | undefined, language: string): boolean {
  const wanted = cleanList(o?.list("languages") ?? []);
  return wanted.length === 0 || wanted.includes(language);
}

/** An argument list shaped like the daemon's: per-stream maps, then codecs, then metadata. */
function command(plan: Plan, file: FileFacts): string[] {
  const args = ["-i", "{input}", "-map", "0:v:0"];
  let audio = file.audio.map((track, index) => ({ track, index })).filter(({ track }) => keeps(plan.audioKeep, track.language));
  if (audio.length === 0) audio = file.audio.map((track, index) => ({ track, index })).slice(0, plan.audioKeep?.text("fallback") === "keep_first" ? 1 : undefined);
  for (const { index } of audio) args.push("-map", `0:a:${index}`);
  let subtitles = plan.removeAll ? [] : file.subtitles.map((track, index) => ({ track, index })).filter(({ track }) => !(plan.dropBitmap && BITMAP_SUBTITLES.includes(track.codec)));
  const kept = subtitles.filter(({ track }) => keeps(plan.subtitlesKeep, track.language));
  if (kept.length > 0 || plan.subtitlesKeep?.text("fallback") !== "keep_all") subtitles = kept;
  for (const { index } of subtitles) args.push("-map", `0:s:${index}`);

  if (plan.video) {
    const o = plan.video;
    const software = o.text("accelerator") === "software";
    if (plan.crop) args.push("-vf", "{crop}");
    args.push("-c:v", encoderOf(o), "-preset", o.text("preset").trim() || "medium", software ? "-crf" : "-global_quality", o.text("quality_mode") === "fixed" ? String(o.number("crf")) : "{crf}");
    args.push("-pix_fmt", o.text("bit_depth") === "8" ? "yuv420p" : "yuv420p10le", ...o.list("ffmpeg_args"));
  } else args.push("-c:v", "copy");

  audio.forEach(({ track }, out) => {
    const channels = track.channels ?? 2;
    const downmix = plan.audioDownmix && channels > Number(plan.audioDownmix.text("max_channels")) ? plan.audioDownmix : undefined;
    const encode = plan.audioEncode && (plan.audioEncode.list("source_codecs").length === 0 || cleanList(plan.audioEncode.list("source_codecs")).includes(track.codec)) ? plan.audioEncode : undefined;
    const target = downmix ?? (encode && encode.text("codec") !== track.codec ? encode : undefined);
    if (!target) return void args.push(`-c:a:${out}`, "copy");
    const outChannels = downmix ? Number(downmix.text("max_channels")) : channels;
    args.push(`-c:a:${out}`, target.text("codec") === "opus" ? "libopus" : target.text("codec"));
    if (audioBitrate(target) > 0) args.push(`-b:a:${out}`, `${audioBitrate(target) * outChannels}k`);
    if (downmix) args.push(`-ac:a:${out}`, String(outChannels));
  });
  if (subtitles.length > 0) args.push("-c:s", "copy");

  const meta = plan.metadata;
  if (meta?.text("metadata") === "strip") args.push("-map_metadata", "-1");
  if (meta?.text("chapters") === "strip") args.push("-map_chapters", "-1");
  if (meta?.text("attachments") !== "strip") args.push("-map", "0:t?");
  args.push("-f", "matroska", "{output}");
  return args;
}

/** Walks a graph for one file. The graph must have no error issues. */
export function testGraph(graph: Graph, file: FileFacts): FlowTest {
  const a = analyze(graph);
  const check = checkGraph(graph);
  const path: string[] = [];
  const decisions: string[] = [];
  const plan: Plan = { crop: false, dropBitmap: false, removeAll: false };
  let outcome: Outcome = "skip";

  // A valid graph always arrives at an output node; the guard only protects the mock from a bad fixture.
  for (let id = a.input, steps = 0; id && steps <= graph.nodes.length; steps++) {
    const node = a.nodes.get(id);
    const type = node && NODE_TYPE_BY_ID.get(node.type);
    const o = a.options.get(id);
    if (!node || !type || !o) break;
    path.push(id);
    let port = 0;
    if (type.kind === "condition") {
      const { ok, reason } = decide(type.id, o, file);
      if (!ok) port = 1;
      decisions.push(`${node.label || type.label} → ${ok} (${reason})`);
    }
    if (type.id === "video.encode") plan.video = o;
    else if (type.id === "video.copy") plan.video = undefined;
    else if (type.id === "video.crop") plan.crop = true;
    else if (type.id === "audio.keep") plan.audioKeep = o;
    else if (type.id === "audio.encode") plan.audioEncode = o;
    else if (type.id === "audio.downmix") plan.audioDownmix = o;
    else if (type.id === "subtitles.keep") plan.subtitlesKeep = o;
    else if (type.id === "subtitles.drop_bitmap") plan.dropBitmap = true;
    else if (type.id === "subtitles.remove_all") plan.removeAll = true;
    else if (type.id === "container.metadata") plan.metadata = o;
    if (type.kind === "output") {
      outcome = type.id === "output.publish" ? "process" : "skip";
      break;
    }
    id = a.targets.get(id)?.[port] ?? "";
  }

  const skip = outcome === "skip";
  return { check, outcome, path, decisions, file, plan: skip ? ["File: skip, leave it untouched"] : planLines(plan), command: skip ? [] : command(plan, file) };
}
