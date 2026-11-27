// Queue rows and job detail, synthesized from the mock files. Attempts, events
// and stream selection are derived deterministically from the file so the
// same job always shows the same history.

import type { AttemptDetail, AttemptEventDetail, AttemptStreamSelection, Job, JobDetail, JobFlow, StreamDecision, StreamSelectionDecision } from "../src/api/types.ts";
import { absolutePath, db, factsOf, findLibrary, progressOf } from "./db.ts";
import type { MockFile } from "./fixtures/files.ts";
import { testGraph } from "./flowcheck.ts";

const iso = (ms: number) => new Date(ms).toISOString();

export function jobView(file: MockFile): Job {
  const library = findLibrary(file.library);
  const occurrence = { path: file.path, absolute_path: absolutePath(file), generation: 1, current: true, status: "present" };
  return {
    id: file.job_id ?? 0,
    slug: slugOf(file),
    library: file.library,
    state: file.job_state ?? "pending",
    source: occurrence,
    asset: occurrence,
    attempt_count: file.attempt_count,
    created_at: iso(file.created_at),
    updated_at: iso(file.updated_at),
    ...(file.completed_at ? { completed_at: iso(file.completed_at) } : {}),
    ...(file.status === "running" ? { lease_owner: "worker-1", heartbeat_at: iso(Date.now()) } : {}),
    ...(file.last_error ? { last_error: file.last_error } : {}),
    ...(file.status === "done" ? { destination_path: library?.kind === "download" ? `${library.download.handoff_path}/${file.path}` : absolutePath(file), publish_stage: "committed" } : {}),
    status: file.status,
    input_size_bytes: file.size_bytes,
    output_size_bytes: file.output_size_bytes ?? 0,
    ...(file.status === "running" ? { progress: progressOf(file) } : {}),
  };
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
      reason: commentary ? "commentary" : wanted ? "language_match" : "language_not_requested",
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

function attempts(file: MockFile, flow: JobFlow | undefined): { attempts: AttemptDetail[]; selections: AttemptStreamSelection[] } {
  const list: AttemptDetail[] = [];
  const selections: AttemptStreamSelection[] = [];
  const count = file.attempt_count;
  const end = file.completed_at ?? file.updated_at;
  let eventId = (file.job_id ?? 0) * 100;

  for (let number = 1; number <= count; number++) {
    const last = number === count;
    const running = last && file.status === "running";
    const failed = !last || file.status === "failed";
    const id = (file.job_id ?? 0) * 10 + number;
    const started = running ? (file.started_at ?? end) : end - (count - number + 1) * 780_000;
    const events: AttemptEventDetail[] = [];
    let clock = started;
    const event = (type: AttemptEventDetail["type"], name: string, message: string, extra: Partial<AttemptEventDetail> = {}) => {
      events.push({ id: ++eventId, attempt_id: id, created_at: iso(clock), type, name, message, ...extra });
    };
    const block = (name: string, seconds: number, message: string) => {
      event("block_started", name, "");
      clock += seconds * 1000;
      event("block_finished", name, message);
    };

    const current = running ? progressOf(file)?.step : undefined;
    const skipped = last && file.job_state === "skipped";
    const steps: [string, number, string][] = [
      ["probe", 2, `${file.video_codec} ${file.width}x${file.height}, ${file.audio.length} audio, ${file.subtitles.length} subtitle streams`],
      ["crop-detect", 6, file.height === 2160 ? "crop=3840:1600:0:280 keeps 74.1% of the frame" : "no crop: the frame has no black bars"],
      ["audio-cleanup", 1, `kept ${file.audio.filter((a) => a.title !== "Commentary").length} of ${file.audio.length} audio streams`],
      ["subtitle-cleanup", 1, `kept ${file.subtitles.length} of ${file.subtitles.length} subtitle streams`],
      ["stage", 1, "planned the destination beside the source"],
      ["crf-search", 50, "crf 31 reaches vmaf 95.2 at 41% of the source size"],
      ["encode", 400, "encoded 1 video, 1 audio, 1 subtitle stream"],
      ["validate", 6, "duration and stream counts match the source"],
      ["publish", 3, "replaced the source and committed the journal"],
      ["cleanup", 1, "removed scratch files"],
    ];
    for (const [name, seconds, message] of steps) {
      if (skipped && name === "audio-cleanup") break;
      if (name === current) {
        event("block_started", name, "");
        break;
      }
      if (failed && name === (file.last_error?.split(":")[0] ?? "encode")) {
        event("block_started", name, "");
        clock += seconds * 400;
        event("block_failed", name, file.last_error ?? "ffmpeg exited with code 1", {
          process_output: { step: name, command: ["ffmpeg", "-hide_banner", "-i", absolutePath(file), ...(flow?.outcome === "process" ? ["-c:v", "libsvtav1", "-preset", "6", "-crf", "31"] : []), "out.mkv"], exit_code: 187, duration_ms: seconds * 400, stdout_path: `/var/lib/anvil/logs/job-${file.job_id}/attempt-${number}/${name}.stdout.log`, stderr_path: `/var/lib/anvil/logs/job-${file.job_id}/attempt-${number}/${name}.stderr.log`, stdout_bytes: 0, stderr_bytes: 18432, error: "exit status 187" },
        });
        break;
      }
      block(name, seconds, message);
      if (name === "audio-cleanup" || name === "subtitle-cleanup") {
        const decision = selection(file, name === "audio-cleanup" ? "audio" : "subtitle");
        event("artifact", "stream-selection", `${decision.kind} selection by ${decision.rule}`, { stream_selection: decision });
        if (last) selections.push({ attempt_id: id, attempt_number: number, recorded_at: iso(clock), decision });
      }
    }

    list.push({
      id,
      number,
      state: running ? "running" : failed ? "failed" : file.job_state === "canceled" ? "canceled" : "succeeded",
      worker_id: `worker-${(number % 2) + 1}`,
      started_at: iso(started),
      ...(running ? {} : { finished_at: iso(clock) }),
      error: failed ? (file.last_error ?? "retried after a worker restart") : "",
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
  return {
    api_version: "v1",
    server_time: iso(Date.now()),
    job: {
      id: file.job_id ?? 0,
      slug: slugOf(file),
      state: file.job_state ?? "pending",
      library: file.library,
      attempt_count: file.attempt_count,
      updated_at: iso(file.updated_at),
      created_at: iso(file.created_at),
      ...(file.completed_at ? { completed_at: iso(file.completed_at) } : {}),
      source_kind: library?.kind === "download" ? "package" : "file",
      source_path: file.path,
      asset_path: file.path,
      asset_role: "primary",
      path: absolutePath(file),
      last_error: file.last_error ?? "",
    },
    ...(done || file.status === "running"
      ? { pipeline_context: { version: 6, crop_evaluated: true, crop_no_op: file.height !== 2160, ...(file.height === 2160 ? { crop_filter: "crop=3840:1600:0:280", crop_source_width: 3840, crop_source_height: 2160, crop_output_width: 3840, crop_output_height: 1600, crop_retained_area_percent: 74.1 } : {}), ...(done ? { search_crf: 31, search_metric: "vmaf", search_vmaf: 95.2 } : {}) } }
      : {}),
    ...(done
      ? { publish_operation: { kind: library?.kind === "download" ? "handoff" : "replace", mode: library?.kind === "download" ? library.download.handoff_mode : (library?.media.replacement_mode ?? "replace"), stage: "committed", artifact_path: `${absolutePath(file)}.job-${file.job_id}.anvil-part`, destination_path: absolutePath(file), artifact_size_bytes: file.output_size_bytes ?? 0, digest_algorithm: "blake3", updated_at: iso(file.completed_at ?? file.updated_at) } }
      : {}),
    ...(history.selections.length > 0 ? { stream_selection: history.selections } : {}),
    attempts: history.attempts,
    status: file.status,
    ...(file.status === "running" ? { progress: progressOf(file) } : {}),
    ...(flow ? { flow } : {}),
    ...(probed ? { file: factsOf(file) } : {}),
  };
}
