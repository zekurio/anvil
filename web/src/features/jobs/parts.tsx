import { Show } from "solid-js";

import type { FileStatus, Progress as LiveProgress } from "../../api";
import { formatProgressDetail } from "../../lib/format";
import { Button, Progress, Spinner, StatusBadge, toast } from "../../ui";
import { badgeOf } from "./labels";

/* ---------- JobBadge ---------- */

/** The StatusBadge of a file or job: its grouped status, worded by the exact state or the live percentage. */
export function JobBadge(props: { status: FileStatus; state?: string; progress?: LiveProgress }) {
  const badge = () => badgeOf(props.status, props.state, props.progress);
  return <StatusBadge status={badge().status}>{badge().label}</StatusBadge>;
}

/* ---------- StepProgress ---------- */

/**
 * The live state of a running job: a bar with the percentage and the encoder
 * figures. A step that reports no percentage (probing, publishing) says so
 * instead of showing an empty bar.
 */
export function StepProgress(props: { progress: LiveProgress; label: string }) {
  return (
    <Show
      when={props.progress.percent >= 0}
      fallback={
        <div class="av-step-open">
          <Spinner size={14} label="Running" />
          <span>This step reports no percentage</span>
        </div>
      }
    >
      <Progress value={props.progress.percent} label={props.label} detail={formatProgressDetail(props.progress)} />
    </Show>
  );
}

/* ---------- Copyable ---------- */

/** Copies text and says so. `what` names it: "source path", "error text". */
export async function copyText(text: string, what: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
    toast.success(`Copied the ${what}.`);
  } catch {
    // Clipboard access needs a secure context; over plain HTTP on a LAN the browser refuses.
    toast.error("The browser did not allow copying.", { description: "Select the text and copy it by hand." });
  }
}

/** Mono text with a Copy button: paths and other values people paste into a terminal. The text wraps and stays selectable. */
export function Copyable(props: { text: string; label: string }) {
  return (
    <div class="av-copy">
      <span class="av-copy-text">{props.text}</span>
      <Button size="sm" variant="ghost" icon="copy" label={`Copy the ${props.label}`} onClick={() => copyText(props.text, props.label)} />
    </div>
  );
}
