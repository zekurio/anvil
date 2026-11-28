import { createMemo, createSignal } from "solid-js";

export interface Draft<T> {
  /** What the form shows: the saved value with the edited fields laid over it. */
  value: () => T;
  /**
   * Changes some fields of the draft. A field is replaced as a whole: to
   * change part of a nested object or a list, pass the complete new value.
   */
  patch: (change: Partial<T>) => void;
  /** True while the draft differs from the saved value. */
  dirty: () => boolean;
  /** Drops the edits; the form shows the saved value again. */
  reset: () => void;
}

/**
 * A form draft over polled server state. The draft keeps only the fields the
 * operator edited; every other field follows `saved`, so a poll or another
 * tab's change (a library paused from the top bar) shows up and is sent back
 * as it is now, not as it was at the first edit. Call `reset` after a
 * successful save.
 *
 * `T` must survive JSON: plain data, no dates or functions.
 */
export function createDraft<T extends object>(saved: () => T): Draft<T> {
  // null until the first edit; then only the fields the operator changed.
  const [edited, setEdited] = createSignal<Partial<T> | null>(null);
  // A poll answers with equal data in new objects. Comparing the JSON keeps
  // the form from re-rendering for those.
  const savedJSON = createMemo(() => JSON.stringify(saved()));
  const base = createMemo(() => JSON.parse(savedJSON()) as T);
  const value = createMemo(() => ({ ...base(), ...edited() }));
  return {
    value,
    patch: (change) => setEdited((current) => ({ ...(current ?? {}), ...change })),
    // Against the merged value, so a field edited back to what is saved is not a change.
    dirty: createMemo(() => JSON.stringify(value()) !== savedJSON()),
    reset: () => setEdited(null),
  };
}
