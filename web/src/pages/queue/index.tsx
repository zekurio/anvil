import { useParams } from "@solidjs/router";
import { createMemo, createSignal, Errored, For, isPending, Loading, Show } from "solid-js";

import { api, createQuery, keys, POLL_LIST, type Job, type JobQuery, type JobState, type Progress } from "../../api";
import { jobAction } from "../../features/jobs/actions";
import { ErrorCell, SizeCell, TimeCell } from "../../features/jobs/cells";
import { JobBulkBar } from "../../features/jobs/JobBulkBar";
import { JobSheet } from "../../features/jobs/JobSheet";
import { STATES_OF_STATUS, stepLabel } from "../../features/jobs/labels";
import { JobBadge, StepProgress } from "../../features/jobs/parts";
import { Pager } from "../../features/layout/Pager";
import { loadPage } from "../../features/layout/paging";
import { createSelection } from "../../features/layout/selection";
import { useAppNavigate } from "../../lib/base";
import { createDebounced } from "../../lib/debounce";
import { formatCount, joinDot, plural } from "../../lib/format";
import { useTitle } from "../../lib/title";
import { useAppData } from "../../shell/data";
import { Button, confirm, DataTable, EmptyState, ErrorState, Input, LoadingRows, PathCell, Segmented, Select, Topbar, type DataTableColumn } from "../../ui";

import "./queue.css";

const PAGE_SIZE = 100;

type Filter = "all" | "queued" | "failed" | "done" | "skipped";

const FILTERS: { value: Filter; label: string }[] = [
  { value: "all", label: "All" },
  { value: "queued", label: "Queued" },
  { value: "failed", label: "Failed" },
  { value: "done", label: "Done" },
  { value: "skipped", label: "Skipped" },
];

// What each filter says when it matches nothing. Every line states what is true, then what leads to a row.
const EMPTY: Record<Filter, string> = {
  all: "No jobs",
  queued: "Nothing queued",
  failed: "No failed jobs",
  done: "No finished jobs",
  skipped: "No skipped or canceled jobs",
};

const fileName = (path: string) => path.slice(path.lastIndexOf("/") + 1);

/** The queue: running jobs with live progress, then every job, filtered and paged. /queue/:jobId opens that job in the side sheet. */
export default function QueuePage() {
  const params = useParams();
  const navigate = useAppNavigate();
  const { status } = useAppData();
  const jobId = () => (params.jobId ? Number(params.jobId) : undefined);
  useTitle(() => (params.jobId ? `Job ${params.jobId}` : "Queue"));

  const [busy, setBusy] = createSignal<"retry" | "cancel" | null>(null);
  const count = (states: JobState[]) => states.reduce((sum, state) => sum + (status().queue[state] ?? 0), 0);
  const running = () => count(STATES_OF_STATUS.running);
  const queued = () => count(STATES_OF_STATUS.queued);
  const failed = () => count(STATES_OF_STATUS.failed);

  const open = (id: number) => navigate(`/queue/${id}`);

  const retryFailed = async () => {
    const agreed = await confirm({
      title: `Retry ${plural(failed(), "failed job")}?`,
      description: "Failed jobs in every library start again from the first step.",
      confirmLabel: `Retry ${plural(failed(), "failed job")}`,
    });
    if (!agreed) return;
    setBusy("retry");
    await jobAction("retry", () => api.jobs.retry({ states: STATES_OF_STATUS.failed }));
    setBusy(null);
  };

  const cancelQueued = async () => {
    const agreed = await confirm({
      title: `Cancel ${plural(queued(), "queued job")}?`,
      description: "Queued jobs in every library are canceled. Running jobs continue, and no file is changed. Canceled jobs can be retried.",
      confirmLabel: `Cancel ${plural(queued(), "queued job")}`,
      cancelLabel: "Keep them",
      danger: true,
    });
    if (!agreed) return;
    setBusy("cancel");
    await jobAction("cancel", () => api.jobs.cancel({ states: STATES_OF_STATUS.queued }));
    setBusy(null);
  };

  return (
    <>
      <Topbar title="Queue" meta={joinDot(`${formatCount(running())} running`, `${formatCount(queued())} queued`, `${formatCount(failed())} failed`)}>
        <Show when={queued() > 0}>
          <Button icon="square" busy={busy() === "cancel"} disabled={busy() !== null} onClick={cancelQueued}>
            Cancel {plural(queued(), "queued job")}
          </Button>
        </Show>
        <Show when={failed() > 0}>
          <Button icon="rotate-ccw" busy={busy() === "retry"} disabled={busy() !== null} onClick={retryFailed}>
            Retry {plural(failed(), "failed job")}
          </Button>
        </Show>
      </Topbar>
      <div class={["av-workspace", { "av-sheet-open": jobId() !== undefined }]}>
        <div class="av-queue">
          <RunningJobs queued={queued()} activeId={jobId()} onOpen={open} />
          <JobsTable activeId={jobId()} onOpen={open} />
        </div>
        <Show when={jobId()}>{(id) => <JobSheet jobId={id()} onClose={() => navigate("/queue")} />}</Show>
      </div>
    </>
  );
}

/* ---------- running ---------- */

/** The running jobs, straight from the status poll: it names each job's library, file and flow next to its progress. */
function RunningJobs(props: { queued: number; activeId?: number; onOpen: (id: number) => void }) {
  const { status } = useAppData();
  const workers = () => status().workers;
  // A job no worker of this daemon run has reported on, last touched before the
  // run began: the daemon was stopped hard in the middle of it. Its lease has
  // to run out before a worker takes it again.
  const interrupted = (job: Progress) => job.step === "" && Date.parse(job.updated_at) < Date.parse(status().started_at);

  return (
    <section class="av-running" aria-label="Running jobs">
      <header class="av-running-head">
        <h2 class="av-running-title">Running</h2>
        <span class="av-running-meta">
          {formatCount(workers().active)} of {plural(workers().configured, "worker")} busy
        </span>
      </header>
      <ul class="av-running-list">
        <For
          each={status().running}
          keyed={(job) => job.job_id}
          fallback={
            <li class="av-running-empty">
              {props.queued > 0 ? `Nothing running. ${plural(props.queued, "job")} ${props.queued === 1 ? "waits" : "wait"} for a free worker or a resumed library.` : "Nothing running."}
            </li>
          }
        >
          {(job) => (
            <li>
              <div
                class={["av-running-row", { "av-running-active": props.activeId === job().job_id }]}
                role="button"
                tabindex="0"
                aria-label={`Open the job for ${fileName(job().path)}`}
                onClick={() => props.onOpen(job().job_id)}
                onKeyDown={(event) => {
                  if (event.key !== "Enter" && event.key !== " ") return;
                  event.preventDefault();
                  props.onOpen(job().job_id);
                }}
              >
                <PathCell path={job().path} />
                <span class="av-running-library" title={job().flow_name ? `Flow: ${job().flow_name}` : undefined}>
                  {job().library}
                </span>
                <span class="av-running-step">{job().step ? stepLabel(job().step) : interrupted(job()) ? "Interrupted" : "Starting"}</span>
                <Show
                  when={job().step !== ""}
                  fallback={<span class="av-step-open">{interrupted(job()) ? "Stopped with the daemon. Restarts when its lease expires." : "Waiting for progress"}</span>}
                >
                  <StepProgress progress={job()} label={fileName(job().path)} />
                </Show>
              </div>
            </li>
          )}
        </For>
      </ul>
    </section>
  );
}

/* ---------- every job ---------- */

function JobsTable(props: { activeId?: number; onOpen: (id: number) => void }) {
  const { status, libraries } = useAppData();
  const [filter, setFilter] = createSignal<Filter>("all");
  const [library, setLibrary] = createSignal("");
  const [page, setPage] = createSignal(0);
  const search = createDebounced();

  const query = createMemo(
    (): JobQuery => ({
      status: filter() === "all" ? undefined : (filter() as Exclude<Filter, "all">),
      library: library() || undefined,
      q: search.settled() || undefined,
      limit: PAGE_SIZE,
    }),
  );
  const list = createQuery(
    () => keys.jobList({ ...query(), offset: page() * PAGE_SIZE }),
    () => loadPage(page(), PAGE_SIZE, (offset) => api.jobs.list({ ...query(), offset })),
    { deferred: true, poll: POLL_LIST },
  );
  const selection = createSelection(
    () => list().jobs,
    (job: Job) => job.id,
  );
  const now = () => Date.parse(status().server_time);
  const filtered = () => library() !== "" || search.settled() !== "";

  // A changed filter starts at the first page.
  const refine = (change: () => void) => {
    change();
    setPage(0);
  };
  const clearFilters = () =>
    refine(() => {
      setLibrary("");
      search.set("");
    });

  const columns: DataTableColumn<Job>[] = [
    { key: "path", label: "File", sortable: false, render: (job) => <PathCell path={job.path} /> },
    { key: "library", label: "Library", width: 104, sortable: false },
    { key: "status", label: "Status", width: 148, sortable: false, render: (job) => <JobBadge status={job.status} state={job.state} progress={status().running.find((live) => live.job_id === job.id) ?? job.progress} message={job.last_error} /> },
    { key: "attempt_count", label: "Attempts", width: 76, align: "right", sortable: false },
    { key: "size", label: "Size", width: 160, mono: true, sortable: false, render: (job) => <SizeCell input={job.input_size_bytes} output={job.output_size_bytes} /> },
    { key: "updated_at", label: "Updated", width: 108, sortable: false, render: (job) => <TimeCell iso={job.updated_at} now={now()} /> },
    { key: "last_error", label: "Last error", width: 176, sortable: false, render: (job) => <ErrorCell text={job.last_error} failed={job.status === "failed"} /> },
  ];

  return (
    <div class="av-tablearea av-table-queue">
      <Show
        when={selection.count() > 0}
        fallback={
          <div class="av-toolbar">
            <div class="av-toolbar-search">
              <Input size="sm" icon="search" placeholder="Filter by name or path" aria-label="Filter jobs by name or path" value={search.text()} onInput={(event) => refine(() => search.set(event.currentTarget.value))} />
            </div>
            <div class="av-toolbar-select">
              <Select size="sm" aria-label="Library" value={library()} options={[{ value: "", label: "All libraries" }, ...libraries().map((entry) => entry.name)]} onChange={(event) => refine(() => setLibrary(event.currentTarget.value))} />
            </div>
            <Segmented size="sm" label="Status" value={filter()} options={FILTERS} onChange={(value) => refine(() => setFilter(value as Filter))} />
          </div>
        }
      >
        <Loading>
          <JobBulkBar count={selection.count()} noun="job" jobs={selection.rows()} onClear={selection.clear} />
        </Loading>
      </Show>
      <Errored fallback={(error, reset) => <ErrorState title="The jobs could not be loaded" error={error()} onRetry={reset} />}>
        <Loading fallback={<LoadingRows label="Loading jobs" />}>
          <div class="av-tablearea" aria-busy={isPending(() => list()) ? "true" : undefined}>
            <DataTable
              label="Jobs"
              columns={columns}
              rows={list().jobs}
              rowId={(job) => job.id}
              selected={selection.pageIds()}
              onSelect={selection.select}
              onRowClick={(job) => props.onOpen(job.id)}
              activeId={props.activeId}
              empty={
                <Show when={filtered()} fallback={<EmptyState icon="list-video" title={EMPTY[filter()]} />}>
                  <EmptyState icon="search" title="No job matches these filters">
                    <Button onClick={clearFilters}>Clear filters</Button>
                  </EmptyState>
                </Show>
              }
            />
            <Pager page={list().page} pageSize={PAGE_SIZE} total={list().total} noun="job" pending={isPending(() => list())} onPage={setPage} />
          </div>
        </Loading>
      </Errored>
    </div>
  );
}
