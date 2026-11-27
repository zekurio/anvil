import { useNavigate } from "@solidjs/router";

import type { FlowSummary } from "../../api";
import { formatRelative } from "../../lib/format";
import { useTitle } from "../../lib/title";
import { useAppData } from "../../shell/data";
import { DataTable, EmptyState, LinkButton, Topbar, type DataTableColumn } from "../../ui";

/** The list of flows. A row opens the flow in the editor. */
export default function FlowsPage() {
  const { flows, status } = useAppData();
  const navigate = useNavigate();
  useTitle(() => "Flows");

  const columns: DataTableColumn<FlowSummary>[] = [
    { key: "name", label: "Name", sortable: false },
    { key: "description", label: "Description", sortable: false },
    { key: "node_count", label: "Nodes", align: "right", width: 80, sortable: false },
    { key: "libraries", label: "Libraries", width: 200, sortable: false, render: (flow) => (flow.libraries.length > 0 ? flow.libraries.join(", ") : <span class="av-muted">Not assigned</span>) },
    { key: "updated_at", label: "Changed", width: 120, sortable: false, render: (flow) => formatRelative(flow.updated_at, Date.parse(status().server_time)) },
  ];

  return (
    <>
      <Topbar title="Flows">
        <LinkButton variant="primary" icon="plus" href="/flows/new">
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
          empty={<EmptyState icon="workflow" title="No flows yet" description="A flow decides what happens to each file of a library." />}
        />
      </div>
    </>
  );
}
