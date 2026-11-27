import { useParams } from "@solidjs/router";
import { createSignal, Match, Show, Switch } from "solid-js";

import { api, keys, type Library } from "../../api";
import { formatBytes, formatCount, formatRelative, joinDot, plural } from "../../lib/format";
import { run } from "../../lib/mutate";
import { useTitle } from "../../lib/title";
import type { LibraryTab } from "../../routes";
import { useAppData } from "../../shell/data";
import { Button, EmptyState, LinkButton, StatBar, Tabs, Topbar } from "../../ui";
import { FilesTab } from "./tabs/Files";
import { HistoryTab } from "./tabs/History";
import { RulesTab } from "./tabs/Rules";
import { SettingsTab } from "./tabs/Settings";

/**
 * One library: Topbar, StatBar and the Files / Rules / History / Settings
 * tabs. The tab is part of the URL (/libraries/:name/:tab?), so reloads and
 * links land on it. Each tab's body lives in ./tabs.
 */
export default function LibraryPage() {
  const params = useParams();
  const { libraries } = useAppData();
  const library = () => libraries().find((candidate) => candidate.name === params.name);
  useTitle(() => params.name);

  return (
    <Show
      when={library()}
      fallback={
        <>
          <Topbar title={params.name ?? "Library"} />
          <EmptyState icon="library-big" title="This library does not exist" description="It may have been removed. Pick another one from the navigation." />
        </>
      }
    >
      {(found) => <LibraryView library={found()} tab={(params.tab as LibraryTab | undefined) ?? "files"} />}
    </Show>
  );
}

function LibraryView(props: { library: Library; tab: LibraryTab }) {
  const { status } = useAppData();
  const [scanning, setScanning] = createSignal(false);
  const base = () => `/libraries/${encodeURIComponent(props.library.name)}`;
  const stats = () => props.library.stats;

  // The status poll carries the server's clock, so "4 min ago" keeps counting without a timer.
  const scanned = () => (props.library.last_scan_at ? `scanned ${formatRelative(props.library.last_scan_at, Date.parse(status().server_time))}` : "not scanned yet");

  const scan = async () => {
    setScanning(true);
    await run(() => api.libraries.scan(props.library.name), {
      invalidate: [keys.libraries, keys.jobs, keys.status],
      success: (result) => (result.enqueued_jobs > 0 ? `Scan finished. Queued ${plural(result.enqueued_jobs, "file")}.` : "Scan finished. Nothing new to queue."),
      failure: "The scan could not start.",
    });
    setScanning(false);
  };

  return (
    <>
      <Topbar title={props.library.name} meta={joinDot(props.library.path, scanned(), props.library.paused && "paused")}>
        <LinkButton variant="ghost" icon="workflow" href={`/flows/${props.library.flow_id}`}>
          Flow: {props.library.flow_name}
        </LinkButton>
        <Button icon="refresh-cw" busy={scanning()} onClick={scan}>
          Scan
        </Button>
      </Topbar>
      <div class="av-page-stats">
        <StatBar
          items={[
            { label: "Files", value: formatCount(stats().files) },
            { label: "Total size", value: formatBytes(stats().total_bytes) },
            { label: "Pending", value: formatCount(stats().pending), tone: "running" },
            { label: "Failed", value: formatCount(stats().failed), tone: stats().failed > 0 ? "danger" : undefined },
            { label: "Space saved", value: formatBytes(stats().saved_bytes), tone: "ok" },
          ]}
        />
      </div>
      <div class="av-page-tabs">
        <Tabs
          label={`Views of ${props.library.name}`}
          value={props.tab}
          tabs={[
            { id: "files", label: "Files", count: formatCount(stats().files), href: base() },
            { id: "rules", label: "Rules", count: props.library.rules.length, href: `${base()}/rules` },
            { id: "history", label: "History", href: `${base()}/history` },
            { id: "settings", label: "Settings", href: `${base()}/settings` },
          ]}
        />
      </div>
      <Switch>
        <Match when={props.tab === "files"}>
          <FilesTab library={props.library} />
        </Match>
        <Match when={props.tab === "rules"}>
          <RulesTab library={props.library} />
        </Match>
        <Match when={props.tab === "history"}>
          <HistoryTab library={props.library} />
        </Match>
        <Match when={props.tab === "settings"}>
          <SettingsTab library={props.library} />
        </Match>
      </Switch>
    </>
  );
}
