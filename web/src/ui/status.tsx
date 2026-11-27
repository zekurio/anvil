import type { JSX } from "@solidjs/web";
import { For, Show } from "solid-js";

import { Icon, type IconName } from "./icon";

/* ---------- StatusBadge ---------- */

export type JobStatus = "queued" | "running" | "done" | "failed" | "skipped" | "warning";

const STATUS: Record<JobStatus, { icon: IconName; label: string; spin?: boolean }> = {
  queued: { icon: "clock", label: "Queued" },
  running: { icon: "loader-circle", label: "Running", spin: true },
  done: { icon: "circle-check", label: "Done" },
  failed: { icon: "circle-x", label: "Failed" },
  skipped: { icon: "minus", label: "Skipped" },
  warning: { icon: "triangle-alert", label: "Warning" },
};

export interface StatusBadgeProps {
  status: JobStatus;
  /** Overrides the word, e.g. "Running 62%". */
  children?: JSX.Element;
}

/** The state of a file or job: icon plus word, always both. */
export function StatusBadge(props: StatusBadgeProps) {
  const status = () => STATUS[props.status] ?? STATUS.queued;
  return (
    <span class={["av-badge", `av-badge-${props.status}`]}>
      <Icon name={status().icon} size={12} spin={status().spin} strokeWidth={2.5} />
      {props.children ?? status().label}
    </span>
  );
}

/* ---------- Progress ---------- */

export interface ProgressProps {
  /** 0-100. */
  value: number;
  decimals?: number;
  /** aria-label: the file name. */
  label?: string;
  /** Right side: "84 fps · 1.9x · ETA 6:12" or "14.7 GB → 6.1 GB". */
  detail?: JSX.Element;
}

/** A 4px bar with the percentage and live figures underneath. The width never animates. */
export function Progress(props: ProgressProps) {
  const value = () => Math.max(0, Math.min(100, props.value || 0));
  return (
    <div class="av-progress" role="progressbar" aria-valuenow={Math.round(value())} aria-valuemin={0} aria-valuemax={100} aria-label={props.label}>
      <div class="av-progress-track">
        <div class="av-progress-bar" style={{ width: `${value()}%` }} />
      </div>
      <div class="av-progress-meta">
        <b>{value().toFixed(props.decimals ?? 0)}%</b>
        <Show when={props.detail}>
          <span>{props.detail}</span>
        </Show>
      </div>
    </div>
  );
}

/* ---------- StatBar ---------- */

export interface StatItem {
  label: string;
  /** Pre-formatted with units: "18.2 TB", "4,812". */
  value: JSX.Element;
  tone?: "running" | "ok" | "danger";
}

export interface StatBarProps {
  items: StatItem[];
}

/** One row of figures split by hairlines. At most six, each something people act on. */
export function StatBar(props: StatBarProps) {
  return (
    <div class="av-stats">
      <For each={props.items} keyed={false}>
        {(item) => (
          <div class={["av-stat", item().tone && `av-stat-${item().tone}`]}>
            <div class="av-stat-label">{item().label}</div>
            <div class="av-stat-value">{item().value}</div>
          </div>
        )}
      </For>
    </div>
  );
}
