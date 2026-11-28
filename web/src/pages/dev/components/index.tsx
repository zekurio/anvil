// The components gallery, at /_dev/components in dev builds only. It shows
// every ui/ component in both themes, and rebuilds the design package's two
// page layouts for a side-by-side comparison:
//
//   /_dev/components                    every component, dark and light
//   /_dev/components?theme=light        one theme, full width
//   /_dev/components?print              full height, for one tall screenshot
//   /_dev/components?view=library       the Library page layout
//   /_dev/components?view=flow-editor   the FlowEditor page layout

import "./gallery.css";

import { useSearchParams } from "@solidjs/router";
import { For, Match, Switch } from "solid-js";

import { useTitle } from "../../../lib/title";
import type { Theme } from "../../../lib/theme";
import { Segmented } from "../../../ui";
import { FlowEditorComposition, LibraryComposition } from "./compositions";
import { Feedback, Overlays } from "./overlays";
import { Foundations, Primitives } from "./primitives";
import { Data, Flow, Shell, Status } from "./sections";

const SECTIONS = ["foundations", "primitives", "status", "data", "flow", "shell", "overlays", "feedback"];

function Everything() {
  return (
    <>
      <Foundations />
      <Primitives />
      <Status />
      <Data />
      <Flow />
      <Shell />
      <Overlays />
      <Feedback />
    </>
  );
}

export default function Gallery() {
  const [search, setSearch] = useSearchParams();
  useTitle(() => "Components");

  const themes = (): Theme[] => (search.theme === "dark" || search.theme === "light" ? [search.theme] : ["dark", "light"]);
  const view = () => (typeof search.view === "string" ? search.view : "");

  return (
    <Switch>
      <Match when={view() === "library" || view() === "flow-editor"}>
        <div class="gal-page" data-theme={themes()[0]}>
          <Switch>
            <Match when={view() === "library"}>
              <LibraryComposition />
            </Match>
            <Match when={view() === "flow-editor"}>
              <FlowEditorComposition />
            </Match>
          </Switch>
        </div>
      </Match>
      <Match when={true}>
        <div class={["gal", { "gal-print": search.print !== undefined }]}>
          <header class="gal-bar">
            <h1>Components</h1>
            <nav aria-label="Sections">
              <For each={SECTIONS}>
                {(id) => (
                  <a class="av-link" href={`#${id}`} target="_self" onClick={(event) => (event.preventDefault(), document.querySelector(`[data-section="${id}"]`)?.scrollIntoView())}>
                    {id}
                  </a>
                )}
              </For>
            </nav>
            <Segmented
              size="sm"
              label="Theme"
              value={themes().length === 2 ? "both" : themes()[0]}
              onChange={(value) => setSearch({ theme: value === "both" ? undefined : value })}
              options={[
                { value: "both", label: "Both" },
                { value: "dark", label: "Dark" },
                { value: "light", label: "Light" },
              ]}
            />
          </header>
          <div class="gal-cols">
            <For each={themes()}>
              {(theme) => (
                <div class="gal-col" data-theme={theme}>
                  <Everything />
                </div>
              )}
            </For>
          </div>
        </div>
      </Match>
    </Switch>
  );
}
