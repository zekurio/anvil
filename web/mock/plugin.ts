// Dev-only mock of the daemon's /api/v1, mounted on the Vite dev server by
// `npm run dev:mock`. It is imported from vite.config.ts only, so nothing
// here reaches the production bundle.

import type { IncomingMessage, ServerResponse } from "node:http";

import type { Plugin } from "vite";

import type { ErrorResponse } from "../src/api/types.ts";
import { advance, db } from "./db.ts";
import { arrRoutes } from "./handlers/arrs.ts";
import { flowRoutes } from "./handlers/flows.ts";
import { jobRoutes } from "./handlers/jobs.ts";
import { libraryRoutes } from "./handlers/libraries.ts";
import { systemRoutes } from "./handlers/system.ts";
import { HttpError, Reply, Router } from "./http.ts";

const PREFIX = "/api/v1";
// Enough to make loading states visible without slowing work down.
const LATENCY_MS = 120;

export interface MockOptions {
  /** Require the password "anvil" before anything but /session answers. */
  auth: boolean;
}

function readBody(req: IncomingMessage): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    req.on("data", (chunk: Buffer) => chunks.push(chunk));
    req.on("error", reject);
    req.on("end", () => {
      const text = Buffer.concat(chunks).toString("utf8");
      if (text === "") return resolve(undefined);
      try {
        resolve(JSON.parse(text));
      } catch {
        reject(new HttpError(400, "invalid", "The request body is not valid JSON."));
      }
    });
  });
}

function send(res: ServerResponse, status: number, body?: unknown): void {
  res.statusCode = status;
  if (body === undefined) return void res.end();
  res.setHeader("Content-Type", "application/json; charset=utf-8");
  res.end(JSON.stringify(body));
}

export function mockApi(options: MockOptions): Plugin {
  const router = new Router();
  systemRoutes(router, options.auth);
  flowRoutes(router);
  libraryRoutes(router);
  jobRoutes(router);
  arrRoutes(router);

  async function handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const url = new URL(req.url ?? "/", "http://mock");
    const path = url.pathname.slice(PREFIX.length);
    try {
      const match = router.match(req.method ?? "GET", path);
      if (match === undefined) throw new HttpError(404, "not_found", `No route for ${url.pathname}.`);
      if (match === "method") throw new HttpError(400, "invalid", `${req.method} is not supported on ${url.pathname}.`);
      if (options.auth && !db.authenticated && path !== "/session") throw new HttpError(401, "unauthenticated", "Sign in first.");
      const body = await readBody(req);
      advance();
      const result = match.handler({ params: match.params, query: url.searchParams, body });
      if (result instanceof Reply) send(res, result.status, result.body);
      else send(res, 200, result);
    } catch (err) {
      if (!(err instanceof HttpError)) {
        console.error("[mock api]", err);
        return send(res, 500, { error: { code: "internal", message: "The mock API threw. See the dev server log." } } satisfies ErrorResponse);
      }
      send(res, err.status, { error: { code: err.code, message: err.message, ...(err.field ? { field: err.field } : {}), ...(err.issues ? { issues: err.issues } : {}) } } satisfies ErrorResponse);
    }
  }

  return {
    name: "anvil:mock-api",
    apply: "serve",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (!req.url?.startsWith(`${PREFIX}/`)) return next();
        setTimeout(() => void handle(req, res), LATENCY_MS);
      });
    },
  };
}
