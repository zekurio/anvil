import { createSignal, Errored, For, Loading, Match, Show, Switch } from "solid-js";

import { api, createQuery, keys, type ApiError, type Arr, type DaemonKeyName, type DaemonSettings, type DaemonSettingsWrite, type Settings } from "../../api";
import { FormBar, FormSection } from "../../features/layout/Form";
import { createDraft } from "../../lib/draft";
import { formatCount } from "../../lib/format";
import { useUnsavedChanges } from "../../lib/guard";
import { run } from "../../lib/mutate";
import { setTheme, theme, type Theme } from "../../lib/theme";
import { useTitle } from "../../lib/title";
import { useAppData } from "../../shell/data";
import { signOut } from "../../shell/session";
import { Banner, Button, confirm, DataTable, EmptyState, ErrorState, Field, Input, LoadingRows, LoadingState, Menu, NumberInput, Segmented, Select, StatusBadge, Topbar, type DataTableColumn } from "../../ui";
import { ArrDialog } from "./ArrDialog";

import "./settings.css";

// The [daemon] keys only anvil.toml sets, in the order of pkg/config. The hint shows on hover only.
const FILE_KEYS: { key: "media_roots" | "temp_dir" | "store_path" | "control_socket"; hint: string }[] = [
  { key: "media_roots", hint: "Folders the web UI may browse and use for libraries." },
  { key: "temp_dir", hint: "Search samples and process logs." },
  { key: "store_path", hint: "Libraries, flows and job history." },
  { key: "control_socket", hint: "Where anvilctl connects." },
];

// The [daemon] keys the daemon lets the web UI change, in the order of pkg/config. Labels are anvil.toml's key names.
const DAEMON_KEYS: { key: DaemonKeyName; hint?: string }[] = [
  { key: "worker_count", hint: "Jobs that run at once." },
  { key: "total_threads", hint: "Shared by all jobs." },
  { key: "max_threads_per_job", hint: "0 splits total_threads." },
  { key: "max_attempts", hint: "Before a job stays failed." },
  { key: "scan_interval", hint: "Unless a library sets one." },
  { key: "filesystem_event_debounce", hint: "Quiet time before a scan." },
  { key: "scheduler_interval", hint: "How often the queue is planned." },
  { key: "lease_duration", hint: "Silence before a job is abandoned." },
  { key: "shutdown_policy", hint: "drain lets running jobs finish." },
  { key: "shutdown_timeout", hint: "0s waits for ever." },
  { key: "staging_cleanup_age", hint: "0s keeps leftover scratch." },
  { key: "log_level" },
];

/** What the form edits: the value saved in the web UI for each editable key, null for its default. */
type DaemonDraft = Partial<Record<DaemonKeyName, string | number | null>>;

function toDraft(daemon: DaemonSettings): DaemonDraft {
  const draft: DaemonDraft = {};
  for (const { key } of DAEMON_KEYS) {
    const described = daemon.keys[key];
    if (described.editable) draft[key] = described.source === "store" ? daemon[key] : null;
  }
  return draft;
}

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

/** Settings: what the daemon runs with, who may reach the web UI, Arr connections, the theme, and version facts. */
export default function SettingsPage() {
  const settings = createQuery(() => keys.settings, api.settings);
  useTitle(() => "Settings");
  return (
    <>
      <Topbar title="Settings" />
      <div class="av-page-body av-surface">
        <Errored fallback={(error, reset) => <ErrorState title="The daemon settings could not be loaded" error={error()} onRetry={reset} />}>
          <Loading fallback={<LoadingState label="Loading settings" />}>
            <SettingsForm settings={settings()} />
          </Loading>
        </Errored>
      </div>
    </>
  );
}

/** Every section of the page, and the bar that saves the daemon keys. */
function SettingsForm(props: { settings: Settings }) {
  const draft = createDraft(() => toDraft(props.settings.daemon));
  const [errors, setErrors] = createSignal<Partial<Record<DaemonKeyName, string>>>({});
  const [saving, setSaving] = createSignal(false);
  useUnsavedChanges(draft.dirty, { title: "Discard the unsaved daemon settings?", description: "The changes made to the daemon settings since the last save are lost." });

  /** Changes one key and clears the error the daemon reported for it. */
  const set = (key: DaemonKeyName, value: string | number | null) => {
    draft.patch({ [key]: value });
    if (errors()[key]) setErrors((current) => ({ ...current, [key]: undefined }));
  };

  const save = async () => {
    setSaving(true);
    setErrors({});
    const saved = await run(() => api.updateDaemonSettings(draft.value() as DaemonSettingsWrite), {
      invalidate: [keys.settings, keys.status],
      success: "Saved the daemon settings.",
      failure: "The daemon settings could not be saved.",
      onError: (error: ApiError) => {
        const field = DAEMON_KEYS.find((entry) => entry.key === error.field)?.key;
        if (!field) return false;
        setErrors({ [field]: error.message });
        return true;
      },
    });
    setSaving(false);
    if (saved) draft.reset();
  };

  return (
    <>
      <div class="av-form">
        <FormSection title="Daemon" description="An empty key uses its default.">
          <div class="av-grid-2">
            <For each={DAEMON_KEYS} keyed={(entry) => entry.key}>
              {(entry) => <DaemonField daemon={props.settings.daemon} name={entry().key} hint={entry().hint} value={draft.value()[entry().key] ?? null} error={errors()[entry().key]} onChange={(value) => set(entry().key, value)} />}
            </For>
          </div>
        </FormSection>
        <LocationSection settings={props.settings} />
        <WebSection settings={props.settings} />
        <ArrSection />
        <AppearanceSection />
        <AboutSection />
      </div>
      <FormBar dirty={draft.dirty()} saving={saving()} saveLabel="Save settings" onSave={save} onDiscard={() => (setErrors({}), draft.reset())} />
    </>
  );
}

/* ---------- daemon ---------- */

/**
 * One key of the Daemon section. A key the file or the command line sets is
 * shown, not edited. An editable key left empty is saved as null: its
 * default, which the placeholder shows.
 */
function DaemonField(props: { daemon: DaemonSettings; name: DaemonKeyName; hint?: string; value: string | number | null; error?: string; onChange: (value: string | number | null) => void }) {
  const described = () => props.daemon.keys[props.name];
  const fallback = () => String(props.daemon.defaults[props.name]);
  const id = () => `daemon-${props.name}`;
  const hint = () => [props.hint, described().at_start ? "Applies at the next start." : ""].filter(Boolean).join(" ") || undefined;
  return (
    <Show
      when={described().editable}
      fallback={
        <Field label={props.name} hint={described().source === "flag" ? "Set on the anvild command line." : "Set in anvil.toml."}>
          <p class="av-readonly">{String(props.daemon[props.name])}</p>
        </Field>
      }
    >
      <Field label={props.name} htmlFor={id()} hint={hint()} error={props.error}>
        <Switch
          fallback={
            <Input
              id={id()}
              mono
              autocomplete="off"
              spellcheck={false}
              placeholder={fallback()}
              invalid={!!props.error}
              value={typeof props.value === "string" ? props.value : ""}
              onInput={(event) => props.onChange(event.currentTarget.value.trim() || null)}
            />
          }
        >
          <Match when={typeof props.daemon.defaults[props.name] === "number"}>
            <NumberInput id={id()} integer placeholder={fallback()} invalid={!!props.error} value={typeof props.value === "number" ? props.value : null} onChange={(value) => props.onChange(value)} />
          </Match>
          <Match when={described().choices.length > 0}>
            <Select
              id={id()}
              invalid={!!props.error}
              value={typeof props.value === "string" ? props.value : ""}
              options={[{ value: "", label: `${fallback()} (default)` }, ...described().choices]}
              onChange={(event) => props.onChange(event.currentTarget.value || null)}
            />
          </Match>
        </Switch>
      </Field>
    </Show>
  );
}

/** The keys only anvil.toml changes. */
function LocationSection(props: { settings: Settings }) {
  const daemon = () => props.settings.daemon;
  return (
    <FormSection title="Locations" description="Set in anvil.toml.">
      <dl class="av-dl av-dl-keys">
        <dt>config file</dt>
        <dd class="av-mono">{props.settings.config_path}</dd>
        <For each={FILE_KEYS} keyed={(entry) => entry.key}>
          {(entry) => (
            <>
              <dt title={entry().hint}>{entry().key}</dt>
              <dd class="av-mono">{entry().key === "media_roots" ? (daemon().media_roots.length > 0 ? daemon().media_roots.join(", ") : "unrestricted") : daemon()[entry().key]}</dd>
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
