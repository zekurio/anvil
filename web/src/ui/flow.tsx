import { createMemo, createSignal, For, Show } from "solid-js";

import { Button } from "./button";
import { Icon, NODE_KINDS, type IconName, type NodeKind } from "./icon";
import { Input } from "./inputs";

/* ---------- FlowNode ---------- */

export const NODE_WIDTH = 240;
export const NODE_HEIGHT = 64;

/** x offset of output port `index` of `count` along the bottom edge. */
export function outputX(count: number, index: number, width = NODE_WIDTH): number {
  return count === 1 ? width / 2 : Math.round((width * (index + 1)) / (count + 1));
}

function defaultOutputs(kind: NodeKind, outputs?: string[]): string[] {
  return outputs ?? (kind === "output" ? [] : [""]);
}

export interface FlowNodeProps {
  kind: NodeKind;
  title?: string;
  /** One line generated from the options, e.g. "libsvtav1 · vmaf 95 · preset 6". */
  summary?: string;
  /** Output port labels. Default one unlabeled port; [] for output nodes; ["true", "false"] for conditions. */
  outputs?: string[];
  status?: "running" | "done" | "failed";
  invalid?: boolean;
  selected?: boolean;
  /** Canvas position in px. */
  x?: number;
  y?: number;
  /** Render in normal flow (for docs) instead of absolutely on a canvas. */
  static?: boolean;
  connectedIn?: boolean;
  connectedOut?: boolean;
  onClick?: () => void;
}

const STATE_ICON: Record<string, { icon: IconName; label: string; spin?: boolean }> = {
  running: { icon: "loader-circle", label: "Running", spin: true },
  done: { icon: "circle-check", label: "Done" },
  failed: { icon: "circle-x", label: "Failed" },
  invalid: { icon: "triangle-alert", label: "Needs attention" },
};

/** One step of a flow: a fixed 240 x 64 box with a header and one summary line. */
export function FlowNode(props: FlowNodeProps) {
  const kind = () => NODE_KINDS[props.kind] ?? NODE_KINDS.command;
  const outputs = () => defaultOutputs(props.kind, props.outputs);
  const state = () => (props.invalid ? "invalid" : props.status);
  return (
    <div
      role="button"
      tabindex="0"
      class={["av-node", { "av-node-static": props.static, "av-node-invalid": props.invalid, "av-node-running": props.status === "running" }]}
      aria-pressed={props.selected ? "true" : "false"}
      style={props.static ? undefined : { left: `${props.x ?? 0}px`, top: `${props.y ?? 0}px` }}
      onClick={() => props.onClick?.()}
      onKeyDown={(event) => {
        if (event.key !== "Enter" && event.key !== " ") return;
        event.preventDefault();
        props.onClick?.();
      }}
    >
      <div class="av-node-head">
        <Icon name={kind().icon} size={16} />
        <span class="av-node-title">{props.title ?? kind().label}</span>
        <Show when={STATE_ICON[state() ?? ""]}>
          {(icon) => (
            <span class={["av-node-state", `is-${state()}`]}>
              <Icon name={icon().icon} size={14} spin={icon().spin} label={icon().label} />
            </span>
          )}
        </Show>
      </div>
      <div class="av-node-body">
        <span>{props.summary ?? ""}</span>
      </div>
      <Show when={props.kind !== "input"}>
        <span class={["av-port", { "av-port-on": props.connectedIn }]} style={{ left: `${NODE_WIDTH / 2}px`, top: "0px" }} />
      </Show>
      <For each={outputs()} keyed={false}>
        {(label, index) => (
          <>
            <span class={["av-port", { "av-port-on": props.connectedOut }]} style={{ left: `${outputX(outputs().length, index)}px`, top: `${NODE_HEIGHT}px` }} />
            <Show when={label()}>
              <span class="av-port-label" style={{ left: `${outputX(outputs().length, index)}px`, top: `${NODE_HEIGHT + 2}px` }}>
                {label()}
              </span>
            </Show>
          </>
        )}
      </For>
    </div>
  );
}

/* ---------- FlowCanvas ---------- */

export interface FlowCanvasNode extends Omit<FlowNodeProps, "selected" | "static" | "connectedIn" | "connectedOut" | "onClick"> {
  id: string;
  x: number;
  y: number;
}

export interface FlowCanvasEdge {
  from: string;
  to: string;
  /** Index of the source's output port. */
  port?: number;
}

export interface FlowCanvasProps {
  nodes: FlowCanvasNode[];
  edges: FlowCanvasEdge[];
  selectedId?: string | null;
  onSelect?: (id: string) => void;
  /** Node ids whose connecting edges draw in edge-active: the selected node's path or the running file's. */
  activePath?: string[];
  zoom?: number;
  offsetX?: number;
  offsetY?: number;
  /** Fixed height in px. Without it the canvas fills its parent. */
  height?: number;
  /** false hides the zoom bar. */
  tools?: boolean;
  onZoomIn?: () => void;
  onZoomOut?: () => void;
  onFit?: () => void;
}

/** Right-angled edge from an output port down to the top centre of the target, with small bends. */
export function edgePath(x1: number, y1: number, x2: number, y2: number): string {
  if (Math.abs(x1 - x2) < 1) return `M${x1} ${y1}V${y2}`;
  const mid = Math.round((y1 + y2) / 2);
  const r = Math.min(6, Math.abs(x2 - x1) / 2, (y2 - y1) / 4);
  const d = x2 > x1 ? 1 : -1;
  return `M${x1} ${y1}V${mid - r}Q${x1} ${mid} ${x1 + d * r} ${mid}H${x2 - d * r}Q${x2} ${mid} ${x2} ${mid + r}V${y2}`;
}

/**
 * Nodes on a dot grid joined by orthogonal edges. This draws a graph at a
 * given zoom and offset; panning, dragging and connecting belong to the
 * editor that owns the draft.
 */
export function FlowCanvas(props: FlowCanvasProps) {
  const byId = createMemo(() => new Map(props.nodes.map((node) => [node.id, node])));
  const active = createMemo(() => new Set(props.activePath ?? []));
  const linked = createMemo(() => ({
    in: new Set(props.edges.map((edge) => edge.to)),
    out: new Set(props.edges.map((edge) => edge.from)),
  }));
  const paths = createMemo(() =>
    props.edges.flatMap((edge) => {
      const from = byId().get(edge.from);
      const to = byId().get(edge.to);
      if (!from || !to) return [];
      const count = defaultOutputs(from.kind, from.outputs).length;
      return [{ d: edgePath(from.x + outputX(count, edge.port ?? 0), from.y + NODE_HEIGHT, to.x + NODE_WIDTH / 2, to.y), active: active().has(edge.from) && active().has(edge.to) }];
    }),
  );
  const zoom = () => props.zoom ?? 1;

  return (
    <div class="av-canvas" style={{ height: props.height ? `${props.height}px` : "100%" }}>
      <div class="av-canvas-layer" style={{ transform: `translate(${props.offsetX ?? 0}px, ${props.offsetY ?? 0}px) scale(${zoom()})` }}>
        <svg class="av-canvas-edges" width="1" height="1" aria-hidden="true">
          <For each={paths()} keyed={false}>{(path) => <path class={["av-edge", { "av-edge-active": path().active }]} d={path().d} />}</For>
        </svg>
        <For each={props.nodes} keyed={(node) => node.id}>
          {(node) => (
            <FlowNode
              kind={node().kind}
              title={node().title}
              summary={node().summary}
              outputs={node().outputs}
              status={node().status}
              invalid={node().invalid}
              x={node().x}
              y={node().y}
              selected={props.selectedId === node().id}
              connectedIn={linked().in.has(node().id)}
              connectedOut={linked().out.has(node().id)}
              onClick={() => props.onSelect?.(node().id)}
            />
          )}
        </For>
      </div>
      <Show when={props.tools !== false}>
        <div class="av-canvas-tools">
          <Button size="sm" variant="ghost" icon="zoom-out" label="Zoom out" onClick={() => props.onZoomOut?.()} />
          <span class="av-canvas-zoom">{Math.round(zoom() * 100)}%</span>
          <Button size="sm" variant="ghost" icon="zoom-in" label="Zoom in" onClick={() => props.onZoomIn?.()} />
          <Button size="sm" variant="ghost" icon="maximize" label="Fit to view" onClick={() => props.onFit?.()} />
        </div>
      </Show>
    </div>
  );
}

/* ---------- NodePalette ---------- */

export interface PaletteItem {
  /** What `onPick` identifies the entry by: a catalog node type id. */
  id: string;
  kind: NodeKind;
  label: string;
  /** Shown as the tooltip. */
  description?: string;
}

export interface NodePaletteProps {
  /** In pipeline order: Input, Inspect, Video, Audio, Subtitles, Container, Output. */
  groups: { label: string; items: PaletteItem[] }[];
  onPick?: (item: PaletteItem) => void;
}

/** The node templates on the left of the flow editor, grouped, with a filter. */
export function NodePalette(props: NodePaletteProps) {
  const [query, setQuery] = createSignal("");
  const groups = createMemo(() => {
    const q = query().trim().toLowerCase();
    return props.groups.map((group) => ({ label: group.label, items: group.items.filter((item) => !q || item.label.toLowerCase().includes(q)) })).filter((group) => group.items.length > 0);
  });
  return (
    <aside class="av-palette" aria-label="Nodes">
      <div class="av-palette-search">
        <Input size="sm" icon="search" placeholder="Find a node" aria-label="Find a node" value={query()} onInput={(event) => setQuery(event.currentTarget.value)} />
      </div>
      <div class="av-palette-list">
        <For each={groups()} keyed={(group) => group.label}>
          {(group) => (
            <div role="group" aria-label={group().label}>
              <div class="av-pal-group">{group().label}</div>
              <For each={group().items} keyed={(item) => item.id}>
                {(item) => (
                  <button
                    type="button"
                    class="av-pal-item"
                    draggable="true"
                    title={item().description}
                    onClick={() => props.onPick?.(item())}
                    onDragStart={(event) => event.dataTransfer?.setData("application/x-anvil-node-type", item().id)}
                  >
                    <Icon name={NODE_KINDS[item().kind].icon} size={16} />
                    {item().label}
                  </button>
                )}
              </For>
            </div>
          )}
        </For>
        <Show when={groups().length === 0}>
          <div class="av-pal-empty">No node matches "{query()}".</div>
        </Show>
      </div>
    </aside>
  );
}
