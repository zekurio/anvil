import { useNavigate } from "@solidjs/router";

import { api, invalidate, keys, type ApiError, type FlowSummary } from "../../api";
import { appHref } from "../../lib/base";
import { formatCount, formatDateTime, formatRelative } from "../../lib/format";
import { run } from "../../lib/mutate";
import { useTitle } from "../../lib/title";
import { useAppData } from "../../shell/data";
import { confirm, DataTable, EmptyState, LinkButton, Menu, toast, Topbar, type DataTableColumn } from "../../ui";

/** "movies", "movies and tv", "anime, movies and tv". */
function listNames(names: string[]): string {
  if (names.length < 2) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/** The list of flows. A row opens the flow in the editor. */
export default function FlowsPage() {
  const { flows, status } = useAppData();
  const navigate = useNavigate();
  useTitle(() => "Flows");

  /** Why a flow cannot be deleted. There is no way to force it: its libraries need another flow first. */
  const inUse = (flow: FlowSummary, libraries: string[]) =>
    toast.error(`"${flow.name}" is still in use`, {
      description: `${listNames(libraries)} ${libraries.length === 1 ? "uses" : "use"} it. Assign another flow first.`,
    });

  const duplicate = (flow: FlowSummary) =>
    void run(() => api.flows.duplicate(flow.id), {
      invalidate: [keys.flows],
      success: (copy) => `Duplicated "${flow.name}" as "${copy.name}"`,
      failure: "The flow could not be duplicated",
    });

  const remove = async (flow: FlowSummary) => {
    // Known from the list already: say so instead of asking a question whose answer cannot be acted on.
    if (flow.libraries.length > 0) return void inUse(flow, flow.libraries);
    const agreed = await confirm({
      title: `Delete "${flow.name}"?`,
      description: "No library uses it. No file is touched.",
      confirmLabel: "Delete flow",
      danger: true,
    });
    if (!agreed) return;
    await run(() => api.flows.remove(flow.id), {
      invalidate: [keys.flows],
      success: `Deleted "${flow.name}"`,
      failure: "The flow could not be deleted",
      onError: (error: ApiError) => {
        if (error.code !== "conflict") return false;
        // A library took the flow since the list was loaded. The fresh list names it.
        void invalidate(keys.flows).then(() => {
          const libraries = flows().find((candidate) => candidate.id === flow.id)?.libraries ?? [];
          if (libraries.length > 0) inUse(flow, libraries);
          else toast.error(`"${flow.name}" is still in use`, { description: error.message });
        });
        return true;
      },
    });
  };

  const columns: DataTableColumn<FlowSummary>[] = [
    { key: "name", label: "Name", width: 240, sortable: false },
    { key: "description", label: "Description", sortable: false, render: (flow) => flow.description || <span class="av-muted">No description</span> },
    { key: "node_count", label: "Nodes", align: "right", width: 72, sortable: false, render: (flow) => formatCount(flow.node_count) },
    { key: "libraries", label: "Used by", width: 220, sortable: false, render: (flow) => (flow.libraries.length > 0 ? <span title={flow.libraries.join(", ")}>{flow.libraries.join(", ")}</span> : <span class="av-muted">Not assigned</span>) },
    { key: "updated_at", label: "Updated", width: 112, sortable: false, render: (flow) => <span title={formatDateTime(flow.updated_at)}>{formatRelative(flow.updated_at, Date.parse(status().server_time))}</span> },
    {
      key: "actions",
      label: "",
      width: 56,
      sortable: false,
      render: (flow) => (
        // The row opens the flow on a press; a press on its menu must not.
        <span class="fl-row-menu" onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
          <Menu
            size="sm"
            label={`Actions for ${flow.name}`}
            items={[
              { id: "duplicate", label: "Duplicate", icon: "copy", onSelect: () => duplicate(flow) },
              { separator: true },
              { id: "delete", label: "Delete", icon: "trash-2", danger: true, onSelect: () => void remove(flow) },
            ]}
          />
        </span>
      ),
    },
  ];

  return (
    <>
      <Topbar title="Flows">
        <LinkButton variant="primary" icon="plus" href={appHref("/flows/new")}>
          New flow
        </LinkButton>
      </Topbar>
      <div class="av-page-body">
        <DataTable
          label="Flows"
          columns={columns}
          rows={flows()}
          rowId={(flow) => flow.id}
          onRowClick={(flow) => navigate(`/flows/${flow.id}`)}
          empty={
            <EmptyState icon="workflow" title="No flows">
              <LinkButton variant="primary" icon="plus" href={appHref("/flows/new")}>
                New flow
              </LinkButton>
            </EmptyState>
          }
        />
      </div>
    </>
  );
}
