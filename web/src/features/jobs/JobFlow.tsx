import { createMemo, createSignal, For, Loading, onSettled, Show } from "solid-js";

import type { JobFlow, NodeType } from "../../api";
import { appHref } from "../../lib/base";
import { FlowCanvas, NODE_HEIGHT, NODE_WIDTH, type FlowCanvasNode, type NodeKind } from "../../ui";

/** The node types of the daemon by id. */
export type NodeCatalog = Map<string, NodeType>;

const PAD = 12;
// Room under the lowest node for its port labels.
const PORT_LABELS = 20;

/**
 * How the flow resolved for one job: the outcome, each decision on the way,
 * the plan, and a map of the graph with the file's path drawn in the active
 * edge colour.
 */
export function JobFlowSection(props: { flow: JobFlow; catalog: NodeCatalog }) {
  return (
    <>
      <dl class="av-dl av-dl-sheet">
        <dt>Flow</dt>
        <dd>
          <a class="av-link" href={appHref(`/flows/${props.flow.flow_id}`)}>
            {props.flow.flow_name}
          </a>{" "}
          <span class="av-muted">revision {props.flow.revision}</span>
        </dd>
        <dt>Outcome</dt>
        <dd>{props.flow.outcome === "skip" ? "Skipped by the flow" : "Processed"}</dd>
      </dl>
      <Lines title="Decisions" lines={props.flow.decisions} />
      <Lines title="Plan" lines={props.flow.plan} />
      {/* The node catalog names the nodes; the map waits for it without holding the rest of the sheet. */}
      <Loading fallback={<div class="av-flowmap av-flowmap-loading" />}>
        <Show when={props.catalog}>{(catalog) => <FlowMap flow={props.flow} catalog={catalog()} />}</Show>
      </Loading>
    </>
  );
}

function Lines(props: { title: string; lines: string[] }) {
  return (
    <Show when={props.lines.length > 0}>
      <div class="av-sub">
        <h4 class="av-sub-title">{props.title}</h4>
        <ul class="av-lines">
          <For each={props.lines} keyed={false}>{(line) => <li>{line()}</li>}</For>
        </ul>
      </div>
    </Show>
  );
}

function FlowMap(props: { flow: JobFlow; catalog: NodeCatalog }) {
  const [width, setWidth] = createSignal(0);
  let host: HTMLDivElement | undefined;

  onSettled(() => {
    if (!host) return;
    setWidth(host.clientWidth);
    const observer = new ResizeObserver(() => setWidth(host?.clientWidth ?? 0));
    observer.observe(host);
    return () => observer.disconnect();
  });

  const nodes = createMemo((): FlowCanvasNode[] =>
    props.flow.graph.nodes.map((node) => {
      const type = props.catalog.get(node.type);
      // A type the catalog no longer has still draws, as a plain command node under its raw name.
      const kind: NodeKind = type?.kind ?? "command";
      return { id: node.id, x: node.x, y: node.y, kind, title: node.label || type?.label || node.type, outputs: type?.outputs };
    }),
  );

  // FlowCanvas has no fit-to-width of its own: scale and offset it from the node bounds.
  const view = createMemo(() => {
    const list = props.flow.graph.nodes;
    if (list.length === 0 || width() === 0) return { zoom: 1, offsetX: 0, offsetY: 0, height: 0 };
    const left = Math.min(...list.map((node) => node.x));
    const top = Math.min(...list.map((node) => node.y));
    const right = Math.max(...list.map((node) => node.x + NODE_WIDTH));
    const bottom = Math.max(...list.map((node) => node.y + NODE_HEIGHT)) + PORT_LABELS;
    const room = width() - 2 * PAD;
    const zoom = Math.min(1, room / (right - left));
    return { zoom, offsetX: PAD + (room - (right - left) * zoom) / 2 - left * zoom, offsetY: PAD - top * zoom, height: Math.ceil((bottom - top) * zoom + 2 * PAD) };
  });

  return (
    <div class="av-flowmap" ref={(el) => (host = el)} inert role="img" aria-label={`Map of the flow ${props.flow.flow_name} with the path this file took`}>
      <Show when={view().height > 0}>
        <FlowCanvas
          nodes={nodes()}
          edges={props.flow.graph.edges}
          activePath={props.flow.path}
          // The outline marks where the file ended up.
          selectedId={props.flow.path[props.flow.path.length - 1] ?? null}
          zoom={view().zoom}
          offsetX={view().offsetX}
          offsetY={view().offsetY}
          height={view().height}
          tools={false}
        />
      </Show>
    </div>
  );
}
