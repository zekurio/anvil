import { createMemo, createSignal, Errored, isPending, Loading, Show } from "solid-js";

import { api, createQuery, keys, POLL_LIST, type Job, type JobQuery, type JobStatusFilter } from "../../../api";
import { ErrorCell, SizeCell, TimeCell } from "../../../features/jobs/cells";
import { JobSheet } from "../../../features/jobs/JobSheet";
import { savedLabel } from "../../../features/jobs/labels";
import { JobBadge } from "../../../features/jobs/parts";
import { Pager } from "../../../features/layout/Pager";
import { loadPage } from "../../../features/layout/paging";
import { formatDuration } from "../../../lib/format";
import { useAppData } from "../../../shell/data";
import { DataTable, EmptyState, ErrorState, LoadingRows, PathCell, type DataTableColumn } from "../../../ui";

const PAGE_SIZE = 100;
const FINISHED: JobStatusFilter[] = ["done", "failed", "skipped"];

const fileName = (path: string) => path.slice(path.lastIndexOf("/") + 1);

/** How long the job's latest attempt ran, or "" for a job that never started (canceled while queued, skipped by a scan). */
function took(job: Job): string {
  if (!job.started_at || !job.finished_at) return "";
  return formatDuration(Math.max(0, (Date.parse(job.finished_at) - Date.parse(job.started_at)) / 1000));
}

/** The History tab: the library's finished jobs, newest first as the daemon lists them. */
export function HistoryTab(props: { name: string }) {
  const { status } = useAppData();
  const [page, setPage] = createSignal(0);
  const [opened, setOpened] = createSignal<Job | null>(null);
  const query = createMemo((): JobQuery => ({ library: props.name, status: FINISHED, limit: PAGE_SIZE }));
  const list = createQuery(
    () => keys.jobList({ ...query(), offset: page() * PAGE_SIZE }),
    () => loadPage(page(), PAGE_SIZE, (offset) => api.jobs.list({ ...query(), offset })),
    { deferred: true, poll: POLL_LIST },
  );
  const now = () => Date.parse(status().server_time);

  const columns: DataTableColumn<Job>[] = [
    { key: "path", label: "File", sortable: false, render: (job) => <PathCell path={job.path} /> },
    { key: "status", label: "Result", width: 148, sortable: false, render: (job) => <JobBadge status={job.status} state={job.state} message={job.last_error} /> },
    { key: "size", label: "Size", width: 160, mono: true, sortable: false, render: (job) => <SizeCell input={job.input_size_bytes} output={job.output_size_bytes} /> },
    { key: "saved", label: "Change", width: 104, mono: true, sortable: false, render: (job) => savedLabel(job.input_size_bytes, job.output_size_bytes) || <span class="av-cell-muted">—</span> },
    { key: "attempt_count", label: "Attempts", width: 76, align: "right", sortable: false },
    { key: "duration", label: "Took", width: 88, mono: true, sortable: false, render: (job) => took(job) || <span class="av-cell-muted">—</span> },
    { key: "completed_at", label: "Finished", width: 108, sortable: false, render: (job) => <TimeCell iso={job.completed_at ?? job.updated_at} now={now()} /> },
    { key: "last_error", label: "Last error", width: 200, sortable: false, render: (job) => <ErrorCell text={job.last_error} failed={job.status === "failed"} /> },
  ];

  return (
    <div class={["av-workspace", { "av-sheet-open": opened() !== null }]}>
      <div class="av-tablearea av-table-queue">
        <Errored fallback={(error, reset) => <ErrorState title="The history could not be loaded" error={error()} onRetry={reset} />}>
          <Loading fallback={<LoadingRows label="Loading history" />}>
            <div class="av-tablearea" aria-busy={isPending(() => list()) ? "true" : undefined}>
              <DataTable
                label={`Finished jobs of ${props.name}`}
                columns={columns}
                rows={list().jobs}
                rowId={(job) => job.id}
                onRowClick={setOpened}
                activeId={opened()?.id}
                empty={<EmptyState icon="list-video" title="No finished jobs" />}
              />
              <Pager page={list().page} pageSize={PAGE_SIZE} total={list().total} noun="job" pending={isPending(() => list())} onPage={setPage} />
            </div>
          </Loading>
        </Errored>
      </div>
      <Show when={opened()}>{(job) => <JobSheet jobId={job().id} title={fileName(job().path)} onClose={() => setOpened(null)} />}</Show>
    </div>
  );
}
