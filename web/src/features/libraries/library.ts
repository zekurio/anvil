// What the Library page's actions share: turning a Library into the body of a
// PUT, and the state of manual scans.

import { createSignal } from "solid-js";

import { api, keys, type Library, type LibraryWrite, type ScanResult } from "../../api";
import { formatCount, joinDot } from "../../lib/format";
import { run } from "../../lib/mutate";

/**
 * The writable part of a library. PUT replaces the whole library, so every
 * change (pause, another flow, new rules) sends all of it, built from the last
 * polled state.
 */
export function toWrite(library: Library): LibraryWrite {
  return {
    name: library.name,
    path: library.path,
    flow_id: library.flow_id,
    paused: library.paused,
    priority: library.priority,
    concurrency_limit: library.concurrency_limit,
    scan_interval: library.scan_interval,
    arr: library.arr,
    rules: library.rules,
    rule_fallback: library.rule_fallback,
    reprocess: library.reprocess,
    stable_for: library.stable_for,
    package_mode: library.package_mode,
    ignorable_globs: library.ignorable_globs,
  };
}

/**
 * True when the flow may rewrite the operator's files in place: it has a
 * Replace file output. A flow that cannot be read counts as one that does,
 * so the confirmation errs toward asking.
 */
export async function flowReplacesInPlace(flowId: number): Promise<boolean> {
  try {
    const flow = await api.flows.get(flowId);
    return flow.graph.nodes.some((node) => node.type === "output.replace");
  } catch {
    return true;
  }
}

/** What resuming does, for the confirm dialog of a library whose flow replaces files in place. */
export function resumeConsequence(pending: number): string {
  const start = pending > 0 ? `${formatCount(pending)} queued ${pending === 1 ? "file starts" : "files start"}` : "Jobs start as scans queue files";
  return `${start}. Where the flow replaces a file, the original is deleted.`;
}

/** "Scan finished · 12 queued · 3 ignored" */
export function scanSummary(result: ScanResult): string {
  return joinDot(
    "Scan finished",
    result.enqueued_jobs > 0 ? `${formatCount(result.enqueued_jobs)} queued` : "nothing new",
    result.skipped_ignored > 0 && `${formatCount(result.skipped_ignored)} ignored`,
    result.skipped_unstable > 0 && `${formatCount(result.skipped_unstable)} still being written`,
    result.denied_jobs > 0 && `${formatCount(result.denied_jobs)} removed by rules`,
  );
}

// Module scope: a scan started from the Add library dialog keeps its busy
// state when the Library page mounts afterwards. The plain set is the guard
// (a signal write is not readable until the next flush); the signal is what
// buttons read.
const active = new Set<string>();
const [scanning, setScanning] = createSignal<ReadonlySet<string>>(new Set());

export function isScanning(name: string): boolean {
  return scanning().has(name);
}

/** Runs a manual scan and reports what it queued. A second call for a library that is being scanned does nothing. */
export async function scanLibrary(name: string): Promise<void> {
  if (active.has(name)) return;
  active.add(name);
  setScanning(new Set(active));
  await run(() => api.libraries.scan(name), {
    invalidate: [keys.libraries, keys.jobs, keys.status],
    success: scanSummary,
    failure: `The scan of ${name} could not run.`,
  });
  active.delete(name);
  setScanning(new Set(active));
}
