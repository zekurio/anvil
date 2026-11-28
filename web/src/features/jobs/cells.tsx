import { Show } from "solid-js";

import { formatBytes, formatDateTime, formatRelative } from "../../lib/format";

/** A table cell for a time: "4 min ago", with the exact local time as the tooltip. */
export function TimeCell(props: { iso: string | undefined; now: number }) {
  return (
    <Show when={props.iso} fallback={<span class="av-cell-muted">—</span>}>
      {(iso) => <span title={formatDateTime(iso())}>{formatRelative(iso(), props.now)}</span>}
    </Show>
  );
}

/**
 * A table cell for sizes: the input, and "→ 4.1 GB" once an encode was
 * published. The input is right-aligned in a fixed slot, so sizes and arrows
 * line up down the column.
 */
export function SizeCell(props: { input: number; output?: number }) {
  return (
    <Show when={props.input > 0} fallback={<span class="av-cell-muted">—</span>}>
      <span class="av-size">
        <span class="av-size-in">{formatBytes(props.input)}</span>
        <Show when={props.output && props.output > 0 ? props.output : undefined}>{(output) => <span>→ {formatBytes(output())}</span>}</Show>
      </span>
    </Show>
  );
}

/** A table cell for an error: its first line, truncated by the cell. The whole text is in the job sheet. */
export function ErrorCell(props: { text: string | undefined; failed: boolean }) {
  const first = () => (props.text ?? "").split("\n")[0] ?? "";
  return <span class={props.failed ? "av-cell-error" : "av-cell-muted"}>{first()}</span>;
}
