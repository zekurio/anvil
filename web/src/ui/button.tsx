import type { JSX } from "@solidjs/web";
import { children, omit, Show } from "solid-js";

import { Icon, type IconName } from "./icon";

export interface ButtonProps extends JSX.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "secondary" | "primary" | "ghost" | "danger";
  size?: "md" | "sm";
  /** Leading icon. With no children the button is icon-only and `label` is required. */
  icon?: IconName;
  iconRight?: IconName;
  /** The accessible name and tooltip of an icon-only button. */
  label?: string;
  /** The action is running: shows a spinner and refuses presses. */
  busy?: boolean;
}

/** A labelled action. Verb first, sentence case: "Scan library", "Save flow". */
export function Button(props: ButtonProps) {
  const rest = omit(props, "variant", "size", "icon", "iconRight", "label", "busy", "class", "children", "disabled");
  const content = children(() => props.children);
  const iconOnly = () => !!props.icon && (content() == null || content() === false);
  const iconSize = () => (props.size === "sm" ? 14 : 16);
  return (
    <button
      type="button"
      {...rest}
      class={[
        "av-btn",
        `av-btn-${props.variant ?? "secondary"}`,
        props.class,
        { "av-btn-sm": props.size === "sm", "av-btn-icon": iconOnly() },
      ]}
      disabled={props.disabled || props.busy}
      aria-busy={props.busy ? "true" : undefined}
      aria-label={iconOnly() ? props.label : props["aria-label"]}
      // A Tooltip around the button describes it; the native tooltip would show a second one.
      title={iconOnly() && !props["aria-describedby"] ? props.label : props.title}
    >
      <Show when={props.busy} fallback={<Show when={props.icon}>{(name) => <Icon name={name()} size={iconSize()} />}</Show>}>
        <Icon name="loader-circle" size={iconSize()} spin />
      </Show>
      <Show when={!iconOnly()}>{content()}</Show>
      <Show when={props.iconRight}>{(name) => <Icon name={name()} size={14} />}</Show>
    </button>
  );
}

export interface LinkButtonProps extends JSX.AnchorHTMLAttributes<HTMLAnchorElement> {
  variant?: "secondary" | "primary" | "ghost" | "danger";
  size?: "md" | "sm";
  icon?: IconName;
}

/** A link that looks like a Button, for actions that are navigation: "New flow", "Open the queue". */
export function LinkButton(props: LinkButtonProps) {
  const rest = omit(props, "variant", "size", "icon", "class", "children");
  return (
    <a {...rest} class={["av-btn", `av-btn-${props.variant ?? "secondary"}`, props.class, { "av-btn-sm": props.size === "sm" }]}>
      <Show when={props.icon}>{(name) => <Icon name={name()} size={props.size === "sm" ? 14 : 16} />}</Show>
      {props.children}
    </a>
  );
}
