// The editor's state besides the document: the node catalog, what is
// selected, where the canvas is looking, and the result of a test. One
// object, created by the page and handed to the top bar, the palette, the
// canvas and the sheet.

import { createMemo, createSignal, flush } from "solid-js";

import { api, ApiError, errorMessage, type FlowTest, type GraphNode, type NodeType } from "../../api";
import { NODE_HEIGHT, NODE_KINDS, NODE_WIDTH, type NodeKind } from "../../ui";
import { createLiveCheck, type LiveCheckSeed } from "./check";
import { createDraft, type DraftSnapshot } from "./draft";
import { ancestors, BRANCH, freePosition, GRID, outgoing, pathTo, ROW, sameGraph, snap, type Doc } from "./graph";
import { tidy } from "./tidy";

export type Selection = { kind: "node"; id: string } | { kind: "edge"; from: string; port: number } | null;

export interface View {
  /** Canvas offset in px. */
  x: number;
  y: number;
  /** Zoom; 1 is 100%. */
  k: number;
}

export type TestState =
  | { status: "running"; path: string }
  | { status: "done"; path: string; result: FlowTest; doc: Doc }
  | { status: "failed"; path: string; message: string };

/** What the first save of a new flow carries over to the editor of the stored flow, so nothing on screen moves. */
export interface Handoff {
  draft: DraftSnapshot;
  view: View;
  selection: Selection;
}

export interface EditorInit {
  /** The stored flow's id, or null for a flow that was never saved. */
  flowId: number | null;
  doc: Doc;
  saved: Doc | null;
  catalog: NodeType[];
  seed: LiveCheckSeed;
  handoff?: Handoff;
}

/** The zoom range of the design: 25% to 200%. */
const MIN_ZOOM = 0.25;
const MAX_ZOOM = 2;
const ZOOM_STEPS = [0.25, 0.33, 0.5, 0.67, 0.8, 1, 1.25, 1.5, 2];
/** Width of the side sheet (token `sheet`), which covers the right of the canvas while open. */
const SHEET = 400;
const EMPTY: ReadonlySet<string> = new Set();

const clampZoom = (k: number) => Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, k));

const TEST_PATH_KEY = "anvil.flow-test-path.";

/** The file last tested with a flow. Storage may be unavailable (private mode, blocked): then nothing is remembered. */
function recallTestPath(flowId: number | null): string {
  try {
    return localStorage.getItem(TEST_PATH_KEY + (flowId ?? "new")) ?? "";
  } catch {
    return "";
  }
}

function rememberTestPath(flowId: number | null, path: string): void {
  try {
    localStorage.setItem(TEST_PATH_KEY + (flowId ?? "new"), path);
  } catch {
    // Nothing to do: the next test starts at the daemon's root listing.
  }
}

export type Editor = ReturnType<typeof createEditor>;

export function createEditor(init: EditorInit) {
  const draft = createDraft(init.handoff?.draft.doc ?? init.doc, init.saved, init.handoff?.draft);
  const check = createLiveCheck(draft, init.seed);
  const types = new Map(init.catalog.map((type) => [type.id, type]));

  const typeOf = (node: GraphNode | undefined): NodeType | undefined => (node ? types.get(node.type) : undefined);
  const kindOf = (node: GraphNode | undefined): NodeKind => typeOf(node)?.kind ?? "command";
  const titleOf = (node: GraphNode | undefined): string => node?.label || typeOf(node)?.label || node?.type || "";
  const isInput = (node: GraphNode | undefined) => typeOf(node)?.kind === "input";

  /* ---------- selection ---------- */

  const [rawSelection, setSelection] = createSignal<Selection>(init.handoff?.selection ?? null);
  /** The selection, dropped as soon as what it names is gone (deleted, or undone away). */
  const selection = createMemo<Selection>(() => {
    const current = rawSelection();
    if (current?.kind === "node") return draft.order().includes(current.id) ? current : null;
    if (current?.kind === "edge") return draft.edges().some((edge) => edge.from === current.from && edge.port === current.port) ? current : null;
    return null;
  });
  const selectedId = createMemo(() => {
    const current = selection();
    return current?.kind === "node" ? current.id : undefined;
  });

  /* ---------- test on a file ---------- */

  const [test, setTest] = createSignal<TestState | null>(null);
  let testRun = 0;
  /** True once the graph differs from the one that was tested: the result no longer describes the draft. */
  const testStale = createMemo(() => {
    const state = test();
    return state?.status === "done" && !sameGraph(draft.doc(), state.doc);
  });
  /** The nodes and edges the tested file passed through, while the result still holds. */
  const marks = createMemo(() => {
    const state = test();
    if (state?.status !== "done" || testStale()) return null;
    const path = state.result.path;
    const edges = new Set<string>();
    path.forEach((id, index) => {
      const next = path[index + 1];
      const edge = next && state.doc.edges.find((candidate) => candidate.from === id && candidate.to === next);
      if (edge) edges.add(`${edge.from}:${edge.port}`);
    });
    return { nodes: new Set(path), edges };
  });

  /* ---------- view ---------- */

  let canvas: HTMLElement | undefined;
  let view: View = init.handoff?.view ?? { x: 0, y: 0, k: 1 };
  const [viewSignal, setViewSignal] = createSignal(view);
  /** False until the canvas has been measured and the first view set, so the graph never shows at a stale offset. */
  const [placed, setPlaced] = createSignal(false);
  /** Offsets stay fractional here, so many small pans add up exactly; the canvas rounds them when it draws. */
  const setView = (next: View) => {
    view = { x: next.x, y: next.y, k: clampZoom(next.k) };
    setViewSignal(view);
  };

  const sheetOpen = () => selectedId() !== undefined || test() !== null;
  /** The part of the canvas nothing covers, in px. */
  const visible = () => {
    const rect = canvas?.getBoundingClientRect();
    const width = rect?.width ?? 0;
    return { width: sheetOpen() && width > SHEET + 240 ? width - SHEET : width, height: rect?.height ?? 0 };
  };

  const toWorld = (clientX: number, clientY: number) => {
    const rect = canvas?.getBoundingClientRect();
    return { x: (clientX - (rect?.left ?? 0) - view.x) / view.k, y: (clientY - (rect?.top ?? 0) - view.y) / view.k };
  };

  /** Zooms to `k`, keeping the canvas point under (clientX, clientY) where it is. */
  const zoomAt = (clientX: number, clientY: number, k: number) => {
    const rect = canvas?.getBoundingClientRect();
    const px = clientX - (rect?.left ?? 0);
    const py = clientY - (rect?.top ?? 0);
    const next = clampZoom(k);
    setView({ x: px - ((px - view.x) / view.k) * next, y: py - ((py - view.y) / view.k) * next, k: next });
  };

  const zoomStep = (direction: 1 | -1) => {
    const next = direction > 0 ? ZOOM_STEPS.find((step) => step > view.k + 0.001) : ZOOM_STEPS.findLast((step) => step < view.k - 0.001);
    if (next === undefined) return;
    const rect = canvas?.getBoundingClientRect();
    const area = visible();
    zoomAt((rect?.left ?? 0) + area.width / 2, (rect?.top ?? 0) + area.height / 2, next);
  };

  /** Shows the whole flow: centred across, starting at the top, never enlarged past 100%. */
  const fit = () => {
    const nodes = draft.current().nodes;
    const area = visible();
    if (nodes.length === 0 || area.width === 0) return setView({ x: 0, y: 0, k: 1 });
    const pad = 2 * GRID;
    const left = Math.min(...nodes.map((node) => node.x));
    const top = Math.min(...nodes.map((node) => node.y));
    const width = Math.max(...nodes.map((node) => node.x)) + NODE_WIDTH - left;
    const height = Math.max(...nodes.map((node) => node.y)) + NODE_HEIGHT - top;
    // Twice the padding below: port labels hang under the last row, and the toolbar floats over the bottom edge.
    const k = clampZoom(Math.min(1, (area.width - 2 * pad) / width, (area.height - 3 * pad) / height));
    setView({ x: (area.width - width * k) / 2 - left * k, y: pad - top * k, k });
  };

  /** Pans just far enough to bring a node clear of the canvas edges and the sheet. */
  const reveal = (id: string) => {
    const node = draft.find(id);
    const area = visible();
    if (!node || area.width === 0) return;
    const margin = GRID;
    const left = view.x + node.x * view.k;
    const top = view.y + node.y * view.k;
    const width = NODE_WIDTH * view.k;
    const height = NODE_HEIGHT * view.k;
    let dx = 0;
    let dy = 0;
    if (left + width > area.width - margin) dx = area.width - margin - (left + width);
    if (left + dx < margin) dx = margin - left;
    if (top + height > area.height - margin) dy = area.height - margin - (top + height);
    if (top + dy < margin) dy = margin - top;
    if (dx !== 0 || dy !== 0) setView({ ...view, x: view.x + dx, y: view.y + dy });
  };

  const select = (next: Selection) => {
    setSelection(next);
    if (next?.kind !== "node") return;
    // The sheet that opens covers the right of the canvas; the node must not end up under it.
    // Flushed first, so the measurement already counts the sheet.
    flush();
    reveal(next.id);
  };

  /* ---------- edits that need the catalog or the view ---------- */

  /** A flow has one input, so the only input node cannot go; every other node can. */
  const canRemove = (id: string) => {
    const node = draft.find(id);
    return !!node && (!isInput(node) || draft.current().nodes.filter(isInput).length > 1);
  };
  const canDuplicate = (id: string) => {
    const node = draft.find(id);
    return !!node && !isInput(node);
  };

  /** The first output port of a node that has no edge yet. */
  const freePort = (node: GraphNode): number | undefined => {
    const taken = outgoing(draft.current().edges).get(node.id);
    const count = typeOf(node)?.outputs.length ?? 0;
    for (let port = 0; port < count; port++) if (!taken?.has(port)) return port;
    return undefined;
  };

  /**
   * Adds a node from the palette. With a node selected it goes below that
   * node, on the side of the output it is wired to when one is free;
   * otherwise it goes to the middle of what the canvas shows. Never on top
   * of another node.
   */
  const addFromPalette = (type: string): string | undefined => {
    const kind = types.get(type)?.kind;
    if (!kind) return undefined;
    const doc = draft.current();
    const anchorId = selectedId();
    const anchor = anchorId ? draft.find(anchorId) : undefined;
    let wanted: { x: number; y: number };
    let from: { id: string; port: number } | undefined;
    if (anchor) {
      // An input starts the flow, so nothing is wired into it.
      const port = kind === "input" ? undefined : freePort(anchor);
      const ports = typeOf(anchor)?.outputs.length ?? 1;
      const side = port === undefined || ports < 2 ? 0 : port === 0 ? -BRANCH : port === ports - 1 ? BRANCH : 0;
      wanted = { x: anchor.x + side, y: anchor.y + ROW };
      if (port !== undefined) from = { id: anchor.id, port };
    } else {
      const area = visible();
      const centre = { x: (area.width / 2 - view.x) / view.k, y: (area.height / 2 - view.y) / view.k };
      wanted = { x: centre.x - NODE_WIDTH / 2, y: centre.y - NODE_HEIGHT / 2 };
    }
    const at = freePosition(wanted.x, wanted.y, doc.nodes);
    const id = draft.addNode(type, at.x, at.y, from);
    select({ kind: "node", id });
    return id;
  };

  /** Adds a node with its centre under a viewport point: a drop from the palette. */
  const addAt = (type: string, clientX: number, clientY: number): string | undefined => {
    if (!types.has(type)) return undefined;
    const point = toWorld(clientX, clientY);
    const at = freePosition(point.x - NODE_WIDTH / 2, point.y - NODE_HEIGHT / 2, draft.current().nodes);
    const id = draft.addNode(type, at.x, at.y);
    select({ kind: "node", id });
    return id;
  };

  const duplicate = (id: string): string | undefined => {
    const node = draft.find(id);
    if (!node || !canDuplicate(id)) return undefined;
    const at = freePosition(node.x + 2 * GRID, node.y + ROW, draft.current().nodes);
    const copy = draft.addNode(node.type, at.x, at.y, undefined, node);
    check.hint(copy, check.summary(id, typeOf(node)));
    select({ kind: "node", id: copy });
    return copy;
  };

  const removeNode = (id: string) => {
    if (canRemove(id)) draft.removeNode(id);
  };

  /** Deletes the selected node or edge. Returns false when there was nothing it could delete. */
  const removeSelection = (): boolean => {
    const current = selection();
    if (current?.kind === "edge") draft.disconnect(current.from, current.port);
    else if (current?.kind === "node" && canRemove(current.id)) draft.removeNode(current.id);
    else return false;
    return true;
  };

  const nudge = (id: string, dx: number, dy: number) => {
    const node = draft.find(id);
    if (!node) return;
    draft.moveNode(id, snap(node.x) + dx * GRID, snap(node.y) + dy * GRID, { coalesce: `nudge:${id}` });
    reveal(id);
  };

  /** Whether an edge from `from` may end at `to`: not at itself, not at an input, and not where it would close a loop. */
  const validTargets = (from: string): Set<string> => {
    const doc = draft.current();
    const above = ancestors(from, doc.edges);
    return new Set(doc.nodes.filter((node) => !above.has(node.id) && !isInput(node)).map((node) => node.id));
  };

  const tidyUp = () => {
    const doc = draft.current();
    draft.moveNodes(tidy(doc.nodes, doc.edges, (node) => typeOf(node)?.outputs.length ?? 1, isInput));
    fit();
  };

  /* ---------- test ---------- */

  const runTest = (path: string) => {
    const doc = draft.current();
    const run = ++testRun;
    const covered = !sheetOpen();
    setTest({ status: "running", path });
    rememberTestPath(init.flowId, path);
    if (covered) {
      // The result sheet now covers the right of the canvas, and the path it marks may run under it.
      flush();
      fit();
    }
    api.flows.test({ nodes: [...doc.nodes], edges: [...doc.edges] }, path).then(
      (result) => {
        if (run === testRun) setTest({ status: "done", path, result, doc });
      },
      (err: unknown) => {
        if (run !== testRun) return;
        // A flow the daemon refuses to evaluate comes back with its issues; they belong on the canvas too.
        if (err instanceof ApiError && err.issues.length > 0) check.report(err.issues);
        setTest({ status: "failed", path, message: errorMessage(err) });
      },
    );
  };

  return {
    flowId: init.flowId,
    draft,
    check,
    catalog: init.catalog,
    types,
    typeOf,
    kindOf,
    titleOf,
    isInput,
    kindLabel: (node: GraphNode | undefined) => NODE_KINDS[kindOf(node)].label,

    selection,
    selectedId,
    select,
    /** The edges that draw in edge-active: the tested file's path, or the ways down to the selected node. */
    activeEdges: createMemo<ReadonlySet<string>>(() => {
      const tested = marks();
      if (tested) return tested.edges;
      const id = selectedId();
      return id ? pathTo(id, draft.edges()) : EMPTY;
    }),

    view: viewSignal,
    currentView: () => view,
    placed,
    setView,
    zoomAt,
    zoomStep,
    fit,
    reveal,
    toWorld,
    /** Called once the canvas is in the document. Fits the flow unless a view came with the handoff. */
    attach(element: HTMLElement) {
      canvas = element;
      if (!init.handoff) fit();
      setPlaced(true);
    },
    canvasElement: () => canvas,

    canRemove,
    canDuplicate,
    addFromPalette,
    addAt,
    duplicate,
    removeNode,
    removeSelection,
    nudge,
    validTargets,
    tidyUp,

    test,
    testStale,
    marks,
    runTest,
    clearTest: () => {
      testRun += 1;
      setTest(null);
    },
    lastTestPath: () => recallTestPath(init.flowId),

    handoff: (): Handoff => ({ draft: draft.snapshot(), view, selection: selection() }),
  };
}
