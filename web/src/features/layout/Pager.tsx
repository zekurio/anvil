import { Show } from "solid-js";

import { formatCount } from "../../lib/format";
import { Button } from "../../ui";

import "./layout.css";

export interface PagerProps {
  /** Zero-based page that is on screen: the `page` of what `loadPage` answered. */
  page: number;
  pageSize: number;
  /** Every row matching the filter, across all pages. */
  total: number;
  onPage: (page: number) => void;
  /** What a row is: "file", "job". */
  noun: string;
  /** True while another page or filter is loading. */
  pending?: boolean;
}

/** The footer of a paged table: which rows are shown, and Previous / Next. Only one page is ever loaded, however large the list. */
export function Pager(props: PagerProps) {
  const pages = () => Math.max(1, Math.ceil(props.total / props.pageSize));
  const first = () => props.page * props.pageSize + 1;
  const last = () => Math.min(props.total, (props.page + 1) * props.pageSize);
  return (
    <div class="av-tablefoot">
      {/* While the next page loads the rows above are still the previous ones, so the range says which one is on its way. */}
      <span role="status">
        <Show when={props.total > 0} fallback={props.pending ? "Loading" : `0 ${props.noun}s`}>
          {props.pending ? "Loading " : ""}
          {formatCount(first())}–{formatCount(last())} of {formatCount(props.total)} {props.total === 1 ? props.noun : `${props.noun}s`}
        </Show>
      </span>
      <span class="av-spacer" />
      <Show when={pages() > 1}>
        <span>
          Page {formatCount(props.page + 1)} of {formatCount(pages())}
        </span>
        <Button size="sm" variant="ghost" icon="chevron-left" label="Previous page" disabled={props.page <= 0} onClick={() => props.onPage(props.page - 1)} />
        <Button size="sm" variant="ghost" icon="chevron-right" label="Next page" disabled={props.page >= pages() - 1} onClick={() => props.onPage(props.page + 1)} />
      </Show>
    </div>
  );
}
