import { useTitle } from "../../lib/title";
import { EmptyState, Topbar } from "../../ui";

/** Daemon settings and Arr connections. Placeholder. */
export default function SettingsPage() {
  useTitle(() => "Settings");
  return (
    <>
      <Topbar title="Settings" />
      <EmptyState icon="settings" title="This page is not built yet" description="Daemon settings and Arr connections go here." />
    </>
  );
}
