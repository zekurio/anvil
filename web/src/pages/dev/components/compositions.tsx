import { createSignal, Show } from "solid-js";

import { Button, FlowCanvas, NodePalette, RuleList, Select, Sidebar, StatBar, Tabs, Topbar } from "../../../ui";
import { EncodeSheet, FilesDemo } from "./sections";
import { FLOW_EDGES, FLOW_NODES, NAV, PALETTE, RULES, STATS } from "./samples";

/** The design package's Library page, rebuilt from the ported components for a 1:1 comparison. */
export function LibraryComposition() {
  const [tab, setTab] = createSignal("files");
  const [nav, setNav] = createSignal("movies");
  return (
    <div class="av-app">
      <Sidebar items={NAV} value={nav()} onChange={setNav} footer={"v0.4.0 · 2 workers"} />
      <div class="av-main">
        <Topbar title="Movies" meta={"/media/movies · scanned 4 min ago"}>
          <div style={{ width: "200px" }}>
            <Select aria-label="Flow" defaultValue="hevc" options={[{ value: "hevc", label: "Flow: HEVC 1080p" }, { value: "remux", label: "Flow: Remux only" }]} />
          </div>
          <Button icon="refresh-cw">Scan</Button>
          <Button variant="primary" icon="play">
            Process 312 files
          </Button>
        </Topbar>
        <div class="av-page-stats">
          <StatBar items={STATS} />
        </div>
        <div class="av-page-tabs">
          <Tabs
            label="Views of Movies"
            value={tab()}
            onChange={setTab}
            tabs={[
              { id: "files", label: "Files", count: "4,812" },
              { id: "rules", label: "Rules", count: 5 },
              { id: "history", label: "History" },
              { id: "settings", label: "Settings" },
            ]}
          />
        </div>
        <Show
          when={tab() === "rules"}
          fallback={
            <div class="av-page-body">
              <FilesDemo />
            </div>
          }
        >
          <div class="av-pad av-page-body">
            <RuleList rules={RULES} onChange={() => {}} fallback="allow" />
          </div>
        </Show>
      </div>
    </div>
  );
}

/** The design package's FlowEditor page, rebuilt from the ported components for a 1:1 comparison. */
export function FlowEditorComposition() {
  const [selected, setSelected] = createSignal<string | null>("enc");
  return (
    <div class="av-main av-main-full">
      <Topbar title="HEVC 1080p" meta={<span style={{ color: "var(--warn)" }}>Draft {"·"} 2 unsaved changes</span>}>
        <Button variant="ghost" icon="play">
          Test on a file
        </Button>
        <Button>Discard</Button>
        <Button variant="primary">Save flow</Button>
      </Topbar>
      <div class="av-workspace">
        <NodePalette groups={PALETTE} />
        <div style={{ position: "relative", flex: 1, "min-width": 0 }}>
          <FlowCanvas nodes={FLOW_NODES} edges={FLOW_EDGES} selectedId={selected()} onSelect={setSelected} activePath={["in", "cond", "enc"]} zoom={0.8} offsetX={8} offsetY={24} />
          <Show when={selected() === "enc"}>
            <EncodeSheet onClose={() => setSelected(null)} />
          </Show>
        </div>
      </div>
    </div>
  );
}
