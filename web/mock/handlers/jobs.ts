// Queue routes: list, detail, cancel and retry.

import type { JobActionRequest, JobActionResult, JobList, JobState } from "../../src/api/types.ts";
import { advance, db, findLibrary } from "../db.ts";
import type { MockFile } from "../fixtures/files.ts";
import { intParam, invalid, notFound, objectBody, type Context, type Router } from "../http.ts";
import { jobDetail, jobView, stateOf } from "../jobs.ts";

const STATES: JobState[] = ["pending", "leased", "running", "validating", "replacing", "complete", "failed", "retrying", "skipped", "canceled"];
// What pkg/store lets a retry start from.
const RETRYABLE: JobState[] = ["failed", "skipped", "canceled", "retrying"];

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
  return b.updated_at - a.updated_at || (b.job_id ?? 0) - (a.job_id ?? 0);
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
  return jobs().filter((f) => (!request.library || f.library === request.library) && (wanted.length === 0 || wanted.includes(stateOf(f))));
}

export function jobRoutes(router: Router): void {
  router.on("GET", "/jobs", (ctx): JobList => {
    const library = ctx.query.get("library");
    const wanted = states(ctx.query.getAll("state"));
    const q = (ctx.query.get("q") ?? "").trim().toLowerCase();
    const limit = Math.min(intParam(ctx.query.get("limit"), 100), 500);
    const offset = intParam(ctx.query.get("offset"), 0);
    const matched = jobs()
      .filter((f) => (!library || f.library === library) && (wanted.length === 0 || wanted.includes(stateOf(f))) && (!q || f.path.toLowerCase().includes(q)))
      .sort(order);
    return { total: matched.length, jobs: matched.slice(offset, offset + limit).map(jobView) };
  });

  router.on("GET", "/jobs/:id", (ctx) => {
    const file = jobs().find((f) => f.job_id === Number(ctx.params.id));
    if (!file) throw notFound(`Job ${ctx.params.id}`);
    return jobDetail(file);
  });

  // Skip reasons are the stable codes of pkg/store (CancelSkipReason).
  router.on("POST", "/jobs/cancel", (ctx): JobActionResult => {
    const result: JobActionResult = { matched: 0, changed: 0, skipped: [] };
    for (const file of select(ctx)) {
      result.matched += 1;
      if (file.status !== "queued" && file.status !== "running") {
        result.skipped.push({ id: file.job_id ?? 0, reason: "already_terminal" });
        continue;
      }
      // A journaled publish must finish: interrupting it could leave the destination half written.
      if (stateOf(file) === "replacing") {
        result.skipped.push({ id: file.job_id ?? 0, reason: "publish_in_progress" });
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
      const state = stateOf(file);
      if (!RETRYABLE.includes(state)) {
        result.skipped.push({ id: file.job_id ?? 0, reason: state === "complete" ? "already_complete" : state === "pending" || state === "leased" ? "already_queued" : state === "replacing" ? "publish_in_progress" : "running" });
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

  // Mock only, not part of the contract: moves a running job to the start of a
  // step, so the short ones (validate, publish) can be looked at and tested.
  // The offsets mirror STEPS in ../db.ts.
  router.on("POST", "/_mock/jobs/:id/step", (ctx) => {
    const offsets: Record<string, number> = { probe: 0, "crop-detect": 2, "crf-search": 8, encode: 58, validate: 458, publish: 464 };
    const file = jobs().find((f) => f.job_id === Number(ctx.params.id));
    const offset = offsets[objectBody<{ step?: string }>(ctx).step ?? ""];
    if (!file || file.status !== "running" || offset === undefined) throw invalid("Name a running job and one of: " + Object.keys(offsets).join(", ") + ".");
    file.started_at = Date.now() - offset * 1000;
    return jobView(file);
  });
}
