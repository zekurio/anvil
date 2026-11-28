import { createMemo, For, Show } from "solid-js";

import type { AttemptEventDetail, JobDetail, StreamDecision, StreamKind, StreamSelectionDecision } from "../../api";
import { formatPercent, joinDot } from "../../lib/format";
import { Icon } from "../../ui";
import { audioOutput, channelLayout, decisionReason, qualityOfContext, qualityOfSelection, selectionSummary, type AudioTrackPlan, type CrfSelection, type QualitySummary } from "./labels";

/** The newest event with this name, looking at the latest attempt first. */
function latestEvent(detail: JobDetail, name: string): AttemptEventDetail | undefined {
  const attempts = [...detail.attempts].sort((a, b) => b.number - a.number);
  for (const attempt of attempts) {
    const found = attempt.events.findLast((event) => event.name === name);
    if (found) return found;
  }
  return undefined;
}

/* ---------- Streams ---------- */

interface KindSelection {
  kind: StreamKind;
  decision?: StreamSelectionDecision;
  /** Set when the record exists but could not be decoded. */
  error?: string;
}

const KIND_TITLES: Record<StreamKind, string> = { audio: "Audio", subtitle: "Subtitles" };

/** The latest selection per kind. The job's own list is preferred; attempt events are the fallback. */
function selections(detail: JobDetail): KindSelection[] {
  const newest = new Map<StreamKind, { at: string; decision?: StreamSelectionDecision; error?: string }>();
  const offer = (kind: StreamKind | undefined, at: string, decision: StreamSelectionDecision | undefined, error?: string) => {
    if (!kind) return;
    const known = newest.get(kind);
    if (!known || at >= known.at) newest.set(kind, { at, decision, error });
  };
  for (const record of detail.stream_selection ?? []) offer(record.decision?.kind, record.recorded_at, record.decision, record.decision_error);
  if (newest.size === 0) {
    for (const attempt of detail.attempts) for (const event of attempt.events) offer(event.stream_selection?.kind, event.created_at, event.stream_selection);
  }
  return (["audio", "subtitle"] as const).flatMap((kind) => {
    const found = newest.get(kind);
    return found ? [{ kind, decision: found.decision, error: found.error }] : [];
  });
}

/** True when the job recorded any stream selection. */
export function hasStreams(detail: JobDetail): boolean {
  return selections(detail).length > 0 || (detail.stream_selection ?? []).some((record) => record.decision_error);
}

/** What happened to every audio and subtitle track: kept or dropped, why, and how kept audio was written. */
export function JobStreamsSection(props: { detail: JobDetail }) {
  const kinds = createMemo(() => selections(props.detail));
  const unreadable = () => (props.detail.stream_selection ?? []).filter((record) => !record.decision && record.decision_error);
  const outputs = createMemo(() => {
    const payload = latestEvent(props.detail, "audio-output")?.payload?.json as { tracks?: AudioTrackPlan[] } | undefined;
    return new Map((payload?.tracks ?? []).map((plan) => [plan.stream_index, plan]));
  });
  return (
    <>
      <For each={unreadable()} keyed={false}>
        {(record) => (
          <p class="av-note av-note-warn">
            The selection record of attempt {record().attempt_number} could not be read: {record().decision_error}
          </p>
        )}
      </For>
      <For each={kinds()} keyed={(entry) => entry.kind}>
        {(entry) => (
          <div class="av-sub">
            <h4 class="av-sub-title">{KIND_TITLES[entry().kind]}</h4>
            <Show when={entry().decision}>
              {(decision) => (
                <>
                  <p class="av-note">{selectionSummary(decision())}</p>
                  <Show when={(decision().streams ?? []).length > 0}>
                    <ul class="av-streams">
                      <For each={decision().streams ?? []} keyed={(stream) => stream.index}>{(stream) => <StreamRow stream={stream()} output={entry().kind === "audio" ? outputs().get(stream().index) : undefined} />}</For>
                    </ul>
                  </Show>
                </>
              )}
            </Show>
          </div>
        )}
      </For>
    </>
  );
}

function StreamRow(props: { stream: StreamDecision; output?: AudioTrackPlan }) {
  const language = () => props.stream.resolved_language || props.stream.language || "und";
  const flags = () => [props.stream.default && "default", props.stream.forced && "forced"].filter(Boolean).join(" ");
  return (
    <li class="av-stream">
      <span class="av-stream-id">{props.stream.index}</span>
      <div class="av-stream-body">
        <div class="av-stream-facts">
          {[props.stream.codec || "unknown", channelLayout(props.stream.channels), language(), flags()].filter(Boolean).join(" ")}
          <Show when={props.stream.title}>
            <span class="av-stream-title">{props.stream.title}</span>
          </Show>
        </div>
        <div class="av-stream-why">{joinDot(decisionReason(props.stream), props.stream.kept && props.output ? audioOutput(props.output) : "")}</div>
      </div>
      <span class={["av-stream-state", { "av-stream-dropped": !props.stream.kept }]}>
        <Icon name={props.stream.kept ? "check" : "minus"} size={12} strokeWidth={2.5} />
        {props.stream.kept ? "Kept" : "Dropped"}
      </span>
    </li>
  );
}

/* ---------- Quality ---------- */

function quality(detail: JobDetail): QualitySummary | undefined {
  const selection = latestEvent(detail, "crf-selection")?.payload?.json as CrfSelection | undefined;
  if (selection && typeof selection.crf === "number") return qualityOfSelection(selection);
  return detail.pipeline_context ? qualityOfContext(detail.pipeline_context) : undefined;
}

function crop(detail: JobDetail): string | undefined {
  const context = detail.pipeline_context;
  if (!context?.crop_evaluated) return undefined;
  if (context.crop_filter) return joinDot(context.crop_filter, context.crop_retained_area_percent ? `keeps ${formatPercent(context.crop_retained_area_percent, 1)} of the frame` : "");
  if (context.crop_rejection_reason) return `not cropped: ${context.crop_rejection_reason}`;
  return "not cropped";
}

/** True when the job got far enough to choose a quality or a crop. */
export function hasQuality(detail: JobDetail): boolean {
  return quality(detail) !== undefined || crop(detail) !== undefined;
}

/** The CRF the encode used and how it was arrived at, plus the crop. */
export function JobQualitySection(props: { detail: JobDetail }) {
  const chosen = createMemo(() => quality(props.detail));
  return (
    <dl class="av-dl av-dl-sheet">
      <Show when={chosen()}>
        {(summary) => (
          <>
            <dt>Video quality</dt>
            <dd>
              <span class="av-mono">{summary().headline}</span>
              <Show when={summary().note}>
                <span class="av-dl-note">{summary().note}</span>
              </Show>
            </dd>
          </>
        )}
      </Show>
      <Show when={crop(props.detail)}>
        {(text) => (
          <>
            <dt>Crop</dt>
            <dd class="av-mono">{text()}</dd>
          </>
        )}
      </Show>
    </dl>
  );
}
