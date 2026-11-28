import type { JSX } from "@solidjs/web";
import { Show } from "solid-js";

import { Icon } from "./icon";

export interface FieldProps {
  label: string;
  /** id of the control. */
  htmlFor?: string;
  hint?: string;
  /** Replaces the hint. Say what is valid: "Must be between 1 and 51." */
  error?: string | null;
  /** Label left, control right. For Switch. */
  inline?: boolean;
  children: JSX.Element;
}

function Note(props: Pick<FieldProps, "hint" | "error">) {
  return (
    <Show
      when={props.error}
      fallback={
        <Show when={props.hint}>
          <div class="av-field-hint">{props.hint}</div>
        </Show>
      }
    >
      <div class="av-field-error" role="alert">
        <Icon name="triangle-alert" size={12} />
        {props.error}
      </div>
    </Show>
  );
}

/** Label, control, and one line of hint or error. */
export function Field(props: FieldProps) {
  const label = () => (
    <label class="av-field-label" for={props.htmlFor}>
      {props.label}
    </label>
  );
  return (
    <Show
      when={props.inline}
      fallback={
        <div class="av-field">
          {label()}
          {props.children}
          <Note hint={props.hint} error={props.error} />
        </div>
      }
    >
      <div class="av-field av-field-inline">
        <div class="av-field-text">
          {label()}
          <Note hint={props.hint} error={props.error} />
        </div>
        {props.children}
      </div>
    </Show>
  );
}
