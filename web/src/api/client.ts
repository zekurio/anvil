import { createSignal } from "solid-js";

import type { ErrorBody, ErrorCode, Issue } from "./types";

const BASE = "/api/v1";
const UNREACHABLE = "Cannot reach the anvil daemon.";

/** A failed request. `code` is the daemon's error code, or "network" when no response arrived. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: ErrorCode | "network";
  /** The request field at fault, when there is exactly one. */
  readonly field?: string;
  /** Graph problems when code is "invalid_flow". */
  readonly issues: Issue[];

  constructor(status: number, body: { code: ErrorCode | "network"; message: string; field?: string; issues?: Issue[] }) {
    super(body.message);
    this.name = "ApiError";
    this.status = status;
    this.code = body.code;
    this.field = body.field;
    this.issues = body.issues ?? [];
  }
}

/** The message to show for any thrown value. */
export function errorMessage(err: unknown): string {
  if (err instanceof Error && err.message) return err.message;
  return "Something went wrong. Try again.";
}

// Module-scope signals: this app never renders on the server, and the fetch
// client has to reach them from outside any component.
const [unauthenticated, setUnauthenticated] = createSignal(false);
const [unreachable, setUnreachable] = createSignal(false);

/** True after any request answered 401, until a sign-in succeeds. */
export { unauthenticated };
/** True while the daemon does not answer. */
export { unreachable };

export function markAuthenticated(): void {
  setUnauthenticated(false);
}

export interface RequestOptions {
  query?: object;
  body?: unknown;
  signal?: AbortSignal;
}

function url(path: string, query?: object): string {
  if (!query) return BASE + path;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === "") continue;
    for (const item of Array.isArray(value) ? value : [value]) params.append(key, String(item));
  }
  const search = params.toString();
  return BASE + path + (search ? `?${search}` : "");
}

function fallbackCode(status: number): ErrorCode {
  switch (status) {
    case 400:
      return "invalid";
    case 401:
      return "unauthenticated";
    case 404:
      return "not_found";
    case 409:
      return "conflict";
    case 422:
      return "invalid_flow";
    case 503:
      return "unavailable";
    default:
      return "internal";
  }
}

export async function request<T>(method: string, path: string, options: RequestOptions = {}): Promise<T> {
  let response: Response;
  try {
    response = await fetch(url(path, options.query), {
      method,
      credentials: "same-origin",
      // The daemon refuses every mutating request without a JSON content type,
      // body or not: it is its proof that a script built the request on
      // purpose, which no HTML form on another site can give.
      headers: method === "GET" ? { Accept: "application/json" } : { Accept: "application/json", "Content-Type": "application/json" },
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: options.signal,
    });
  } catch (err) {
    if (options.signal?.aborted) throw err;
    setUnreachable(true);
    throw new ApiError(0, { code: "network", message: UNREACHABLE });
  }

  // A proxy in front of the daemon answers 502/504 when nothing listens behind it.
  const gatewayError = response.status === 502 || response.status === 504;
  setUnreachable(gatewayError);
  if (gatewayError) throw new ApiError(response.status, { code: "network", message: UNREACHABLE });

  if (response.ok) {
    if (response.status === 204) return undefined as T;
    return (await response.json()) as T;
  }

  const body = (await response.json().catch(() => null)) as { error?: ErrorBody } | null;
  // The session probe answers 401 only for a wrong password, which the
  // sign-in form reports itself.
  if (response.status === 401 && path !== "/session") setUnauthenticated(true);
  throw new ApiError(response.status, {
    code: body?.error?.code ?? fallbackCode(response.status),
    message: body?.error?.message ?? `The daemon answered ${response.status} ${response.statusText}.`,
    field: body?.error?.field,
    issues: body?.error?.issues,
  });
}
