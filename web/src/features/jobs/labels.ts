// The words the UI uses for what the daemon reports in identifiers: pipeline
// steps, job states, stream-selection reasons, and the reasons an action left
// a job alone. Anything not listed here is shown as the daemon wrote it.

import type { FileStatus, JobState, PipelineContextDetail, Progress, StreamDecision, StreamSelectionDecision } from "../../api";
import { formatBitrate, formatCount, formatPercent, joinDot, plural } from "../../lib/format";
import type { JobStatus } from "../../ui";

/* ---------- pipeline steps (worker.DefaultPipeline) ---------- */

const STEP_LABELS: Record<string, string> = {
  probe: "Probing",
  "crop-detect": "Detecting crop",
  "audio-cleanup": "Selecting audio",
  "subtitle-cleanup": "Selecting subtitles",
  stage: "Staging",
  "crf-search": "Searching quality",
  encode: "Encoding",
  validate: "Validating",
  publish: "Publishing",
  cleanup: "Cleaning up",
};

/** "Encoding" for "encode"; an unknown step keeps its raw name. */
export function stepLabel(step: string): string {
  return STEP_LABELS[step] ?? step;
}

/* ---------- job states ---------- */

const STATE_LABELS: Record<JobState, string> = {
  pending: "Pending",
  leased: "Assigned to a worker",
  running: "Running",
  validating: "Validating",
  replacing: "Publishing",
  complete: "Complete",
  failed: "Failed",
  retrying: "Waiting to retry",
  skipped: "Skipped",
  canceled: "Canceled",
};

export function stateLabel(state: string): string {
  return STATE_LABELS[state as JobState] ?? state;
}

/** How pkg/webapi groups job states into the statuses the UI filters by. */
export const STATES_OF_STATUS: Record<Exclude<FileStatus, "none">, JobState[]> = {
  queued: ["pending", "leased", "retrying"],
  running: ["running", "validating", "replacing"],
  done: ["complete"],
  failed: ["failed"],
  skipped: ["skipped", "canceled"],
};

/** pkg/domain JobState.Cancelable: anything not terminal. */
export function cancelable(status: FileStatus): boolean {
  return status === "queued" || status === "running";
}

/** What pkg/store lets a retry start from: failed, skipped, canceled, and retrying. */
export function retryable(status: FileStatus, state?: string): boolean {
  return status === "failed" || status === "skipped" || state === "retrying";
}

/** The badge for a file or job: the grouped status, with the exact state or the percentage as its word where that says more. */
export function badgeOf(status: FileStatus, state?: string, progress?: Progress): { status: JobStatus; label?: string } {
  switch (status) {
    case "none":
      return { status: "skipped", label: "Not queued" };
    case "running":
      return { status: "running", label: progress && progress.percent >= 0 ? `Running ${formatPercent(progress.percent)}` : state === "validating" ? "Validating" : state === "replacing" ? "Publishing" : undefined };
    case "queued":
      return { status: "queued", label: state === "retrying" ? "Retrying" : undefined };
    case "skipped":
      return { status: "skipped", label: state === "canceled" ? "Canceled" : undefined };
    default:
      return { status };
  }
}

/** The share of the input an encode saved, or undefined when either size is unknown. */
export function savedPercent(input: number, output: number): number | undefined {
  if (!(input > 0) || !(output > 0)) return undefined;
  return ((input - output) / input) * 100;
}

/** "58% smaller" or "4% larger". */
export function savedLabel(input: number, output: number): string {
  const saved = savedPercent(input, output);
  if (saved === undefined) return "";
  return saved >= 0 ? `${formatPercent(saved)} smaller` : `${formatPercent(-saved)} larger`;
}

/* ---------- stream selection (pkg/domain/selection.go) ---------- */

const DECISION_REASONS: Record<string, string> = {
  language_match: "its language is on the keep list",
  original_language: "it is in the original language",
  unknown_as_original: "its language is unknown and counts as the original",
  cleanup_disabled: "stream cleanup was off for this job",
  keep_all: "the flow keeps every track",
  fallback_keep_all: "no track matched, so all were kept",
  fallback_keep_first: "no track matched, so the first was kept",
  language_not_requested: "its language is not on the keep list",
  commentary: "it is a commentary track",
  forced: "it is a forced track",
  sdh: "it is an SDH track",
  remove_all: "the flow removes every subtitle track",
  bitmap: "it is an image-based subtitle",
};

/** Why one track was kept or dropped, in words; an unknown reason keeps its raw name. */
export function decisionReason(decision: StreamDecision): string {
  return DECISION_REASONS[decision.reason] ?? decision.reason;
}

const tracks = (kind: string) => (kind === "subtitle" ? "subtitle track" : "audio track");
const list = (languages: string[] | undefined) => (languages && languages.length > 0 ? languages.join(", ") : "the requested languages");

/** What a whole selection did and why, as one sentence. */
export function selectionSummary(decision: StreamSelectionDecision): string {
  const kind = tracks(decision.kind);
  const missing = decision.missing_languages?.length ? ` The file has no ${kind} in ${decision.missing_languages.join(", ")}.` : "";
  switch (decision.rule) {
    case "no_streams":
      return `The file has no ${kind}s.`;
    case "cleanup_disabled":
      return `Every ${kind} was kept: ${decision.cleanup_disabled_reason || "stream cleanup was off for this job"}.`;
    case "keep_all":
      return `The flow keeps every ${kind}.`;
    case "remove_all":
      return "The flow removes every subtitle track.";
    case "bitmap_only":
      return "Every subtitle track is image-based, and the flow removes those.";
    case "language_filter":
      return `Kept the ${kind}s in ${list(decision.resolved_languages ?? decision.requested_languages)}.${missing}`;
    case "fallback_keep_all":
      return `No ${kind} is in ${list(decision.resolved_languages ?? decision.requested_languages)}, so every one was kept.`;
    case "fallback_keep_first":
      return `No ${kind} is in ${list(decision.resolved_languages ?? decision.requested_languages)}, so the first one was kept.`;
    case "fallback_fail_job":
      return `No ${kind} is in ${list(decision.resolved_languages ?? decision.requested_languages)}, and the flow fails the job in that case.`;
    default:
      return decision.rule;
  }
}

const LAYOUTS: Record<number, string> = { 1: "1.0", 2: "2.0", 6: "5.1", 8: "7.1" };

/** "5.1" for 6 channels; an unusual count reads "4 ch". */
export function channelLayout(channels: number | undefined): string {
  if (!channels) return "";
  return LAYOUTS[channels] ?? `${channels} ch`;
}

/* ---------- attempt-event payloads ---------- */

/** pkg/domain AudioTrackPlan: how one kept audio track is written. The payload of the "audio-output" event is `{ tracks: AudioTrackPlan[] }`. */
export interface AudioTrackPlan {
  stream_index: number;
  action: "copy" | "encode";
  source_codec?: string;
  source_channels?: number;
  codec?: string;
  channels?: number;
  layout?: string;
  bitrate_kbps?: number;
  note?: string;
}

/** "copied", "encoded to eac3 5.1 at 576 kb/s", or "downmixed to opus 2.0 at 128 kb/s". */
export function audioOutput(plan: AudioTrackPlan): string {
  if (plan.action !== "encode") return joinDot("copied", plan.note);
  const downmixed = (plan.channels ?? 0) < (plan.source_channels ?? 0);
  const target = [plan.codec, channelLayout(plan.channels)].filter(Boolean).join(" ");
  const rate = plan.bitrate_kbps ? ` at ${formatBitrate(plan.bitrate_kbps)}` : "";
  return joinDot(`${downmixed ? "downmixed" : "encoded"} to ${target}${rate}`, plan.note);
}

/** pkg/search selectionPayload: the payload of the "crf-selection" event. */
export interface CrfSelection {
  mode: "search" | "fixed";
  crf: number;
  metric?: string;
  score?: number;
  trials?: number;
  skip_reason?: string;
  forced_reason?: string;
}

export interface QualitySummary {
  /** "Fixed CRF 24", "CRF 31 · VMAF 95.2 · 6 trials", or "Video copied, not encoded". */
  headline: string;
  /** The daemon's reason for a skipped or forced encode. */
  note?: string;
}

const metricLabel = (metric: string | undefined) => (metric ? metric.toUpperCase() : "");

export function qualityOfSelection(selection: CrfSelection): QualitySummary {
  if (selection.skip_reason) return { headline: "Video copied, not encoded", note: selection.skip_reason };
  if (selection.mode === "fixed") return { headline: `Fixed CRF ${selection.crf}` };
  const headline = joinDot(`CRF ${selection.crf}`, selection.score ? `${metricLabel(selection.metric)} ${selection.score.toFixed(1)}` : "", selection.trials ? plural(selection.trials, "trial") : "");
  return { headline, note: selection.forced_reason };
}

/** The same summary from the job's checkpoint, for a job whose attempt events carry no crf-selection (a resumed search). */
export function qualityOfContext(context: PipelineContextDetail): QualitySummary | undefined {
  if (context.search_skip_reason) return { headline: "Video copied, not encoded", note: context.search_skip_reason };
  if (!context.search_crf) return undefined;
  const score = context.search_metric === "xpsnr" ? context.search_xpsnr : context.search_vmaf;
  return { headline: joinDot(`CRF ${context.search_crf}`, score ? `${metricLabel(context.search_metric)} ${score.toFixed(1)}` : "") };
}

/* ---------- reasons an action left a job alone ---------- */

// pkg/store CancelSkipReason, plus what a retry or requeue may answer.
const SKIP_REASONS: Record<string, string> = {
  already_terminal: "already finished",
  publish_in_progress: "being published, which cannot be interrupted",
  state_changed: "changed state before the action ran",
  not_found: "no longer exists",
  already_queued: "already queued",
  already_complete: "already complete",
  running: "running",
};

export function skipReason(reason: string): string {
  return SKIP_REASONS[reason] ?? reason;
}

/** "2 already finished · 1 being published, which cannot be interrupted" */
export function skipSummary(skipped: { reason: string }[]): string {
  const counts = new Map<string, number>();
  for (const skip of skipped) counts.set(skip.reason, (counts.get(skip.reason) ?? 0) + 1);
  return joinDot(...[...counts].map(([reason, count]) => `${formatCount(count)} ${skipReason(reason)}`));
}
