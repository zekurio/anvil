import type { Library } from "../../../api";
import { EmptyState } from "../../../ui";

/** The Files tab: every file of the library with its status. Placeholder. */
export function FilesTab(_props: { library: Library }) {
  return (
    <div class="av-page-body">
      <EmptyState icon="file-video" title="This page is not built yet" description="The file list of this library goes here." />
    </div>
  );
}
