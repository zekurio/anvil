import type { JSX } from "@solidjs/web";
import { For, Show } from "solid-js";

import { Icon } from "./icon";
import { Checkbox } from "./inputs";

/* ---------- PathCell ---------- */

/** A path whose folder truncates before the file name. The full path is the tooltip. */
export function PathCell(props: { path: string }) {
  const cut = () => props.path.lastIndexOf("/");
  return (
    <span class="av-path" title={props.path}>
      <Show when={cut() >= 0}>
        <span class="av-path-dir">{props.path.slice(0, cut() + 1)}</span>
      </Show>
      <span class="av-path-file">{props.path.slice(cut() + 1)}</span>
    </span>
  );
}

/* ---------- DataTable ---------- */

export type RowId = string | number;

export interface DataTableColumn<R> {
  key: string;
  label: string;
  width?: number;
  align?: "left" | "right";
  mono?: boolean;
  /** Render the value as a PathCell. */
  path?: boolean;
  /** false renders a plain header for a column the server cannot sort by. */
  sortable?: boolean;
  /** Renders the cell. Without it the cell shows `row[key]`. */
  render?: (row: R) => JSX.Element;
}

export interface DataTableProps<R> {
  columns: DataTableColumn<R>[];
  rows: R[];
  /** Returns the row id: its identity across refetches and in `selected`. */
  rowId: (row: R) => RowId;
  /** Selected ids. Passing onSelect adds the checkbox column. */
  selected?: RowId[];
  onSelect?: (ids: RowId[]) => void;
  sortKey?: string;
  sortDir?: "asc" | "desc";
  onSort?: (key: string) => void;
  /** Opens the row, usually in the SideSheet. Rows become focusable and answer Enter. */
  onRowClick?: (row: R) => void;
  /** The id of the row shown in the SideSheet. */
  activeId?: RowId;
  /** 28px rows. */
  compact?: boolean;
  /** Max height; the header stays put. */
  height?: number;
  /** Names the table for assistive tech. */
  label?: string;
  /** Shown in place of rows when there are none. */
  empty?: JSX.Element;
}

const columnKey = (column: { key: string }) => column.key;

function Cell<R>(props: { column: DataTableColumn<R>; row: R }) {
  const value = () => (props.row as Record<string, unknown>)[props.column.key];
  return <>{props.column.render ? props.column.render(props.row) : props.column.path ? <PathCell path={String(value() ?? "")} /> : (value() as JSX.Element)}</>;
}

/** Dense, sortable, selectable records. Cells truncate; nothing wraps. */
export function DataTable<R>(props: DataTableProps<R>) {
  const selected = () => props.selected ?? [];
  const allSelected = () => props.rows.length > 0 && props.rows.every((row) => selected().includes(props.rowId(row)));
  const toggle = (id: RowId) => props.onSelect?.(selected().includes(id) ? selected().filter((other) => other !== id) : [...selected(), id]);

  return (
    <div class="av-table-wrap" style={props.height ? { "max-height": `${props.height}px` } : undefined}>
      <table class={["av-table", { "av-table-compact": props.compact }]} aria-label={props.label}>
        <colgroup>
          <Show when={props.onSelect}>
            <col style={{ width: "40px" }} />
          </Show>
          <For each={props.columns} keyed={columnKey}>{(column) => <col style={column().width ? { width: `${column().width}px` } : undefined} />}</For>
        </colgroup>
        <thead>
          <tr>
            <Show when={props.onSelect}>
              <th class="av-sel">
                <Checkbox
                  aria-label="Select all"
                  checked={allSelected()}
                  indeterminate={selected().length > 0 && !allSelected()}
                  onChange={() => props.onSelect?.(allSelected() ? [] : props.rows.map(props.rowId))}
                />
              </th>
            </Show>
            <For each={props.columns} keyed={columnKey}>
              {(column) => {
                const sorted = () => props.sortKey === column().key;
                return (
                  <th class={["av-th", { "av-num": column().align === "right" }]} aria-sort={sorted() ? (props.sortDir === "desc" ? "descending" : "ascending") : undefined}>
                    <Show when={column().sortable !== false} fallback={column().label}>
                      <button type="button" onClick={() => props.onSort?.(column().key)}>
                        {column().label}
                        <Show when={sorted()}>
                          <Icon name={props.sortDir === "desc" ? "chevron-down" : "chevron-up"} size={12} />
                        </Show>
                      </button>
                    </Show>
                  </th>
                );
              }}
            </For>
          </tr>
        </thead>
        <tbody>
          {/* Keyed by id, not identity: a refetch returns new objects for the same rows. */}
          <For each={props.rows} keyed={props.rowId}>
            {(row) => {
              const id = () => props.rowId(row());
              return (
                <tr
                  class={{ "av-row-link": !!props.onRowClick, "av-row-active": props.activeId === id() }}
                  aria-selected={selected().includes(id()) ? "true" : undefined}
                  tabindex={props.onRowClick ? 0 : undefined}
                  onClick={() => props.onRowClick?.(row())}
                  onKeyDown={(event) => {
                    if (event.key !== "Enter" || event.target !== event.currentTarget) return;
                    props.onRowClick?.(row());
                  }}
                >
                  <Show when={props.onSelect}>
                    <td class="av-sel" onClick={(event) => event.stopPropagation()}>
                      <Checkbox aria-label="Select row" checked={selected().includes(id())} onChange={() => toggle(id())} />
                    </td>
                  </Show>
                  <For each={props.columns} keyed={columnKey}>
                    {(column) => (
                      <td class={{ "av-num": column().align === "right", "av-mono": column().mono }}>
                        <Cell column={column()} row={row()} />
                      </td>
                    )}
                  </For>
                </tr>
              );
            }}
          </For>
        </tbody>
      </table>
      <Show when={props.rows.length === 0 && props.empty}>{props.empty}</Show>
    </div>
  );
}
