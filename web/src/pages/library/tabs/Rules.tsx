import type { Library } from "../../../api";
import { EmptyState } from "../../../ui";

/** The Rules tab: the Allow and Deny rules that decide which files are queued. Placeholder. */
export function RulesTab(_props: { library: Library }) {
  return (
    <div class="av-page-body">
      <EmptyState icon="arrow-up-down" title="This page is not built yet" description="The Allow and Deny rules of this library go here." />
    </div>
  );
}
