import { useParams } from "@solidjs/router";
import { Show } from "solid-js";

import { api, createQuery, keys } from "../../api";
import { useTitle } from "../../lib/title";
import { EmptyState, Topbar } from "../../ui";

/**
 * The flow editor: /flows/new and /flows/:id. Full-bleed, without the app
 * Sidebar; the Topbar links back to the list. Placeholder.
 */
export default function FlowEditorPage() {
  const params = useParams();
  return (
    <Show when={params.id} fallback={<Editor title="New flow" />}>
      {(id) => <StoredFlow id={Number(id())} />}
    </Show>
  );
}

function StoredFlow(props: { id: number }) {
  const flow = createQuery(
    () => keys.flow(props.id),
    () => api.flows.get(props.id),
  );
  return <Editor title={flow().name} />;
}

function Editor(props: { title: string }) {
  useTitle(() => props.title);
  return (
    <>
      <Topbar title={props.title} back={{ href: "/flows", label: "Back to flows" }} />
      <EmptyState icon="workflow" title="This page is not built yet" description="The flow editor goes here: palette, canvas and node options." />
    </>
  );
}
