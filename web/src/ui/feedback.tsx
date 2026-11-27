import type { JSX } from "@solidjs/web";
import { Repeat, Show } from "solid-js";

import { errorMessage } from "../api/client";
import { Button } from "./button";
import { Icon, type IconName } from "./icon";

/* ---------- Spinner ---------- */

export interface SpinnerProps {
  size?: number;
  /** What is loading, for assistive tech. Default "Loading". */
  label?: string;
}

/** The running spinner: the only looping motion, and it stops under prefers-reduced-motion. */
export function Spinner(props: SpinnerProps) {
  return <Icon class="av-spinner" name="loader-circle" size={props.size ?? 16} spin label={props.label ?? "Loading"} />;
}

/** A centred spinner with a line of text, for a region that has nothing to show yet. */
export function LoadingState(props: { label?: string }) {
  return (
    <div class="av-state" role="status">
      <Icon class="av-spinner" name="loader-circle" size={16} spin />
      <span class="av-state-text">{props.label ?? "Loading"}</span>
    </div>
  );
}

/** Placeholder rows for a table that is loading. Static: data never animates. */
export function LoadingRows(props: { rows?: number; label?: string }) {
  return (
    <div class="av-skeleton" role="status" aria-label={props.label ?? "Loading"}>
      <Repeat count={props.rows ?? 8}>
        {(index) => (
          <div class="av-skeleton-row">
            <span class="av-skeleton-bar" style={{ width: `${28 + ((index * 17) % 40)}%` }} />
            <span class="av-skeleton-bar av-skeleton-short" />
          </div>
        )}
      </Repeat>
    </div>
  );
}

/* ---------- EmptyState ---------- */

export interface EmptyStateProps {
  icon?: IconName;
  /** What is missing: "No libraries yet." */
  title: string;
  /** What to do next: "Add a folder to scan." */
  description?: string;
  /** The action that fixes it. */
  children?: JSX.Element;
}

/** Says what is missing and what to do next. */
export function EmptyState(props: EmptyStateProps) {
  return (
    <div class="av-state">
      <Show when={props.icon}>{(name) => <Icon class="av-state-icon" name={name()} size={24} strokeWidth={1.5} />}</Show>
      <h2 class="av-state-title">{props.title}</h2>
      <Show when={props.description}>
        <p class="av-state-text">{props.description}</p>
      </Show>
      <Show when={props.children}>
        <div class="av-state-actions">{props.children}</div>
      </Show>
    </div>
  );
}

/* ---------- ErrorState ---------- */

export interface ErrorStateProps {
  /** What failed, in the words of the task: "The files could not be loaded". */
  title?: string;
  /** The thrown value. Its message is shown as the detail. */
  error: unknown;
  onRetry?: () => void;
}

/** A failed load: what failed, the daemon's message, and Retry. */
export function ErrorState(props: ErrorStateProps) {
  return (
    <div class="av-state" role="alert">
      <Icon class="av-state-icon av-state-danger" name="circle-alert" size={24} strokeWidth={1.5} />
      <h2 class="av-state-title">{props.title ?? "This could not be loaded"}</h2>
      <p class="av-state-text">{errorMessage(props.error)}</p>
      <Show when={props.onRetry}>
        <div class="av-state-actions">
          <Button icon="rotate-ccw" onClick={() => props.onRetry?.()}>
            Retry
          </Button>
        </div>
      </Show>
    </div>
  );
}

/* ---------- Banner ---------- */

export interface BannerProps {
  tone?: "info" | "warning" | "danger";
  /** An action on the right, e.g. a small Retry button. */
  action?: JSX.Element;
  children: JSX.Element;
}

const BANNER_ICON: Record<NonNullable<BannerProps["tone"]>, IconName> = { info: "info", warning: "triangle-alert", danger: "circle-alert" };

/** One line across the top of a region about its state: the daemon is unreachable, the library is paused. */
export function Banner(props: BannerProps) {
  const tone = () => props.tone ?? "info";
  return (
    <div class={["av-banner", `av-banner-${tone()}`]} role={tone() === "danger" ? "alert" : "status"}>
      <Icon name={BANNER_ICON[tone()]} size={14} />
      <div class="av-banner-text">{props.children}</div>
      {props.action}
    </div>
  );
}
