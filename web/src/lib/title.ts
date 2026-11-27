import { createEffect } from "solid-js";

const APP = "anvil";

/**
 * Sets the document title to "<title> · anvil" while the calling component is
 * mounted. Pass an accessor so the title follows the data: `useTitle(() => library().name)`.
 */
export function useTitle(title: () => string | undefined): void {
  createEffect(title, (value) => {
    document.title = value ? `${value} · ${APP}` : APP;
    return () => {
      document.title = APP;
    };
  });
}
