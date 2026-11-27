import { createEffect, onCleanup } from "solid-js";

const APP = "anvil";

interface Entry {
  /** False until the title is known, so a page that is still loading does not blank the title of the one on screen. */
  known: boolean;
  value: string | undefined;
}

// Every mounted useTitle, oldest first. The newest names the document, so the
// sign-in screen over a page takes the title and gives it back.
const titles: Entry[] = [];

function show(): void {
  const value = titles.findLast((entry) => entry.known)?.value;
  document.title = value ? `${value} · ${APP}` : APP;
}

/**
 * Sets the document title to "<title> · anvil" while the calling component is
 * mounted. Pass an accessor so the title follows the data: `useTitle(() => library().name)`.
 */
export function useTitle(title: () => string | undefined): void {
  const entry: Entry = { known: false, value: undefined };
  titles.push(entry);
  onCleanup(() => {
    titles.splice(titles.indexOf(entry), 1);
    show();
  });
  createEffect(title, (value) => {
    entry.known = true;
    entry.value = value;
    show();
  });
}
