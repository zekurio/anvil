import { api, keys, type JobActionResult } from "../../api";
import { plural } from "../../lib/format";
import { run } from "../../lib/mutate";
import { toast } from "../../ui";
import { skipSummary } from "./labels";

export type JobActionKind = "retry" | "cancel" | "requeue";

const CHANGED: Record<JobActionKind, (count: number) => string> = {
  retry: (count) => `Queued ${plural(count, "job")} to run again.`,
  cancel: (count) => `Canceled ${plural(count, "job")}.`,
  requeue: (count) => `Queued ${plural(count, "file")} again.`,
};

const UNCHANGED: Record<JobActionKind, string> = {
  retry: "No job was queued to run again.",
  cancel: "No job was canceled.",
  requeue: "No file was queued again.",
};

const FAILED: Record<JobActionKind, string> = {
  retry: "The jobs could not be retried.",
  cancel: "The jobs could not be canceled.",
  requeue: "The files could not be queued again.",
};

/**
 * Runs a retry, cancel or requeue and reports it the way the daemon answered:
 * how many jobs changed, and why the rest were left alone. A request that
 * matched jobs but changed none is a warning, not a success.
 */
export async function jobAction(kind: JobActionKind, work: () => Promise<JobActionResult>): Promise<JobActionResult | undefined> {
  const result = await run(work, { invalidate: [keys.jobs, keys.libraries, keys.status], failure: FAILED[kind] });
  if (!result) return undefined;
  const description = result.skipped.length > 0 ? `Left alone: ${skipSummary(result.skipped)}.` : undefined;
  if (result.changed > 0) toast.success(CHANGED[kind](result.changed), { description });
  else toast.warning(UNCHANGED[kind], { description: description ?? "Nothing matched the selection." });
  return result;
}

export const retryJobs = (ids: number[]) => jobAction("retry", () => api.jobs.retry({ ids }));
export const cancelJobs = (ids: number[]) => jobAction("cancel", () => api.jobs.cancel({ ids }));
export const requeueFiles = (library: string, paths: string[]) => jobAction("requeue", () => api.libraries.requeue(library, paths));
