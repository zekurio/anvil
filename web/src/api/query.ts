// Server state on Solid 2's async primitives: a query is an async memo with a
// key. Read it under <Loading> and <Errored>; invalidate it by key after a
// mutation; give it `poll` to keep it fresh while the tab is visible.

import { createDeferred, createMemo, onCleanup, onFlush, onSettled, refresh, type Accessor } from "solid-js";

export type QueryKey = readonly (string | number | boolean | null | undefined | object)[];

/** Poll cadences. There is no websocket or SSE; these are the freshness contract. */
export const POLL_STATUS = 2_000;
export const POLL_LIST = 5_000;

export interface QueryOptions {
  /** Refetch every this many milliseconds while the tab is visible. */
  poll?: number;
  /**
   * Keep showing the previous answer while a changed key loads, instead of
   * holding the change that caused it. For filtered and paged tables; pair
   * with `isPending(() => data())` for a "refreshing" hint.
   */
  deferred?: boolean;
}

interface Entry {
  key: string;
  /** Resolves once the refetch has landed, whether it succeeded or not. */
  refetch: () => Promise<void>;
}

const live = new Set<Entry>();

function serialize(key: QueryKey): string {
  return JSON.stringify(key);
}

/** True when `prefix` (serialized without its closing bracket) starts `key`. */
function matches(key: string, prefix: string): boolean {
  return key === `${prefix}]` || key.startsWith(`${prefix},`);
}

/**
 * Refetches every mounted query whose key starts with one of the prefixes:
 * `invalidate(["libraries"])` reaches `["libraries"]` and
 * `["libraries", "movies", "files", {...}]`. The promise resolves once the
 * fresh answers are on screen; await it before navigating to something the
 * mutation just created.
 */
export function invalidate(...prefixes: QueryKey[]): Promise<void> {
  const wanted = prefixes.map((prefix) => serialize(prefix).slice(0, -1));
  // Refetching is async work, and Solid holds every write of the same frame
  // with it. Starting it after the flush keeps the caller's own writes (a
  // toast, a closed dialog) from waiting on the network.
  return new Promise((resolve) => {
    onFlush(() => {
      const hit = [...live].filter((entry) => wanted.some((prefix) => matches(entry.key, prefix)));
      void Promise.all(hit.map((entry) => entry.refetch())).then(() => resolve());
    });
  });
}

/**
 * Creates a keyed query. `key` and `fetcher` run in one tracking scope, so a
 * signal read by either refetches the query.
 *
 * A failed background refetch (a poll, or an invalidation) keeps the last
 * answer on screen; the shell reports an unreachable daemon on its own. A
 * failed first load, or a failed load after the key changed, throws to the
 * nearest <Errored>.
 */
export function createQuery<T>(key: () => QueryKey, fetcher: () => Promise<T>, options: QueryOptions = {}): Accessor<T> {
  let background = false;
  let answered: { key: string; value: T } | undefined;

  const compute = (): Promise<T> => {
    const id = serialize(key());
    const entry: Entry = { key: id, refetch };
    live.add(entry);
    onCleanup(() => live.delete(entry));

    const keep = background && answered?.key === id ? answered : undefined;
    background = false;
    return fetcher().then(
      (value) => {
        answered = { key: id, value };
        return value;
      },
      (err: unknown) => {
        if (keep) return keep.value;
        throw err;
      },
    );
  };

  const data = options.deferred ? createDeferred(compute) : createMemo(compute);

  function refetch(): Promise<void> {
    background = true;
    return refresh(data).then(
      () => undefined,
      () => undefined,
    );
  }

  const interval = options.poll;
  if (interval) {
    onSettled(() => {
      let timer: ReturnType<typeof setInterval> | undefined;
      const start = () => {
        timer = setInterval(() => void refetch(), interval);
      };
      const onVisibility = () => {
        clearInterval(timer);
        if (document.hidden) return;
        void refetch();
        start();
      };
      if (!document.hidden) start();
      document.addEventListener("visibilitychange", onVisibility);
      return () => {
        clearInterval(timer);
        document.removeEventListener("visibilitychange", onVisibility);
      };
    });
  }

  return data;
}
