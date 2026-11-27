import { createMemo, createSignal } from "solid-js";

export interface Draft<T> {
  /** What the form shows: the edits, or the saved value while nothing was edited. */
  value: () => T;
  /** Changes some fields of the draft. */
  patch: (change: Partial<T>) => void;
  /** True while the draft differs from the saved value. */
  dirty: () => boolean;
  /** Drops the edits; the form shows the saved value again. */
  reset: () => void;
}

/**
 * A form draft over polled server state. Until the first edit the form
 * follows `saved`, so a poll or another tab's change shows up; from the first
 * edit on, the draft is its own value and no refetch touches it. Call `reset`
 * after a successful save.
 *
 * `T` must survive JSON: plain data, no dates or functions.
 */
export function createDraft<T extends object>(saved: () => T): Draft<T> {
  const [edited, setEdited] = createSignal<T | null>(null);
  // A poll answers with equal data in new objects. Comparing the JSON keeps
  // the form from re-rendering for those.
  const savedJSON = createMemo(() => JSON.stringify(saved()));
  const base = createMemo(() => JSON.parse(savedJSON()) as T);
  return {
    value: () => edited() ?? base(),
    patch: (change) => setEdited((current) => ({ ...(current ?? base()), ...change })),
    dirty: createMemo(() => edited() !== null && JSON.stringify(edited()) !== savedJSON()),
    reset: () => setEdited(null),
  };
}
