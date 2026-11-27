import { createMemo, createSignal, Errored, isPending, Loading, Match, Show, Switch } from "solid-js";

import { api, createQuery, keys, POLL_LIST, type FileQuery, type FileSort, type FileStatus, type Library, type LibraryFile } from "../../../api";
import { requeueFiles } from "../../../features/jobs/actions";
import { SizeCell, TimeCell } from "../../../features/jobs/cells";
import { JobBulkBar } from "../../../features/jobs/JobBulkBar";
import { JobSheet } from "../../../features/jobs/JobSheet";
import { cancelable } from "../../../features/jobs/labels";
import { JobBadge } from "../../../features/jobs/parts";
import { Pager } from "../../../features/layout/Pager";
import { loadPage } from "../../../features/layout/paging";
import { createSelection } from "../../../features/layout/selection";
import { isScanning, scanLibrary } from "../../../features/libraries/library";
import { createDebounced } from "../../../lib/debounce";
import { formatBytes } from "../../../lib/format";
import { useAppData } from "../../../shell/data";
import { Button, DataTable, EmptyState, ErrorState, Input, LoadingRows, Segmented, SideSheet, type DataTableColumn } from "../../../ui";

// One page of rows is in the DOM at a time, so a library of 50,000 files costs the same as one of 100.
const PAGE_SIZE = 100;

type Filter = "all" | "pending" | "running" | "failed" | "done" | "skipped";

const FILTERS: { value: Filter; label: string }[] = [
  { value: "all", label: "All" },
  { value: "pending", label: "Pending" },
  { value: "running", label: "Running" },
  { value: "failed", label: "Failed" },
  { value: "done", label: "Done" },
  { value: "skipped", label: "Skipped" },
];

// "Pending" is the UI's word for the contract's "queued" status.
const STATUS_OF_FILTER: Record<Exclude<Filter, "all">, FileStatus> = { pending: "queued", running: "running", failed: "failed", done: "done", skipped: "skipped" };

const EMPTY: Record<Exclude<Filter, "all">, { title: string; description: string }> = {
  pending: { title: "Nothing is pending", description: "A scan queues the files the rules allow." },
  running: { title: "Nothing is running", description: "A pending file starts when a worker is free." },
  failed: { title: "No failed files", description: "A file fails after its job has used up its attempts." },
  done: { title: "No finished files yet", description: "A file is done once its encode is published." },
  skipped: { title: "No skipped files", description: "The flow skips files it has nothing to do for, and canceled jobs count as skipped." },
};

const fileName = (path: string) => path.slice(path.lastIndexOf("/") + 1);

/** A probe-derived cell: mono text, or a dash until a job has probed the file. */
const probed = (value: string) => value || <span class="av-cell-muted">—</span>;

/**
 * The Files tab: every file of the library with the state of its job, paged,
 * sorted and filtered by the daemon. The list needs only the library's name;
 * `library` is undefined until the list of libraries has answered.
 */
export function FilesTab(props: { name: string; library: Library | undefined }) {
  const { status } = useAppData();
  const [filter, setFilter] = createSignal<Filter>("all");
  const [sort, setSort] = createSignal<{ key: FileSort; dir: "asc" | "desc" }>({ key: "path", dir: "asc" });
  const [page, setPage] = createSignal(0);
  const [opened, setOpened] = createSignal<LibraryFile | null>(null);
  const search = createDebounced();
  // Constant for this component: the page is keyed by library name.
  const name = () => props.name;

  const query = createMemo(
    (): FileQuery => ({
      status: filter() === "all" ? undefined : STATUS_OF_FILTER[filter() as Exclude<Filter, "all">],
      q: search.settled() || undefined,
      sort: sort().key,
      dir: sort().dir,
      limit: PAGE_SIZE,
    }),
  );
  const list = createQuery(
    () => keys.libraryFiles(name(), { ...query(), offset: page() * PAGE_SIZE }),
    () => loadPage(page(), PAGE_SIZE, (offset) => api.libraries.files(name(), { ...query(), offset })),
    { deferred: true, poll: POLL_LIST },
  );
  const selection = createSelection(
    () => list().files,
    (file: LibraryFile) => file.source_id,
  );
  const now = () => Date.parse(status().server_time);
  // The status poll (2 s) is fresher than the file list (5 s).
  const progressOf = (file: LibraryFile) => status().running.find((live) => live.job_id === file.job_id) ?? file.progress;
  // The open file as the current page has it; its snapshot once it has left the page.
  const openFile = () => {
    const snapshot = opened();
    return snapshot ? (list().files.find((file) => file.source_id === snapshot.source_id) ?? snapshot) : undefined;
  };

  // A changed filter or order starts at the first page.
  const refine = (change: () => void) => {
    change();
    setPage(0);
  };
  // Column keys of the sortable columns are the daemon's sort keys (FileSort).
  const onSort = (key: string) => refine(() => setSort((current) => ({ key: key as FileSort, dir: current.key === key && current.dir === "asc" ? "desc" : "asc" })));

  const selected = () => selection.rows();
  const bulkJobs = () => selected().flatMap((file) => (file.job_id !== undefined ? [{ id: file.job_id, status: file.status, state: file.job_state }] : []));
  // Queueing again applies to everything that has no job in flight.
  const requeueable = () => selected().filter((file) => !cancelable(file.status));

  const columns: DataTableColumn<LibraryFile>[] = [
    { key: "path", label: "File", path: true },
    // The probe columns come from each job's checkpoint; the daemon cannot order by them.
    { key: "container", label: "Container", mono: true, width: 92, sortable: false, render: (file) => probed(file.container) },
    { key: "video_codec", label: "Video", mono: true, width: 76, sortable: false, render: (file) => probed(file.video_codec) },
    { key: "resolution", label: "Resolution", mono: true, width: 96, sortable: false, render: (file) => probed(file.resolution) },
    { key: "audio", label: "Audio", mono: true, width: 100, sortable: false, render: (file) => probed(file.audio) },
    { key: "size", label: "Size", mono: true, width: 160, render: (file) => <SizeCell input={file.size_bytes} output={file.output_size_bytes} /> },
    { key: "status", label: "Status", width: 148, render: (file) => <JobBadge status={file.status} state={file.job_state} progress={progressOf(file)} message={file.last_error} /> },
    { key: "updated_at", label: "Updated", width: 108, render: (file) => <TimeCell iso={file.updated_at} now={now()} /> },
  ];

  return (
    <div class={["av-workspace", { "av-sheet-open": opened() !== null }]}>
      <div class="av-tablearea av-table-wide">
        <Show
          when={selection.count() > 0}
          fallback={
            <div class="av-toolbar">
              <div class="av-toolbar-search">
                <Input size="sm" icon="search" placeholder="Filter by name or path" aria-label="Filter files by name or path" value={search.text()} onInput={(event) => refine(() => search.set(event.currentTarget.value))} />
              </div>
              <Segmented size="sm" label="Status" value={filter()} options={FILTERS} onChange={(value) => refine(() => setFilter(value as Filter))} />
            </div>
          }
        >
          <Loading>
            <JobBulkBar count={selection.count()} noun="file" jobs={bulkJobs()} requeue={{ library: name(), paths: requeueable().map((file) => file.path), done: requeueable().filter((file) => file.status === "done").length }} onClear={selection.clear} />
          </Loading>
        </Show>
        <Errored fallback={(error, reset) => <ErrorState title="The files could not be loaded" error={error()} onRetry={reset} />}>
          <Loading fallback={<LoadingRows label="Loading files" />}>
            <div class="av-tablearea" aria-busy={isPending(() => list()) ? "true" : undefined}>
              <DataTable
                label={`Files of ${name()}`}
                columns={columns}
                rows={list().files}
                rowId={(file) => file.source_id}
                selected={selection.pageIds()}
                onSelect={selection.select}
                sortKey={sort().key}
                sortDir={sort().dir}
                onSort={onSort}
                onRowClick={setOpened}
                activeId={opened()?.source_id}
                empty={
                  <Switch
                    fallback={
                      <Show
                        when={props.library?.last_scan_at}
                        fallback={
                          <EmptyState icon="file-video" title="No files yet" description="This library has never been scanned. A scan finds its media files and queues the ones the rules allow.">
                            <Button variant="primary" icon="refresh-cw" busy={isScanning(name())} onClick={() => void scanLibrary(name())}>
                              Scan library
                            </Button>
                          </EmptyState>
                        }
                      >
                        <EmptyState icon="file-video" title="No files found" description={`The last scan found no media file in ${props.library?.path ?? "the library folder"}.`} />
                      </Show>
                    }
                  >
                    <Match when={search.settled() !== ""}>
                      <EmptyState icon="search" title={`No file matches "${search.settled()}"`} description="The filter looks at the path below the library folder.">
                        <Button onClick={() => refine(() => search.set(""))}>Clear the filter</Button>
                      </EmptyState>
                    </Match>
                    <Match when={filter() !== "all" ? EMPTY[filter() as Exclude<Filter, "all">] : undefined}>{(empty) => <EmptyState icon="file-video" title={empty().title} description={empty().description} />}</Match>
                  </Switch>
                }
              />
              <Pager page={list().page} pageSize={PAGE_SIZE} total={list().total} noun="file" pending={isPending(() => list())} onPage={setPage} />
            </div>
          </Loading>
        </Errored>
      </div>
      <Loading>
        <Show when={openFile()}>{(file) => <FileSheet library={name()} file={file()} onClose={() => setOpened(null)} />}</Show>
      </Loading>
    </div>
  );
}

/** The side sheet of one file: its job when it has one, otherwise what is known without one. */
function FileSheet(props: { library: string; file: LibraryFile; onClose: () => void }) {
  const [queueing, setQueueing] = createSignal(false);
  const queue = async () => {
    setQueueing(true);
    await requeueFiles(props.library, [props.file.path]);
    setQueueing(false);
  };
  return (
    <Show
      when={props.file.job_id}
      fallback={
        <SideSheet
          title={fileName(props.file.path)}
          subtitle="No job"
          icon="file-video"
          onClose={props.onClose}
          footer={
            <>
              <span class="av-spacer" />
              <Button size="sm" icon="play" busy={queueing()} onClick={queue}>
                Queue file
              </Button>
            </>
          }
        >
          <div class="av-jobhead">
            <JobBadge status={props.file.status} />
          </div>
          <div class="av-pad av-stack">
            <dl class="av-dl av-dl-sheet">
              <dt>Path</dt>
              <dd class="av-mono">{props.file.absolute_path}</dd>
              <dt>Size</dt>
              <dd class="av-mono">{formatBytes(props.file.size_bytes)}</dd>
            </dl>
            <p class="av-note">A scan has seen this file, but it has no job. Queueing it starts one now.</p>
          </div>
        </SideSheet>
      }
    >
      {(jobId) => <JobSheet jobId={jobId()} title={fileName(props.file.path)} onClose={props.onClose} />}
    </Show>
  );
}
