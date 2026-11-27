import { useNavigate, useParams } from "@solidjs/router";
import { createEffect, createMemo, createSignal, Errored, For, isPending, Loading, Show } from "solid-js";

import { api, createQuery, invalidate, keys, POLL_LIST, type Job, type JobQuery, type JobState } from "../../api";
import { jobAction } from "../../features/jobs/actions";
import { ErrorCell, SizeCell, TimeCell } from "../../features/jobs/cells";
import { JobBulkBar } from "../../features/jobs/JobBulkBar";
import { JobSheet } from "../../features/jobs/JobSheet";
import { STATES_OF_STATUS, stepLabel } from "../../features/jobs/labels";
import { JobBadge, StepProgress } from "../../features/jobs/parts";
import { Pager } from "../../features/layout/Pager";
import { createSelection } from "../../features/layout/selection";
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
const EMPTY: Record<Filter, { title: string; description: string }> = {
  all: { title: "No jobs yet", description: "Files are queued when a scan finds them." },
  queued: { title: "Nothing is queued", description: "Files are queued when a scan finds them." },
  failed: { title: "No failed jobs", description: "A job fails after it has used up its attempts." },
  done: { title: "No finished jobs yet", description: "A job is done once its encode is published." },
  skipped: { title: "No skipped or canceled jobs", description: "A flow skips files it has nothing to do for." },
};

const RUNNING_KEY = ["jobs", "running"];

const pathOf = (job: Job) => job.asset?.path ?? job.source.path;
const fileName = (path: string) => path.slice(path.lastIndexOf("/") + 1);

/** The queue: running jobs with live progress, then every job, filtered and paged. /queue/:jobId opens that job in the side sheet. */
export default function QueuePage() {
  const params = useParams();
  const navigate = useNavigate();
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
      description: "Every failed job in every library goes back into the queue and starts again from its first step.",
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
      description: "Every job that has not started yet is canceled, in every library. Running jobs keep running, and no file is changed. A canceled job can be retried later.",
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

function RunningJobs(props: { queued: number; activeId?: number; onOpen: (id: number) => void }) {
  const { status } = useAppData();
  // GET /status names running jobs by id only. Their files come from GET /jobs,
  // asked again as soon as the set of running ids changes.
  const ids = createMemo(() => status().running.map((progress) => progress.job_id).sort((a, b) => a - b).join(","));
  const jobs = createQuery(
    () => RUNNING_KEY,
    async () => (await api.jobs.list({ state: STATES_OF_STATUS.running, limit: 50 })).jobs,
    { poll: POLL_LIST },
  );
  // The first set of ids is what the query above already answers for.
  let known: string | undefined;
  createEffect(ids, (current) => {
    if (known !== undefined && known !== current) void invalidate(RUNNING_KEY);
    known = current;
  });
  const workers = () => status().workers;

  return (
    <section class="av-running" aria-label="Running jobs">
      <header class="av-running-head">
        <h2 class="av-running-title">Running</h2>
        <span class="av-running-meta">
          {formatCount(workers().active)} of {plural(workers().configured, "worker")} busy
        </span>
      </header>
      <Errored fallback={(error, reset) => <ErrorState title="The running jobs could not be loaded" error={error()} onRetry={reset} />}>
        <Loading fallback={<LoadingRows rows={2} label="Loading running jobs" />}>
          <ul class="av-running-list">
            <For
              each={jobs()}
              keyed={(job) => job.id}
              fallback={
                <li class="av-running-empty">
                  {props.queued > 0 ? `Nothing is running. ${plural(props.queued, "job")} ${props.queued === 1 ? "waits" : "wait"}: a job starts when a worker is free and its library is not paused.` : "Nothing is running. Files are queued when a scan finds them."}
                </li>
              }
            >
              {(job) => {
                // The status poll (2 s) is fresher than the job list (5 s).
                const progress = () => status().running.find((live) => live.job_id === job().id) ?? job().progress;
                return (
                  <li>
                    <div
                      class={["av-running-row", { "av-running-active": props.activeId === job().id }]}
                      role="button"
                      tabindex="0"
                      aria-label={`Open the job for ${fileName(pathOf(job()))}`}
                      onClick={() => props.onOpen(job().id)}
                      onKeyDown={(event) => {
                        if (event.key !== "Enter" && event.key !== " ") return;
                        event.preventDefault();
                        props.onOpen(job().id);
                      }}
                    >
                      <PathCell path={pathOf(job())} />
                      <span class="av-running-library">{job().library}</span>
                      <span class="av-running-step">{progress() ? stepLabel(progress()?.step ?? "") : "Starting"}</span>
                      <Show when={progress()} fallback={<span class="av-step-open">Waiting for the first progress report</span>}>
                        {(live) => <StepProgress progress={live()} label={fileName(pathOf(job()))} />}
                      </Show>
                    </div>
                  </li>
                );
              }}
            </For>
          </ul>
        </Loading>
      </Errored>
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
      state: filter() === "all" ? undefined : STATES_OF_STATUS[filter() as Exclude<Filter, "all">],
      library: library() || undefined,
      q: search.settled() || undefined,
      limit: PAGE_SIZE,
      offset: page() * PAGE_SIZE,
    }),
  );
  const list = createQuery(
    () => keys.jobList(query()),
    () => api.jobs.list(query()),
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
    { key: "path", label: "File", sortable: false, render: (job) => <PathCell path={pathOf(job)} /> },
    { key: "library", label: "Library", width: 104, sortable: false },
    { key: "status", label: "Status", width: 136, sortable: false, render: (job) => <JobBadge status={job.status} state={job.state} progress={job.progress} /> },
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
                <Show when={filtered()} fallback={<EmptyState icon="list-video" title={EMPTY[filter()].title} description={EMPTY[filter()].description} />}>
                  <EmptyState icon="search" title="No job matches these filters" description="Change the text or the library, or clear both.">
                    <Button onClick={clearFilters}>Clear filters</Button>
                  </EmptyState>
                </Show>
              }
            />
            <Pager page={page()} pageSize={PAGE_SIZE} total={list().total} noun="job" pending={isPending(() => list())} onPage={setPage} />
          </div>
        </Loading>
      </Errored>
    </div>
  );
}
