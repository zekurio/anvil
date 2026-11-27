import { createSignal } from "solid-js";

export type Theme = "dark" | "light";

const STORAGE_KEY = "anvil.theme";

// Storage can throw (private mode, blocked cookies); the theme then lasts for the page.
function stored(): Theme {
  try {
    return localStorage.getItem(STORAGE_KEY) === "light" ? "light" : "dark";
  } catch {
    return "dark";
  }
}

const [theme, setCurrent] = createSignal<Theme>(stored());

/** The active theme. Dark is the default; index.html applies the saved one before first paint. */
export { theme };

export function setTheme(next: Theme): void {
  setCurrent(next);
  document.documentElement.dataset.theme = next;
  try {
    localStorage.setItem(STORAGE_KEY, next);
  } catch {
    // Not persisted; nothing else to do.
  }
}
