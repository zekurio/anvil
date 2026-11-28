// The API layer: types mirroring the Go contract, one function per route, and
// keyed queries on Solid's async primitives.

export { ApiError, errorMessage, unauthenticated, unreachable } from "./client";
export { api } from "./endpoints";
export { keys } from "./keys";
export { createQuery, invalidate, POLL_LIST, POLL_STATUS, type QueryKey, type QueryOptions } from "./query";
export type * from "./types";
