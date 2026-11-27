// Library routes: CRUD, scan, the file list and requeue.

import type { FileList, FileStatus, JobActionResult, LibraryList, LibraryWrite, RequeueRequest, Rule, ScanResult } from "../../src/api/types.ts";
import { advance, db, fileView, findLibrary, libraryView, type StoredLibrary } from "../db.ts";
import { discoverFiles, type MockFile } from "../fixtures/files.ts";
import { conflict, created, intParam, invalid, noContent, notFound, objectBody, type Context, type Router } from "../http.ts";

const STATUSES: FileStatus[] = ["none", "queued", "running", "done", "failed", "skipped"];
const DURATION = /^(\d+(\.\d+)?(ns|us|ms|s|m|h))+$/;

const RULE_OPS: Record<string, string[]> = { path: ["matches", "regex"], extension: ["is", "is_not"], size: ["gt", "lt"] };

function find(ctx: Context): StoredLibrary {
  const library = findLibrary(ctx.params.name ?? "");
  if (!library) throw notFound(`Library "${ctx.params.name}"`);
  return library;
}

function oneOf<T extends string>(value: unknown, allowed: readonly T[], field: string): T {
  if (!allowed.includes(value as T)) throw invalid(`Must be one of: ${allowed.join(", ")}.`, field);
  return value as T;
}

function duration(value: unknown, field: string): string {
  if (value === undefined || value === "") return "";
  if (typeof value !== "string" || !DURATION.test(value)) throw invalid('Must be a duration such as "30m" or "45s".', field);
  return value;
}

function absolute(value: unknown, field: string): string {
  if (typeof value !== "string" || !value.startsWith("/")) throw invalid("Must be an absolute path.", field);
  return value.length > 1 ? value.replace(/\/+$/, "") : value;
}

function count(value: unknown, field: string): number {
  if (value === undefined) return 0;
  if (typeof value !== "number" || !Number.isInteger(value) || value < 0) throw invalid("Must be a whole number, 0 or more.", field);
  return value;
}

function rules(value: unknown): Rule[] {
  if (value === undefined) return [];
  if (!Array.isArray(value)) throw invalid("Must be a list of rules.", "rules");
  return value.map((raw: Partial<Rule>, i) => {
    const field = oneOf(raw.field, ["path", "extension", "size"] as const, `rules.${i}.field`);
    const rule: Rule = { action: oneOf(raw.action, ["allow", "deny"] as const, `rules.${i}.action`), field, op: oneOf(raw.op, (RULE_OPS[field] ?? []) as Rule["op"][], `rules.${i}.op`), value: typeof raw.value === "string" ? raw.value.trim() : "" };
    if (rule.value === "") throw invalid("A rule needs a value.", `rules.${i}.value`);
    if (field === "size" && !/^\d+(\.\d+)?\s*(B|KB|MB|GB|TB)$/i.test(rule.value)) throw invalid('Must be a size with a unit, such as "200 MB".', `rules.${i}.value`);
    if (field === "extension" && !/^[a-z0-9]+(\s*,\s*[a-z0-9]+)*$/i.test(rule.value)) throw invalid('Must be a comma-separated list of extensions without dots, such as "mkv, mp4".', `rules.${i}.value`);
    if (rule.op === "regex") {
      try {
        new RegExp(rule.value);
      } catch {
        throw invalid("Must be a valid regular expression.", `rules.${i}.value`);
      }
    }
    return rule;
  });
}

/** Validates a write and fills what it leaves out. `existing` is set on PUT. */
function parse(write: Partial<LibraryWrite>, existing?: StoredLibrary): LibraryWrite {
  const name = existing?.name ?? write.name;
  if (typeof name !== "string" || !/^[a-z0-9_-]{1,64}$/.test(name)) throw invalid('Must be 1 to 64 characters of a-z, 0-9, "-" and "_".', "name");
  if (existing && write.name !== undefined && write.name !== existing.name) throw invalid("A library cannot be renamed.", "name");
  const kind = oneOf(write.kind ?? existing?.kind, ["media", "download"] as const, "kind");
  if (existing && kind !== existing.kind) throw invalid("The kind cannot change after creation.", "kind");
  const path = absolute(write.path, "path");
  const clash = db.libraries.find((l) => l !== existing && (l.path === path || path.startsWith(`${l.path}/`) || l.path.startsWith(`${path}/`)));
  if (clash) throw invalid(`${path} overlaps the library "${clash.name}" at ${clash.path}. Library folders must not contain each other.`, "path");
  if (!db.flows.some((f) => f.id === write.flow_id)) throw invalid("Choose a flow that exists.", "flow_id");
  const arr = write.arr ?? "";
  if (arr !== "" && !db.arrs.some((a) => a.name === arr)) throw invalid(`No Arr connection is named "${arr}".`, "arr");

  const download = write.download;
  return {
    name,
    kind,
    path,
    flow_id: write.flow_id as number,
    paused: write.paused === true,
    priority: typeof write.priority === "number" ? write.priority : 0,
    concurrency_limit: count(write.concurrency_limit, "concurrency_limit"),
    scan_interval: duration(write.scan_interval, "scan_interval"),
    arr,
    rules: rules(write.rules),
    rule_fallback: oneOf(write.rule_fallback ?? "allow", ["allow", "deny"] as const, "rule_fallback"),
    media: { replacement_mode: oneOf(write.media?.replacement_mode ?? "replace", ["replace", "copy"] as const, "media.replacement_mode") },
    download: {
      handoff_path: kind === "download" ? absolute(download?.handoff_path, "download.handoff_path") : "",
      stable_for: duration(download?.stable_for, "download.stable_for"),
      package_mode: oneOf(download?.package_mode ?? "auto", ["auto", "directory", "file"] as const, "download.package_mode"),
      handoff_mode: oneOf(download?.handoff_mode ?? "move", ["copy", "move"] as const, "download.handoff_mode"),
      preserve_relative_path: download?.preserve_relative_path !== false,
      cleanup_source_media: download?.cleanup_source_media === true,
      prune_empty_dirs: download?.prune_empty_dirs !== false,
      ignorable_globs: Array.isArray(download?.ignorable_globs) ? download.ignorable_globs.map(String) : [],
    },
  };
}

// How much attention a status needs, as pkg/store orders them.
const STATUS_RANK: FileStatus[] = ["running", "queued", "failed", "done", "skipped", "none"];

// The keys of webapi.FileList's sort parameter (FileSort).
const SORTERS: Record<string, (a: MockFile, b: MockFile) => number> = {
  path: (a, b) => a.path.localeCompare(b.path),
  size: (a, b) => a.size_bytes - b.size_bytes,
  status: (a, b) => STATUS_RANK.indexOf(a.status) - STATUS_RANK.indexOf(b.status),
  updated_at: (a, b) => a.updated_at - b.updated_at,
};

function queue(file: MockFile): void {
  file.status = "queued";
  file.job_state = "pending";
  file.job_id = db.nextJobId++;
  file.attempt_count = 0;
  file.last_error = undefined;
  file.output_size_bytes = undefined;
  file.completed_at = undefined;
  file.started_at = undefined;
  file.updated_at = Date.now();
}

export function libraryRoutes(router: Router): void {
  router.on("GET", "/libraries", (): LibraryList => ({ libraries: [...db.libraries].sort((a, b) => a.name.localeCompare(b.name)).map(libraryView) }));

  router.on("POST", "/libraries", (ctx) => {
    const write = parse(objectBody<Partial<LibraryWrite>>(ctx));
    if (findLibrary(write.name)) throw conflict(`A library named "${write.name}" already exists. Library names are unique: choose a different one.`, "name");
    const at = new Date().toISOString();
    const library: StoredLibrary = { ...write, created_at: at, updated_at: at };
    db.libraries.push(library);
    return created(libraryView(library));
  });

  router.on("GET", "/libraries/:name", (ctx) => libraryView(find(ctx)));

  router.on("PUT", "/libraries/:name", (ctx) => {
    const library = find(ctx);
    Object.assign(library, parse(objectBody<Partial<LibraryWrite>>(ctx), library), { updated_at: new Date().toISOString() });
    advance();
    return libraryView(library);
  });

  router.on("DELETE", "/libraries/:name", (ctx) => {
    const library = find(ctx);
    const running = db.files.filter((f) => f.library === library.name && f.status === "running").length;
    if (running > 0) throw conflict(`${running} ${running === 1 ? "job is" : "jobs are"} still running in "${library.name}". Cancel ${running === 1 ? "it" : "them"} or wait until ${running === 1 ? "it finishes" : "they finish"}, then delete the library.`);
    db.libraries.splice(db.libraries.indexOf(library), 1);
    db.files = db.files.filter((f) => f.library !== library.name);
    return noContent();
  });

  router.on("POST", "/libraries/:name/scan", (ctx): ScanResult => {
    const library = find(ctx);
    library.last_scan_at = new Date().toISOString();
    // The first scan of a library added in the UI finds a dozen files.
    if (!db.files.some((f) => f.library === library.name)) db.files.push(...discoverFiles(library.name, db.files.reduce((max, f) => Math.max(max, f.source_id), 0) + 1, Date.now()));
    const files = db.files.filter((f) => f.library === library.name);
    const fresh = files.filter((f) => f.status === "none");
    // A scan queues what it has seen but not queued yet, a dozen at a time.
    const enqueued = fresh.slice(0, 12);
    enqueued.forEach(queue);
    advance();
    return { sources: files.length, assets: files.length, enqueued_jobs: enqueued.length, existing_jobs: files.length - fresh.length, skipped_ignored: library.rules.length > 0 ? 3 : 0, skipped_unstable: 0, denied_jobs: 0 };
  });

  router.on("GET", "/libraries/:name/files", (ctx): FileList => {
    const library = find(ctx);
    const status = ctx.query.get("status");
    if (status && !STATUSES.includes(status as FileStatus)) throw invalid(`Must be one of: ${STATUSES.join(", ")}.`, "status");
    const q = (ctx.query.get("q") ?? "").trim().toLowerCase();
    const sort = ctx.query.get("sort") || "path";
    const sorter = SORTERS[sort];
    if (!sorter) throw invalid("sort must be one of path, size, status, or updated_at.", "sort");
    const direction = ctx.query.get("dir") === "desc" ? -1 : 1;
    const limit = Math.min(intParam(ctx.query.get("limit"), 100), 500);
    const offset = intParam(ctx.query.get("offset"), 0);

    const matched = db.files
      .filter((f) => f.library === library.name && (!status || f.status === status) && (!q || f.path.toLowerCase().includes(q)))
      .sort((a, b) => direction * sorter(a, b) || a.source_id - b.source_id);
    return { total: matched.length, files: matched.slice(offset, offset + limit).map(fileView) };
  });

  router.on("POST", "/libraries/:name/requeue", (ctx): JobActionResult => {
    const library = find(ctx);
    const { paths } = objectBody<Partial<RequeueRequest>>(ctx);
    if (!Array.isArray(paths) || paths.length === 0) throw invalid("List at least one path.", "paths");
    const result: JobActionResult = { matched: 0, changed: 0, skipped: [] };
    for (const path of paths) {
      const file = db.files.find((f) => f.library === library.name && f.path === path);
      result.matched += 1;
      if (!file) {
        result.skipped.push({ path, reason: "not_found", detail: `Target "${path}" was not found in library "${library.name}".` });
        continue;
      }
      if (file.status === "running" || file.status === "queued") {
        result.skipped.push({ path, reason: "active", detail: `Library "${library.name}" path "${path}" still has active work; wait for it or cancel it first.` });
        continue;
      }
      queue(file);
      result.changed += 1;
    }
    advance();
    return result;
  });
}
