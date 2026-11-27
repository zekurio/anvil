import { useNavigate } from "@solidjs/router";
import { createEffect, Show } from "solid-js";

import { homeTarget } from "../../features/libraries/home";
import { useTitle } from "../../lib/title";
import { openAddLibrary } from "../../shell/AddLibraryDialog";
import { useAppData } from "../../shell/data";
import { Button, EmptyState, Topbar } from "../../ui";

/** "/" has no view of its own: it opens the first library that has files, or says how to add one. */
export default function Home() {
  const { libraries } = useAppData();
  const navigate = useNavigate();
  useTitle(() => "Libraries");

  createEffect(
    () => homeTarget(libraries()),
    (target) => {
      if (target) navigate(target, { replace: true });
    },
  );

  return (
    <Show when={libraries().length === 0}>
      <Topbar title="Libraries" />
      <EmptyState icon="library-big" title="No libraries yet" description="A library is a folder anvil scans. Add one to get started.">
        <Button variant="primary" icon="plus" onClick={openAddLibrary}>
          Add library
        </Button>
      </EmptyState>
    </Show>
  );
}
