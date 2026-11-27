// Queue routes: list, detail, cancel and retry.

import type { JobActionRequest, JobActionResult, JobList, JobState } from "../../src/api/types.ts";
import { advance, db, findLibrary } from "../db.ts";
import type { MockFile } from "../fixtures/files.ts";
import { intParam, invalid, notFound, objectBody, type Context, type Router } from "../http.ts";
import { jobDetail, jobView } from "../jobs.ts";

const STATES: JobState[] = ["pending", "leased", "running", "validating", "replacing", "complete", "failed", "retrying", "skipped", "canceled"];
const ACTIVE: JobState[] = ["running", "validating", "replacing", "leased"];

function states(values: string[]): JobState[] {
  const list = values.flatMap((v) => v.split(",")).map((v) => v.trim()).filter(Boolean);
  const unknown = list.find((v) => !STATES.includes(v as JobState));
  if (unknown) throw invalid(`"${unknown}" is not a job state. Use one of: ${STATES.join(", ")}.`, "state");
  return list as JobState[];
}

const jobs = () => db.files.filter((f) => f.job_id !== undefined);

/** Running first, then the queue in order, then the newest finished. */
function order(a: MockFile, b: MockFile): number {
  const rank = (f: MockFile) => (f.status === "running" ? 0 : f.status === "queued" ? 1 : 2);
  if (rank(a) !== rank(b)) return rank(a) - rank(b);
  if (rank(a) === 1) return (a.job_id ?? 0) - (b.job_id ?? 0);
  return b.updated_at - a.updated_at;
}

function select(ctx: Context): MockFile[] {
  const request = objectBody<JobActionRequest>(ctx);
  if (Array.isArray(request.ids) && request.ids.length > 0) {
    const ids = new Set(request.ids);
    return jobs().filter((f) => ids.has(f.job_id ?? -1));
  }
  const wanted = states(request.states ?? []);
  if (!request.library && wanted.length === 0) throw invalid("Select jobs by ids, by library, or by states.");
  if (request.library && !findLibrary(request.library)) throw notFound(`Library "${request.library}"`);
  return jobs().filter((f) => (!request.library || f.library === request.library) && (wanted.length === 0 || wanted.includes(f.job_state as JobState)));
}

export function jobRoutes(router: Router): void {
  router.on("GET", "/jobs", (ctx): JobList => {
    const library = ctx.query.get("library");
    const wanted = states(ctx.query.getAll("state"));
    const q = (ctx.query.get("q") ?? "").trim().toLowerCase();
    const limit = Math.min(intParam(ctx.query.get("limit"), 100), 500);
    const offset = intParam(ctx.query.get("offset"), 0);
    const matched = jobs()
      .filter((f) => (!library || f.library === library) && (wanted.length === 0 || wanted.includes(f.job_state as JobState)) && (!q || f.path.toLowerCase().includes(q)))
      .sort(order);
    return { total: matched.length, jobs: matched.slice(offset, offset + limit).map(jobView) };
  });

  router.on("GET", "/jobs/:id", (ctx) => {
    const file = jobs().find((f) => f.job_id === Number(ctx.params.id));
    if (!file) throw notFound(`Job ${ctx.params.id}`);
    return jobDetail(file);
  });

  router.on("POST", "/jobs/cancel", (ctx): JobActionResult => {
    const result: JobActionResult = { matched: 0, changed: 0, skipped: [] };
    for (const file of select(ctx)) {
      result.matched += 1;
      if (file.status !== "queued" && file.status !== "running") {
        result.skipped.push({ id: file.job_id ?? 0, reason: `job is already ${file.job_state}` });
        continue;
      }
      file.status = "skipped";
      file.job_state = "canceled";
      file.last_error = "canceled by operator";
      file.started_at = undefined;
      file.completed_at = file.updated_at = Date.now();
      result.changed += 1;
    }
    advance();
    return result;
  });

  router.on("POST", "/jobs/retry", (ctx): JobActionResult => {
    const result: JobActionResult = { matched: 0, changed: 0, skipped: [] };
    for (const file of select(ctx)) {
      result.matched += 1;
      if (file.job_state !== "failed" && file.job_state !== "canceled") {
        result.skipped.push({ id: file.job_id ?? 0, reason: ACTIVE.includes(file.job_state as JobState) ? "job is running" : `job is ${file.job_state}, not failed or canceled` });
        continue;
      }
      file.status = "queued";
      file.job_state = "pending";
      file.last_error = undefined;
      file.completed_at = undefined;
      file.updated_at = Date.now();
      result.changed += 1;
    }
    advance();
    return result;
  });
}
