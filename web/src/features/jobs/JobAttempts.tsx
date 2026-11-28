import { createMemo, createSignal, For, Show } from "solid-js";

import type { AttemptDetail, AttemptEventDetail, AttemptState, ProcessOutputDetail } from "../../api";
import { formatBytes, formatDateTime, formatDuration, joinDot } from "../../lib/format";
import { CommandPreview, Icon, StatusBadge, type IconName, type JobStatus } from "../../ui";
import { stepLabel } from "./labels";
import { Copyable } from "./parts";

const ATTEMPT_BADGE: Record<AttemptState, { status: JobStatus; label: string }> = {
  running: { status: "running", label: "Running" },
  succeeded: { status: "done", label: "Succeeded" },
  failed: { status: "failed", label: "Failed" },
  canceled: { status: "skipped", label: "Canceled" },
};

const ARTIFACT_LABELS: Record<string, string> = {
  "process-output": "Process output",
  "flow-resolution": "Flow",
  "crop-selection": "Crop",
  "stream-selection": "Stream selection",
  "audio-output": "Audio output",
  "crf-selection": "Quality selection",
};

/** "note" is a record that stands between steps: the daemon writes most artifacts after the step that produced them. */
type StepOutcome = "finished" | "failed" | "running" | "unfinished" | "note";

interface TimelineStep {
  /** The id of the event that opened the step. */
  id: number;
  name: string;
  startedAt: string;
  endedAt?: string;
  outcome: StepOutcome;
  message: string;
  artifacts: AttemptEventDetail[];
}

const pad = (n: number) => String(n).padStart(2, "0");

/** A local time of day with seconds, for a timeline: "13:04:10". */
function clock(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

function seconds(from: string, to: string | undefined): number | undefined {
  if (!to) return undefined;
  const span = (Date.parse(to) - Date.parse(from)) / 1000;
  return Number.isFinite(span) && span >= 0 ? span : undefined;
}

/**
 * Folds an attempt's events into steps: block_started opens one,
 * block_finished or block_failed closes it, and artifacts recorded in between
 * belong to it. An artifact outside any step stands on its own.
 */
function timeline(attempt: AttemptDetail): TimelineStep[] {
  const steps: TimelineStep[] = [];
  let open: TimelineStep | undefined;
  for (const event of attempt.events) {
    switch (event.type) {
      case "block_started":
        open = { id: event.id, name: event.name, startedAt: event.created_at, outcome: attempt.state === "running" ? "running" : "unfinished", message: "", artifacts: [] };
        steps.push(open);
        break;
      case "block_finished":
      case "block_failed": {
        const step = open?.name === event.name ? open : undefined;
        const outcome: StepOutcome = event.type === "block_failed" ? "failed" : "finished";
        if (step) Object.assign(step, { endedAt: event.created_at, outcome, message: event.message });
        else steps.push({ id: event.id, name: event.name, startedAt: event.created_at, endedAt: event.created_at, outcome, message: event.message, artifacts: [] });
        open = undefined;
        break;
      }
      default:
        if (open) open.artifacts.push(event);
        // Its own line: the message is the content, and only a command or an unreadable payload needs more.
        else steps.push({ id: event.id, name: event.name, startedAt: event.created_at, outcome: "note", message: event.message, artifacts: event.process_output || event.payload_error ? [event] : [] });
    }
  }
  // Only the last open step of a running attempt is still running.
  steps.forEach((step, index) => {
    if (step.outcome === "running" && index !== steps.length - 1) step.outcome = "unfinished";
  });
  return steps;
}

const OUTCOME: Record<StepOutcome, { icon: IconName; word: string; spin?: boolean }> = {
  finished: { icon: "circle-check", word: "finished" },
  failed: { icon: "circle-x", word: "failed" },
  running: { icon: "loader-circle", word: "running", spin: true },
  unfinished: { icon: "minus", word: "did not finish" },
  note: { icon: "info", word: "recorded" },
};

/** Every attempt of a job, newest first. The newest starts open. */
export function JobAttemptsSection(props: { attempts: AttemptDetail[] }) {
  const ordered = createMemo(() => [...props.attempts].sort((a, b) => b.number - a.number));
  return (
    <div class="av-attempts">
      <For each={ordered()} keyed={(attempt) => attempt.id}>{(attempt, index) => <Attempt attempt={attempt()} startOpen={index() === 0} />}</For>
    </div>
  );
}

function Attempt(props: { attempt: AttemptDetail; startOpen: boolean }) {
  const [open, setOpen] = createSignal(() => props.startOpen);
  const badge = () => ATTEMPT_BADGE[props.attempt.state] ?? { status: "queued" as const, label: props.attempt.state };
  const took = () => seconds(props.attempt.started_at, props.attempt.finished_at);
  const steps = createMemo(() => timeline(props.attempt));
  return (
    <div class="av-attempt">
      <button type="button" class="av-attempt-head" aria-expanded={open() ? "true" : "false"} onClick={() => setOpen(!open())}>
        <span class="av-attempt-n">Attempt {props.attempt.number}</span>
        <StatusBadge status={badge().status}>{badge().label}</StatusBadge>
        <span class="av-attempt-meta">{joinDot(took() !== undefined && formatDuration(took() ?? 0), props.attempt.worker_id)}</span>
        <Icon name={open() ? "chevron-up" : "chevron-down"} size={14} />
      </button>
      <Show when={open()}>
        <div class="av-attempt-body">
          <div class="av-attempt-times">{joinDot(`started ${formatDateTime(props.attempt.started_at)}`, props.attempt.finished_at ? `finished ${formatDateTime(props.attempt.finished_at)}` : "still running")}</div>
          <Show when={props.attempt.error}>
            <pre class="av-pre av-pre-danger">{props.attempt.error}</pre>
          </Show>
          <ol class="av-timeline">
            <For each={steps()} keyed={(step) => step.id} fallback={<li class="av-note">This attempt recorded no events.</li>}>
              {(step) => <Step step={step()} />}
            </For>
          </ol>
        </div>
      </Show>
    </div>
  );
}

function Step(props: { step: TimelineStep }) {
  const outcome = () => OUTCOME[props.step.outcome];
  const took = () => seconds(props.step.startedAt, props.step.endedAt);
  // An artifact recorded outside a step is named by what it is; a step by what it does.
  const title = () => ARTIFACT_LABELS[props.step.name] ?? stepLabel(props.step.name);
  return (
    <li class={["av-tl", `av-tl-${props.step.outcome}`]}>
      <Icon class="av-tl-icon" name={outcome().icon} size={14} spin={outcome().spin} label={outcome().word} />
      <div class="av-tl-body">
        <div class="av-tl-head">
          <span class="av-tl-name">{title()}</span>
          <span class="av-tl-time">{joinDot(clock(props.step.startedAt), props.step.outcome === "note" ? "" : took() !== undefined ? formatDuration(took() ?? 0) : outcome().word)}</span>
        </div>
        <Show when={props.step.message}>
          <div class={["av-tl-message", { "av-tl-error": props.step.outcome === "failed" }]}>{props.step.message}</div>
        </Show>
        <For each={props.step.artifacts} keyed={(event) => event.id}>{(event) => <Artifact event={event()} standalone={props.step.outcome === "note"} />}</For>
      </div>
    </li>
  );
}

function Artifact(props: { event: AttemptEventDetail; standalone: boolean }) {
  return (
    <div class="av-artifact">
      <Show
        when={props.event.process_output}
        fallback={
          // Inside a standalone record the line above already carries the message.
          <Show when={!props.standalone}>
            <div class="av-artifact-line">
              <span class="av-artifact-name">{ARTIFACT_LABELS[props.event.name] ?? props.event.name}</span> {props.event.message}
            </div>
          </Show>
        }
      >
        {(output) => <ProcessOutput output={output()} />}
      </Show>
      <Show when={props.event.payload_error}>
        <div class="av-tl-message av-tl-error">The recorded payload could not be read: {props.event.payload_error}</div>
      </Show>
    </div>
  );
}

/** One external command: what ran, how it ended, and where its output was written. The API returns log paths, not the logs. */
function ProcessOutput(props: { output: ProcessOutputDetail }) {
  const args = () => props.output.command.slice(1);
  return (
    <>
      <CommandPreview prefix={props.output.command[0]} args={args()} label={`Command of the ${props.output.step} step`} />
      <div class={["av-artifact-line", { "av-tl-error": props.output.exit_code !== 0 }]}>{joinDot(`exit code ${props.output.exit_code}`, formatDuration(props.output.duration_ms / 1000), props.output.error)}</div>
      <LogPath name="stdout" path={props.output.stdout_path} bytes={props.output.stdout_bytes} />
      <LogPath name="stderr" path={props.output.stderr_path} bytes={props.output.stderr_bytes} />
    </>
  );
}

function LogPath(props: { name: string; path: string; bytes: number }) {
  return (
    <Show when={props.path}>
      <div class="av-logpath">
        <span class="av-artifact-name">
          {props.name} {formatBytes(props.bytes)}
        </span>
        <Copyable text={props.path} label={`${props.name} log path`} />
      </div>
    </Show>
  );
}
