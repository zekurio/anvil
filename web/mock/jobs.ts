// Queue rows and job detail, synthesized from the mock files. Attempts, events
// and stream selection are derived deterministically from the file so the
// same job always shows the same history.

import type { AttemptDetail, AttemptEventDetail, AttemptStreamSelection, EventPayload, Job, JobDetail, JobFlow, StreamDecision, StreamSelectionDecision } from "../src/api/types.ts";
import { absolutePath, db, factsOf, findLibrary, progressOf } from "./db.ts";
import type { MockFile } from "./fixtures/files.ts";
import { testGraph } from "./flowcheck.ts";

const iso = (ms: number) => new Date(ms).toISOString();

/** The job state the wire shows: a running job moves through validating and replacing with its step. */
export function stateOf(file: MockFile): Job["state"] {
  if (file.status !== "running") return file.job_state ?? "pending";
  const step = progressOf(file)?.step;
  return step === "validate" ? "validating" : step === "publish" ? "replacing" : "running";
}

export function jobView(file: MockFile): Job {
  const occurrence = { path: file.path, absolute_path: absolutePath(file), generation: 1, current: true, status: "present" };
  return {
    id: file.job_id ?? 0,
    slug: slugOf(file),
    library: file.library,
    state: stateOf(file),
    source: occurrence,
    asset: occurrence,
    attempt_count: file.attempt_count,
    created_at: iso(file.created_at),
    updated_at: iso(file.updated_at),
    ...(file.completed_at ? { completed_at: iso(file.completed_at) } : {}),
    ...(file.status === "running" ? { lease_owner: "worker-1", heartbeat_at: iso(Date.now()) } : {}),
    ...(file.last_error ? { last_error: file.last_error } : {}),
    ...(file.status === "done" ? { destination_path: destinationOf(file), publish_stage: "committed" } : {}),
    status: file.status,
    path: file.path,
    input_size_bytes: file.size_bytes,
    output_size_bytes: file.output_size_bytes ?? 0,
    ...attemptTimes(file),
    ...(file.status === "running" ? { progress: progressOf(file) } : {}),
  };
}

/** When the latest attempt started and ended. A job that never ran has neither. */
function attemptTimes(file: MockFile): { started_at?: string; finished_at?: string } {
  if (file.status === "running" && file.started_at !== undefined) return { started_at: iso(file.started_at) };
  if (file.attempt_count === 0 || file.completed_at === undefined) return {};
  // Derived from the file, so the same job always took the same time.
  const seconds = file.status === "done" ? 240 + (file.source_id % 13) * 47 : 4 + (file.source_id % 7) * 3;
  return { started_at: iso(file.completed_at - seconds * 1000), finished_at: iso(file.completed_at) };
}

/** Where the encode was published: the handoff folder, beside the source in copy mode, or over the source. */
function destinationOf(file: MockFile): string {
  const library = findLibrary(file.library);
  const source = absolutePath(file);
  if (library?.kind === "download") return `${library.download.handoff_path}/${file.path}`;
  if (library?.media.replacement_mode === "copy") return source.replace(/\.[^.]+$/, ".av1.mkv");
  return source.replace(/\.[^.]+$/, ".mkv");
}

function slugOf(file: MockFile): string {
  const name = file.path.slice(file.path.lastIndexOf("/") + 1).replace(/\.[^.]+$/, "");
  return `${name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "").slice(0, 40)}-${(file.job_id ?? 0).toString(36)}`;
}

function selection(file: MockFile, kind: "audio" | "subtitle"): StreamSelectionDecision {
  const requested = kind === "audio" ? ["eng", "jpn"] : ["eng"];
  const streams: StreamDecision[] = (kind === "audio" ? file.audio : file.subtitles).map((s) => {
    const commentary = s.title === "Commentary";
    const wanted = requested.includes(s.language);
    return {
      index: s.index,
      codec: s.codec,
      language: s.language,
      resolved_language: s.language,
      ...(s.title ? { title: s.title } : {}),
      ...(s.channels ? { channels: s.channels } : {}),
      ...(s.default ? { default: true } : {}),
      ...(s.forced ? { forced: true } : {}),
      kept: wanted && !commentary,
      reason: commentary ? "commentary" : wanted ? (s.language === "eng" ? "original_language" : "language_match") : "language_not_requested",
    };
  });
  const present = new Set(streams.map((s) => s.resolved_language));
  return {
    kind,
    rule: streams.length === 0 ? "no_streams" : "language_filter",
    original_language: "eng",
    requested_languages: requested,
    resolved_languages: requested,
    missing_languages: requested.filter((l) => !present.has(l)),
    streams,
  };
}

const LOSSLESS = new Set(["truehd", "dts", "flac", "mlp"]);

/** The audio-output artifact payload: a pkg/domain AudioTrackPlan for every kept track. */
function audioTracks(decision: StreamSelectionDecision) {
  return (decision.streams ?? [])
    .filter((s) => s.kept)
    .map((s) => {
      const channels = s.channels ?? 2;
      if (!LOSSLESS.has(s.codec ?? "")) return { stream_index: s.index, action: "copy", source_codec: s.codec, source_channels: channels, codec: s.codec, channels };
      const out = Math.min(channels, 6);
      return { stream_index: s.index, action: "encode", source_codec: s.codec, source_channels: channels, codec: "eac3", channels: out, ...(out < channels ? { layout: "5.1" } : {}), bitrate_kbps: out * 96 };
    });
}

interface Quality {
  mode: "search" | "fixed";
  crf: number;
  metric?: string;
  score?: number;
  trials?: number;
  skip_reason?: string;
  forced_reason?: string;
}

/** The crf-selection artifact payload (pkg/search selectionPayload), varied so every wording shows up somewhere. */
function quality(file: MockFile, flow: JobFlow | undefined): Quality {
  if (flow?.path.includes("copy")) return { mode: "search", crf: 0, metric: "vmaf", skip_reason: "the Dolby Vision profile 5 base layer is not viewable on its own, so the video is copied" };
  const encode = flow?.graph.nodes.find((n) => n.type === "video.encode" && flow.path.includes(n.id));
  if (encode?.options?.rate_control === "crf") return { mode: "fixed", crf: Number(encode.options.crf ?? 24) };
  if (file.source_id % 17 === 0) return { mode: "search", crf: 38, metric: "vmaf", score: 93.1, trials: 7, forced_reason: "CRF search found no candidate satisfying quality and size constraints; forcing encode with best tested CRF 38" };
  return { mode: "search", crf: 27 + (file.source_id % 8), metric: "vmaf", score: Number((95 + (file.source_id % 7) / 10).toFixed(1)), trials: 4 + (file.source_id % 4) };
}

function searchContext(chosen: Quality) {
  if (chosen.skip_reason) return { search_metric: chosen.metric, search_skip_reason: chosen.skip_reason };
  return { search_crf: chosen.crf, ...(chosen.metric ? { search_metric: chosen.metric, search_vmaf: chosen.score } : {}) };
}

const jsonPayload = (json: unknown): EventPayload => ({ kind: "json", size_bytes: JSON.stringify(json).length, json });

const STEP_NAMES = new Set(["probe", "crop-detect", "audio-cleanup", "subtitle-cleanup", "stage", "crf-search", "encode", "validate", "publish", "cleanup"]);
const FFMPEG_STEPS = new Set(["crop-detect", "crf-search", "encode"]);

function attempts(file: MockFile, flow: JobFlow | undefined): { attempts: AttemptDetail[]; selections: AttemptStreamSelection[] } {
  const list: AttemptDetail[] = [];
  const selections: AttemptStreamSelection[] = [];
  const count = file.attempt_count;
  const end = file.completed_at ?? file.updated_at;
  const chosen = quality(file, flow);
  let eventId = (file.job_id ?? 0) * 100;

  for (let number = 1; number <= count; number++) {
    const last = number === count;
    const running = last && file.status === "running";
    const canceled = last && file.job_state === "canceled";
    const skipped = last && file.job_state === "skipped";
    const failed = !last || file.status === "failed";
    const id = (file.job_id ?? 0) * 10 + number;
    const started = running ? (file.started_at ?? end) : end - (count - number + 1) * 780_000;
    const events: AttemptEventDetail[] = [];
    let clock = started;
    const event = (type: AttemptEventDetail["type"], name: string, message: string, extra: Partial<AttemptEventDetail> = {}) => {
      events.push({ id: ++eventId, attempt_id: id, created_at: iso(clock), type, name, message, ...extra });
    };
    const command = (name: string): string[] => {
      const input = ["ffmpeg", "-hide_banner", "-nostdin", "-i", absolutePath(file)];
      if (name === "crop-detect") return [...input, "-vf", "cropdetect=24:2:0", "-frames:v", "240", "-f", "null", "-"];
      if (name === "crf-search") return [...input, "-ss", "00:12:00", "-t", "20", "-c:v", "libsvtav1", "-preset", "6", "-crf", String(chosen.crf), "-an", "-sn", `/var/lib/anvil/staging/job-${file.job_id}/sample-3.mkv`];
      return [...input, "-map", "0:v:0", "-map", "0:a", "-map", "0:s?", "-c:v", "libsvtav1", "-preset", "6", "-crf", String(chosen.crf), "-c:a", "copy", "-c:s", "copy", `${absolutePath(file)}.job-${file.job_id}.anvil-part`];
    };
    // The daemon records one "process-output" artifact per ffmpeg run, with the log paths rather than the logs.
    const processOutput = (name: string, seconds: number, exitCode: number) => {
      const base = `/var/lib/anvil/staging/logs/job-${file.job_id}/attempt-${number}/${name}-ffmpeg`;
      event("artifact", "process-output", "captured process output for ffmpeg", {
        process_output: { step: name, command: command(name), exit_code: exitCode, duration_ms: Math.round(seconds * 1000), stdout_path: `${base}.stdout.log`, stderr_path: `${base}.stderr.log`, stdout_bytes: 0, stderr_bytes: exitCode === 0 ? 48_211 : 18_432, error: exitCode === 0 ? "" : `exit status ${exitCode}` },
      });
    };

    const current = running ? progressOf(file)?.step : undefined;
    const named = (file.last_error ?? "").split(":")[0] ?? "";
    const failedStep = STEP_NAMES.has(named) ? named : "encode";
    const steps: [string, number, string][] = [
      ["probe", 2, `${file.video_codec} ${file.width}x${file.height}, ${file.audio.length} audio, ${file.subtitles.length} subtitle streams`],
      ["crop-detect", 6, file.height === 2160 ? "crop=3840:1600:0:280 keeps 74.1% of the frame" : "no crop: the frame has no black bars"],
      ["audio-cleanup", 1, `kept ${file.audio.filter((a) => a.title !== "Commentary").length} of ${file.audio.length} audio streams`],
      ["subtitle-cleanup", 1, `kept ${file.subtitles.length} of ${file.subtitles.length} subtitle streams`],
      ["stage", 1, "planned the destination beside the source"],
      ["crf-search", 50, chosen.mode === "fixed" ? `fixed CRF ${chosen.crf}, search skipped` : `crf ${chosen.crf} reaches vmaf ${chosen.score ?? 95}`],
      ["encode", 400, "encoded 1 video, 1 audio, 1 subtitle stream"],
      ["validate", 6, "duration and stream counts match the source"],
      ["publish", 3, "published the encode and committed the journal"],
      ["cleanup", 1, "removed scratch files"],
    ];
    for (const [name, seconds, message] of steps) {
      if (skipped && name === "audio-cleanup") break;
      if (canceled && name === "crf-search") break;
      if (name === current) {
        event("block_started", name, "");
        break;
      }
      if (failed && name === failedStep) {
        event("block_started", name, "");
        clock += seconds * 400;
        if (FFMPEG_STEPS.has(name)) processOutput(name, seconds * 0.4, 187);
        event("block_failed", name, last ? (file.last_error ?? "ffmpeg exited with code 187") : "the worker restarted while this step was running");
        break;
      }
      event("block_started", name, "");
      clock += seconds * 1000;
      if (FFMPEG_STEPS.has(name) && !(name === "crf-search" && (chosen.mode === "fixed" || chosen.skip_reason))) processOutput(name, seconds - 0.4, 0);
      event("block_finished", name, message);
      if (name === "audio-cleanup" || name === "subtitle-cleanup") {
        const decision = selection(file, name === "audio-cleanup" ? "audio" : "subtitle");
        event("artifact", "stream-selection", `${decision.kind} selection by ${decision.rule}`, { stream_selection: decision });
        if (last) selections.push({ attempt_id: id, attempt_number: number, recorded_at: iso(clock), decision });
        if (name === "audio-cleanup") {
          const tracks = audioTracks(decision);
          // A flow that copies every track reports nothing, as the daemon does.
          if (tracks.some((t) => t.action === "encode")) event("artifact", "audio-output", tracks.map((t) => `${t.stream_index}:${t.action}:${t.source_codec}:${t.source_channels}ch`).join(", "), { payload: jsonPayload({ tracks }) });
        }
      }
      if (name === "crf-search") {
        const summary = chosen.skip_reason ? `video encode skipped: ${chosen.skip_reason}` : chosen.mode === "fixed" ? `fixed CRF ${chosen.crf}, search skipped` : (chosen.forced_reason ?? `selected CRF ${chosen.crf} (${chosen.metric} ${chosen.score?.toFixed(2)}, ${chosen.trials} trials)`);
        event("artifact", "crf-selection", summary, { payload: jsonPayload(chosen) });
      }
    }

    list.push({
      id,
      number,
      state: running ? "running" : failed ? "failed" : canceled ? "canceled" : "succeeded",
      worker_id: `worker-${(number % 2) + 1}`,
      started_at: iso(started),
      ...(running ? {} : { finished_at: iso(clock) }),
      error: failed ? (last ? (file.last_error ?? "") : "the worker restarted while this step was running") : canceled ? "canceled by operator" : "",
      events,
    });
  }
  return { attempts: list, selections };
}

export function jobDetail(file: MockFile): JobDetail {
  const library = findLibrary(file.library);
  const stored = db.flows.find((f) => f.id === library?.flow_id);
  const probed = file.attempt_count > 0;
  let flow: JobFlow | undefined;
  if (stored && probed) {
    const run = testGraph(stored.graph, factsOf(file));
    flow = { flow_id: stored.id, flow_name: stored.name, revision: stored.revision, graph: stored.graph, outcome: run.outcome, path: run.path, decisions: run.decisions, plan: run.plan };
  }
  const history = attempts(file, flow);
  const done = file.status === "done";
  const download = library?.kind === "download";
  return {
    api_version: "v1",
    server_time: iso(Date.now()),
    job: {
      id: file.job_id ?? 0,
      slug: slugOf(file),
      state: stateOf(file),
      library: file.library,
      attempt_count: file.attempt_count,
      updated_at: iso(file.updated_at),
      created_at: iso(file.created_at),
      ...(file.completed_at ? { completed_at: iso(file.completed_at) } : {}),
      source_kind: download ? "package" : "file",
      source_path: file.path,
      asset_path: file.path,
      asset_role: "primary",
      path: file.path,
      last_error: file.last_error ?? "",
    },
    ...(done || file.status === "running"
      ? { pipeline_context: { version: 7, crop_evaluated: true, crop_no_op: file.height !== 2160, ...(file.height === 2160 ? { crop_filter: "crop=3840:1600:0:280", crop_source_width: 3840, crop_source_height: 2160, crop_output_width: 3840, crop_output_height: 1600, crop_retained_area_percent: 74.1 } : {}), ...(done ? searchContext(quality(file, flow)) : {}) } }
      : {}),
    ...(done
      ? { publish_operation: { kind: download ? "handoff" : "replace", mode: download ? library.download.handoff_mode : (library?.media.replacement_mode ?? "replace"), stage: "committed", artifact_path: `${absolutePath(file)}.job-${file.job_id}.anvil-part`, destination_path: destinationOf(file), artifact_size_bytes: file.output_size_bytes ?? 0, digest_algorithm: "blake3", updated_at: iso(file.completed_at ?? file.updated_at) } }
      : {}),
    ...(history.selections.length > 0 ? { stream_selection: history.selections } : {}),
    attempts: history.attempts,
    status: file.status,
    input_path: absolutePath(file),
    input_size_bytes: file.size_bytes,
    output_size_bytes: file.output_size_bytes ?? 0,
    destination_path: destinationOf(file),
    ...(file.status === "running" ? { progress: progressOf(file) } : {}),
    ...(flow ? { flow } : {}),
    ...(probed ? { file: factsOf(file) } : {}),
  };
}
