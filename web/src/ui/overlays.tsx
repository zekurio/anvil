import type { JSX } from "@solidjs/web";
import { Menu as MenuRoot, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from "@ui-sdk/solid/menu";
import { Toast as ToastPart, ToastAction, ToastClose, ToastDescription, ToastRegion, ToastTitle, toast as push } from "@ui-sdk/solid/toast";
import { Tooltip as TooltipRoot, TooltipContent, TooltipTrigger } from "@ui-sdk/solid/tooltip";
import { For, Show } from "solid-js";

import { Icon, type IconName } from "./icon";

/* ---------- Menu ---------- */

export type MenuEntry =
  | {
      id: string;
      label: string;
      icon?: IconName;
      /** A destructive action. Follow it with `confirm()`. */
      danger?: boolean;
      disabled?: boolean;
      onSelect: () => void;
    }
  | { separator: true };

export interface MenuProps {
  /** Names the trigger: "Actions for movies". */
  label: string;
  items: MenuEntry[];
  /** The trigger's icon. Default: ellipsis. */
  icon?: IconName;
  size?: "md" | "sm";
  /** Shown beside the icon. Without it the trigger is icon-only. */
  children?: JSX.Element;
}

/** Secondary actions behind a button: the "..." at the end of a row. Arrow keys, typeahead, Escape. */
export function Menu(props: MenuProps) {
  return (
    <MenuRoot>
      <MenuTrigger class={["av-btn", "av-btn-ghost", { "av-btn-sm": props.size === "sm", "av-btn-icon": !props.children }]} aria-label={props.label} title={props.children ? undefined : props.label}>
        <Icon name={props.icon ?? "ellipsis"} size={props.size === "sm" ? 14 : 16} />
        {props.children}
      </MenuTrigger>
      <MenuContent class="av-menu" align="end" sideOffset={4}>
        <For each={props.items} keyed={false}>
          {(entry) => (
            <Show when={"id" in entry() ? (entry() as Extract<MenuEntry, { id: string }>) : undefined} fallback={<MenuSeparator class="av-menu-sep" />}>
              {(item) => (
                <MenuItem class={["av-menu-item", { "av-menu-danger": item().danger }]} value={item().id} disabled={item().disabled} onSelect={() => item().onSelect()}>
                  <Show when={item().icon}>{(name) => <Icon name={name()} size={14} />}</Show>
                  {item().label}
                </MenuItem>
              )}
            </Show>
          )}
        </For>
      </MenuContent>
    </MenuRoot>
  );
}

/* ---------- Tooltip ---------- */

/** What the trigger element must spread to stay connected to its tooltip. */
export type TooltipTriggerProps = Record<string, unknown>;

export interface TooltipProps {
  /** Plain text: a tooltip cannot be focused, so nothing inside it can be operated. */
  content: JSX.Element;
  side?: "top" | "right" | "bottom" | "left";
  /**
   * Renders the trigger from an existing control, e.g.
   * `render={(p) => <Button {...p} icon="copy" label="Copy" />}`. Without it
   * the tooltip wraps `children` in a plain button, which a press also toggles so touch screens can read it.
   */
  render?: (props: TooltipTriggerProps) => JSX.Element;
  children?: JSX.Element;
}

/** A short hint on hover and keyboard focus: why a file was skipped, what an icon button does. */
export function Tooltip(props: TooltipProps) {
  return (
    <TooltipRoot delay={300} toggleable={!props.render}>
      <Show when={props.render} fallback={<TooltipTrigger class="av-tip-target">{props.children}</TooltipTrigger>}>
        {(render) => <TooltipTrigger render={(part) => render()(part)} />}
      </Show>
      <TooltipContent class="av-tip" side={props.side ?? "top"} sideOffset={6}>
        {props.content}
      </TooltipContent>
    </TooltipRoot>
  );
}

/* ---------- Toast ---------- */

/**
 * Feedback for an action that just ran: `toast.success("Scan started")`,
 * `toast.error(message)`. Needs one <Toaster /> mounted, which the app root does.
 */
export const toast = push;

const TOAST_ICON: Partial<Record<string, IconName>> = {
  success: "circle-check",
  error: "circle-x",
  warning: "triangle-alert",
  info: "info",
  loading: "loader-circle",
};

/** Where toasts appear: bottom right, newest at the bottom. Mount it once, near the root. */
export function Toaster() {
  return (
    <ToastRegion class="av-toasts" side="bottom" align="end" expanded maxVisible={4} aria-label="Notifications">
      {(data) => (
        <ToastPart class="av-toast">
          <Show when={TOAST_ICON[data.type ?? "default"]}>{(name) => <Icon class="av-toast-icon" name={name()} size={16} spin={data.type === "loading"} />}</Show>
          <div class="av-toast-text">
            <ToastTitle class="av-toast-title" />
            <ToastDescription class="av-toast-desc" />
          </div>
          <Show when={data.action}>
            <ToastAction class="av-btn av-btn-secondary av-btn-sm" />
          </Show>
          <ToastClose class="av-btn av-btn-ghost av-btn-sm av-btn-icon" aria-label="Dismiss" title="Dismiss">
            <Icon name="x" size={14} />
          </ToastClose>
        </ToastPart>
      )}
    </ToastRegion>
  );
}
