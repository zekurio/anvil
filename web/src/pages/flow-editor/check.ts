// The live check: after every change the draft graph goes to POST
// /flows/check, debounced, and the answer supplies each node's summary,
// which nodes are invalid, and the issues list. The daemon validates; the
// editor shows.

import { createEffect, createMemo, createSignal, onSettled } from "solid-js";

import { api, type FlowCheck, type Issue, type NodeType } from "../../api";
import type { FlowDraft } from "./draft";
import { sameGraph, type Doc } from "./graph";

const DEBOUNCE_MS = 250;
/** How long a failed check waits before it asks again. */
const RETRY_MS = 3_000;

export type LiveCheck = ReturnType<typeof createLiveCheck>;

export interface LiveCheckSeed {
  /** The check of the graph the draft starts from. */
  check: FlowCheck;
  /** What a node of each type says with every option at its default, for nodes added before the next answer. */
  defaults: Map<string, string>;
}

export function createLiveCheck(draft: FlowDraft, seed: LiveCheckSeed) {
  const [check, setCheck] = createSignal(seed.check);
  /** Summaries to show for nodes the last answer does not know yet, e.g. a duplicate before it is checked. */
  const hints = new Map<string, string>();
  /** The graph the answer on screen describes. */
  let checked: Doc = draft.current();
  let pending: { doc: Doc; controller: AbortController } | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let disposed = false;

  const send = (doc: Doc) => {
    const controller = new AbortController();
    pending = { doc, controller };
    api.flows.check({ nodes: [...doc.nodes], edges: [...doc.edges] }, controller.signal).then(
      (result) => {
        // An answer to a graph that has changed since is dropped; its successor is already scheduled.
        if (pending?.controller !== controller) return;
        pending = undefined;
        checked = doc;
        setCheck(() => result);
      },
      () => {
        // A failed check keeps the last answer. The shell reports an unreachable daemon.
        // An aborted one was replaced or disposed of, and is not pending any more.
        if (pending?.controller !== controller) return;
        pending = undefined;
        // The answer on screen describes an older graph, and its errors may block Save and Test
        // although the draft has fixed them. Ask again until the daemon answers; an edit
        // meanwhile clears this timer and schedules its own check.
        if (disposed) return;
        clearTimeout(timer);
        timer = setTimeout(() => {
          const current = draft.current();
          if (!pending && !sameGraph(current, checked)) send(current);
        }, RETRY_MS);
      },
    );
  };

  createEffect(
    () => draft.doc(),
    (doc) => {
      clearTimeout(timer);
      if (pending && !sameGraph(doc, pending.doc)) {
        pending.controller.abort();
        pending = undefined;
      }
      // Moving a node, or renaming the flow, changes nothing the daemon checks.
      if (pending || sameGraph(doc, checked)) return;
      timer = setTimeout(() => send(doc), DEBOUNCE_MS);
    },
  );
  onSettled(() => () => {
    disposed = true;
    clearTimeout(timer);
    pending?.controller.abort();
  });

  const byNode = createMemo(() => {
    const map = new Map<string, Issue[]>();
    for (const issue of check().issues) {
      if (!issue.node_id) continue;
      const list = map.get(issue.node_id);
      if (list) list.push(issue);
      else map.set(issue.node_id, [issue]);
    }
    return map;
  });
  const errors = createMemo(() => check().issues.filter((issue) => issue.severity === "error"));

  return {
    issues: () => check().issues,
    errors,
    warnings: createMemo(() => check().issues.filter((issue) => issue.severity === "warning")),
    /** Nodes with at least one error. */
    invalidNodes: createMemo(() => new Set(errors().flatMap((issue) => (issue.node_id ? [issue.node_id] : [])))),
    issuesOf: (id: string): Issue[] => byNode().get(id) ?? [],
    invalid: (id: string): boolean => !!check().nodes[id]?.invalid || !!byNode().get(id)?.some((issue) => issue.severity === "error"),
    /** The line under a node's title: its last known summary, never blank while an answer is on the way. */
    summary: (id: string, type: NodeType | undefined): string => check().nodes[id]?.summary ?? hints.get(id) ?? (type ? (seed.defaults.get(type.id) ?? "") : ""),
    /** Lends a new node the summary of the node it was copied from. */
    hint(id: string, summary: string): void {
      hints.set(id, summary);
    },
    /** Shows issues the daemon reported another way, e.g. with a refused save. */
    report(issues: Issue[]): void {
      setCheck((last) => ({ valid: !issues.some((issue) => issue.severity === "error"), issues, nodes: last.nodes }));
    },
  };
}
