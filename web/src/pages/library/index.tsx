import { useParams } from "@solidjs/router";
import { createMemo, createSignal, Match, Show, Switch } from "solid-js";

import { api, keys, type Library, type LibraryWrite } from "../../api";
import { isScanning, replacesInPlace, resumeConsequence, scanLibrary, toWrite } from "../../features/libraries/library";
import { formatBytes, formatCount, formatRelative, joinDot, plural } from "../../lib/format";
import { run } from "../../lib/mutate";
import { useTitle } from "../../lib/title";
import type { LibraryTab } from "../../routes";
import { useAppData } from "../../shell/data";
import { Banner, Button, confirm, ErrorState, LinkButton, Select, StatBar, Tabs, Topbar } from "../../ui";
import { FilesTab } from "./tabs/Files";
import { HistoryTab } from "./tabs/History";
import { RulesTab } from "./tabs/Rules";
import { SettingsTab } from "./tabs/Settings";

import "./library.css";

/**
 * One library: Topbar, StatBar and the Files / Rules / History / Settings
 * tabs. The tab is part of the URL (/libraries/:name/:tab?), so reloads and
 * links land on it. Each tab's body lives in ./tabs.
 */
export default function LibraryPage() {
  const params = useParams();
  const { libraries } = useAppData();
  const known = createMemo(() => (libraries().some((candidate) => candidate.name === params.name) ? params.name : undefined));
  useTitle(() => params.name);

  return (
    // Keyed by name: another library is another view, with its own filters, selection and drafts.
    <Show
      when={known()}
      keyed
      fallback={
        <>
          <Topbar title={params.name ?? "Library"} />
          <ErrorState title="This library does not exist" error={new Error(`No library is named "${params.name}". It may have been deleted, or the address is mistyped.`)}>
            <LinkButton href="/">Open the first library</LinkButton>
          </ErrorState>
        </>
      }
    >
      {(name) => <LibraryView name={name} tab={(params.tab as LibraryTab | undefined) ?? "files"} />}
    </Show>
  );
}

function LibraryView(props: { name: string; tab: LibraryTab }) {
  const { status, libraries, flows } = useAppData();
  const [busy, setBusy] = createSignal<"flow" | "pause" | null>(null);
  // The last known state stays up for the moment between a delete and the navigation away.
  const library = createMemo<Library>((previous) => libraries().find((candidate) => candidate.name === props.name) ?? (previous as Library));
  const base = () => `/libraries/${encodeURIComponent(props.name)}`;
  const stats = () => library().stats;

  // The status poll carries the server's clock, so "4 min ago" keeps counting without a timer.
  const scanned = () => (library().last_scan_at ? `scanned ${formatRelative(library().last_scan_at ?? "", Date.parse(status().server_time))}` : "never scanned");

  const update = async (kind: "flow" | "pause", change: Partial<LibraryWrite>, success: string, failure: string) => {
    setBusy(kind);
    await run(() => api.libraries.update(props.name, { ...toWrite(library()), ...change }), { invalidate: [keys.libraries, keys.flows, keys.status], success, failure });
    setBusy(null);
  };

  const assignFlow = async (select: HTMLSelectElement) => {
    const flow = flows().find((candidate) => String(candidate.id) === select.value);
    if (!flow || flow.id === library().flow_id) return;
    if (stats().pending > 0) {
      const agreed = await confirm({
        title: `Use the flow "${flow.name}" for ${props.name}?`,
        description: `${plural(stats().pending, "file")} of this library ${stats().pending === 1 ? "is" : "are"} queued. Queued files use the new flow from their next attempt. Files that are done are not processed again.`,
        confirmLabel: "Change flow",
      });
      if (!agreed) {
        // The native select already shows the new choice; put the saved one back.
        select.value = String(library().flow_id);
        return;
      }
    }
    await update("flow", { flow_id: flow.id }, `${props.name} now uses the flow ${flow.name}.`, "The flow could not be changed.");
    select.value = String(library().flow_id);
  };

  const pause = () => update("pause", { paused: true }, `Paused ${props.name}. Running jobs finish; no new job starts.`, "The library could not be paused.");

  const resume = async () => {
    if (replacesInPlace(library())) {
      const agreed = await confirm({ title: `Resume ${props.name}?`, description: resumeConsequence(stats().pending), confirmLabel: "Resume library", cancelLabel: "Keep it paused" });
      if (!agreed) return;
    }
    await update("pause", { paused: false }, `Resumed ${props.name}.`, "The library could not be resumed.");
  };

  return (
    <>
      <Topbar title={props.name} meta={joinDot(library().path, scanned())}>
        <div class="av-topbar-select">
          <Select
            aria-label="Flow of this library"
            value={String(library().flow_id)}
            disabled={busy() !== null}
            options={flows().map((flow) => ({ value: String(flow.id), label: `Flow: ${flow.name}` }))}
            onChange={(event) => void assignFlow(event.currentTarget)}
          />
        </div>
        <Button icon="refresh-cw" busy={isScanning(props.name)} onClick={() => void scanLibrary(props.name)}>
          Scan library
        </Button>
        <Show
          when={library().paused}
          fallback={
            <Button icon="pause" busy={busy() === "pause"} disabled={busy() !== null} onClick={pause}>
              Pause library
            </Button>
          }
        >
          <Button icon="play" busy={busy() === "pause"} disabled={busy() !== null} onClick={resume}>
            Resume library
          </Button>
        </Show>
      </Topbar>
      <Show when={library().paused}>
        <Banner tone="warning">
          This library is paused. Scans still queue files, but no new job starts
          {stats().pending > 0 ? `: ${plural(stats().pending, "file")} ${stats().pending === 1 ? "waits" : "wait"}.` : "."}
          {stats().running > 0 ? ` ${plural(stats().running, "running job")} ${stats().running === 1 ? "finishes" : "finish"} first.` : ""}
        </Banner>
      </Show>
      <div class="av-page-stats">
        <StatBar
          items={[
            { label: "Files", value: formatCount(stats().files) },
            { label: "Total size", value: formatBytes(stats().total_bytes) },
            { label: "Pending", value: formatCount(stats().pending), tone: stats().pending > 0 ? "running" : undefined },
            { label: "Failed", value: formatCount(stats().failed), tone: stats().failed > 0 ? "danger" : undefined },
            { label: "Space saved", value: formatBytes(stats().saved_bytes), tone: "ok" },
          ]}
        />
      </div>
      <div class="av-page-tabs">
        <Tabs
          label={`Views of ${props.name}`}
          value={props.tab}
          tabs={[
            { id: "files", label: "Files", count: formatCount(stats().files), href: base() },
            { id: "rules", label: "Rules", count: library().rules.length, href: `${base()}/rules` },
            { id: "history", label: "History", href: `${base()}/history` },
            { id: "settings", label: "Settings", href: `${base()}/settings` },
          ]}
        />
      </div>
      <Switch>
        <Match when={props.tab === "files"}>
          <FilesTab library={library()} />
        </Match>
        <Match when={props.tab === "rules"}>
          <RulesTab library={library()} />
        </Match>
        <Match when={props.tab === "history"}>
          <HistoryTab library={library()} />
        </Match>
        <Match when={props.tab === "settings"}>
          <SettingsTab library={library()} />
        </Match>
      </Switch>
    </>
  );
}
