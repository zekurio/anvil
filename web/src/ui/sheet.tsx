import type { JSX } from "@solidjs/web";
import { createSignal, For, Show } from "solid-js";

import { Button } from "./button";
import { Icon, type IconName } from "./icon";

/* ---------- SheetSection ---------- */

export interface SheetSectionProps {
  title: string;
  collapsible?: boolean;
  /** Whether a collapsible section starts open. Default true. */
  defaultOpen?: boolean;
  children?: JSX.Element;
}

/** One titled block of a SideSheet. Sections are separated by a line. */
export function SheetSection(props: SheetSectionProps) {
  const [open, setOpen] = createSignal(() => props.defaultOpen !== false);
  const isOpen = () => !props.collapsible || open();
  return (
    <section class="av-sec">
      <Show when={props.collapsible} fallback={<h3 class="av-sec-head">{props.title}</h3>}>
        <h3 class="av-sec-heading">
          <button type="button" class="av-sec-head" aria-expanded={isOpen() ? "true" : "false"} onClick={() => setOpen(!open())}>
            {props.title}
            <Icon name={isOpen() ? "chevron-up" : "chevron-down"} size={14} />
          </button>
        </h3>
      </Show>
      <Show when={isOpen()}>
        <div class="av-sec-body">{props.children}</div>
      </Show>
    </section>
  );
}

/* ---------- SideSheet ---------- */

export interface SideSheetProps {
  title: string;
  /** Type and position: "Video · node 4 of 6". */
  subtitle?: string;
  icon?: IconName;
  onClose?: () => void;
  /** Sheet-level actions: destructive left, a `.av-spacer`, then the rest. */
  footer?: JSX.Element;
  open?: boolean;
  children?: JSX.Element;
}

/**
 * The one place where details are edited. Docked right inside a
 * `position: relative` parent and fills its height. It never dims or blocks
 * the page: Esc closes it while focus is inside.
 */
export function SideSheet(props: SideSheetProps) {
  return (
    <Show when={props.open !== false}>
      <aside
        class="av-sheet"
        aria-label={props.title}
        onKeyDown={(event) => {
          if (event.key !== "Escape" || event.defaultPrevented) return;
          props.onClose?.();
        }}
      >
        <header class="av-sheet-head">
          <Show when={props.icon}>{(name) => <Icon name={name()} size={16} />}</Show>
          <div class="av-sheet-titles">
            <h2 class="av-sheet-title">{props.title}</h2>
            <Show when={props.subtitle}>
              <div class="av-sheet-sub">{props.subtitle}</div>
            </Show>
          </div>
          <Button variant="ghost" size="sm" icon="x" label="Close" class="av-sheet-x" onClick={() => props.onClose?.()} />
        </header>
        <div class="av-sheet-body">{props.children}</div>
        <Show when={props.footer}>
          <footer class="av-sheet-foot">{props.footer}</footer>
        </Show>
      </aside>
    </Show>
  );
}

/* ---------- CommandPreview ---------- */

export interface CommandPreviewProps {
  /** argv, e.g. ["-i", "{input}", "-c:v", "libsvtav1"]. */
  args: string[];
  prefix?: string;
  label?: string;
}

/** Read-only block showing the ffmpeg arguments anvil will run. */
export function CommandPreview(props: CommandPreviewProps) {
  return (
    <pre class="av-code" aria-label={props.label ?? "Command preview"}>
      <Show when={props.prefix}>
        <span class="av-code-dim">{props.prefix} </span>
      </Show>
      <For each={props.args} keyed={false}>
        {(arg, index) => (
          <>
            {index > 0 ? " " : ""}
            <span class={{ "av-code-flag": arg().startsWith("-"), "av-code-dim": /^\{.*\}$/.test(arg()) }}>{arg()}</span>
          </>
        )}
      </For>
    </pre>
  );
}
