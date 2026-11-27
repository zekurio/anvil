import type { Library } from "../../../api";
import { EmptyState } from "../../../ui";

/** The Settings tab: path, flow, scan schedule and output handling. Placeholder. */
export function SettingsTab(_props: { library: Library }) {
  return (
    <div class="av-page-body">
      <EmptyState icon="settings" title="This page is not built yet" description="The settings of this library go here." />
    </div>
  );
}
