import { createMemo, createSignal, type Accessor } from "solid-js";

import type { RowId } from "../../ui";

export interface Selection<R> {
  /** The selected ids that are on the current page: what the DataTable shows as checked. */
  pageIds: Accessor<RowId[]>;
  /** How many rows are selected across all pages. Safe to read before the first page has loaded. */
  count: Accessor<number>;
  /** Every selected row. Rows on the current page are the fresh ones; the rest are as they were when selected. */
  rows: Accessor<R[]>;
  /** DataTable's onSelect: replaces the selection of the current page and keeps other pages'. */
  select: (pageIds: RowId[]) => void;
  clear: () => void;
}

/**
 * Row selection for a paged, polled table. Only one page is loaded at a time,
 * so the selection remembers each row as it was picked; a poll refreshes the
 * ones still on screen without touching what is selected.
 */
export function createSelection<R>(pageRows: () => R[], rowId: (row: R) => RowId): Selection<R> {
  const [picked, setPicked] = createSignal<Map<RowId, R>>(new Map());
  const onPage = createMemo(() => new Map(pageRows().map((row) => [rowId(row), row])));
  return {
    pageIds: createMemo(() => [...onPage().keys()].filter((id) => picked().has(id))),
    count: () => picked().size,
    rows: createMemo(() => [...picked()].map(([id, row]) => onPage().get(id) ?? row)),
    select: (pageIds) =>
      setPicked((current) => {
        const next = new Map(current);
        for (const id of onPage().keys()) next.delete(id);
        for (const id of pageIds) {
          const row = onPage().get(id);
          if (row !== undefined) next.set(id, row);
        }
        return next;
      }),
    clear: () => setPicked(new Map()),
  };
}
