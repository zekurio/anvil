import { createMemo, createSignal, Errored, isPending, Loading, Show } from "solid-js";

import { api, createQuery, keys, POLL_LIST, type Job, type JobQuery, type JobStatusFilter, type Library } from "../../../api";
import { ErrorCell, SizeCell, TimeCell } from "../../../features/jobs/cells";
import { JobSheet } from "../../../features/jobs/JobSheet";
import { savedLabel } from "../../../features/jobs/labels";
import { JobBadge } from "../../../features/jobs/parts";
import { Pager } from "../../../features/layout/Pager";
import { useAppData } from "../../../shell/data";
import { DataTable, EmptyState, ErrorState, LoadingRows, PathCell, type DataTableColumn } from "../../../ui";

const PAGE_SIZE = 100;
const FINISHED: JobStatusFilter[] = ["done", "failed", "skipped"];

const pathOf = (job: Job) => job.asset?.path ?? job.source.path;
const fileName = (path: string) => path.slice(path.lastIndexOf("/") + 1);

/** The History tab: the library's finished jobs, newest first as the daemon lists them. */
export function HistoryTab(props: { library: Library }) {
  const { status } = useAppData();
  const [page, setPage] = createSignal(0);
  const [opened, setOpened] = createSignal<Job | null>(null);
  const query = createMemo((): JobQuery => ({ library: props.library.name, status: FINISHED, limit: PAGE_SIZE, offset: page() * PAGE_SIZE }));
  const list = createQuery(
    () => keys.jobList(query()),
    () => api.jobs.list(query()),
    { deferred: true, poll: POLL_LIST },
  );
  const now = () => Date.parse(status().server_time);

  const columns: DataTableColumn<Job>[] = [
    { key: "path", label: "File", sortable: false, render: (job) => <PathCell path={pathOf(job)} /> },
    { key: "status", label: "Result", width: 116, sortable: false, render: (job) => <JobBadge status={job.status} state={job.state} /> },
    { key: "size", label: "Size", width: 160, mono: true, sortable: false, render: (job) => <SizeCell input={job.input_size_bytes} output={job.output_size_bytes} /> },
    { key: "saved", label: "Change", width: 104, mono: true, sortable: false, render: (job) => savedLabel(job.input_size_bytes, job.output_size_bytes) || <span class="av-cell-muted">—</span> },
    { key: "attempt_count", label: "Attempts", width: 76, align: "right", sortable: false },
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
                label={`Finished jobs of ${props.library.name}`}
                columns={columns}
                rows={list().jobs}
                rowId={(job) => job.id}
                onRowClick={setOpened}
                activeId={opened()?.id}
                empty={<EmptyState icon="list-video" title="No finished jobs yet" description="Jobs of this library show up here once they are done, failed, skipped or canceled." />}
              />
              <Pager page={page()} pageSize={PAGE_SIZE} total={list().total} noun="job" pending={isPending(() => list())} onPage={setPage} />
            </div>
          </Loading>
        </Errored>
      </div>
      <Show when={opened()}>{(job) => <JobSheet jobId={job().id} title={fileName(pathOf(job()))} onClose={() => setOpened(null)} />}</Show>
    </div>
  );
}
