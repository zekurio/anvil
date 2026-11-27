// Server state on Solid 2's async primitives: a query is a memo over a push
// source with a key. Read it under <Loading> and <Errored>; invalidate it by
// key after a mutation; give it `poll` to keep it fresh while the tab is
// visible.
//
// The first answer for a key is the question the screen waits for: the memo
// is not ready until it lands, and a failure throws to <Errored>. Every later
// answer for the same key (a poll, an invalidation) is news from outside, the
// way a websocket message would be. It is fetched outside the reactive graph
// and pushed into the memo through from(), so it never opens a transition.
// A refetch that went through refresh() would: for as long as it is in
// flight, Solid holds every write that lands, and with a poll every two
// seconds that is most clicks on a slow link.

import { createDeferred, createMemo, from, onFlush, type Accessor } from "solid-js";

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
  // After the flush, so the answers land on what the caller's own writes (a
  // closed dialog, a cleared selection) left on screen.
  return new Promise((resolve) => {
    onFlush(() => {
      const hit = [...live].filter((entry) => wanted.some((prefix) => matches(entry.key, prefix)));
      void Promise.all(hit.map((entry) => entry.refetch())).then(() => resolve());
    });
  });
}

/**
 * Creates a keyed query. `key` and `fetcher` run in one tracking scope, so a
 * signal read by either asks again under the new key.
 *
 * A failed background refetch (a poll, or an invalidation) keeps the last
 * answer on screen; the shell reports an unreachable daemon on its own. A
 * failed first load, or a failed load after the key changed, throws to the
 * nearest <Errored>.
 */
export function createQuery<T>(key: () => QueryKey, fetcher: () => Promise<T>, options: QueryOptions = {}): Accessor<T> {
  const compute = () => {
    const id = serialize(key());
    // Asked here rather than in the subscription, which runs untracked: the
    // signals the fetcher reads are what make this a new question.
    const first = fetcher();

    return from<T>((next, error) => {
      let open = true;
      // Requests are numbered so that an answer overtaken by a newer one is dropped.
      let asked = 0;
      let shown = 0;
      let flying = 0;
      let timer: ReturnType<typeof setInterval> | undefined;

      const ask = (request: Promise<T>): Promise<void> => {
        const number = ++asked;
        flying++;
        return request.then(
          (value) => {
            flying--;
            if (!open || number < shown) return;
            shown = number;
            next(value);
          },
          (err: unknown) => {
            flying--;
            // Only a question that never got an answer fails. It closes the source.
            if (open && shown === 0 && flying === 0) error(err);
          },
        );
      };

      const entry: Entry = { key: id, refetch: () => ask(fetcher()) };
      live.add(entry);

      // A poll waits its turn: on a slow link, or while a request waits for
      // the operator to sign in again, asking more often answers nothing sooner.
      const poll = () => {
        if (flying === 0) void entry.refetch();
      };
      const watch = () => {
        clearInterval(timer);
        if (!options.poll || document.hidden) return;
        timer = setInterval(poll, options.poll);
      };
      // Only a polled query catches up on what it missed while hidden. One
      // without a poll answered once on purpose: a fresh answer there would
      // be pushed over what the page built on the first one.
      const onVisibility = () => {
        if (!document.hidden) poll();
        watch();
      };
      if (options.poll) document.addEventListener("visibilitychange", onVisibility);
      watch();
      void ask(first);

      return () => {
        open = false;
        live.delete(entry);
        clearInterval(timer);
        document.removeEventListener("visibilitychange", onVisibility);
      };
    });
  };

  return options.deferred ? createDeferred(compute) : createMemo(compute);
}
