import { render } from "@solidjs/web";

import "./ui/styles/index.css";

import { App } from "./app";

// A page that stayed open across an upgrade of the daemon still names the
// chunks of the old build, and the new daemon does not have them: the first
// view that was not loaded yet fails. Loading the page again gets the new
// build. Only when the daemon answers, though, because a reload against a
// daemon that is down trades the app for the browser's error page, and at
// most once in ten seconds, so a chunk that is really broken cannot loop.
const RELOADED_AT = "anvil.reloaded-at";
window.addEventListener("vite:preloadError", () => {
  let last = 0;
  try {
    last = Number(sessionStorage.getItem(RELOADED_AT) ?? 0);
  } catch {
    // Without session storage there is no telling a loop from a first try.
    return;
  }
  if (Date.now() - last < 10_000) return;
  void fetch("/", { cache: "no-store" }).then(
    (response) => {
      if (!response.ok) return;
      sessionStorage.setItem(RELOADED_AT, String(Date.now()));
      window.location.reload();
    },
    () => {},
  );
});

render(() => <App />, document.getElementById("root") as HTMLElement);
