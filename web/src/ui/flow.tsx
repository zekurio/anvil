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

  /* The rest is for the flow editor, which moves and connects nodes. */

  /** Written to data-node-id, so a pointer position can be traced back to its node. */
  nodeId?: string;
  /** Which output ports have an edge. Overrides `connectedOut`, which fills every port alike. */
  connectedOutputs?: boolean[];
  /** Lifted off the canvas: draws `shadow-drag`. */
  dragging?: boolean;
  /** Not part of what is being shown, e.g. off the path a tested file took. */
  dimmed?: boolean;
  /** A place the edge being drawn may end. */
  dropTarget?: boolean;
  onPointerDown?: (event: PointerEvent & { currentTarget: HTMLDivElement }) => void;
  /** Runs before the node's own Enter and Space handling; prevent the default to replace it. */
  onKeyDown?: (event: KeyboardEvent & { currentTarget: HTMLDivElement }) => void;
  /** Makes every output port a button that starts an edge. */
  onOutputPointerDown?: (index: number, event: PointerEvent & { currentTarget: HTMLButtonElement }) => void;
  /** The accessible name of output port `index`. Required with `onOutputPointerDown`. */
  outputLabel?: (index: number) => string;
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
  const connected = (index: number) => props.connectedOutputs?.[index] ?? props.connectedOut;
  return (
    <div
      role="button"
      tabindex="0"
      class={[
        "av-node",
        {
          "av-node-static": props.static,
          "av-node-invalid": props.invalid,
          "av-node-running": props.status === "running",
          "av-node-dragging": props.dragging,
          "av-node-dimmed": props.dimmed,
          "av-node-target": props.dropTarget,
        },
      ]}
      aria-pressed={props.selected ? "true" : "false"}
      data-node-id={props.nodeId}
      style={props.static ? undefined : { left: `${props.x ?? 0}px`, top: `${props.y ?? 0}px` }}
      onClick={() => props.onClick?.()}
      onPointerDown={(event) => props.onPointerDown?.(event)}
      onKeyDown={(event) => {
        props.onKeyDown?.(event);
        if (event.defaultPrevented || event.target !== event.currentTarget) return;
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
        {/* The line truncates; the tooltip has all of it. */}
        <span title={props.summary}>{props.summary ?? ""}</span>
      </div>
      <Show when={props.kind !== "input"}>
        <span class={["av-port", { "av-port-on": props.connectedIn }]} style={{ left: `${NODE_WIDTH / 2}px`, top: "0px" }} />
      </Show>
      <For each={outputs()} keyed={false}>
        {(label, index) => (
          <>
            <span class={["av-port", { "av-port-on": connected(index) }]} style={{ left: `${outputX(outputs().length, index)}px`, top: `${NODE_HEIGHT}px` }} />
            <Show when={label()}>
              <span class="av-port-label" style={{ left: `${outputX(outputs().length, index)}px`, top: `${NODE_HEIGHT + 2}px` }}>
                {label()}
              </span>
            </Show>
            <Show when={props.onOutputPointerDown}>
              {/* Pointer only, so out of the tab order: the palette connects a new node from the keyboard. */}
              <button
                type="button"
                class="av-port-hit"
                tabindex="-1"
                aria-label={props.outputLabel?.(index)}
                title={props.outputLabel?.(index)}
                style={{ left: `${outputX(outputs().length, index)}px`, top: `${NODE_HEIGHT}px` }}
                onPointerDown={(event) => props.onOutputPointerDown?.(index, event)}
              />
            </Show>
          </>
        )}
      </For>
    </div>
  );
}

/* ---------- FlowCanvas ---------- */

export interface FlowCanvasNode extends Pick<FlowNodeProps, "kind" | "title" | "summary" | "outputs" | "status" | "invalid"> {
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

const BEND = 6;
/** How far above its target an edge turns towards it, so edges that rejoin share one line. */
const LEAD = 24;
/** How far an edge runs past a port before it turns, when it has to go around. */
const STUB = 16;

/** A path through right-angled corner points, each corner rounded by at most `BEND`. */
function roundedPath(points: (readonly [number, number])[]): string {
  let d = "";
  points.forEach(([x, y], i) => {
    const before = points[i - 1];
    const after = points[i + 1];
    if (!before) d += `M${x} ${y}`;
    else if (!after) d += `L${x} ${y}`;
    else {
      const into = Math.hypot(x - before[0], y - before[1]);
      const out = Math.hypot(after[0] - x, after[1] - y);
      const r = Math.min(BEND, into / 2, out / 2);
      if (r < 1) d += `L${x} ${y}`;
      else d += `L${x + ((before[0] - x) / into) * r} ${y + ((before[1] - y) / into) * r}Q${x} ${y} ${x + ((after[0] - x) / out) * r} ${y + ((after[1] - y) / out) * r}`;
    }
  });
  return d;
}

/** The corner points of the edge `edgePath` draws. */
function edgeCorners(x1: number, y1: number, x2: number, y2: number): (readonly [number, number])[] {
  if (y2 - y1 >= STUB) {
    if (Math.abs(x1 - x2) < 1) {
      return [
        [x1, y1],
        [x2, y2],
      ];
    }
    const turn = Math.max(Math.round((y1 + y2) / 2), y2 - LEAD);
    return [
      [x1, y1],
      [x1, turn],
      [x2, turn],
      [x2, y2],
    ];
  }
  // Between the two nodes when there is room, otherwise past the right edge of both.
  const side = Math.abs(x2 - x1) >= NODE_WIDTH + 2 * STUB ? Math.round((x1 + x2) / 2) : Math.max(x1, x2) + NODE_WIDTH / 2 + 2 * STUB;
  return [
    [x1, y1],
    [x1, y1 + STUB],
    [side, y1 + STUB],
    [side, y2 - STUB],
    [x2, y2 - STUB],
    [x2, y2],
  ];
}

/**
 * Right-angled edge from an output port down to the top centre of the target,
 * with small bends. It turns just above the target, so branches that rejoin
 * meet in one line above the shared node. A target that is not below the
 * port is reached around the side.
 */
export function edgePath(x1: number, y1: number, x2: number, y2: number): string {
  return roundedPath(edgeCorners(x1, y1, x2, y2));
}

/** The middle of an edge's middle stretch: where a control that belongs to the edge sits. */
export function edgeMidpoint(x1: number, y1: number, x2: number, y2: number): { x: number; y: number } {
  const corners = edgeCorners(x1, y1, x2, y2);
  const a = corners[Math.floor((corners.length - 1) / 2)] ?? [x1, y1];
  const b = corners[Math.floor((corners.length - 1) / 2) + 1] ?? [x2, y2];
  return { x: (a[0] + b[0]) / 2, y: (a[1] + b[1]) / 2 };
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
    out: new Set(props.edges.map((edge) => `${edge.from}:${edge.port ?? 0}`)),
  }));
  const paths = createMemo(() =>
    props.edges
      .flatMap((edge) => {
        const from = byId().get(edge.from);
        const to = byId().get(edge.to);
        if (!from || !to) return [];
        const count = defaultOutputs(from.kind, from.outputs).length;
        return [{ d: edgePath(from.x + outputX(count, edge.port ?? 0), from.y + NODE_HEIGHT, to.x + NODE_WIDTH / 2, to.y), active: active().has(edge.from) && active().has(edge.to) }];
      })
      // Active edges last, so a resting edge that shares a stretch never paints over them.
      .sort((a, b) => Number(a.active) - Number(b.active)),
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
              connectedOutputs={defaultOutputs(node().kind, node().outputs).map((_, port) => linked().out.has(`${node().id}:${port}`))}
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

/** One step of dragging a palette entry, in viewport coordinates. */
export interface PaletteDrag {
  item: PaletteItem;
  phase: "move" | "drop" | "cancel";
  x: number;
  y: number;
}

export interface NodePaletteProps {
  /** In pipeline order: Input, Inspect, Video, Audio, Subtitles, Container, Output. */
  groups: { label: string; items: PaletteItem[] }[];
  /** A click, or Enter and Space. */
  onPick?: (item: PaletteItem) => void;
  /**
   * Follows an entry dragged out of the list. Passing it makes entries
   * draggable; the owner draws what is dragged and decides what a drop does.
   */
  onDrag?: (drag: PaletteDrag) => void;
}

/** How far the pointer travels before a press on an entry becomes a drag. */
const DRAG_SLOP = 4;

/** The node templates on the left of the flow editor, grouped, with a filter. */
export function NodePalette(props: NodePaletteProps) {
  const [query, setQuery] = createSignal("");
  const groups = createMemo(() => {
    const q = query().trim().toLowerCase();
    return props.groups.map((group) => ({ label: group.label, items: group.items.filter((item) => !q || item.label.toLowerCase().includes(q)) })).filter((group) => group.items.length > 0);
  });

  // Pointer events rather than HTML drag and drop: Firefox does not start a
  // native drag from a <button>, and this way a touch screen works as well.
  let suppressClick = false;
  const press = (item: PaletteItem, down: PointerEvent & { currentTarget: HTMLButtonElement }) => {
    if (!props.onDrag || down.button !== 0) return;
    const el = down.currentTarget;
    let dragging = false;
    const move = (event: PointerEvent) => {
      if (event.pointerId !== down.pointerId) return;
      if (!dragging && Math.hypot(event.clientX - down.clientX, event.clientY - down.clientY) < DRAG_SLOP) return;
      if (!dragging) el.setPointerCapture(down.pointerId);
      dragging = true;
      props.onDrag?.({ item, phase: "move", x: event.clientX, y: event.clientY });
    };
    const end = (event: PointerEvent) => {
      if (event.pointerId !== down.pointerId) return;
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", end);
      window.removeEventListener("pointercancel", end);
      window.removeEventListener("keydown", key, true);
      if (!dragging) return;
      // The click that follows a drag must not also pick the entry.
      suppressClick = true;
      setTimeout(() => (suppressClick = false));
      props.onDrag?.({ item, phase: event.type === "pointerup" ? "drop" : "cancel", x: event.clientX, y: event.clientY });
    };
    const key = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || !dragging) return;
      event.preventDefault();
      event.stopPropagation();
      end(new PointerEvent("pointercancel", { pointerId: down.pointerId, clientX: down.clientX, clientY: down.clientY }));
    };
    // On the window, not the entry: until the drag has started nothing holds
    // the pointer, and a quick pull leaves the 28px row between two move
    // events. The entry would never hear of it and the drag would not start.
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", end);
    window.addEventListener("pointercancel", end);
    window.addEventListener("keydown", key, true);
  };

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
                    title={item().description}
                    onPointerDown={(event) => press(item(), event)}
                    onClick={() => {
                      if (!suppressClick) props.onPick?.(item());
                    }}
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
