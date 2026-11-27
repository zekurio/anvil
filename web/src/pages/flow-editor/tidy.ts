// "Tidy up": lays a flow out top to bottom with the conventions of
// pkg/flow/generate.go. Rows are 112 px apart, a condition's first output
// goes left and its last goes right at least 448 px apart, and a node that
// several branches lead to sits below all of them, in the column of the node
// where those branches split.

import type { GraphEdge, GraphNode } from "../../api";
import { NODE_WIDTH } from "../../ui";
import { BRANCH, GRID, ROW, snap } from "./graph";

const HALF = NODE_WIDTH / 2;
/** Least space between two nodes of one row. */
const GAP = 32;
/** Space between two trees that are not connected. */
const TREE_GAP = 64;

/** Left and right edge per row, relative to the x centre of the subtree's root. */
type Contour = Map<number, [number, number]>;

const ceilTo = (value: number, step: number) => Math.ceil(value / step) * step;

function merge(into: Contour, from: Contour, offset: number): void {
  for (const [row, [left, right]] of from) {
    const have = into.get(row);
    into.set(row, have ? [Math.min(have[0], left + offset), Math.max(have[1], right + offset)] : [left + offset, right + offset]);
  }
}

/** How far apart two subtree roots must be for `right` to clear `left` on every row they share. */
function clearance(left: Contour, right: Contour): number {
  let need = -Infinity;
  for (const [row, [, edge]] of left) {
    const other = right.get(row);
    if (other) need = Math.max(need, edge - other[0] + GAP);
  }
  return need;
}

/**
 * Returns a position for every node. `outputs` gives a node's number of
 * output ports, and `isInput` marks the node the flow starts at.
 */
export function tidy(nodes: readonly GraphNode[], edges: readonly GraphEdge[], outputs: (node: GraphNode) => number, isInput: (node: GraphNode) => boolean): Map<string, { x: number; y: number }> {
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const out = new Map<string, { port: number; to: string }[]>();
  const hasIncoming = new Set<string>();
  for (const edge of [...edges].sort((a, b) => a.port - b.port)) {
    if (!byId.has(edge.from) || !byId.has(edge.to) || edge.from === edge.to) continue;
    const list = out.get(edge.from) ?? [];
    if (list.some((other) => other.port === edge.port)) continue;
    list.push({ port: edge.port, to: edge.to });
    out.set(edge.from, list);
    hasIncoming.add(edge.to);
  }

  // The input first, then whatever nothing leads to, then what is left (nodes that only sit on loops).
  const roots = [...nodes.filter(isInput), ...nodes.filter((node) => !isInput(node) && !hasIncoming.has(node.id)), ...nodes];
  const placed = new Map<string, { x: number; y: number }>();
  const anchor = roots[0];
  if (!anchor) return placed;
  const top = snap(anchor.y);
  let nextLeft = 0;

  for (const root of roots) {
    if (placed.has(root.id)) continue;

    // The nodes this root reaches that no earlier tree took, in an order where every node follows the ones leading to it.
    const preds = new Map<string, { from: string; port: number }[]>();
    const order: string[] = [];
    const state = new Map<string, "open" | "done">();
    const visit = (id: string) => {
      state.set(id, "open");
      for (const edge of out.get(id) ?? []) {
        // An edge back into the walk closes a loop; the layout leaves it out.
        if (placed.has(edge.to) || state.get(edge.to) === "open") continue;
        preds.set(edge.to, [...(preds.get(edge.to) ?? []), { from: id, port: edge.port }]);
        if (!state.has(edge.to)) visit(edge.to);
      }
      state.set(id, "done");
      order.push(id);
    };
    visit(root.id);
    order.reverse();
    const index = new Map(order.map((id, i) => [id, i]));

    // Row: one below the lowest node leading here. Split: the nearest node every way here passes through.
    const row = new Map<string, number>([[root.id, 0]]);
    const split = new Map<string, string>([[root.id, root.id]]);
    const common = (a: string, b: string) => {
      while (a !== b) {
        if ((index.get(a) ?? 0) > (index.get(b) ?? 0)) a = split.get(a) ?? b;
        else b = split.get(b) ?? a;
      }
      return a;
    };
    const branches = new Map<string, { id: string; port: number }[]>();
    const joins = new Map<string, string[]>();
    for (const id of order.slice(1)) {
      const into = preds.get(id) ?? [];
      row.set(id, Math.max(...into.map((edge) => row.get(edge.from) ?? 0)) + 1);
      const parent = into.map((edge) => edge.from).reduce(common);
      split.set(id, parent);
      const only = into.length === 1 ? into[0] : undefined;
      if (only) branches.set(only.from, [...(branches.get(only.from) ?? []), { id, port: only.port }]);
      else joins.set(parent, [...(joins.get(parent) ?? []), id]);
    }

    // x of every node relative to the node it hangs under.
    const offset = new Map<string, number>([[root.id, 0]]);
    const shape = (id: string): Contour => {
      const contour: Contour = new Map([[row.get(id) ?? 0, [-HALF, HALF]]]);
      const node = byId.get(id) as GraphNode;
      const ports = Math.max(outputs(node), 1);
      const kids = (branches.get(id) ?? []).sort((a, b) => a.port - b.port).map((kid) => ({ ...kid, contour: shape(kid.id), x: 0 }));
      const shared = (joins.get(id) ?? []).map((kid) => ({ id: kid, contour: shape(kid) }));

      // Ports spread 2 * BRANCH apart around the node, wider where the subtrees need it.
      kids.forEach((kid, i) => {
        const before = kids[i - 1];
        if (!before) kid.x = (kid.port - (ports - 1) / 2) * 2 * BRANCH;
        else kid.x = before.x + Math.max((kid.port - before.port) * 2 * BRANCH, ceilTo(clearance(before.contour, kid.contour), 2 * GRID));
      });
      const first = kids[0];
      const last = kids[kids.length - 1];
      if (first && last && first !== last) {
        const shift = snap((first.x + last.x) / 2);
        for (const kid of kids) kid.x -= shift;
      }
      // A shared node sits in this node's column; a branch that reaches down to its row moves aside.
      for (const kid of kids) {
        for (const below of shared) {
          if (kid.x < 0) kid.x = Math.min(kid.x, -ceilTo(clearance(kid.contour, below.contour), GRID));
          else if (kid.x > 0) kid.x = Math.max(kid.x, ceilTo(clearance(below.contour, kid.contour), GRID));
        }
      }

      for (const kid of kids) {
        offset.set(kid.id, snap(kid.x));
        merge(contour, kid.contour, snap(kid.x));
      }
      for (const below of shared) {
        offset.set(below.id, 0);
        merge(contour, below.contour, 0);
      }
      return contour;
    };
    const contour = shape(root.id);

    // The first tree keeps its root where it is; the others line up to its right.
    const extent = [...contour.values()].reduce(([left, right], [l, r]) => [Math.min(left, l), Math.max(right, r)], [0, 0]);
    const centre = root === anchor ? snap(anchor.x) + HALF : nextLeft - snap(extent[0]);
    nextLeft = ceilTo(centre + extent[1] + TREE_GAP, GRID);

    const centres = new Map<string, number>([[root.id, centre]]);
    for (const id of order) {
      const x = id === root.id ? centre : (centres.get(split.get(id) ?? root.id) ?? centre) + (offset.get(id) ?? 0);
      centres.set(id, x);
      placed.set(id, { x: x - HALF, y: top + (row.get(id) ?? 0) * ROW });
    }
  }
  return placed;
}
