import { createSignal, createUniqueId, For, Show } from "solid-js";

import type { Rule } from "../../../api";
import {
  Button,
  CommandPreview,
  DataTable,
  Field,
  FlowCanvas,
  FlowNode,
  Input,
  NodePalette,
  Progress,
  RuleList,
  Segmented,
  Select,
  SheetSection,
  Sidebar,
  SideSheet,
  StatBar,
  StatusBadge,
  Switch,
  Tabs,
  Topbar,
  type DataTableColumn,
  type JobStatus,
  type RowId,
} from "../../../ui";
import { Item, Section } from "./parts";
import { COMMAND, FLOW_EDGES, FLOW_NODES, NAV, PALETTE, ROWS, RULES, STATS, type SampleRow } from "./samples";

const STATUSES: JobStatus[] = ["queued", "running", "done", "failed", "skipped", "warning"];

export const COLUMNS: DataTableColumn<SampleRow>[] = [
  { key: "path", label: "File", path: true },
  { key: "container", label: "Container", mono: true, width: 88 },
  { key: "video", label: "Video", mono: true, width: 72 },
  { key: "res", label: "Resolution", mono: true, width: 92 },
  { key: "audio", label: "Audio", mono: true, width: 104 },
  { key: "size", label: "Size", align: "right", width: 84 },
  { key: "status", label: "Status", width: 136, render: (row) => (row.status === "running" ? <StatusBadge status={row.status}>Running 62%</StatusBadge> : <StatusBadge status={row.status} />) },
  { key: "flow", label: "Flow", width: 120, render: (row) => (row.flow === "Default" ? <span class="av-muted">Library default</span> : row.flow) },
];

export function Status() {
  return (
    <Section id="status" title="Status">
      <Item title="StatusBadge">
        <div class="av-row">
          <For each={STATUSES}>{(status) => <StatusBadge status={status} />}</For>
          <StatusBadge status="running">Running 62%</StatusBadge>
        </div>
      </Item>
      <Item title="Progress">
        <div class="gal-grid">
          <Progress value={62} label="Dune (2021).mp4" detail={"84 fps · 1.9x · ETA 6:12"} />
          <Progress value={100} label="Interstellar (2014).mkv" detail={"14.7 GB → 6.1 GB"} />
          <Progress value={7.4} decimals={1} label="Arrival (2016).mkv" />
        </div>
      </Item>
      <Item title="StatBar" bare>
        <StatBar items={STATS} />
      </Item>
    </Section>
  );
}

/** The Files tab of the design's Library page: toolbar or bulk bar, then the table. */
export function FilesDemo(props: { height?: number }) {
  const [selected, setSelected] = createSignal<RowId[]>([3, 6]);
  const [sort, setSort] = createSignal<{ key: string; dir: "asc" | "desc" }>({ key: "path", dir: "asc" });
  const [open, setOpen] = createSignal<RowId | undefined>();
  return (
    <>
      <Show
        when={selected().length > 0}
        fallback={
          <div class="av-toolbar">
            <div style={{ width: "280px" }}>
              <Input size="sm" icon="search" placeholder="Filter by name or path" aria-label="Filter" />
            </div>
            <Segmented size="sm" label="Status" options={[{ value: "all", label: "All" }, { value: "pending", label: "Pending" }, { value: "failed", label: "Failed" }, { value: "done", label: "Done" }]} />
          </div>
        }
      >
        <div class="av-bulk">
          {selected().length} selected
          <span class="av-spacer" />
          <Button size="sm" icon="play">
            Queue
          </Button>
          <Button size="sm">Assign flow</Button>
          <Button size="sm">Skip</Button>
          <Button size="sm" variant="ghost" onClick={() => setSelected([])}>
            Clear
          </Button>
        </div>
      </Show>
      <DataTable
        label="Files"
        columns={COLUMNS}
        rows={ROWS}
        rowId={(row) => row.id}
        selected={selected()}
        onSelect={setSelected}
        sortKey={sort().key}
        sortDir={sort().dir}
        onSort={(key) => setSort((current) => ({ key, dir: current.key === key && current.dir === "asc" ? "desc" : "asc" }))}
        onRowClick={(row) => setOpen(row.id)}
        activeId={open()}
        height={props.height}
      />
    </>
  );
}

export function Data() {
  const [rules, setRules] = createSignal<Rule[]>(RULES);
  return (
    <Section id="data" title="Data">
      <Item title="DataTable with toolbar and bulk bar" flush>
        <FilesDemo />
      </Item>
      <Item title="DataTable compact, empty">
        <DataTable compact label="Empty" columns={COLUMNS.slice(0, 3)} rows={[]} rowId={(row) => row.id} empty={<div class="av-pad av-muted small">No file matches this filter.</div>} />
      </Item>
      <Item title="RuleList" bare>
        <RuleList rules={rules()} onChange={setRules} fallback="allow" />
        <div class="av-row" style={{ "margin-top": "var(--space-2)" }}>
          <Button size="sm" icon="plus" onClick={() => setRules([...rules(), { action: "deny", field: "path", op: "matches", value: "" }])}>
            Add rule
          </Button>
        </div>
      </Item>
    </Section>
  );
}

/** The node options sheet of the design's FlowEditor page. */
export function EncodeSheet(props: { onClose?: () => void }) {
  const id = createUniqueId();
  return (
    <SideSheet
      title="Encode video"
      subtitle={"Video · node 4 of 6"}
      icon="film"
      onClose={props.onClose}
      footer={
        <>
          <Button variant="danger" size="sm" icon="trash-2">
            Delete node
          </Button>
          <span class="av-spacer" />
          <Button variant="ghost" size="sm" icon="copy">
            Duplicate
          </Button>
        </>
      }
    >
      <SheetSection title="Encoder">
        <Field label="Label" htmlFor={`${id}-label`}>
          <Input id={`${id}-label`} value="Encode video" />
        </Field>
        <div class="av-grid-2">
          <Field label="Codec" htmlFor={`${id}-codec`}>
            <Select id={`${id}-codec`} defaultValue="hevc" options={[{ value: "hevc", label: "HEVC (H.265)" }, { value: "h264", label: "H.264" }, { value: "av1", label: "AV1" }]} />
          </Field>
          <Field label="Encoder" htmlFor={`${id}-encoder`}>
            <Select id={`${id}-encoder`} defaultValue="hevc_qsv" options={["libx265", "hevc_qsv", "hevc_vaapi", "hevc_nvenc"]} />
          </Field>
        </div>
        <Field label="Preset" htmlFor={`${id}-preset`} hint="Slower presets give smaller files at the same quality.">
          <Select id={`${id}-preset`} defaultValue="slow" options={["veryfast", "faster", "medium", "slow", "veryslow"]} />
        </Field>
      </SheetSection>
      <SheetSection title="Quality">
        <Field label="Rate control">
          <Segmented label="Rate control" defaultValue="q" options={[{ value: "q", label: "Constant quality" }, { value: "br", label: "Target bitrate" }]} />
        </Field>
        <Field label="Global quality" htmlFor={`${id}-quality`} hint={"1–51. Lower is better quality and larger files."}>
          <Input id={`${id}-quality`} mono value="22" />
        </Field>
      </SheetSection>
      <SheetSection title="Filters" collapsible>
        <Field label="Maximum resolution" htmlFor={`${id}-resolution`}>
          <Select id={`${id}-resolution`} defaultValue="keep" options={[{ value: "keep", label: "Keep source" }, { value: "1080", label: "1080p" }, { value: "720", label: "720p" }]} />
        </Field>
        <Field inline label="Tone-map HDR to SDR" hint="Only applies to HDR sources.">
          <Switch label="Tone-map HDR to SDR" />
        </Field>
        <Field inline label="Hardware decoding">
          <Switch label="Hardware decoding" defaultChecked />
        </Field>
      </SheetSection>
      <SheetSection title="Advanced" collapsible defaultOpen={false} />
      <SheetSection title="Command preview">
        <CommandPreview prefix="ffmpeg" args={COMMAND} />
      </SheetSection>
    </SideSheet>
  );
}

export function Flow() {
  const [selected, setSelected] = createSignal<string | null>("enc");
  return (
    <Section id="flow" title="Flow">
      <Item title="FlowNode">
        <div class="gal-grid" style={{ "row-gap": "var(--space-5)" }}>
          <FlowNode static kind="input" title="Input file" summary="*.mkv, *.mp4, *.avi" connectedOut />
          <FlowNode static kind="condition" title="Video is hevc?" summary="video.codec == hevc" outputs={["true", "false"]} connectedIn />
          <FlowNode static kind="video" title="Encode video" summary={"libsvtav1 · vmaf 95 · preset 6"} selected />
          <FlowNode static kind="audio" title="Keep audio" summary={"eng, jpn · original"} status="running" />
          <FlowNode static kind="subtitles" title="Keep subtitles" summary={"eng · forced"} status="done" />
          <FlowNode static kind="container" title="Remux to mkv" summary="mkv" status="failed" />
          <FlowNode static kind="output" title="Publish" summary="Smallest saving must be between 0 and 90." invalid />
          <FlowNode static kind="command" title="Run command with a title that truncates" summary="a summary that is far too long to fit on one line of the node" />
        </div>
      </Item>
      <Item title="FlowCanvas with SideSheet and NodePalette" flush>
        <div class="gal-frame">
          <NodePalette groups={PALETTE} />
          <div style={{ position: "relative", flex: 1, "min-width": 0 }}>
            <FlowCanvas nodes={FLOW_NODES} edges={FLOW_EDGES} selectedId={selected()} onSelect={setSelected} activePath={["in", "cond", "enc"]} zoom={0.6} offsetX={8} offsetY={16} />
            <Show when={selected() === "enc"}>
              <EncodeSheet onClose={() => setSelected(null)} />
            </Show>
          </div>
        </div>
      </Item>
      <Item title="CommandPreview">
        <CommandPreview prefix="ffmpeg" args={COMMAND} />
      </Item>
    </Section>
  );
}

export function Shell() {
  const [nav, setNav] = createSignal("movies");
  const [tab, setTab] = createSignal("files");
  return (
    <Section id="shell" title="Shell">
      <Item title="Topbar" flush>
        <Topbar title="Movies" meta={"/media/movies · scanned 4 min ago"}>
          <Button icon="refresh-cw">Scan</Button>
          <Button variant="primary" icon="play">
            Process 312 files
          </Button>
        </Topbar>
        <Topbar title="HEVC 1080p" back={{ href: "#shell", label: "Back to flows" }} meta={<span style={{ color: "var(--warn)" }}>Draft {"·"} 2 unsaved changes</span>}>
          <Button variant="ghost" icon="play">
            Test on a file
          </Button>
          <Button>Discard</Button>
          <Button variant="primary">Save flow</Button>
        </Topbar>
      </Item>
      <Item title="Sidebar with a page" flush>
        <div class="av-app" style={{ height: "420px" }}>
          <Sidebar items={NAV} value={nav()} onChange={setNav} footer={"v0.4.0 · 2 workers"} />
          <div class="av-main">
            <div class="av-page-stats">
              <StatBar items={STATS} />
            </div>
            <div class="av-page-tabs">
              <Tabs label="Views" value={tab()} onChange={setTab} tabs={[{ id: "files", label: "Files", count: "4,812" }, { id: "rules", label: "Rules", count: 5 }]} />
            </div>
          </div>
        </div>
      </Item>
    </Section>
  );
}
