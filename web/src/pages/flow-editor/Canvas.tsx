// The editing canvas: pan and zoom, drag nodes on the grid, draw edges from
// output ports, select and delete. It draws with the design system's
// FlowNode and edgePath, and owns only the pointer work; every change goes
// through the draft.
//
// A node reads its own signal and each edge reads the two nodes it joins, so
// dragging updates one node and its edges, whatever the size of the flow.

import { createMemo, createSignal, For, onSettled, Show } from "solid-js";

import type { GraphEdge, GraphNode } from "../../api";
import { Button, edgeMidpoint, edgePath, FlowNode, Icon, NODE_HEIGHT, NODE_WIDTH, outputX } from "../../ui";
import type { Editor } from "./editor";
import { GRID, snap } from "./graph";

/** How far the pointer travels before a press becomes a drag. */
const SLOP = 4;

interface Connecting {
  from: string;
  port: number;
  /** The pointer, in canvas coordinates. */
  x: number;
  y: number;
  /** Nodes the edge may end at. */
  targets: Set<string>;
  /** The valid target under the pointer. */
  over: string | null;
}

const edgeKey = (edge: GraphEdge) => `${edge.from}:${edge.port}`;

export function EditorCanvas(props: { editor: Editor }) {
  const editor = props.editor;
  const draft = editor.draft;
  let root: HTMLDivElement | undefined;

  const [dragging, setDragging] = createSignal<string | null>(null);
  const [connecting, setConnecting] = createSignal<Connecting | null>(null);
  const [panning, setPanning] = createSignal(false);

  const linked = createMemo(() => ({
    in: new Set(draft.edges().map((edge) => edge.to)),
    out: new Set(draft.edges().map(edgeKey)),
  }));

  /** Where an edge starts and ends, read from the two nodes it joins. */
  const ends = (edge: GraphEdge) => {
    const from = draft.node(edge.from);
    const to = draft.node(edge.to);
    if (!from || !to) return undefined;
    const ports = Math.max(editor.typeOf(from)?.outputs.length ?? 1, 1);
    return { x1: from.x + outputX(ports, edge.port), y1: from.y + NODE_HEIGHT, x2: to.x + NODE_WIDTH / 2, y2: to.y };
  };

  const isSelectedEdge = (edge: GraphEdge) => {
    const current = editor.selection();
    return current?.kind === "edge" && current.from === edge.from && current.port === edge.port;
  };

  /* ---------- pan and zoom ---------- */

  const pointers = new Map<number, { x: number; y: number }>();
  let travelled = 0;

  const canvasDown = (event: PointerEvent & { currentTarget: HTMLDivElement }) => {
    if ((event.target as Element).closest(".av-node, .av-edge-hit, .av-edge-remove, .av-canvas-tools")) return;
    if (event.button !== 0 && event.button !== 1) return;
    // The middle button pans too; without this the browser starts its own scroll mode.
    if (event.button === 1) event.preventDefault();
    if (pointers.size === 0) travelled = 0;
    pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    event.currentTarget.setPointerCapture(event.pointerId);
    setPanning(true);
  };

  const canvasMove = (event: PointerEvent) => {
    const last = pointers.get(event.pointerId);
    if (!last) return;
    const view = editor.currentView();
    const other = [...pointers].find(([id]) => id !== event.pointerId)?.[1];
    if (other) {
      // Two fingers: the distance between them zooms, their midpoint pans.
      const before = Math.hypot(last.x - other.x, last.y - other.y);
      const after = Math.hypot(event.clientX - other.x, event.clientY - other.y);
      if (before > 0) editor.zoomAt((event.clientX + other.x) / 2, (event.clientY + other.y) / 2, view.k * (after / before));
      const moved = editor.currentView();
      editor.setView({ ...moved, x: moved.x + (event.clientX - last.x) / 2, y: moved.y + (event.clientY - last.y) / 2 });
    } else {
      editor.setView({ ...view, x: view.x + event.clientX - last.x, y: view.y + event.clientY - last.y });
    }
    travelled += Math.abs(event.clientX - last.x) + Math.abs(event.clientY - last.y);
    pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
  };

  const canvasUp = (event: PointerEvent) => {
    if (!pointers.delete(event.pointerId)) return;
    if (pointers.size > 0) return;
    setPanning(false);
    // A press that went nowhere is a click on the empty canvas.
    if (event.type === "pointerup" && travelled < SLOP) editor.select(null);
  };

  const wheel = (event: WheelEvent) => {
    if ((event.target as Element).closest(".av-canvas-tools")) return;
    event.preventDefault();
    const view = editor.currentView();
    const unit = event.deltaMode === 1 ? GRID : 1;
    if (event.ctrlKey || event.metaKey) {
      // A trackpad pinch arrives as small ctrl+wheel steps, a mouse wheel as notches of 100 or more.
      const step = Math.max(-0.25, Math.min(0.25, -event.deltaY * unit * 0.01));
      editor.zoomAt(event.clientX, event.clientY, view.k * Math.exp(step));
      return;
    }
    const dx = (event.shiftKey && event.deltaX === 0 ? event.deltaY : event.deltaX) * unit;
    const dy = (event.shiftKey && event.deltaX === 0 ? 0 : event.deltaY) * unit;
    editor.setView({ ...view, x: view.x - dx, y: view.y - dy });
  };

  onSettled(() => {
    if (!root) return;
    const el = root;
    // Not through JSX: the handler prevents the page from scrolling and zooming, which a passive listener may not.
    el.addEventListener("wheel", wheel, { passive: false });
    editor.attach(el);
    return () => el.removeEventListener("wheel", wheel);
  });

  /* ---------- drag a node ---------- */

  let gesture = 0;

  /** Runs `cancel` if Escape is pressed before the returned function is called. */
  const onEscape = (cancel: () => void) => {
    const listener = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      cancel();
    };
    window.addEventListener("keydown", listener, true);
    return () => window.removeEventListener("keydown", listener, true);
  };

  const nodeDown = (id: string, down: PointerEvent & { currentTarget: HTMLDivElement }) => {
    if (down.button !== 0 || (down.target as Element).closest(".av-port-hit")) return;
    const start = draft.find(id);
    if (!start) return;
    const el = down.currentTarget;
    const key = `drag:${++gesture}`;
    let moved = false;
    el.setPointerCapture(down.pointerId);

    const move = (event: PointerEvent) => {
      if (event.pointerId !== down.pointerId) return;
      const dx = event.clientX - down.clientX;
      const dy = event.clientY - down.clientY;
      if (!moved && Math.hypot(dx, dy) < SLOP) return;
      if (!moved) setDragging(id);
      moved = true;
      const k = editor.currentView().k;
      draft.moveNode(id, snap(start.x + dx / k), snap(start.y + dy / k), { coalesce: key, sticky: true });
    };
    const finish = (cancelled: boolean) => {
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
      el.removeEventListener("pointercancel", up);
      stopEscape();
      if (el.hasPointerCapture(down.pointerId)) el.releasePointerCapture(down.pointerId);
      setDragging(null);
      const end = draft.find(id);
      // A drag that ends where it began, or was cancelled, leaves no undo step.
      if (cancelled || (moved && end?.x === start.x && end.y === start.y)) draft.abort(key);
      // A press without movement is a click. A drag leaves the selection, and so the sheet, as it was.
      else if (!moved) editor.select({ kind: "node", id });
    };
    const up = (event: PointerEvent) => {
      if (event.pointerId === down.pointerId) finish(event.type === "pointercancel");
    };
    const stopEscape = onEscape(() => {
      if (moved) finish(true);
    });
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
    el.addEventListener("pointercancel", up);
  };

  /* ---------- draw an edge ---------- */

  const portDown = (id: string, port: number, down: PointerEvent & { currentTarget: HTMLButtonElement }) => {
    if (down.button !== 0) return;
    // The press belongs to the port: it must not start dragging the node, and it must not move focus.
    down.stopPropagation();
    down.preventDefault();
    const el = down.currentTarget;
    el.setPointerCapture(down.pointerId);
    let state: Connecting = { from: id, port, ...editor.toWorld(down.clientX, down.clientY), targets: editor.validTargets(id), over: null };
    setConnecting(state);

    const move = (event: PointerEvent) => {
      if (event.pointerId !== down.pointerId) return;
      // elementFromPoint, not the event target: the port holds the pointer capture, and anything lying over a node (the sheet) must hide it.
      const hit = document.elementFromPoint(event.clientX, event.clientY)?.closest("[data-node-id]");
      const over = hit && root?.contains(hit) ? hit.getAttribute("data-node-id") : null;
      state = { ...state, ...editor.toWorld(event.clientX, event.clientY), over: over && state.targets.has(over) ? over : null };
      setConnecting(state);
    };
    const finish = (connect: boolean) => {
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
      el.removeEventListener("pointercancel", up);
      stopEscape();
      if (el.hasPointerCapture(down.pointerId)) el.releasePointerCapture(down.pointerId);
      setConnecting(null);
      // A drop anywhere but on a valid node cancels and leaves the port's edge as it was.
      if (connect && state.over) draft.connect(id, port, state.over);
    };
    const up = (event: PointerEvent) => {
      if (event.pointerId === down.pointerId) finish(event.type === "pointerup");
    };
    const stopEscape = onEscape(() => finish(false));
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
    el.addEventListener("pointercancel", up);
  };

  const preview = createMemo(() => {
    const state = connecting();
    const from = state && draft.node(state.from);
    if (!state || !from) return undefined;
    const ports = Math.max(editor.typeOf(from)?.outputs.length ?? 1, 1);
    const over = state.over ? draft.node(state.over) : undefined;
    // Over a valid node the edge snaps to that node's input.
    return edgePath(from.x + outputX(ports, state.port), from.y + NODE_HEIGHT, over ? over.x + NODE_WIDTH / 2 : state.x, over ? over.y : state.y);
  });

  /* ---------- view ---------- */

  /** Whole pixels, so 1px borders stay sharp at 100%. */
  const offset = () => ({ x: Math.round(editor.view().x), y: Math.round(editor.view().y) });

  /** The dot grid follows the view. Zoomed far out it thins to every second or fourth dot, so it never turns into a fill. */
  const grid = () => {
    let step = GRID * editor.view().k;
    while (step < 12) step *= 2;
    return { "background-size": `${step}px ${step}px`, "background-position": `${offset().x - step / 2}px ${offset().y - step / 2}px` };
  };

  const portName = (node: GraphNode, index: number) => {
    const label = editor.typeOf(node)?.outputs[index];
    return label ? `Connect the ${label} output of ${editor.titleOf(node)}` : `Connect the output of ${editor.titleOf(node)}`;
  };

  const removeHandle = createMemo(() => {
    const edge = draft.edges().find(isSelectedEdge);
    const at = edge && ends(edge);
    return edge && at ? { edge, ...edgeMidpoint(at.x1, at.y1, at.x2, at.y2) } : undefined;
  });

  return (
    <div
      ref={(el) => (root = el)}
      class={["av-canvas", "av-canvas-live", { "av-canvas-panning": panning(), "av-canvas-connecting": !!connecting() }]}
      style={grid()}
      onPointerDown={canvasDown}
      onPointerMove={canvasMove}
      onPointerUp={canvasUp}
      onPointerCancel={canvasUp}
    >
      <div class="av-canvas-layer" style={{ transform: `translate(${offset().x}px, ${offset().y}px) scale(${editor.view().k})`, visibility: editor.placed() ? "visible" : "hidden" }}>
        {/* Nodes first in the document, so Tab reaches them before the edges; z-index puts them on top. */}
        <For each={draft.order()}>
          {(id) => {
            const node = () => draft.node(id) as GraphNode;
            const type = createMemo(() => editor.typeOf(node()));
            const invalid = createMemo(() => editor.check.invalid(id));
            // An invalid node names its problem where its summary would be.
            const summary = createMemo(() => {
              const problem = invalid() ? editor.check.issuesOf(id).find((issue) => issue.severity === "error") : undefined;
              if (!problem) return editor.check.summary(id, type());
              const field = problem.field && type()?.sections.flatMap((section) => section.fields).find((candidate) => candidate.key === problem.field);
              return field ? `${field.label}: ${problem.message}` : problem.message;
            });
            return (
              <FlowNode
                nodeId={id}
                kind={editor.kindOf(node())}
                title={editor.titleOf(node())}
                summary={summary()}
                outputs={type()?.outputs ?? []}
                invalid={invalid()}
                status={editor.marks()?.nodes.has(id) ? "done" : undefined}
                dimmed={!!editor.marks() && !editor.marks()?.nodes.has(id)}
                selected={editor.selectedId() === id}
                x={node().x}
                y={node().y}
                connectedIn={linked().in.has(id)}
                connectedOutputs={(type()?.outputs ?? []).map((_, port) => linked().out.has(`${id}:${port}`))}
                dragging={dragging() === id}
                dropTarget={connecting()?.targets.has(id)}
                onPointerDown={(event) => nodeDown(id, event)}
                onKeyDown={(event) => {
                  if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return;
                  event.preventDefault();
                  editor.select({ kind: "node", id });
                }}
                onOutputPointerDown={(port, event) => portDown(id, port, event)}
                outputLabel={(port) => portName(node(), port)}
              />
            );
          }}
        </For>

        <svg class="av-canvas-edges" width="1" height="1" role="group" aria-label="Edges">
          <For each={draft.edges()} keyed={edgeKey}>
            {(edge) => {
              const d = createMemo(() => {
                const at = ends(edge());
                return at ? edgePath(at.x1, at.y1, at.x2, at.y2) : "";
              });
              const select = () => editor.select({ kind: "edge", from: edge().from, port: edge().port });
              return (
                <g class={{ "av-edge-replaced": connecting()?.from === edge().from && connecting()?.port === edge().port }}>
                  <path
                    class="av-edge-hit"
                    d={d()}
                    tabindex="0"
                    role="button"
                    aria-pressed={isSelectedEdge(edge()) ? "true" : "false"}
                    aria-label={`Edge from ${editor.titleOf(draft.node(edge().from))} to ${editor.titleOf(draft.node(edge().to))}`}
                    onPointerDown={(event) => {
                      if (event.button === 0) select();
                    }}
                    onKeyDown={(event) => {
                      if (event.key !== "Enter" && event.key !== " ") return;
                      event.preventDefault();
                      select();
                    }}
                  />
                  <path class="av-edge" d={d()} />
                </g>
              );
            }}
          </For>
          {/* Drawn again above the rest: a resting edge that shares a stretch with an active one must not paint over it. */}
          <For each={draft.edges()} keyed={edgeKey}>
            {(edge) => (
              <Show when={editor.activeEdges().has(edgeKey(edge())) || isSelectedEdge(edge())}>
                <path
                  class={["av-edge", "av-edge-active", { "av-edge-selected": isSelectedEdge(edge()) }]}
                  d={(() => {
                    const at = ends(edge());
                    return at ? edgePath(at.x1, at.y1, at.x2, at.y2) : "";
                  })()}
                />
              </Show>
            )}
          </For>
          <Show when={preview()}>{(d) => <path class="av-edge av-edge-preview" d={d()} />}</Show>
        </svg>

        <Show when={removeHandle()}>
          {(handle) => (
            <button
              type="button"
              class="av-edge-remove"
              aria-label="Delete edge"
              title="Delete edge"
              style={{ left: `${handle().x}px`, top: `${handle().y}px`, transform: `translate(-50%, -50%) scale(${1 / editor.view().k})` }}
              onClick={() => draft.disconnect(handle().edge.from, handle().edge.port)}
            >
              <Icon name="x" size={12} />
            </button>
          )}
        </Show>
      </div>

      <div class="av-canvas-tools" role="toolbar" aria-label="Canvas">
        <Button size="sm" variant="ghost" icon="zoom-out" label="Zoom out" onClick={() => editor.zoomStep(-1)} />
        <span class="av-canvas-zoom">{Math.round(editor.view().k * 100)}%</span>
        <Button size="sm" variant="ghost" icon="zoom-in" label="Zoom in" onClick={() => editor.zoomStep(1)} />
        <Button size="sm" variant="ghost" icon="maximize" label="Fit to view" onClick={() => editor.fit()} />
        <span class="av-canvas-sep" />
        <Button size="sm" variant="ghost" icon="undo-2" label="Undo" disabled={!draft.canUndo()} onClick={() => draft.undo()} />
        <Button size="sm" variant="ghost" icon="redo-2" label="Redo" disabled={!draft.canRedo()} onClick={() => draft.redo()} />
        <span class="av-canvas-sep" />
        <Button size="sm" variant="ghost" onClick={() => editor.tidyUp()}>
          Tidy up
        </Button>
      </div>
    </div>
  );
}
