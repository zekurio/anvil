// The flow draft: every edit to the graph, the name and the description
// lands here, with undo and redo.
//
// The document is an immutable value (see Doc). The draft keeps a plain copy
// for event handlers, which must read what they just wrote (Solid applies a
// signal write only at the next flush), and mirrors it into signals for the
// view. Each node has its own signal, so moving one node re-renders that node
// and the edges that touch it, not the graph.

import { createMemo, createSignal, type Accessor } from "solid-js";

import type { GraphEdge, GraphNode, OptionValue } from "../../api";
import { countChanges, freshId, sameOptionValue, type Doc } from "./graph";

/** One undo step: the document as it was before the edit. */
interface Step {
  before: Doc;
  /** Edits sharing a key within `COALESCE_MS` of each other fold into this step. */
  key?: string;
  at: number;
}

export interface CommitOptions {
  /** Folds a run of like edits (typing in one field, one drag) into a single undo step. */
  coalesce?: string;
  /** Keeps folding however long the run takes. For a drag, which ends with the pointer and not with a pause. */
  sticky?: boolean;
}

/** What survives the editor being rebuilt: the first save of a new flow moves to another route. */
export interface DraftSnapshot {
  doc: Doc;
  past: Doc[];
  future: Doc[];
}

const COALESCE_MS = 1000;
const HISTORY_LIMIT = 200;

export type FlowDraft = ReturnType<typeof createDraft>;

export function createDraft(initial: Doc, savedDoc: Doc | null, history?: Pick<DraftSnapshot, "past" | "future">) {
  let doc = initial;
  let past: Step[] = (history?.past ?? []).map((before) => ({ before, at: 0 }));
  let future: Doc[] = [...(history?.future ?? [])];

  /** Never shrinks: a view of a node that was just removed may still read it once before it is disposed. */
  const cells = new Map<string, readonly [Accessor<GraphNode>, (node: GraphNode) => void]>();
  const plain = new Map<string, GraphNode>();

  const addCell = (node: GraphNode) => {
    const [read, write] = createSignal(node);
    cells.set(node.id, [read, (value) => write(() => value)]);
    plain.set(node.id, node);
  };
  // Filled here rather than through sync(): a component body may create signals but not write them.
  for (const node of doc.nodes) if (!plain.has(node.id)) addCell(node);

  const [docSignal, setDocSignal] = createSignal(doc);
  const [order, setOrder] = createSignal<readonly string[]>(
    doc.nodes.map((node) => node.id),
    { equals: (a, b) => a.length === b.length && a.every((id, index) => id === b[index]) },
  );
  const [edges, setEdges] = createSignal<readonly GraphEdge[]>(doc.edges);
  const [name, setNameSignal] = createSignal(doc.name);
  const [description, setDescriptionSignal] = createSignal(doc.description);
  const [saved, setSaved] = createSignal(savedDoc);
  const [depth, setDepth] = createSignal({ undo: past.length, redo: future.length }, { equals: (a, b) => a.undo === b.undo && a.redo === b.redo });

  /** Writes the document into the signals, touching only what differs. */
  function sync(next: Doc): void {
    for (const node of next.nodes) {
      if (plain.get(node.id) === node) continue;
      const cell = cells.get(node.id);
      if (!cell) addCell(node);
      else {
        plain.set(node.id, node);
        cell[1](node);
      }
    }
    if (next.nodes !== doc.nodes) {
      const live = new Set(next.nodes.map((node) => node.id));
      for (const id of plain.keys()) if (!live.has(id)) plain.delete(id);
    }
    doc = next;
    setOrder(next.nodes.map((node) => node.id));
    setEdges(() => next.edges);
    setNameSignal(next.name);
    setDescriptionSignal(next.description);
    setDocSignal(() => next);
    setDepth({ undo: past.length, redo: future.length });
  }

  function commit(next: Doc, options: CommitOptions = {}): void {
    if (next === doc) return;
    const now = Date.now();
    const last = past[past.length - 1];
    const folds = options.coalesce !== undefined && future.length === 0 && last?.key === options.coalesce && (options.sticky || now - last.at < COALESCE_MS);
    if (folds && last) last.at = now;
    else {
      past.push({ before: doc, key: options.coalesce, at: now });
      if (past.length > HISTORY_LIMIT) past = past.slice(-HISTORY_LIMIT);
    }
    future = [];
    sync(next);
  }

  const withNodes = (nodes: readonly GraphNode[]): Doc => ({ ...doc, nodes });
  const mapNode = (id: string, change: (node: GraphNode) => GraphNode): Doc => {
    const index = doc.nodes.findIndex((node) => node.id === id);
    const old = doc.nodes[index];
    if (!old) return doc;
    const next = change(old);
    return next === old ? doc : withNodes(doc.nodes.with(index, next));
  };

  return {
    /* ----- reads for the view ----- */

    /** The whole document. Changes with every edit; read it only where that is wanted. */
    doc: docSignal,
    /** Node ids in document order. Changes only when a node is added or removed. */
    order,
    edges,
    name,
    description,
    /** One node, tracked on its own. */
    node: (id: string): GraphNode | undefined => cells.get(id)?.[0](),
    /** What the daemon has, or null for a flow that was never saved. */
    saved,
    /** How many things differ from what is saved; 0 for a clean draft. A never-saved flow has nothing to compare with and reports 0. */
    changes: createMemo(() => {
      const base = saved();
      return base ? countChanges(docSignal(), base) : 0;
    }),
    canUndo: () => depth().undo > 0,
    canRedo: () => depth().redo > 0,
    /** Whether any edit was made in this session, undone or not. */
    touched: () => depth().undo + depth().redo > 0,

    /* ----- reads for handlers: always what was last written ----- */

    current: (): Doc => doc,
    find: (id: string): GraphNode | undefined => plain.get(id),
    snapshot: (): DraftSnapshot => ({ doc, past: past.map((step) => step.before), future: [...future] }),

    /* ----- edits ----- */

    setName(value: string): void {
      if (value !== doc.name) commit({ ...doc, name: value }, { coalesce: "name" });
    },

    setDescription(value: string): void {
      if (value !== doc.description) commit({ ...doc, description: value }, { coalesce: "description" });
    },

    moveNode(id: string, x: number, y: number, options?: CommitOptions): void {
      commit(
        mapNode(id, (node) => (node.x === x && node.y === y ? node : { ...node, x, y })),
        options,
      );
    },

    /** Moves several nodes in one step, e.g. after an automatic layout. */
    moveNodes(positions: Map<string, { x: number; y: number }>): void {
      let changed = false;
      const nodes = doc.nodes.map((node) => {
        const to = positions.get(node.id);
        if (!to || (to.x === node.x && to.y === node.y)) return node;
        changed = true;
        return { ...node, x: to.x, y: to.y };
      });
      if (changed) commit(withNodes(nodes));
    },

    setLabel(id: string, label: string): void {
      commit(
        mapNode(id, (node) => {
          if ((node.label ?? "") === label) return node;
          const { label: _dropped, ...rest } = node;
          return label === "" ? rest : { ...rest, label };
        }),
        { coalesce: `label:${id}` },
      );
    },

    /**
     * Sets one option and drops the stored keys the change invalidates, in
     * the same undo step, so they fall back to the defaults of what they now
     * describe.
     */
    setOption(id: string, key: string, value: OptionValue, drop: readonly string[] = []): void {
      commit(
        mapNode(id, (node) => {
          if (sameOptionValue(node.options?.[key], value) && drop.every((candidate) => node.options?.[candidate] === undefined)) return node;
          const options = { ...node.options, [key]: value };
          for (const dropped of drop) delete options[dropped];
          return { ...node, options };
        }),
        { coalesce: `option:${id}:${key}` },
      );
    },

    /** Drops a stored option, so the field is back at its default. For keys the node type no longer has. */
    removeOption(id: string, key: string): void {
      commit(
        mapNode(id, (node) => {
          if (!node.options || !(key in node.options)) return node;
          const { [key]: _dropped, ...options } = node.options;
          return { ...node, options };
        }),
      );
    },

    /** Adds a node of `type` and, when `from` names a port, the edge into it. Returns the new id. */
    addNode(type: string, x: number, y: number, from?: { id: string; port: number }, template?: Pick<GraphNode, "label" | "options">): string {
      const id = freshId(type, doc.nodes);
      const node: GraphNode = { id, type, x, y, ...(template?.label ? { label: template.label } : {}), ...(template?.options ? { options: structuredClone(template.options) } : {}) };
      const edgesNext = from ? [...doc.edges.filter((edge) => !(edge.from === from.id && edge.port === from.port)), { from: from.id, port: from.port, to: id }] : doc.edges;
      commit({ ...doc, nodes: [...doc.nodes, node], edges: edgesNext });
      return id;
    },

    removeNode(id: string): void {
      if (!plain.has(id)) return;
      commit({ ...doc, nodes: doc.nodes.filter((node) => node.id !== id), edges: doc.edges.filter((edge) => edge.from !== id && edge.to !== id) });
    },

    /** Draws the edge from an output port. A port has one edge, so this replaces the one it had. */
    connect(from: string, port: number, to: string): void {
      if (doc.edges.some((edge) => edge.from === from && edge.port === port && edge.to === to)) return;
      commit({ ...doc, edges: [...doc.edges.filter((edge) => !(edge.from === from && edge.port === port)), { from, port, to }] });
    },

    disconnect(from: string, port: number): void {
      const next = doc.edges.filter((edge) => !(edge.from === from && edge.port === port));
      if (next.length !== doc.edges.length) commit({ ...doc, edges: next });
    },

    /* ----- history ----- */

    undo(): void {
      const step = past.pop();
      if (!step) return;
      future.push(doc);
      sync(step.before);
    },

    redo(): void {
      const next = future.pop();
      if (!next) return;
      past.push({ before: doc, at: 0 });
      sync(next);
    },

    /** Takes back the step a coalesced run opened, without leaving it to redo. For a drag that was cancelled. */
    abort(key: string): void {
      const last = past[past.length - 1];
      if (last?.key !== key) return;
      past.pop();
      sync(last.before);
    },

    /** Replaces the document and forgets the history. For Discard. */
    reset(next: Doc): void {
      past = [];
      future = [];
      sync(next);
    },

    /** Records what the daemon now has. `doc` is what was sent, which may already be behind the draft. */
    markSaved(sent: Doc): void {
      setSaved(() => sent);
    },
  };
}
