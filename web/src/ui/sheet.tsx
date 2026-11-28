import type { JSX } from "@solidjs/web";
import { Dialog as DialogRoot, DialogBackdrop, DialogClose, DialogContent, DialogTitle } from "@ui-sdk/solid/dialog";
import { createSignal, For, Show } from "solid-js";

import { shellQuote } from "../lib/shell";
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
 * The one place where details are edited. A modal panel the full height of
 * the window, on its right edge, over a scrim: the page behind is inert, Tab
 * stays inside, and Escape, a press on the scrim, or the close button closes
 * it and returns focus to where it came from.
 */
export function SideSheet(props: SideSheetProps) {
  return (
    <DialogRoot
      open={props.open !== false}
      onOpenChange={(open) => {
        if (!open) props.onClose?.();
      }}
    >
      <DialogBackdrop class="av-scrim" />
      <DialogContent class="av-sheet" initialFocus="content">
        <header class="av-sheet-head">
          <Show when={props.icon}>{(name) => <Icon name={name()} size={16} />}</Show>
          <div class="av-sheet-titles">
            <DialogTitle class="av-sheet-title">{props.title}</DialogTitle>
            <Show when={props.subtitle}>
              <div class="av-sheet-sub">{props.subtitle}</div>
            </Show>
          </div>
          <DialogClose class="av-btn av-btn-ghost av-btn-sm av-btn-icon av-sheet-x" aria-label="Close" title="Close">
            <Icon name="x" size={14} />
          </DialogClose>
        </header>
        <div class="av-sheet-body">{props.children}</div>
        <Show when={props.footer}>
          <footer class="av-sheet-foot">{props.footer}</footer>
        </Show>
      </DialogContent>
    </DialogRoot>
  );
}

/* ---------- CommandPreview ---------- */

export interface CommandPreviewProps {
  /** argv, e.g. ["-i", "{input}", "-c:v", "libsvtav1"]. */
  args: string[];
  prefix?: string;
  label?: string;
}

/** Read-only block showing the ffmpeg arguments anvil will run, shell-quoted the way "copy" would put them. */
export function CommandPreview(props: CommandPreviewProps) {
  return (
    <pre class="av-code" aria-label={props.label ?? "Command preview"}>
      <Show when={props.prefix}>
        <span class="av-code-dim">{shellQuote(props.prefix ?? "")} </span>
      </Show>
      <For each={props.args} keyed={false}>
        {(arg, index) => (
          <>
            {index > 0 ? " " : ""}
            <span class={{ "av-code-flag": arg().startsWith("-"), "av-code-dim": /^\{.*\}$/.test(arg()) }}>{shellQuote(arg())}</span>
          </>
        )}
      </For>
    </pre>
  );
}
