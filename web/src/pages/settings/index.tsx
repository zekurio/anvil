import { createSignal, Errored, For, Loading, Show } from "solid-js";

import { api, createQuery, keys, type Arr, type DaemonSettings, type Settings } from "../../api";
import { FormSection } from "../../features/layout/Form";
import { formatCount } from "../../lib/format";
import { run } from "../../lib/mutate";
import { setTheme, theme, type Theme } from "../../lib/theme";
import { useTitle } from "../../lib/title";
import { useAppData } from "../../shell/data";
import { signOut } from "../../shell/session";
import { Banner, Button, confirm, DataTable, EmptyState, ErrorState, LoadingRows, LoadingState, Menu, Segmented, StatusBadge, Topbar, type DataTableColumn } from "../../ui";
import { ArrDialog } from "./ArrDialog";

import "./settings.css";

// The [daemon] table in the order of pkg/config. The hint shows on hover only:
// the key names are anvil.toml's own, and the reference file explains each.
const DAEMON_KEYS: { key: keyof DaemonSettings; hint: string }[] = [
  { key: "media_roots", hint: "Folders the web UI may browse and use for libraries." },
  { key: "temp_dir", hint: "Search samples and process logs." },
  { key: "store_path", hint: "Libraries, flows and job history." },
  { key: "control_socket", hint: "Where anvilctl connects." },
  { key: "worker_count", hint: "Jobs that run at once." },
  { key: "total_threads", hint: "Encode threads shared by all jobs." },
  { key: "max_threads_per_job", hint: "0 splits total_threads across the workers." },
  { key: "max_attempts", hint: "Attempts before a job stays failed." },
  { key: "scan_interval", hint: "Full rescan, unless a library sets its own." },
  { key: "lease_duration", hint: "Silence before a job counts as abandoned." },
  { key: "shutdown_policy", hint: "drain finishes running jobs, cancel stops them." },
  { key: "staging_cleanup_age", hint: "Age at which leftover scratch is removed. 0s keeps it." },
  { key: "log_level", hint: "debug, info, warn or error." },
];

/** True when only this machine can reach the listen address. An empty host (":8484") listens on every interface. */
function isLoopback(listen: string): boolean {
  const host = listen.slice(0, Math.max(0, listen.lastIndexOf(":"))).replace(/^\[|\]$/g, "");
  return host === "localhost" || host === "::1" || host.startsWith("127.");
}

/** "3 d 1 h", "42 min": how long the daemon has been up. */
function formatUptime(startedAt: string, now: string): string {
  const minutes = Math.max(0, Math.floor((Date.parse(now) - Date.parse(startedAt)) / 60_000));
  if (!Number.isFinite(minutes)) return "unknown";
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  if (days > 0) return `${formatCount(days)} d ${hours} h`;
  if (hours > 0) return `${hours} h ${minutes % 60} min`;
  return `${minutes} min`;
}

/** Settings: what the daemon runs with (read-only), who may reach the web UI, Arr connections, the theme, and version facts. */
export default function SettingsPage() {
  const settings = createQuery(() => keys.settings, api.settings);
  useTitle(() => "Settings");
  return (
    <>
      <Topbar title="Settings" />
      <div class="av-page-body av-surface">
        <div class="av-form">
          <Errored fallback={(error, reset) => <ErrorState title="The daemon settings could not be loaded" error={error()} onRetry={reset} />}>
            <Loading fallback={<LoadingState label="Loading settings" />}>
              <DaemonSection settings={settings()} />
              <WebSection settings={settings()} />
            </Loading>
          </Errored>
          <ArrSection />
          <AppearanceSection />
          <AboutSection />
        </div>
      </div>
    </>
  );
}

/* ---------- daemon ---------- */

function DaemonSection(props: { settings: Settings }) {
  return (
    <FormSection title="Daemon" description="Read-only. Edit anvil.toml and reload the daemon to change these.">
      <dl class="av-dl av-dl-keys">
        <dt>config file</dt>
        <dd class="av-mono">{props.settings.config_path}</dd>
        <For each={DAEMON_KEYS} keyed={(entry) => entry.key}>
          {(entry) => (
            <>
              <dt title={entry().hint}>{entry().key}</dt>
              <dd class="av-mono">{Array.isArray(props.settings.daemon[entry().key]) ? (props.settings.daemon.media_roots.length > 0 ? props.settings.daemon.media_roots.join(", ") : "unrestricted") : String(props.settings.daemon[entry().key])}</dd>
            </>
          )}
        </For>
      </dl>
    </FormSection>
  );
}

/* ---------- web access ---------- */

function WebSection(props: { settings: Settings }) {
  const { session } = useAppData();
  const [busy, setBusy] = createSignal(false);
  const web = () => props.settings.web;
  const open = () => !web().auth_required && !isLoopback(web().listen);

  const leave = async () => {
    setBusy(true);
    await signOut();
    setBusy(false);
  };

  return (
    <FormSection title="Web access">
      <Show when={open()}>
        <Banner tone="warning">Anyone on your network can reach {web().listen} without a password and replace files. Set a password or listen on 127.0.0.1.</Banner>
      </Show>
      <dl class="av-dl av-dl-keys">
        <dt>listen</dt>
        <dd>
          <span class="av-mono">{web().listen}</span> <span class="av-cell-muted">{isLoopback(web().listen) ? "this machine only" : "reachable from the network"}</span>
        </dd>
        <dt>password</dt>
        <dd>{web().auth_required ? "On" : "Off"}</dd>
      </dl>
      <Show when={session().auth_required && session().authenticated}>
        <div>
          <Button icon="log-out" busy={busy()} onClick={leave}>
            Sign out
          </Button>
        </div>
      </Show>
    </FormSection>
  );
}

/* ---------- arr connections ---------- */

function ArrSection() {
  const arrs = createQuery(
    () => keys.arrs,
    async () => (await api.arrs.list()).arrs,
  );
  // null: closed. An object: the dialog, adding or editing `arr`.
  const [dialog, setDialog] = createSignal<{ arr?: Arr } | null>(null);
  // The name of the connection a delete is in flight for: its row offers no second one.
  const [removing, setRemoving] = createSignal<string | null>(null);

  const remove = async (arr: Arr) => {
    const description = arr.libraries.length > 0 ? `${arr.libraries.join(", ")} still use it. Assign them another connection first.` : "No library uses it.";
    if (!(await confirm({ title: `Delete ${arr.name}?`, description, confirmLabel: "Delete connection", danger: true }))) return;
    setRemoving(arr.name);
    // 409 when a library still uses it: the daemon's message names the libraries.
    await run(() => api.arrs.remove(arr.name), { invalidate: [keys.arrs], success: `Deleted the connection ${arr.name}.`, failure: `The connection ${arr.name} was not deleted.` });
    setRemoving(null);
  };

  const columns: DataTableColumn<Arr>[] = [
    { key: "name", label: "Name", mono: true, width: 112, sortable: false },
    { key: "type", label: "Type", width: 80, sortable: false },
    { key: "base_url", label: "Base URL", mono: true, sortable: false },
    { key: "key", label: "API key", width: 192, sortable: false, render: (arr) => <KeyCell arr={arr} /> },
    { key: "libraries", label: "Used by", width: 160, sortable: false, render: (arr) => (arr.libraries.length > 0 ? arr.libraries.join(", ") : <span class="av-cell-muted">No library</span>) },
    {
      key: "actions",
      label: "",
      width: 56,
      sortable: false,
      render: (arr) => (
        // A press on the menu must not also open the row.
        <span class="av-row-actions" onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
          <Menu
            size="sm"
            label={`Actions for ${arr.name}`}
            items={[
              { id: "edit", label: "Edit connection", icon: "pencil", disabled: removing() === arr.name, onSelect: () => setDialog({ arr }) },
              { separator: true },
              { id: "delete", label: removing() === arr.name ? "Deleting" : "Delete connection", icon: "trash-2", danger: true, disabled: removing() === arr.name, onSelect: () => void remove(arr) },
            ]}
          />
        </span>
      ),
    },
  ];

  return (
    <FormSection wide title="Arr connections" description="Radarr and Sonarr, for looking up a title's original language.">
      <Errored fallback={(error, reset) => <ErrorState title="The Arr connections could not be loaded" error={error()} onRetry={reset} />}>
        <Loading fallback={<LoadingRows rows={2} label="Loading Arr connections" />}>
          <div class="av-boxed">
            <DataTable
              label="Arr connections"
              columns={columns}
              rows={arrs()}
              rowId={(arr) => arr.name}
              onRowClick={(arr) => setDialog({ arr })}
              empty={<EmptyState title="No connections" />}
            />
          </div>
        </Loading>
      </Errored>
      <div>
        <Button icon="plus" onClick={() => setDialog({})}>
          Add connection
        </Button>
      </div>
      <Show when={dialog()} keyed>
        {(state) => <ArrDialog arr={state.arr} onClose={() => setDialog(null)} />}
      </Show>
    </FormSection>
  );
}

/** Where the key comes from. A connection with neither a stored key nor a key file cannot authenticate. */
function KeyCell(props: { arr: Arr }) {
  return (
    <Show when={props.arr.api_key_set || props.arr.api_key_file !== ""} fallback={<StatusBadge status="warning">Missing</StatusBadge>}>
      {props.arr.api_key_set ? "stored" : <span title={props.arr.api_key_file}>from file {props.arr.api_key_file}</span>}
    </Show>
  );
}

/* ---------- appearance ---------- */

function AppearanceSection() {
  return (
    <FormSection title="Appearance" description="Saved in this browser.">
      <div>
        <Segmented
          label="Theme"
          value={theme()}
          onChange={(value) => setTheme(value as Theme)}
          options={[
            { value: "dark", label: "Dark", icon: "moon" },
            { value: "light", label: "Light", icon: "sun" },
          ]}
        />
      </div>
    </FormSection>
  );
}

/* ---------- about ---------- */

function AboutSection() {
  const { status } = useAppData();
  const version = () => (/^\d/.test(status().version) ? `v${status().version}` : status().version);
  return (
    <FormSection title="About">
      <dl class="av-dl av-dl-keys">
        <dt>version</dt>
        <dd class="av-mono">{version()}</dd>
        <dt>uptime</dt>
        <dd class="av-mono">{formatUptime(status().started_at, status().server_time)}</dd>
        <dt>workers</dt>
        <dd class="av-mono">
          {formatCount(status().workers.active)} of {formatCount(status().workers.configured)} busy
        </dd>
      </dl>
    </FormSection>
  );
}
