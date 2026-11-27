import type { Library } from "../../../api";
import { EmptyState } from "../../../ui";

/** The History tab: finished jobs with before and after sizes. Placeholder. */
export function HistoryTab(_props: { library: Library }) {
  return (
    <div class="av-page-body">
      <EmptyState icon="clock" title="This page is not built yet" description="The finished jobs of this library go here." />
    </div>
  );
}
