import { useNavigate } from "@solidjs/router";
import { createEffect, Show } from "solid-js";

import { useTitle } from "../../lib/title";
import { openAddLibrary } from "../../shell/AddLibraryDialog";
import { useAppData } from "../../shell/data";
import { Button, EmptyState, Topbar } from "../../ui";

/** "/" has no view of its own: it opens the first library, or says how to add one. */
export default function Home() {
  const { libraries } = useAppData();
  const navigate = useNavigate();
  useTitle(() => "Libraries");

  createEffect(
    () => libraries()[0]?.name,
    (first) => {
      if (first) navigate(`/libraries/${encodeURIComponent(first)}`, { replace: true });
    },
  );

  return (
    <Show when={libraries().length === 0}>
      <Topbar title="Libraries" />
      <EmptyState icon="library-big" title="No libraries yet" description="Add a folder to scan.">
        <Button variant="primary" icon="plus" onClick={openAddLibrary}>
          Add library
        </Button>
      </EmptyState>
    </Show>
  );
}
