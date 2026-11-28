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
    kind: library.kind,
    path: library.path,
    flow_id: library.flow_id,
    paused: library.paused,
    priority: library.priority,
    concurrency_limit: library.concurrency_limit,
    scan_interval: library.scan_interval,
    arr: library.arr,
    rules: library.rules,
    rule_fallback: library.rule_fallback,
    media: library.media,
    download: library.download,
  };
}

/** True when starting jobs in this library rewrites the operator's files in place. */
export function replacesInPlace(library: Pick<LibraryWrite, "kind" | "media">): boolean {
  return library.kind === "media" && library.media.replacement_mode === "replace";
}

/** What resuming does, for the confirm dialog of a library that replaces files in place. */
export function resumeConsequence(pending: number): string {
  const start = pending > 0 ? `${formatCount(pending)} queued ${pending === 1 ? "file starts" : "files start"}` : "Jobs start as scans queue files";
  return `${start}. Each encode replaces its original file in place, deleting it.`;
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
