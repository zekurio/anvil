import { useLocation } from "@solidjs/router";
import { Show } from "solid-js";

import { formatCount, joinDot, plural } from "../lib/format";
import { setTheme, theme } from "../lib/theme";
import { Button, Sidebar, type SidebarItem } from "../ui";
import { openAddLibrary } from "./AddLibraryDialog";
import { useAppData } from "./data";
import { signOut } from "./session";

const ADD_LIBRARY = "add-library";

/** The id of the nav item a pathname belongs to. */
function currentItem(pathname: string): string | undefined {
  const [first, second] = pathname.split("/").filter(Boolean).map(decodeURIComponent);
  switch (first) {
    case "libraries":
      return second ? `library:${second}` : undefined;
    case "flows":
    case "queue":
    case "settings":
      return first;
    default:
      return undefined;
  }
}

/** The app navigation, filled from the API: libraries with their pending counts, the queue with its size. */
export function AppSidebar() {
  const { session, status, libraries } = useAppData();
  const location = useLocation();

  const queued = () => {
    const queue = status().queue;
    return (queue.pending ?? 0) + (queue.leased ?? 0) + (queue.retrying ?? 0) + (queue.running ?? 0) + (queue.validating ?? 0) + (queue.replacing ?? 0);
  };

  const items = (): SidebarItem[] => [
    { section: "Libraries" },
    ...libraries().map((library): SidebarItem => ({ id: `library:${library.name}`, label: library.name, icon: "library-big", count: formatCount(library.stats.pending), href: `/libraries/${encodeURIComponent(library.name)}` })),
    { id: ADD_LIBRARY, label: "Add library", icon: "plus" },
    { section: "Processing" },
    { id: "flows", label: "Flows", icon: "workflow", href: "/flows" },
    { id: "queue", label: "Queue", icon: "list-video", count: formatCount(queued()), href: "/queue" },
    { section: "System" },
    { id: "settings", label: "Settings", icon: "settings", href: "/settings" },
  ];

  const version = () => (/^\d/.test(status().version) ? `v${status().version}` : status().version);

  return (
    <Sidebar
      home="/"
      items={items()}
      value={currentItem(location.pathname)}
      onChange={(id) => {
        if (id === ADD_LIBRARY) openAddLibrary();
      }}
      footer={joinDot(version(), plural(status().workers.configured, "worker"))}
      actions={
        <>
          <Button size="sm" variant="ghost" icon={theme() === "dark" ? "sun" : "moon"} label={theme() === "dark" ? "Switch to the light theme" : "Switch to the dark theme"} onClick={() => setTheme(theme() === "dark" ? "light" : "dark")} />
          <Show when={session().auth_required}>
            <Button size="sm" variant="ghost" icon="log-out" label="Sign out" onClick={() => void signOut()} />
          </Show>
        </>
      }
    />
  );
}
