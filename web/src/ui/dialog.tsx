import type { JSX } from "@solidjs/web";
import { AlertDialog, AlertDialogBackdrop, AlertDialogClose, AlertDialogContent, AlertDialogDescription, AlertDialogTitle, createConfirm } from "@ui-sdk/solid/alert-dialog";
import { Dialog as DialogRoot, DialogBackdrop, DialogClose, DialogContent, DialogDescription, DialogTitle } from "@ui-sdk/solid/dialog";
import { Show } from "solid-js";

import { Icon } from "./icon";

/* ---------- Dialog ---------- */

export interface DialogProps {
  open: boolean;
  /** Runs for every way of closing: the close button, Escape, a press on the scrim. */
  onClose: () => void;
  title: string;
  description?: string;
  /** 400, 480 (default) or 640px wide. */
  size?: "sm" | "md" | "lg";
  /** false while the dialog holds unsaved input: Escape and outside presses then do nothing. */
  dismissible?: boolean;
  /** Buttons, primary last. */
  footer?: JSX.Element;
  children?: JSX.Element;
}

/**
 * A focused task over the page. Modal: the page behind is inert, Tab stays
 * inside, and closing returns focus to where it came from.
 */
export function Dialog(props: DialogProps) {
  let content: HTMLElement | undefined;
  // Start in the first field rather than on the close button; a dialog without fields starts on its primary action.
  const initialFocus = () => content?.querySelector<HTMLElement>(".av-dialog-body :is(input, select, textarea):not(:disabled)") ?? content?.querySelector<HTMLElement>(".av-dialog-foot .av-btn-primary:not(:disabled)");
  return (
    <DialogRoot
      open={props.open}
      dismissible={props.dismissible}
      onOpenChange={(open) => {
        if (!open) props.onClose();
      }}
    >
      <DialogBackdrop class="av-scrim" />
      <DialogContent ref={(el) => (content = el)} initialFocus={initialFocus} class={["av-dialog", { "av-dialog-sm": props.size === "sm", "av-dialog-lg": props.size === "lg" }]}>
        <header class="av-dialog-head">
          <div class="av-dialog-titles">
            <DialogTitle class="av-dialog-title">{props.title}</DialogTitle>
            <Show when={props.description}>
              <DialogDescription class="av-dialog-desc">{props.description}</DialogDescription>
            </Show>
          </div>
          <DialogClose class="av-btn av-btn-ghost av-btn-sm av-btn-icon" aria-label="Close" title="Close">
            <Icon name="x" size={14} />
          </DialogClose>
        </header>
        <div class="av-dialog-body">{props.children}</div>
        <Show when={props.footer}>
          <footer class="av-dialog-foot">{props.footer}</footer>
        </Show>
      </DialogContent>
    </DialogRoot>
  );
}

/* ---------- ConfirmDialog ---------- */

export interface ConfirmOptions {
  /** The question, naming what is affected: 'Delete the library "movies"?' */
  title: string;
  /** What agreeing does, and what it does not touch. */
  description?: string;
  /** The verb of the action: "Delete library". Never "OK" or "Yes". */
  confirmLabel: string;
  cancelLabel?: string;
  /** Styles the confirming button as destructive. */
  danger?: boolean;
}

const ask = createConfirm<ConfirmOptions>();

/**
 * Asks before a destructive action and resolves true when the person agrees:
 * `if (await confirm({ title, confirmLabel: "Delete library", danger: true })) ...`.
 * Needs one <ConfirmDialog /> mounted, which the app root does.
 */
export function confirm(options: ConfirmOptions): Promise<boolean> {
  return ask(options);
}

/** The dialog `confirm()` opens. Mount it once, near the root. */
export function ConfirmDialog() {
  return (
    <AlertDialog controller={ask.controller}>
      <AlertDialogBackdrop class="av-scrim" />
      <AlertDialogContent<ConfirmOptions> class="av-dialog av-dialog-sm">
        {(state) => (
          <>
            <header class="av-dialog-head">
              <div class="av-dialog-titles">
                <AlertDialogTitle class="av-dialog-title">{state.payload?.title}</AlertDialogTitle>
                <Show when={state.payload?.description}>
                  <AlertDialogDescription class="av-dialog-desc">{state.payload?.description}</AlertDialogDescription>
                </Show>
              </div>
            </header>
            <footer class="av-dialog-foot av-dialog-foot-bare">
              <AlertDialogClose class="av-btn av-btn-secondary">{state.payload?.cancelLabel ?? "Cancel"}</AlertDialogClose>
              <AlertDialogClose class={["av-btn", state.payload?.danger ? "av-btn-danger-outline" : "av-btn-primary"]} value="confirm">
                {state.payload?.confirmLabel}
              </AlertDialogClose>
            </footer>
          </>
        )}
      </AlertDialogContent>
    </AlertDialog>
  );
}
