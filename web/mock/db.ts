// The mock daemon's in-memory state. It lives for one dev-server run; restart
// `npm run dev:mock` to get the fixtures back.

import type { Arr, FileFacts, FileStatus, JobState, Library, LibraryFile, LibraryStats, LibraryWrite, Progress } from "../src/api/types.ts";
import { generateFiles, type MockFile } from "./fixtures/files.ts";
import { initialFlows, type StoredFlow } from "./fixtures/flows.ts";

export const VERSION = "0.5.0";
export const WORKERS = 2;

export interface StoredLibrary extends LibraryWrite {
  last_scan_at?: string;
  created_at: string;
  updated_at: string;
}

export interface StoredArr extends Omit<Arr, "libraries"> {}

const DEFAULT_DOWNLOAD: LibraryWrite["download"] = {
  handoff_path: "",
  stable_for: "",
  package_mode: "auto",
  handoff_mode: "move",
  preserve_relative_path: true,
  cleanup_source_media: false,
  prune_empty_dirs: true,
  ignorable_globs: [],
};

function initialLibraries(now: number): StoredLibrary[] {
  const at = (minutesAgo: number) => new Date(now - minutesAgo * 60_000).toISOString();
  const base = { paused: false, priority: 0, concurrency_limit: 0, scan_interval: "", arr: "", rule_fallback: "allow" as const };
  return [
    {
      ...base,
      name: "anime",
      kind: "media",
      path: "/media/anime",
      flow_id: 1,
      arr: "sonarr",
      paused: true,
      rules: [],
      media: { replacement_mode: "replace" },
      download: DEFAULT_DOWNLOAD,
      last_scan_at: at(18),
      created_at: at(60 * 24 * 2),
      updated_at: at(60 * 24 * 2),
    },
    {
      ...base,
      name: "downloads",
      kind: "download",
      path: "/downloads/complete",
      flow_id: 1,
      priority: 10,
      concurrency_limit: 1,
      scan_interval: "2m",
      arr: "sonarr",
      rules: [
        { action: "deny", field: "path", op: "matches", value: "**/Sample/**" },
        { action: "allow", field: "extension", op: "is", value: "mkv, mp4" },
      ],
      rule_fallback: "deny",
      media: { replacement_mode: "replace" },
      download: { ...DEFAULT_DOWNLOAD, handoff_path: "/downloads/import", stable_for: "45s", ignorable_globs: ["*.nfo", "*.txt", "**/Sample/**"] },
      last_scan_at: at(1),
      created_at: at(60 * 24 * 30),
      updated_at: at(60 * 24 * 4),
    },
    {
      ...base,
      name: "movies",
      kind: "media",
      path: "/media/movies",
      flow_id: 1,
      arr: "radarr",
      rules: [
        { action: "deny", field: "path", op: "matches", value: "**/Extras/**" },
        { action: "deny", field: "path", op: "matches", value: "*.sample.*" },
        { action: "allow", field: "extension", op: "is", value: "mkv, mp4, avi, m2ts" },
        { action: "deny", field: "size", op: "lt", value: "200 MB" },
      ],
      media: { replacement_mode: "replace" },
      download: DEFAULT_DOWNLOAD,
      last_scan_at: at(4),
      created_at: at(60 * 24 * 41),
      updated_at: at(60 * 24 * 9),
    },
    { ...base, name: "tv", kind: "media", path: "/media/tv", flow_id: 1, arr: "sonarr", rules: [], media: { replacement_mode: "copy" }, download: DEFAULT_DOWNLOAD, last_scan_at: at(26), created_at: at(60 * 24 * 38), updated_at: at(60 * 24 * 12) },
  ];
}

const now = Date.now();

export const db = {
  startedAt: now - 3 * 86_400_000 - 5_400_000,
  authenticated: false,
  libraries: initialLibraries(now),
  files: generateFiles(now),
  flows: initialFlows(now) as StoredFlow[],
  arrs: [
    { name: "radarr", type: "radarr", base_url: "http://radarr.lan:7878", api_key_file: "/run/secrets/radarr", api_key_set: false },
    { name: "sonarr", type: "sonarr", base_url: "http://sonarr.lan:8989", api_key_file: "", api_key_set: true },
  ] as StoredArr[],
  nextJobId: 4200,
  nextFlowId: 4,
};

// Give every file that has a job an id, oldest first, so ids look like history.
for (const file of [...db.files].sort((a, b) => a.created_at - b.created_at)) {
  if (file.status !== "none") file.job_id = db.nextJobId++;
}

/* ---------- running jobs ---------- */

const STEPS: readonly (readonly [string, number])[] = [
  ["probe", 2],
  ["crop-detect", 6],
  ["crf-search", 50],
  ["encode", 400],
  ["validate", 6],
  ["publish", 3],
];
const TOTAL_SECONDS = STEPS.reduce((sum, [, s]) => sum + s, 0);

function startJob(file: MockFile, at: number): void {
  file.status = "running";
  file.job_state = "running";
  file.job_id ??= db.nextJobId++;
  file.attempt_count += 1;
  file.started_at = at;
  file.updated_at = at;
}

function nextQueued(): MockFile | undefined {
  const paused = new Set(db.libraries.filter((l) => l.paused).map((l) => l.name));
  const limited = new Map(db.libraries.filter((l) => l.concurrency_limit > 0).map((l) => [l.name, l.concurrency_limit]));
  const priority = new Map(db.libraries.map((l) => [l.name, l.priority]));
  return db.files
    .filter((f) => f.status === "queued" && !paused.has(f.library))
    .filter((f) => (limited.get(f.library) ?? Infinity) > db.files.filter((r) => r.library === f.library && r.status === "running").length)
    .sort((a, b) => (priority.get(b.library) ?? 0) - (priority.get(a.library) ?? 0) || (a.job_id ?? 0) - (b.job_id ?? 0))[0];
}

/** Moves running jobs along by wall-clock time. Called before every request. */
export function advance(at = Date.now()): void {
  for (const file of db.files) {
    if (file.status !== "running" || file.started_at === undefined) continue;
    if ((at - file.started_at) / 1000 < TOTAL_SECONDS) continue;
    file.status = "done";
    file.job_state = "complete";
    file.output_size_bytes = Math.round(file.size_bytes * 0.42);
    file.completed_at = file.updated_at = file.started_at + TOTAL_SECONDS * 1000;
    file.started_at = undefined;
  }
  while (db.files.filter((f) => f.status === "running").length < WORKERS) {
    const next = nextQueued();
    if (!next) break;
    startJob(next, at);
  }
}

// Two jobs are mid-encode when the server starts.
const seeded = [db.files.find((f) => f.library === "movies" && f.status === "queued"), db.files.find((f) => f.library === "downloads" && f.status === "queued")];
seeded.forEach((file, i) => file && startJob(file, now - (i === 0 ? 210_000 : 95_000)));

export function progressOf(file: MockFile, at = Date.now()): Progress | undefined {
  if (file.status !== "running" || file.started_at === undefined || file.job_id === undefined) return undefined;
  let elapsed = (at - file.started_at) / 1000;
  for (const [step, seconds] of STEPS) {
    if (elapsed >= seconds) {
      elapsed -= seconds;
      continue;
    }
    const timed = step === "encode" || step === "crf-search";
    // Not derived from the step length: a plausible encoder speed per file.
    const speed = timed ? Number(((1.3 + (file.source_id % 9) * 0.15) * (step === "encode" ? 1 : 3)).toFixed(2)) : 0;
    return {
      job_id: file.job_id,
      library: file.library,
      path: file.path,
      flow_name: db.flows.find((f) => f.id === findLibrary(file.library)?.flow_id)?.name ?? "",
      step,
      percent: timed ? Number(((elapsed / seconds) * 100).toFixed(1)) : -1,
      fps: timed ? Number((speed * 23.976).toFixed(1)) : 0,
      speed,
      eta_seconds: timed ? Math.round(seconds - elapsed) : -1,
      updated_at: new Date(at).toISOString(),
    };
  }
  return undefined;
}

/* ---------- views ---------- */

export function findLibrary(name: string): StoredLibrary | undefined {
  return db.libraries.find((l) => l.name === name);
}

export function statsOf(name: string): LibraryStats {
  const stats: LibraryStats = { files: 0, total_bytes: 0, pending: 0, running: 0, done: 0, failed: 0, skipped: 0, saved_bytes: 0 };
  for (const file of db.files) {
    if (file.library !== name) continue;
    stats.files += 1;
    stats.total_bytes += file.size_bytes;
    if (file.status === "queued") stats.pending += 1;
    else if (file.status !== "none") stats[file.status] += 1;
    if (file.status === "done" && file.output_size_bytes) stats.saved_bytes += file.size_bytes - file.output_size_bytes;
  }
  return stats;
}

export function libraryView(library: StoredLibrary): Library {
  return { ...library, flow_name: db.flows.find((f) => f.id === library.flow_id)?.name ?? "", stats: statsOf(library.name) };
}

export function absolutePath(file: MockFile): string {
  return `${findLibrary(file.library)?.path ?? ""}/${file.path}`;
}

const RESOLUTION_LABEL: Record<number, string> = { 2160: "2160p", 1080: "1080p", 720: "720p", 480: "480p" };

const LAYOUT: Record<number, string> = { 1: "1.0", 2: "2.0", 6: "5.1", 8: "7.1" };

/** Probe-derived columns are empty until a job has probed the file. */
export function fileView(file: MockFile): LibraryFile {
  const probed = file.status !== "none" && file.job_state !== "pending";
  const first = file.audio[0];
  // A media library replaces the file in place: what a scan finds afterwards is the output.
  const replaced = file.output_size_bytes !== undefined && file.output_size_bytes > 0 && findLibrary(file.library)?.kind === "media";
  return {
    source_id: file.source_id,
    asset_id: file.source_id,
    path: file.path,
    absolute_path: absolutePath(file),
    size_bytes: replaced ? (file.output_size_bytes ?? file.size_bytes) : file.size_bytes,
    status: file.status,
    ...(file.job_id !== undefined ? { job_id: file.job_id, job_state: file.job_state } : {}),
    ...(file.last_error ? { last_error: file.last_error } : {}),
    container: probed ? file.container : "",
    video_codec: probed ? file.video_codec : "",
    resolution: probed ? (RESOLUTION_LABEL[file.height] ?? `${file.height}p`) : "",
    audio: probed && first ? `${first.codec} ${LAYOUT[first.channels ?? 2] ?? ""}`.trim() : "",
    ...(file.output_size_bytes ? { input_size_bytes: file.size_bytes, output_size_bytes: file.output_size_bytes } : {}),
    ...(file.status === "running" ? { progress: progressOf(file) } : {}),
    updated_at: new Date(file.updated_at).toISOString(),
  };
}

export function factsOf(file: MockFile): FileFacts {
  return {
    path: absolutePath(file),
    size_bytes: file.size_bytes,
    duration_seconds: file.duration_seconds,
    container: file.container,
    video_codec: file.video_codec,
    width: file.width,
    height: file.height,
    hdr: file.hdr,
    bitrate_kbps: Math.round((file.size_bytes * 8) / file.duration_seconds / 1000),
    audio: file.audio,
    subtitles: file.subtitles,
  };
}

/** The file statuses behind a job state, as pkg/webapi groups them. */
export const STATUS_OF_STATE: Record<JobState, FileStatus> = {
  pending: "queued",
  leased: "queued",
  retrying: "queued",
  running: "running",
  validating: "running",
  replacing: "running",
  complete: "done",
  failed: "failed",
  skipped: "skipped",
  canceled: "skipped",
};

export function queueCounts(): Partial<Record<JobState, number>> {
  const counts: Partial<Record<JobState, number>> = {};
  for (const file of db.files) {
    if (file.job_state) counts[file.job_state] = (counts[file.job_state] ?? 0) + 1;
  }
  return counts;
}
