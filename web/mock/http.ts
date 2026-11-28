// A small request router for the mock API: method + path pattern -> handler.

import type { ErrorCode, Issue } from "../src/api/types.ts";

export class HttpError extends Error {
  constructor(
    readonly status: number,
    readonly code: ErrorCode,
    message: string,
    readonly field?: string,
    readonly issues?: Issue[],
  ) {
    super(message);
  }
}

export const invalid = (message: string, field?: string) => new HttpError(400, "invalid", message, field);
export const notFound = (what: string) => new HttpError(404, "not_found", `${what} does not exist.`);
export const conflict = (message: string, field?: string) => new HttpError(409, "conflict", message, field);

export interface Context {
  params: Record<string, string>;
  query: URLSearchParams;
  /** The parsed JSON body, or undefined when the request had none. */
  body: unknown;
}

/** A handler returns the 200 body, or a Reply for another status. */
export class Reply {
  constructor(
    readonly status: number,
    readonly body?: unknown,
  ) {}
}

export const created = (body: unknown) => new Reply(201, body);
export const noContent = () => new Reply(204);

export type Handler = (ctx: Context) => unknown;

interface Route {
  method: string;
  segments: string[];
  handler: Handler;
}

export class Router {
  private readonly routes: Route[] = [];

  /** Registers a route; `:name` segments become params. Earlier routes win. */
  on(method: string, pattern: string, handler: Handler): this {
    this.routes.push({ method, segments: pattern.split("/").filter(Boolean), handler });
    return this;
  }

  match(method: string, path: string): { handler: Handler; params: Record<string, string> } | "method" | undefined {
    const parts = path.split("/").filter(Boolean).map(decodeURIComponent);
    let pathMatched = false;
    for (const route of this.routes) {
      if (route.segments.length !== parts.length) continue;
      const params: Record<string, string> = {};
      const ok = route.segments.every((segment, i) => {
        const part = parts[i] ?? "";
        if (!segment.startsWith(":")) return segment === part;
        params[segment.slice(1)] = part;
        return true;
      });
      if (!ok) continue;
      if (route.method === method) return { handler: route.handler, params };
      pathMatched = true;
    }
    return pathMatched ? "method" : undefined;
  }
}

/** Reads a required object body. */
export function objectBody<T>(ctx: Context): T {
  if (typeof ctx.body !== "object" || ctx.body === null || Array.isArray(ctx.body)) {
    throw invalid("The request body must be a JSON object.");
  }
  return ctx.body as T;
}

export function intParam(value: string | null, fallback: number): number {
  if (value === null || value === "") return fallback;
  const n = Number(value);
  if (!Number.isInteger(n) || n < 0) throw invalid("Must be a whole number, 0 or more.");
  return n;
}
