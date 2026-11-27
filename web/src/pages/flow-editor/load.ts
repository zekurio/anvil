// Loads what the editor starts from: the node catalog, the flow, a check of
// its graph, and what a node of each type says at its defaults.

import { api, type Flow, type Graph, type NodeType } from "../../api";
import type { LiveCheckSeed } from "./check";
import type { Handoff } from "./editor";
import { freshId, ROW, type Doc } from "./graph";

export interface Loaded {
  /** null for a flow that was never saved. */
  flow: Flow | null;
  catalog: NodeType[];
  doc: Doc;
  /** What the daemon has; null for a new flow. */
  saved: Doc | null;
  seed: LiveCheckSeed;
  handoff?: Handoff;
}

export const docOf = (flow: Flow): Doc => ({ name: flow.name, description: flow.description, nodes: flow.graph.nodes, edges: flow.graph.edges });
export const graphOf = (doc: Doc): Graph => ({ nodes: [...doc.nodes], edges: [...doc.edges] });

/**
 * The first save of a new flow moves from /flows/new to /flows/:id, which
 * builds a new editor. What the old one held waits here for it, so the
 * canvas, the selection and the undo history carry over.
 */
let carried: { flow: Flow; saved: Doc; handoff: Handoff } | undefined;

export function carryOver(flow: Flow, saved: Doc, handoff: Handoff): void {
  carried = { flow, saved, handoff };
}

/** A new flow starts as the shortest one that can be saved: the input wired to the first way of ending a flow. */
function newDoc(catalog: NodeType[]): Doc {
  const input = catalog.find((type) => type.kind === "input");
  const output = catalog.find((type) => type.kind === "output");
  if (!input || !output) return { name: "New flow", description: "", nodes: [], edges: [] };
  const first = { id: freshId(input.id, []), type: input.id, x: 288, y: 32 };
  const last = { id: freshId(output.id, [first]), type: output.id, x: 288, y: 32 + ROW };
  return { name: "New flow", description: "", nodes: [first, last], edges: [{ from: first.id, port: 0, to: last.id }] };
}

/**
 * Asks the daemon what a node of every type shows with nothing set. One
 * check of a graph that holds each type once; it need not be a valid flow.
 * Without an answer new nodes simply start without a summary.
 */
async function defaultSummaries(catalog: NodeType[]): Promise<Map<string, string>> {
  try {
    const check = await api.flows.check({ nodes: catalog.map((type, index) => ({ id: `type-${index}`, type: type.id, x: 0, y: 0 })), edges: [] });
    return new Map(catalog.map((type, index) => [type.id, check.nodes[`type-${index}`]?.summary ?? ""]));
  } catch {
    return new Map();
  }
}

export async function loadEditor(id: number | null): Promise<Loaded> {
  const waiting = carried && carried.flow.id === id ? carried : undefined;
  carried = undefined;

  const [{ node_types: catalog }, flow] = await Promise.all([api.nodeTypes(), waiting ? waiting.flow : id === null ? null : api.flows.get(id)]);
  const doc = flow ? docOf(flow) : newDoc(catalog);
  const [defaults, check] = await Promise.all([defaultSummaries(catalog), flow ? flow.check : api.flows.check(graphOf(doc))]);
  return { flow, catalog, doc, saved: waiting ? waiting.saved : flow ? doc : null, seed: { check, defaults }, handoff: waiting?.handoff };
}
