import { useBeforeLeave } from "@solidjs/router";
import { onSettled } from "solid-js";

import { confirm, type ConfirmOptions } from "../ui";

const DEFAULTS: ConfirmOptions = {
  title: "Discard unsaved changes?",
  description: "The changes made since the last save are lost.",
  confirmLabel: "Discard changes",
  cancelLabel: "Keep editing",
  danger: true,
};

/**
 * Asks before leaving a view that holds an unsaved draft: in-app navigation
 * gets the confirm dialog, closing or reloading the tab gets the browser's
 * own prompt. Call it from the route's component.
 */
export function useUnsavedChanges(dirty: () => boolean, options: Partial<ConfirmOptions> = {}): void {
  useBeforeLeave((event) => {
    if (!dirty() || event.defaultPrevented) return;
    event.preventDefault();
    void confirm({ ...DEFAULTS, ...options }).then((leave) => {
      if (leave) event.retry(true);
    });
  });

  onSettled(() => {
    const onUnload = (event: BeforeUnloadEvent) => {
      if (dirty()) event.preventDefault();
    };
    window.addEventListener("beforeunload", onUnload);
    return () => window.removeEventListener("beforeunload", onUnload);
  });
}
