import type { FileQuery, JobQuery } from "./types";

/**
 * Query keys. A key's prefix is what `invalidate` matches, so they nest by
 * resource: `invalidate(keys.libraries)` also refetches every library's
 * detail and file pages, `invalidate(keys.library("movies"))` only that one's.
 */
export const keys = {
  session: ["session"],
  status: ["status"],
  settings: ["settings"],
  nodeTypes: ["node-types"],
  fs: (path: string, files: boolean) => ["fs", path, files],

  flows: ["flows"],
  flowList: ["flows", "list"],
  flow: (id: number) => ["flows", "detail", id],

  libraries: ["libraries"],
  libraryList: ["libraries", "list"],
  library: (name: string) => ["libraries", "detail", name],
  libraryFiles: (name: string, query: FileQuery = {}) => ["libraries", "detail", name, "files", query],

  jobs: ["jobs"],
  jobList: (query: JobQuery = {}) => ["jobs", "list", query],
  job: (id: number) => ["jobs", "detail", id],

  arrs: ["arrs"],
} as const;
