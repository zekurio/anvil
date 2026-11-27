import { createSignal, Show } from "solid-js";

import type { FileStatus } from "../../api";
import { formatCount, plural } from "../../lib/format";
import { Button, confirm } from "../../ui";
import { cancelJobs, requeueFiles, retryJobs } from "./actions";
import { cancelable, retryable } from "./labels";

export interface BulkJob {
  id: number;
  status: FileStatus;
  state?: string;
}

export interface BulkRequeue {
  library: string;
  /** Library-relative paths of the selected files that can be queued again: everything not queued or running. */
  paths: string[];
  /** How many of those were already processed. */
  done: number;
}

export interface JobBulkBarProps {
  /** How many rows are selected. */
  count: number;
  /** What a row is: "file", "job". */
  noun: string;
  /** The jobs behind the selected rows. Rows without a job are left out. */
  jobs: BulkJob[];
  /** Offers "Queue again" for a selection of library files. */
  requeue?: BulkRequeue;
  onClear: () => void;
}

/**
 * Replaces the toolbar while rows are selected. It offers only what can apply
 * to the selection, with the number of rows each action will reach, and
 * clears the selection once an action ran.
 */
export function JobBulkBar(props: JobBulkBarProps) {
  const [busy, setBusy] = createSignal<"retry" | "cancel" | "requeue" | null>(null);
  const retryIds = () => props.jobs.filter((job) => retryable(job.status, job.state)).map((job) => job.id);
  const cancelIds = () => props.jobs.filter((job) => cancelable(job.status)).map((job) => job.id);
  const running = () => props.jobs.filter((job) => job.status === "running").length;
  const failedOnly = () => props.jobs.every((job) => !retryable(job.status, job.state) || job.status === "failed");

  const act = async (kind: "retry" | "cancel" | "requeue", work: () => Promise<unknown>) => {
    setBusy(kind);
    const result = await work();
    setBusy(null);
    if (result) props.onClear();
  };

  const cancel = async () => {
    const ids = cancelIds();
    if (running() > 0) {
      const agreed = await confirm({
        title: `Cancel ${plural(ids.length, "job")}?`,
        description: `${running() === 1 ? "1 of them is running: its encode stops" : `${formatCount(running())} of them are running: their encodes stop`} and what was done so far is discarded. Source files are not changed.`,
        confirmLabel: `Cancel ${plural(ids.length, "job")}`,
        cancelLabel: "Keep them",
        danger: true,
      });
      if (!agreed) return;
    }
    await act("cancel", () => cancelJobs(ids));
  };

  const requeue = async () => {
    const target = props.requeue;
    if (!target) return;
    if (target.done > 0) {
      const agreed = await confirm({
        title: `Queue ${plural(target.paths.length, "file")} again?`,
        description: `${target.done === 1 ? "1 of them was already processed" : `${formatCount(target.done)} of them were already processed`}. Queueing again starts a new job for every file and runs the whole flow on it once more, also for files that are done.`,
        confirmLabel: `Queue ${plural(target.paths.length, "file")} again`,
      });
      if (!agreed) return;
    }
    await act("requeue", () => requeueFiles(target.library, target.paths));
  };

  return (
    <div class="av-bulk" role="toolbar" aria-label="Actions for the selected rows">
      <span>{plural(props.count, props.noun)} selected</span>
      <span class="av-spacer" />
      <Show when={props.requeue && props.requeue.paths.length > 0 ? props.requeue : undefined}>
        {(target) => (
          <Button size="sm" icon="play" title="Starts a new job for each file, also for files that are done" busy={busy() === "requeue"} disabled={busy() !== null} onClick={requeue}>
            Queue {formatCount(target().paths.length)} again
          </Button>
        )}
      </Show>
      <Show when={retryIds().length > 0}>
        <Button size="sm" icon="rotate-ccw" busy={busy() === "retry"} disabled={busy() !== null} onClick={() => act("retry", () => retryJobs(retryIds()))}>
          Retry {failedOnly() ? `${formatCount(retryIds().length)} failed` : plural(retryIds().length, "job")}
        </Button>
      </Show>
      <Show when={cancelIds().length > 0}>
        <Button size="sm" icon="square" busy={busy() === "cancel"} disabled={busy() !== null} onClick={cancel}>
          Cancel {plural(cancelIds().length, "job")}
        </Button>
      </Show>
      <Button size="sm" variant="ghost" disabled={busy() !== null} onClick={() => props.onClear()}>
        Clear selection
      </Button>
    </div>
  );
}
