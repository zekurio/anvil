// Node catalog and flow routes.

import type { Flow, FlowCheckRequest, FlowList, FlowSummary, FlowTestRequest, FlowWrite, Graph, Issue, NodeTypes } from "../../src/api/types.ts";
import { absolutePath, db, factsOf, pathsOverlap, TEMP_DIR } from "../db.ts";
import { NODE_TYPES } from "../fixtures/catalog.ts";
import type { StoredFlow } from "../fixtures/flows.ts";
import { checkGraph, moveOutputs, testGraph } from "../flowcheck.ts";
import { conflict, created, HttpError, invalid, noContent, notFound, objectBody, type Context, type Router } from "../http.ts";

function summary(flow: StoredFlow): FlowSummary {
  return {
    id: flow.id,
    name: flow.name,
    description: flow.description,
    revision: flow.revision,
    node_count: flow.graph.nodes.length,
    libraries: db.libraries.filter((l) => l.flow_id === flow.id).map((l) => l.name),
    created_at: flow.created_at,
    updated_at: flow.updated_at,
  };
}

const view = (flow: StoredFlow): Flow => ({ ...summary(flow), graph: flow.graph, check: checkGraph(flow.graph) });

function find(ctx: Context): StoredFlow {
  const flow = db.flows.find((f) => f.id === Number(ctx.params.id));
  if (!flow) throw notFound(`Flow ${ctx.params.id}`);
  return flow;
}

function graphOf(value: unknown): Graph {
  const graph = value as Partial<Graph> | undefined;
  if (!graph || !Array.isArray(graph.nodes) || !Array.isArray(graph.edges)) throw invalid("The graph needs a nodes list and an edges list.", "graph");
  return graph as Graph;
}

/** Validates a write and returns its trimmed name. */
function validate(write: FlowWrite, self?: StoredFlow): string {
  const name = typeof write.name === "string" ? write.name.trim() : "";
  if (name.length < 1 || name.length > 80) throw invalid("Must be between 1 and 80 characters.", "name");
  if (db.flows.some((f) => f !== self && f.name.toLowerCase() === name.toLowerCase())) throw conflict(`Another flow is already named "${name}". Choose another name.`, "name");
  const check = checkGraph(graphOf(write.graph));
  if (!check.valid) {
    const count = Object.values(check.nodes).filter((n) => n.invalid).length;
    throw new HttpError(422, "invalid_flow", count > 0 ? `${count} ${count === 1 ? "node needs" : "nodes need"} attention before the flow can be saved.` : "The flow cannot be saved yet.", undefined, check.issues);
  }
  checkMoveTargets(write.graph, self ? moveOutputs(self.graph).map((output) => output.destination) : []);
  return name;
}

/**
 * As pkg/webapi checkMoveTargets: a move output may not publish into temp_dir
 * or a library folder (config.MoveDestinationConflicts). The mock daemon sets
 * no media_roots. A folder the stored flow already moves to is not checked
 * again; `kept` lists those.
 */
function checkMoveTargets(graph: Graph, kept: string[]): void {
  const issues: Issue[] = [];
  for (const { node, destination } of moveOutputs(graph)) {
    if (kept.includes(destination)) continue;
    const add = (message: string) => issues.push({ severity: "error", node_id: node, field: "destination", message });
    if (pathsOverlap(destination, TEMP_DIR)) add(`${destination} overlaps ${TEMP_DIR}, where anvil keeps its working files (daemon.temp_dir). A move destination must be outside it.`);
    for (const library of [...db.libraries].sort((a, b) => a.name.localeCompare(b.name))) {
      if (pathsOverlap(destination, library.path)) add(`${destination} overlaps the library "${library.name}" at ${library.path}. A move destination must be outside every library folder.`);
    }
  }
  if (issues.length > 0) throw new HttpError(422, "invalid_flow", "Fix the problems in this flow before saving it.", undefined, issues);
}

function uniqueName(base: string): string {
  const taken = new Set(db.flows.map((f) => f.name.toLowerCase()));
  let name = `${base} copy`;
  for (let n = 2; taken.has(name.toLowerCase()); n++) name = `${base} copy ${n}`;
  return name;
}

export function flowRoutes(router: Router): void {
  router.on("GET", "/node-types", (): NodeTypes => ({ node_types: NODE_TYPES }));

  router.on("GET", "/flows", (): FlowList => ({ flows: db.flows.map(summary) }));

  router.on("POST", "/flows", (ctx) => {
    const write = objectBody<FlowWrite>(ctx);
    const name = validate(write);
    const at = new Date().toISOString();
    const flow: StoredFlow = { id: db.nextFlowId++, name, description: write.description ?? "", revision: 1, graph: write.graph, created_at: at, updated_at: at };
    db.flows.push(flow);
    return created(view(flow));
  });

  router.on("POST", "/flows/check", (ctx) => checkGraph(graphOf(objectBody<FlowCheckRequest>(ctx).graph)));

  router.on("POST", "/flows/test", (ctx) => {
    const request = objectBody<FlowTestRequest>(ctx);
    const graph = graphOf(request.graph);
    if (typeof request.path !== "string" || !request.path.startsWith("/")) throw invalid("Must be the absolute path of a media file.", "path");
    const file = db.files.find((f) => absolutePath(f) === request.path);
    if (!file) throw invalid("Must be a media file inside a configured library.", "path");
    const check = checkGraph(graph);
    if (!check.valid) throw new HttpError(422, "invalid_flow", "The flow has problems, so it cannot be tested yet.", undefined, check.issues);
    // The files whose jobs failed on corrupt data cannot be probed here either.
    if (file.last_error?.includes("Invalid data found")) throw invalid("ffprobe could not read the file: Invalid data found when processing input.", "path");
    return testGraph(graph, factsOf(file));
  });

  router.on("GET", "/flows/:id", (ctx) => view(find(ctx)));

  router.on("PUT", "/flows/:id", (ctx) => {
    const flow = find(ctx);
    const write = objectBody<FlowWrite>(ctx);
    if (write.revision !== flow.revision) throw conflict("This flow was changed elsewhere. Reload it and apply your changes again.");
    flow.name = validate(write, flow);
    flow.description = write.description ?? "";
    flow.graph = write.graph;
    flow.revision += 1;
    flow.updated_at = new Date().toISOString();
    return view(flow);
  });

  router.on("DELETE", "/flows/:id", (ctx) => {
    const flow = find(ctx);
    const users = db.libraries.filter((l) => l.flow_id === flow.id).map((l) => l.name);
    if (users.length > 0) throw conflict(`"${flow.name}" is still assigned to ${users.join(", ")}. Assign another flow first.`);
    db.flows.splice(db.flows.indexOf(flow), 1);
    return noContent();
  });

  // Mock only: bumps the revision as if someone else had saved the flow, so
  // the next save from an open editor answers 409. From a shell:
  //   curl -X POST localhost:5173/api/v1/flows/1/_touch
  router.on("POST", "/flows/:id/_touch", (ctx) => {
    const flow = find(ctx);
    flow.revision += 1;
    flow.updated_at = new Date().toISOString();
    return view(flow);
  });

  router.on("POST", "/flows/:id/duplicate", (ctx) => {
    const source = find(ctx);
    const at = new Date().toISOString();
    const flow: StoredFlow = { ...source, id: db.nextFlowId++, name: uniqueName(source.name), revision: 1, graph: structuredClone(source.graph), created_at: at, updated_at: at };
    db.flows.push(flow);
    return created(view(flow));
  });
}
