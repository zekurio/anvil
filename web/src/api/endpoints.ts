// One function per route of /api/v1, in the order of the route table in
// pkg/webapi/types.go.

import { request } from "./client";
import type {
  Arr,
  ArrList,
  ArrWrite,
  DaemonSettingsWrite,
  Directory,
  FileList,
  FileQuery,
  Flow,
  FlowCheck,
  FlowList,
  FlowTest,
  FlowWrite,
  Graph,
  JobActionRequest,
  JobActionResult,
  JobDetail,
  JobList,
  JobQuery,
  Library,
  LibraryList,
  LibraryWrite,
  NodeTypes,
  ScanResult,
  Session,
  Settings,
  Status,
} from "./types";

const seg = encodeURIComponent;

export const api = {
  session: {
    get: () => request<Session>("GET", "/session"),
    login: (password: string) => request<Session>("POST", "/session", { body: { password } }),
    logout: () => request<void>("DELETE", "/session"),
  },

  status: () => request<Status>("GET", "/status"),
  settings: () => request<Settings>("GET", "/settings"),
  /** Replaces the daemon settings saved in the web UI; a null or absent key goes back to its default. */
  updateDaemonSettings: (write: DaemonSettingsWrite) => request<Settings>("PUT", "/settings/daemon", { body: write }),
  /** Lists one directory; `files` also lists media files. An empty path asks for the default root. */
  fs: (path: string, files = false) => request<Directory>("GET", "/fs", { query: { path, files: files ? 1 : undefined } }),

  nodeTypes: () => request<NodeTypes>("GET", "/node-types"),

  flows: {
    list: () => request<FlowList>("GET", "/flows"),
    create: (flow: FlowWrite) => request<Flow>("POST", "/flows", { body: flow }),
    check: (graph: Graph, signal?: AbortSignal) => request<FlowCheck>("POST", "/flows/check", { body: { graph }, signal }),
    test: (graph: Graph, path: string) => request<FlowTest>("POST", "/flows/test", { body: { graph, path } }),
    get: (id: number) => request<Flow>("GET", `/flows/${id}`),
    update: (id: number, flow: FlowWrite) => request<Flow>("PUT", `/flows/${id}`, { body: flow }),
    remove: (id: number) => request<void>("DELETE", `/flows/${id}`),
    duplicate: (id: number) => request<Flow>("POST", `/flows/${id}/duplicate`),
  },

  libraries: {
    list: () => request<LibraryList>("GET", "/libraries"),
    create: (library: LibraryWrite) => request<Library>("POST", "/libraries", { body: library }),
    get: (name: string) => request<Library>("GET", `/libraries/${seg(name)}`),
    update: (name: string, library: LibraryWrite) => request<Library>("PUT", `/libraries/${seg(name)}`, { body: library }),
    remove: (name: string) => request<void>("DELETE", `/libraries/${seg(name)}`),
    scan: (name: string) => request<ScanResult>("POST", `/libraries/${seg(name)}/scan`),
    files: (name: string, query: FileQuery = {}) => request<FileList>("GET", `/libraries/${seg(name)}/files`, { query }),
    requeue: (name: string, paths: string[]) => request<JobActionResult>("POST", `/libraries/${seg(name)}/requeue`, { body: { paths } }),
  },

  jobs: {
    list: (query: JobQuery = {}) => request<JobList>("GET", "/jobs", { query }),
    get: (id: number) => request<JobDetail>("GET", `/jobs/${id}`),
    cancel: (selection: JobActionRequest) => request<JobActionResult>("POST", "/jobs/cancel", { body: selection }),
    retry: (selection: JobActionRequest) => request<JobActionResult>("POST", "/jobs/retry", { body: selection }),
  },

  arrs: {
    list: () => request<ArrList>("GET", "/arrs"),
    create: (arr: ArrWrite) => request<Arr>("POST", "/arrs", { body: arr }),
    update: (name: string, arr: ArrWrite) => request<Arr>("PUT", `/arrs/${seg(name)}`, { body: arr }),
    remove: (name: string) => request<void>("DELETE", `/arrs/${seg(name)}`),
  },
};
