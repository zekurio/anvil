// Pure helpers over a flow graph: the draft's document type, equality, ids,
// reachability, and free canvas positions. Nothing here is reactive.

import type { Field, GraphEdge, GraphNode, NodeType, OptionValue } from "../../api";
import { NODE_HEIGHT, NODE_WIDTH } from "../../ui";

/** Canvas grid step; node positions are multiples of it. */
export const GRID = 16;
/** Vertical distance between one row of nodes and the next, as pkg/flow/generate.go lays flows out. */
export const ROW = 112;
/** How far a condition's branches sit to the left and right of it. */
export const BRANCH = 224;

/**
 * Everything a save writes, as one immutable value. Edits replace the
 * objects they touch and share the rest, so two documents compare by
 * reference wherever nothing changed.
 */
export interface Doc {
  readonly name: string;
  readonly description: string;
  readonly nodes: readonly GraphNode[];
  readonly edges: readonly GraphEdge[];
}

export const snap = (value: number) => Math.round(value / GRID) * GRID;

/* ---------- equality ---------- */

function sameValue(a: OptionValue | undefined, b: OptionValue | undefined): boolean {
  if (a === b) return true;
  if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
  return a.every((entry, index) => entry === b[index]);
}

function sameOptions(a: GraphNode["options"], b: GraphNode["options"]): boolean {
  if (a === b) return true;
  const left = a ?? {};
  const right = b ?? {};
  const keys = Object.keys(left);
  return keys.length === Object.keys(right).length && keys.every((key) => key in right && sameValue(left[key], right[key]));
}

/** Whether two nodes mean the same to the daemon: everything but where they sit and what they are called. */
export function sameMeaning(a: GraphNode, b: GraphNode): boolean {
  return a === b || (a.id === b.id && a.type === b.type && sameOptions(a.options, b.options));
}

function sameNode(a: GraphNode, b: GraphNode): boolean {
  return a === b || (sameMeaning(a, b) && a.x === b.x && a.y === b.y && (a.label ?? "") === (b.label ?? ""));
}

const edgeKey = (edge: GraphEdge) => `${edge.from}\u0000${edge.port}`;

/**
 * How many things differ between a draft and what is saved: the name, the
 * description, every node added, removed or changed, and every edge added,
 * removed or moved between nodes both documents have. Edges that came or
 * went with a node are part of that node's change.
 */
export function countChanges(doc: Doc, saved: Doc): number {
  if (doc === saved) return 0;
  let count = Number(doc.name !== saved.name) + Number(doc.description !== saved.description);

  const before = new Map(saved.nodes.map((node) => [node.id, node]));
  const kept = new Set<string>();
  for (const node of doc.nodes) {
    const old = before.get(node.id);
    if (!old) count += 1;
    else {
      kept.add(node.id);
      if (!sameNode(node, old)) count += 1;
    }
  }
  count += before.size - kept.size;

  if (doc.edges !== saved.edges) {
    const between = (edge: GraphEdge) => kept.has(edge.from) && kept.has(edge.to);
    const old = new Map(saved.edges.filter(between).map((edge) => [edgeKey(edge), edge.to]));
    for (const edge of doc.edges.filter(between)) {
      if (old.get(edgeKey(edge)) !== edge.to) count += 1;
      old.delete(edgeKey(edge));
    }
    count += old.size;
  }
  return count;
}

/** Whether a check or a test of `a` also holds for `b`: same nodes, labels, options and edges, wherever they sit. */
export function sameGraph(a: Doc, b: Doc): boolean {
  if (a.nodes !== b.nodes) {
    if (a.nodes.length !== b.nodes.length) return false;
    // Labels count: the daemon's messages quote them.
    if (!a.nodes.every((node, index) => sameMeaning(node, b.nodes[index] as GraphNode) && (node.label ?? "") === (b.nodes[index]?.label ?? ""))) return false;
  }
  if (a.edges === b.edges) return true;
  return a.edges.length === b.edges.length && a.edges.every((edge, index) => edgeKey(edge) === edgeKey(b.edges[index] as GraphEdge) && edge.to === b.edges[index]?.to);
}

/* ---------- ids ---------- */

/** A readable id for a new node of `type`, unique in the graph: "video-encode", "video-encode-2". */
export function freshId(type: string, nodes: readonly GraphNode[]): string {
  const base = type.replace(/[^a-z0-9]+/gi, "-").replace(/^-|-$/g, "").toLowerCase() || "node";
  const taken = new Set(nodes.map((node) => node.id));
  if (!taken.has(base)) return base;
  for (let n = 2; ; n++) if (!taken.has(`${base}-${n}`)) return `${base}-${n}`;
}

/* ---------- structure ---------- */

/** The edge leaving each output port, by node id then port index. */
export function outgoing(edges: readonly GraphEdge[]): Map<string, Map<number, string>> {
  const out = new Map<string, Map<number, string>>();
  for (const edge of edges) {
    let ports = out.get(edge.from);
    if (!ports) out.set(edge.from, (ports = new Map()));
    if (!ports.has(edge.port)) ports.set(edge.port, edge.to);
  }
  return out;
}

/** Every node from which `id` can be reached, `id` included. An edge from `id` to any of them would close a loop. */
export function ancestors(id: string, edges: readonly GraphEdge[]): Set<string> {
  const into = new Map<string, string[]>();
  for (const edge of edges) into.set(edge.to, [...(into.get(edge.to) ?? []), edge.from]);
  const seen = new Set([id]);
  const queue = [id];
  for (let current = queue.pop(); current !== undefined; current = queue.pop()) {
    for (const from of into.get(current) ?? []) {
      if (seen.has(from)) continue;
      seen.add(from);
      queue.push(from);
    }
  }
  return seen;
}

/** The edges on any way from an input down to `id`, as "from:port" keys. */
export function pathTo(id: string, edges: readonly GraphEdge[]): Set<string> {
  const above = ancestors(id, edges);
  return new Set(edges.filter((edge) => above.has(edge.to) && above.has(edge.from)).map((edge) => `${edge.from}:${edge.port}`));
}

/* ---------- placement ---------- */

/** Space kept between two nodes, so ports and their labels stay readable. */
const CLEARANCE = 32;

function isFree(x: number, y: number, nodes: readonly GraphNode[]): boolean {
  return nodes.every((node) => Math.abs(node.x - x) >= NODE_WIDTH + CLEARANCE || Math.abs(node.y - y) >= NODE_HEIGHT + CLEARANCE);
}

/** The free grid position nearest to the wanted one, looking down the column first and then to its sides. */
export function freePosition(x: number, y: number, nodes: readonly GraphNode[]): { x: number; y: number } {
  const column = NODE_WIDTH + CLEARANCE;
  const wanted = { x: snap(x), y: snap(y) };
  for (let row = 0; row < 40; row++) {
    for (const shift of [0, 1, -1, 2, -2]) {
      const candidate = { x: wanted.x + shift * column, y: wanted.y + row * ROW };
      if (isFree(candidate.x, candidate.y, nodes)) return candidate;
    }
  }
  return wanted;
}

/* ---------- options ---------- */

export function fieldsOf(type: NodeType): Field[] {
  return type.sections.flatMap((section) => section.fields);
}

/**
 * The value each field of a node is in effect with, as the daemon decodes
 * it: the stored option, or the default when the key is missing or the
 * field is hidden by `visible_when`.
 */
export function effectiveOptions(type: NodeType, node: GraphNode): Map<string, OptionValue> {
  const values = new Map<string, OptionValue>();
  for (const field of fieldsOf(type)) {
    const stored = node.options?.[field.key];
    values.set(field.key, stored !== undefined && isVisible(field, values) ? stored : field.default);
  }
  return values;
}

/** Fields are decoded in catalog order, so the field a `visible_when` names is already in `values`. */
export function isVisible(field: Field, values: Map<string, OptionValue>): boolean {
  const when = field.visible_when;
  return !when || when.in.includes(values.get(when.key) ?? null);
}

const DURATION = /^[-+]?(\d+(\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)((\d+(\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h))*$/;

/** Whether Go's time.ParseDuration accepts the text: "20s", "1h10m", "1.5s", "0". */
export function isGoDuration(text: string): boolean {
  const trimmed = text.trim();
  return trimmed === "0" || trimmed === "+0" || trimmed === "-0" || DURATION.test(trimmed);
}
