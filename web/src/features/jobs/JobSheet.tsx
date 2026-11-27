import { createSignal, Errored, For, Loading, Show } from "solid-js";

import { api, createQuery, keys, POLL_LIST, type FileFacts, type HDRKind, type JobDetail, type StreamFacts } from "../../api";
import { formatBitrate, formatBytes, formatCount, formatDateTime, formatDuration, formatRelative, formatSizeChange, joinDot } from "../../lib/format";
import { useAppData } from "../../shell/data";
import { Button, confirm, ErrorState, LoadingState, SheetSection, SideSheet } from "../../ui";
import { cancelJobs, retryJobs } from "./actions";
import { JobAttemptsSection } from "./JobAttempts";
import { JobFlowSection, type NodeCatalog } from "./JobFlow";
import { hasQuality, hasStreams, JobQualitySection, JobStreamsSection } from "./JobStreams";
import { cancelable, channelLayout, retryable, savedLabel, stateLabel, stepLabel } from "./labels";
import { Copyable, copyText, JobBadge, StepProgress } from "./parts";

import "../layout/layout.css";
import "./jobs.css";

export interface JobSheetProps {
  jobId: number;
  /** The file name, when the caller knows it: the title while the job loads. */
  title?: string;
  onClose: () => void;
}

const fileName = (path: string) => path.slice(path.lastIndexOf("/") + 1);

/**
 * Everything about one job, in the SideSheet: summary, the probed file, how
 * the flow resolved, what happened to each track, the chosen quality, and
 * every attempt with its events. The Queue page and the Library page share it.
 */
export function JobSheet(props: JobSheetProps) {
  const detail = createQuery(
    () => keys.job(props.jobId),
    () => api.jobs.get(props.jobId),
    { poll: POLL_LIST },
  );
  // Asked for with the job, not after it: the map of the flow needs the node
  // catalog for its node names, and the sheet does not wait for it.
  const catalog = createQuery(
    () => keys.nodeTypes,
    async (): Promise<NodeCatalog> => new Map((await api.nodeTypes()).node_types.map((type) => [type.id, type])),
  );
  const placeholder = () => props.title ?? `Job ${props.jobId}`;
  return (
    <Errored
      fallback={(error, reset) => (
        <SideSheet title={placeholder()} icon="file-video" onClose={props.onClose}>
          <ErrorState title="This job could not be loaded" error={error()} onRetry={reset} />
        </SideSheet>
      )}
    >
      {/* `on`: opening another job shows the loading sheet at once instead of the previous job under a new name. */}
      <Loading
        on={props.jobId}
        fallback={
          <SideSheet title={placeholder()} icon="file-video" onClose={props.onClose}>
            <LoadingState label="Loading job" />
          </SideSheet>
        }
      >
        <LoadedSheet detail={detail()} catalog={catalog()} onClose={props.onClose} />
      </Loading>
    </Errored>
  );
}

function LoadedSheet(props: { detail: JobDetail; catalog: NodeCatalog; onClose: () => void }) {
  const { status } = useAppData();
  const [busy, setBusy] = createSignal<"cancel" | "retry" | null>(null);
  const job = () => props.detail.job;
  // The status poll is the fresher source; the detail's own figure covers the gap before it lists the job.
  const progress = () => status().running.find((running) => running.job_id === job().id) ?? props.detail.progress;
  const canCancel = () => cancelable(props.detail.status);
  const canRetry = () => retryable(props.detail.status, job().state);

  const cancel = async () => {
    if (props.detail.status === "running" && !(await confirm({ title: "Cancel this job?", description: "The encode stops and what it has done so far is discarded. The source file is not changed.", confirmLabel: "Cancel job", cancelLabel: "Keep running", danger: true }))) return;
    setBusy("cancel");
    await cancelJobs([job().id]);
    setBusy(null);
  };

  const retry = async () => {
    setBusy("retry");
    await retryJobs([job().id]);
    setBusy(null);
  };

  return (
    <SideSheet
      title={fileName(job().path)}
      subtitle={`Job ${job().id}`}
      icon="file-video"
      onClose={props.onClose}
      footer={
        canCancel() || canRetry() ? (
          <>
            <Show when={canCancel()}>
              <Button size="sm" variant="danger" icon="square" busy={busy() === "cancel"} disabled={busy() !== null} onClick={cancel}>
                Cancel job
              </Button>
            </Show>
            <span class="av-spacer" />
            <Show when={canRetry()}>
              <Button size="sm" icon="rotate-ccw" busy={busy() === "retry"} disabled={busy() !== null} onClick={retry}>
                Retry job
              </Button>
            </Show>
          </>
        ) : undefined
      }
    >
      <div class="av-jobhead">
        <JobBadge status={props.detail.status} state={job().state} progress={progress()} message={job().last_error} />
        <span class="av-muted">in</span>
        <a class="av-link" href={`/libraries/${encodeURIComponent(job().library)}`}>
          {job().library}
        </a>
      </div>
      <Show when={props.detail.status === "running" ? progress() : undefined}>
        {(live) => (
          <div class="av-jobprogress">
            <div class="av-jobprogress-step">{stepLabel(live().step)}</div>
            <StepProgress progress={live()} label={fileName(job().path)} />
          </div>
        )}
      </Show>
      <SheetSection title="Summary">
        <Summary detail={props.detail} />
      </SheetSection>
      <Show when={props.detail.file}>
        {(file) => (
          <SheetSection title="File" collapsible>
            <Facts file={file()} />
          </SheetSection>
        )}
      </Show>
      <Show when={props.detail.flow}>
        {(flow) => (
          <SheetSection title="Flow" collapsible>
            <JobFlowSection flow={flow()} catalog={props.catalog} />
          </SheetSection>
        )}
      </Show>
      <Show when={hasStreams(props.detail)}>
        <SheetSection title="Streams" collapsible>
          <JobStreamsSection detail={props.detail} />
        </SheetSection>
      </Show>
      <Show when={hasQuality(props.detail)}>
        <SheetSection title="Quality" collapsible>
          <JobQualitySection detail={props.detail} />
        </SheetSection>
      </Show>
      <SheetSection title={`Attempts (${formatCount(props.detail.attempts.length)})`} collapsible>
        <Show when={props.detail.attempts.length > 0} fallback={<p class="av-note">No attempt yet. The job starts when a worker is free{job().state === "pending" ? " and its library is not paused" : ""}.</p>}>
          <JobAttemptsSection attempts={props.detail.attempts} />
        </Show>
      </SheetSection>
    </SideSheet>
  );
}

function Time(props: { iso: string; now: number }) {
  return (
    <>
      <span class="av-mono">{formatDateTime(props.iso)}</span> <span class="av-muted">{formatRelative(props.iso, props.now)}</span>
    </>
  );
}

function Summary(props: { detail: JobDetail }) {
  const job = () => props.detail.job;
  const now = () => Date.parse(props.detail.server_time);
  // The job records its input size when it finishes; until then the probe knows it.
  const input = () => props.detail.input_size_bytes || (props.detail.file?.size_bytes ?? 0);
  const output = () => props.detail.output_size_bytes;
  const publish = () => props.detail.publish_operation;
  // Before a publish the destination is only planned. It is worth showing while the job can still get there.
  const planned = () => (!publish() && props.detail.destination_path && (cancelable(props.detail.status) || props.detail.status === "failed") ? props.detail.destination_path : undefined);
  return (
    <>
      <dl class="av-dl av-dl-sheet">
        <dt>State</dt>
        <dd>{stateLabel(job().state)}</dd>
        <dt>Attempts</dt>
        <dd class="av-mono">{formatCount(job().attempt_count)}</dd>
        <dt>Created</dt>
        <dd>
          <Time iso={job().created_at} now={now()} />
        </dd>
        <dt>Updated</dt>
        <dd>
          <Time iso={job().updated_at} now={now()} />
        </dd>
        <Show when={job().completed_at}>
          {(completed) => (
            <>
              <dt>Completed</dt>
              <dd>
                <Time iso={completed()} now={now()} />
              </dd>
            </>
          )}
        </Show>
        <Show when={input() > 0}>
          <dt>Size</dt>
          <dd class="av-mono">{output() > 0 ? joinDot(formatSizeChange(input(), output()), savedLabel(input(), output())) : formatBytes(input())}</dd>
        </Show>
        <dt>Source</dt>
        <dd>
          <Copyable text={props.detail.input_path || job().path} label="source path" />
        </dd>
        <Show when={planned()}>
          {(destination) => (
            <>
              <dt>Destination</dt>
              <dd>
                <Copyable text={destination()} label="destination path" />
                <span class="av-dl-note">Planned. Nothing is written there until the job publishes.</span>
              </dd>
            </>
          )}
        </Show>
        <Show when={publish()}>
          {(operation) => (
            <>
              <dt>Destination</dt>
              <dd>
                <Copyable text={props.detail.destination_path || operation().destination_path} label="destination path" />
                <span class="av-dl-note">{joinDot(operation().kind, `mode ${operation().mode}`, `stage ${operation().stage}`)}</span>
                <Show when={operation().conflict_description}>
                  <span class="av-dl-note av-danger">{operation().conflict_description}</span>
                </Show>
              </dd>
            </>
          )}
        </Show>
      </dl>
      <Show when={job().last_error}>
        <div class="av-sub">
          <h4 class="av-sub-title">{props.detail.status === "failed" ? "Last error" : "Last message"}</h4>
          <pre class={["av-pre", { "av-pre-danger": props.detail.status === "failed" }]}>{job().last_error}</pre>
          <div class="av-row">
            <Button size="sm" icon="copy" onClick={() => copyText(job().last_error, props.detail.status === "failed" ? "error text" : "message")}>
              {props.detail.status === "failed" ? "Copy error text" : "Copy message"}
            </Button>
          </div>
        </div>
      </Show>
    </>
  );
}

const HDR_LABELS: Record<HDRKind, string> = { "": "SDR", hdr10: "HDR10", hlg: "HLG", dolby_vision: "Dolby Vision" };

function Facts(props: { file: FileFacts }) {
  return (
    <>
      <dl class="av-dl av-dl-sheet">
        <dt>Container</dt>
        <dd class="av-mono">{props.file.container || "unknown"}</dd>
        <dt>Video</dt>
        <dd class="av-mono">{props.file.video_codec || "unknown"}</dd>
        <dt>Resolution</dt>
        <dd class="av-mono">{props.file.width > 0 ? `${props.file.width}×${props.file.height}` : "unknown"}</dd>
        <dt>Dynamic range</dt>
        <dd>{HDR_LABELS[props.file.hdr] ?? props.file.hdr}</dd>
        <dt>Bitrate</dt>
        <dd class="av-mono">{props.file.bitrate_kbps > 0 ? formatBitrate(props.file.bitrate_kbps) : "unknown"}</dd>
        <dt>Duration</dt>
        <dd class="av-mono">{props.file.duration_seconds > 0 ? formatDuration(props.file.duration_seconds) : "unknown"}</dd>
        <dt>Size</dt>
        <dd class="av-mono">{formatBytes(props.file.size_bytes)}</dd>
      </dl>
      <Tracks title="Audio tracks" tracks={props.file.audio} none="No audio track." />
      <Tracks title="Subtitle tracks" tracks={props.file.subtitles} none="No subtitle track." />
    </>
  );
}

function Tracks(props: { title: string; tracks: StreamFacts[]; none: string }) {
  return (
    <div class="av-sub">
      <h4 class="av-sub-title">{props.title}</h4>
      <Show when={props.tracks.length > 0} fallback={<p class="av-note">{props.none}</p>}>
        <ul class="av-lines av-lines-mono">
          <For each={props.tracks} keyed={(track) => track.index}>
            {(track) => (
              <li>
                {[track().index, track().codec, channelLayout(track().channels), track().language || "und", track().default && "default", track().forced && "forced"].filter((part) => part !== "" && part !== false).join(" ")}
                <Show when={track().title}>
                  <span class="av-stream-title">{track().title}</span>
                </Show>
              </li>
            )}
          </For>
        </ul>
      </Show>
    </div>
  );
}
