import { createSignal, Errored, flush, Loading, Show } from "solid-js";

import { api, createQuery, errorMessage, invalidate, keys, type ApiError, type Arr, type Library, type LibraryWrite, type Settings } from "../../../api";
import { FormBar, FormSection } from "../../../features/layout/Form";
import { homeTarget } from "../../../features/libraries/home";
import { flowReplacesInPlace, resumeConsequence, toWrite } from "../../../features/libraries/library";
import { IntakeFields, ReprocessField, type IntakeField } from "../../../features/libraries/LibraryFields";
import { useAppNavigate } from "../../../lib/base";
import { createDraft } from "../../../lib/draft";
import { formatCount, formatGoDuration, plural } from "../../../lib/format";
import { useUnsavedChanges } from "../../../lib/guard";
import { run } from "../../../lib/mutate";
import { useAppData } from "../../../shell/data";
import { Banner, Button, confirm, Dialog, ErrorState, Field, FolderPicker, Input, LoadingState, NumberInput, Select, Switch, toast } from "../../../ui";

/** Everything the Settings tab edits: the library without its identity and its rules. */
type SettingsDraft = Omit<LibraryWrite, "name" | "rules" | "rule_fallback">;

function toDraft(library: Library): SettingsDraft {
  const { name: _name, rules: _rules, rule_fallback: _fallback, ...rest } = toWrite(library);
  return rest;
}

/** The fields this form can show an error on, by the path the daemon reports (webapi.Error.Field). */
const SHOWN_FIELDS = new Set(["path", "flow_id", "paused", "priority", "concurrency_limit", "scan_interval", "arr", "reprocess", "stable_for", "package_mode", "ignorable_globs"]);

const INTAKE_IDS: Record<IntakeField, string> = {
  stable_for: "library-stable-for",
  package_mode: "library-package-mode",
  ignorable_globs: "library-globs",
};

/**
 * The Settings tab: everything about the library except its rules, and
 * deleting it. `library` is undefined until the list of libraries has
 * answered; what the form needs besides it is asked for meanwhile.
 */
export function SettingsTab(props: { library: Library | undefined }) {
  const daemon = createQuery(() => keys.settings, api.settings);
  const arrs = createQuery(
    () => keys.arrs,
    async () => (await api.arrs.list()).arrs,
  );
  return (
    <div class="av-page-body av-surface">
      <Errored fallback={(error, reset) => <ErrorState title="The settings could not be loaded" error={error()} onRetry={reset} />}>
        <Loading fallback={<LoadingState label="Loading settings" />}>
          <Show when={props.library}>{(library) => <SettingsForm library={library()} daemon={daemon()} arrs={arrs()} />}</Show>
        </Loading>
      </Errored>
    </div>
  );
}

function SettingsForm(props: { library: Library; daemon: Settings; arrs: Arr[] }) {
  const { flows } = useAppData();
  const daemon = () => props.daemon;
  const arrs = () => props.arrs;
  const draft = createDraft(() => toDraft(props.library));
  const [errors, setErrors] = createSignal<Record<string, string>>({});
  const [saving, setSaving] = createSignal(false);
  const [browsing, setBrowsing] = createSignal(false);
  const [deleting, setDeleting] = createSignal(false);
  useUnsavedChanges(draft.dirty, { title: "Discard unsaved settings?" });

  const form = () => draft.value();

  /** Changes fields and clears the error the daemon reported for them. */
  const set = (change: Partial<SettingsDraft>, ...fields: string[]) => {
    draft.patch(change);
    if (fields.some((field) => errors()[field])) setErrors((current) => Object.fromEntries(Object.entries(current).filter(([field]) => !fields.includes(field))));
  };

  /**
   * Whether saving is what starts rewriting files in place: unpausing a library
   * whose flow replaces them, or giving a running library such a flow in place
   * of one that does not.
   */
  const startsReplacing = async (next: LibraryWrite): Promise<boolean> => {
    if (next.paused) return false;
    if (!props.library.paused && next.flow_id === props.library.flow_id) return false;
    if (!(await flowReplacesInPlace(next.flow_id))) return false;
    return props.library.paused || !(await flowReplacesInPlace(props.library.flow_id));
  };

  const save = async () => {
    const next: LibraryWrite = { ...toWrite(props.library), ...form() };
    setSaving(true);
    const asks = await startsReplacing(next);
    setSaving(false);
    if (asks && !(await confirm({ title: `Save and start processing ${props.library.name}?`, description: resumeConsequence(props.library.stats.pending), confirmLabel: "Save settings", cancelLabel: "Keep editing" }))) return;
    setSaving(true);
    setErrors({});
    const saved = await run(() => api.libraries.update(props.library.name, next), {
      invalidate: [keys.libraries, keys.flows, keys.arrs, keys.status],
      success: `Saved the settings of ${props.library.name}.`,
      failure: "The settings could not be saved.",
      onError: (error: ApiError) => {
        // A list entry ("ignorable_globs.2") is reported on its list.
        const field = error.field?.replace(/\.\d+$/, "");
        // Anything this form has no field for (the rules, the name) goes to the toast.
        if (!field || !SHOWN_FIELDS.has(field)) return false;
        setErrors({ [field]: error.message });
        return true;
      },
    });
    setSaving(false);
    if (saved) draft.reset();
  };

  const scanDefault = () => formatGoDuration(daemon().daemon.scan_interval) || daemon().daemon.scan_interval;
  const pathChanged = () => form().path !== props.library.path;

  return (
    <>
      <div class="av-form">
        <FormSection title="Library" description="The name cannot be changed.">
          <Field label="Name">
            <p class="av-readonly">{props.library.name}</p>
          </Field>
          <Field label="Folder" htmlFor="library-path" error={errors().path}>
            <div class="av-field-row">
              <Input id="library-path" mono autocomplete="off" spellcheck={false} invalid={!!errors().path} value={form().path} onInput={(event) => set({ path: event.currentTarget.value }, "path")} />
              <Button icon="folder" onClick={() => setBrowsing(true)}>
                Browse
              </Button>
            </div>
          </Field>
          <Show when={pathChanged()}>
            <Banner tone="warning">The new folder is scanned from scratch. The job history keeps the old paths. No file is moved.</Banner>
          </Show>
          <ReprocessField id="library-reprocess" checked={form().reprocess} error={errors().reprocess} onChange={(reprocess) => set({ reprocess }, "reprocess")} />
        </FormSection>

        <FormSection title="Processing">
          <Field label="Flow" htmlFor="library-flow" hint="Queued files use a new flow from their next attempt. The flow also decides where results go." error={errors().flow_id}>
            <Select id="library-flow" value={String(form().flow_id)} invalid={!!errors().flow_id} options={flows().map((flow) => ({ value: String(flow.id), label: flow.name }))} onChange={(event) => set({ flow_id: Number(event.currentTarget.value) }, "flow_id")} />
          </Field>
          <Field inline label="Paused" htmlFor="library-paused" hint="Scans still queue files, but no new job starts." error={errors().paused}>
            <Switch id="library-paused" label="Paused" checked={form().paused} onChange={(paused) => set({ paused }, "paused")} />
          </Field>
          <div class="av-grid-2">
            <Field label="Priority" htmlFor="library-priority" hint="Higher numbers get free workers first." error={errors().priority}>
              <NumberInput id="library-priority" integer value={form().priority} invalid={!!errors().priority} onChange={(value) => set({ priority: value ?? 0 }, "priority")} />
            </Field>
            <Field label="Concurrency limit" htmlFor="library-concurrency" hint="0 means no limit." error={errors().concurrency_limit}>
              <NumberInput id="library-concurrency" integer min={0} suffix="jobs" value={form().concurrency_limit} invalid={!!errors().concurrency_limit} onChange={(value) => set({ concurrency_limit: value ?? 0 }, "concurrency_limit")} />
            </Field>
          </div>
        </FormSection>

        <FormSection title="Scanning" description="Changes on disk are also picked up as they happen.">
          <Field label="Scan interval" htmlFor="library-scan-interval" hint={`Such as 30m or 2h. Empty uses the default, ${scanDefault()}.`} error={errors().scan_interval}>
            <div class="av-narrow">
              <Input id="library-scan-interval" mono autocomplete="off" spellcheck={false} placeholder={daemon().daemon.scan_interval} invalid={!!errors().scan_interval} value={form().scan_interval} onInput={(event) => set({ scan_interval: event.currentTarget.value.trim() }, "scan_interval")} />
            </div>
          </Field>
          <Field
            label="Arr connection"
            htmlFor="library-arr"
            hint={arrs().length > 0 ? "Looks up the original language for the orig token in flows." : "No connections. Add one in Settings."}
            error={errors().arr}
          >
            <Select id="library-arr" value={form().arr} invalid={!!errors().arr} options={[{ value: "", label: "None" }, ...arrs().map((arr) => ({ value: arr.name, label: `${arr.name} (${arr.type})` }))]} onChange={(event) => set({ arr: event.currentTarget.value }, "arr")} />
          </Field>
        </FormSection>

        <FormSection title="Intake" description="For folders a downloader writes into. The defaults suit a media library.">
          <IntakeFields ids={INTAKE_IDS} value={form()} errors={errors()} onChange={(change, field) => set(change, field)} />
        </FormSection>

        <FormSection danger title="Delete library" description="Removes the library, its queue and its history. No media file is deleted or changed.">
          <div>
            <Button variant="danger" icon="trash-2" class="av-btn-danger-outline" onClick={() => setDeleting(true)}>
              Delete library
            </Button>
          </div>
        </FormSection>
      </div>
      <FormBar dirty={draft.dirty()} saving={saving()} saveLabel="Save settings" onSave={save} onDiscard={() => (setErrors({}), draft.reset())} />
      <FolderPicker
        open={browsing()}
        onClose={() => setBrowsing(false)}
        initialPath={form().path || undefined}
        onPick={(path) => {
          set({ path }, "path");
          setBrowsing(false);
        }}
      />
      <Show when={deleting()}>
        <DeleteLibraryDialog
          library={props.library}
          onClose={() => setDeleting(false)}
          onDeleted={() => {
            // The unsaved-changes guard must not ask about a draft of a library that is gone.
            draft.reset();
            flush();
          }}
        />
      </Show>
    </>
  );
}

/**
 * What deleting a library does, as the daemon does it (pkg/store DeleteLibrary):
 * waiting jobs go with the library, and only a running job or an unfinished
 * publish refuses the delete.
 */
function deleteConsequence(library: Library): string {
  const { files, pending, running } = library.stats;
  const queue = pending > 0 ? ` ${pending === 1 ? "1 queued job is dropped." : `${formatCount(pending)} queued jobs are dropped.`}` : "";
  const refused = running > 0 ? ` ${running === 1 ? "1 job is running. Cancel it or wait for it first." : `${formatCount(running)} jobs are running. Cancel them or wait for them first.`}` : "";
  return `Removes the library, its rules and the job history of its ${plural(files, "file")}.${queue} No media file in ${library.path} is deleted or changed.${refused}`;
}

/** Asks for the library's name before deleting it, and keeps the daemon's refusal on screen. */
function DeleteLibraryDialog(props: { library: Library; onClose: () => void; onDeleted: () => void }) {
  const navigate = useAppNavigate();
  const { libraries } = useAppData();
  const [typed, setTyped] = createSignal("");
  const [refusal, setRefusal] = createSignal<string | null>(null);
  const [busy, setBusy] = createSignal(false);
  const name = () => props.library.name;

  const remove = async (event: SubmitEvent) => {
    event.preventDefault();
    if (typed() !== name() || busy()) return;
    setBusy(true);
    setRefusal(null);
    const deleted = name();
    // Decided before the list changes: "/" would still see the deleted library and lead back here.
    const next = homeTarget(libraries().filter((library) => library.name !== deleted));
    try {
      await api.libraries.remove(deleted);
    } catch (error) {
      // 409: a job is running or has not finished publishing. The daemon says which, so its words stay in the dialog.
      setRefusal(errorMessage(error));
      setBusy(false);
      return;
    }
    props.onDeleted();
    // Leave before the list refetches: the page of a library that is gone has nothing to show.
    if (next) navigate(next, { replace: true });
    await invalidate(keys.libraries, keys.flows, keys.arrs, keys.jobs, keys.status);
    if (!next) navigate("/", { replace: true });
    toast.success(`Deleted ${deleted}. No media file was touched.`);
  };

  return (
    <Dialog
      open
      size="md"
      dismissible={!busy()}
      onClose={props.onClose}
      title={`Delete ${name()}?`}
      description={deleteConsequence(props.library)}
      footer={
        <>
          <Button onClick={props.onClose} disabled={busy()}>
            Keep library
          </Button>
          <Button type="submit" form="delete-library" class="av-btn-danger-outline" variant="danger" busy={busy()} disabled={typed() !== name()}>
            Delete library
          </Button>
        </>
      }
    >
      <form id="delete-library" class="av-stack" novalidate onSubmit={remove}>
        <Show when={refusal()}>{(message) => <Banner tone="danger">{message()}</Banner>}</Show>
        <Field label={`Type ${name()} to confirm`} htmlFor="delete-library-name">
          <Input id="delete-library-name" mono autocomplete="off" spellcheck={false} value={typed()} onInput={(event) => setTyped(event.currentTarget.value)} />
        </Field>
      </form>
    </Dialog>
  );
}
