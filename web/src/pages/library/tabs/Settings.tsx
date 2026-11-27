import { useNavigate } from "@solidjs/router";
import { createSignal, Errored, flush, Loading, Show } from "solid-js";

import { api, createQuery, errorMessage, invalidate, keys, type ApiError, type DownloadOptions, type Library, type LibraryWrite } from "../../../api";
import { FormBar, FormSection } from "../../../features/layout/Form";
import { homeTarget } from "../../../features/libraries/home";
import { replacesInPlace, resumeConsequence, toWrite } from "../../../features/libraries/library";
import { createDraft } from "../../../lib/draft";
import { formatGoDuration, plural } from "../../../lib/format";
import { useUnsavedChanges } from "../../../lib/guard";
import { run } from "../../../lib/mutate";
import { useAppData } from "../../../shell/data";
import { Banner, Button, confirm, Dialog, ErrorState, Field, FolderPicker, Input, LoadingState, NumberInput, Segmented, Select, Switch, toast, TokenInput } from "../../../ui";

/** Everything the Settings tab edits: the library without its identity and its rules. */
type SettingsDraft = Omit<LibraryWrite, "name" | "kind" | "rules" | "rule_fallback">;

function toDraft(library: Library): SettingsDraft {
  const { name: _name, kind: _kind, rules: _rules, rule_fallback: _fallback, ...rest } = toWrite(library);
  return rest;
}

/** The fields this form can show an error on, by the path the daemon reports (webapi.Error.Field). */
const SHOWN_FIELDS = new Set([
  "path",
  "flow_id",
  "paused",
  "priority",
  "concurrency_limit",
  "scan_interval",
  "arr",
  "media.replacement_mode",
  "download.handoff_path",
  "download.stable_for",
  "download.package_mode",
  "download.handoff_mode",
  "download.ignorable_globs",
]);

/** The Settings tab: everything about the library except its rules, and deleting it. */
export function SettingsTab(props: { library: Library }) {
  return (
    <div class="av-page-body av-surface">
      <Errored fallback={(error, reset) => <ErrorState title="The settings could not be loaded" error={error()} onRetry={reset} />}>
        <Loading fallback={<LoadingState label="Loading settings" />}>
          <SettingsForm library={props.library} />
        </Loading>
      </Errored>
    </div>
  );
}

function SettingsForm(props: { library: Library }) {
  const { flows } = useAppData();
  const daemon = createQuery(() => keys.settings, api.settings);
  const arrs = createQuery(
    () => keys.arrs,
    async () => (await api.arrs.list()).arrs,
  );
  const draft = createDraft(() => toDraft(props.library));
  const [errors, setErrors] = createSignal<Record<string, string>>({});
  const [saving, setSaving] = createSignal(false);
  const [browsing, setBrowsing] = createSignal<"path" | "handoff_path" | null>(null);
  const [deleting, setDeleting] = createSignal(false);
  useUnsavedChanges(draft.dirty, { title: "Discard the unsaved settings?", description: "The changes made to this library's settings since the last save are lost." });

  const form = () => draft.value();
  const download = () => form().download;
  const isDownload = () => props.library.kind === "download";

  /** Changes fields and clears the error the daemon reported for them. */
  const set = (change: Partial<SettingsDraft>, ...fields: string[]) => {
    draft.patch(change);
    if (fields.some((field) => errors()[field])) setErrors((current) => Object.fromEntries(Object.entries(current).filter(([field]) => !fields.includes(field))));
  };
  const setDownload = (change: Partial<DownloadOptions>, field: keyof DownloadOptions) => set({ download: { ...download(), ...change } }, `download.${field}`);

  const save = async () => {
    const next: LibraryWrite = { ...toWrite(props.library), ...form() };
    // Saving may be what starts the rewriting: unpausing, or switching a running library to replace.
    const startsReplacing = !next.paused && replacesInPlace(next) && (props.library.paused || !replacesInPlace(props.library));
    if (startsReplacing && !(await confirm({ title: `Save and start processing ${props.library.name}?`, description: resumeConsequence(props.library.stats.pending), confirmLabel: "Save settings", cancelLabel: "Keep editing" }))) return;
    setSaving(true);
    setErrors({});
    const saved = await run(() => api.libraries.update(props.library.name, next), {
      invalidate: [keys.libraries, keys.flows, keys.arrs, keys.status],
      success: `Saved the settings of ${props.library.name}.`,
      failure: "The settings could not be saved.",
      onError: (error: ApiError) => {
        // A list entry ("download.ignorable_globs.2") is reported on its list.
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
        <FormSection title="Library" description="What this library is. Name and kind are fixed when a library is created.">
          <div class="av-grid-2">
            <Field label="Name">
              <p class="av-readonly">{props.library.name}</p>
            </Field>
            <Field label="Kind" hint={isDownload() ? "Finished downloads are encoded and handed to an importer." : "Files are encoded where they are."}>
              <p class="av-readonly">{props.library.kind}</p>
            </Field>
          </div>
          <Field label="Folder" htmlFor="library-path" hint="The folder anvil scans." error={errors().path}>
            <div class="av-field-row">
              <Input id="library-path" mono autocomplete="off" spellcheck={false} invalid={!!errors().path} value={form().path} onInput={(event) => set({ path: event.currentTarget.value }, "path")} />
              <Button icon="folder" onClick={() => setBrowsing("path")}>
                Browse
              </Button>
            </div>
          </Field>
          <Show when={pathChanged()}>
            <Banner tone="warning">Changing the folder makes anvil scan the new location from scratch. The job history of this library keeps pointing at the old paths. No file is moved.</Banner>
          </Show>
        </FormSection>

        <FormSection title="Processing" description="Which flow the files run through, and how this library competes with the others for workers.">
          <Field label="Flow" htmlFor="library-flow" hint="Every file of this library runs through this flow. Queued files use a new flow from their next attempt." error={errors().flow_id}>
            <Select id="library-flow" value={String(form().flow_id)} invalid={!!errors().flow_id} options={flows().map((flow) => ({ value: String(flow.id), label: flow.name }))} onChange={(event) => set({ flow_id: Number(event.currentTarget.value) }, "flow_id")} />
          </Field>
          <Field inline label="Paused" htmlFor="library-paused" hint="A paused library is still scanned and still queues files, but starts no new job." error={errors().paused}>
            <Switch id="library-paused" label="Paused" checked={form().paused} onChange={(paused) => set({ paused }, "paused")} />
          </Field>
          <div class="av-grid-2">
            <Field label="Priority" htmlFor="library-priority" hint="Libraries with a higher number get free workers first. 0 is the default." error={errors().priority}>
              <NumberInput id="library-priority" integer value={form().priority} invalid={!!errors().priority} onChange={(value) => set({ priority: value ?? 0 }, "priority")} />
            </Field>
            <Field label="Concurrency limit" htmlFor="library-concurrency" hint="The most jobs of this library that run at once. 0 means no limit." error={errors().concurrency_limit}>
              <NumberInput id="library-concurrency" integer min={0} suffix="jobs" value={form().concurrency_limit} invalid={!!errors().concurrency_limit} onChange={(value) => set({ concurrency_limit: value ?? 0 }, "concurrency_limit")} />
            </Field>
          </div>
        </FormSection>

        <FormSection title="Scanning" description="anvil scans on a schedule and when the filesystem reports a change. The rules decide which of the files it finds are queued.">
          <Field label="Scan interval" htmlFor="library-scan-interval" hint={`How often the whole library is scanned, as a duration such as 30m or 2h. Empty uses the daemon's default, ${scanDefault()}.`} error={errors().scan_interval}>
            <div class="av-narrow">
              <Input id="library-scan-interval" mono autocomplete="off" spellcheck={false} placeholder={daemon().daemon.scan_interval} invalid={!!errors().scan_interval} value={form().scan_interval} onInput={(event) => set({ scan_interval: event.currentTarget.value.trim() }, "scan_interval")} />
            </div>
          </Field>
          <Field
            label="Arr connection"
            htmlFor="library-arr"
            hint={arrs().length > 0 ? "Used to look up the original language of a film or series, which the orig language token in a flow stands for. None turns the lookup off." : "No Arr connection is configured yet. Add one under Settings to look up original languages."}
            error={errors().arr}
          >
            <Select id="library-arr" value={form().arr} invalid={!!errors().arr} options={[{ value: "", label: "None" }, ...arrs().map((arr) => ({ value: arr.name, label: `${arr.name} (${arr.type})` }))]} onChange={(event) => set({ arr: event.currentTarget.value }, "arr")} />
          </Field>
        </FormSection>

        <Show
          when={isDownload()}
          fallback={
            <FormSection title="Publishing" description="What happens to the original once its encode is ready.">
              <Field
                label="Replacement mode"
                hint={form().media.replacement_mode === "replace" ? "The encode takes the place of the original. The original is gone once the encode is published." : "The encode is published beside the original. The original stays, so the library needs room for both."}
                error={errors()["media.replacement_mode"]}
              >
                <div>
                  <Segmented
                    label="Replacement mode"
                    value={form().media.replacement_mode}
                    onChange={(mode) => set({ media: { replacement_mode: mode as "replace" | "copy" } }, "media.replacement_mode")}
                    options={[
                      { value: "replace", label: "Replace the original" },
                      { value: "copy", label: "Keep the original" },
                    ]}
                  />
                </div>
              </Field>
            </FormSection>
          }
        >
          <FormSection title="Handoff" description="How finished downloads are recognised, and how their encodes reach the importer.">
            <Field label="Handoff folder" htmlFor="library-handoff" hint="The folder your importer (Sonarr, Radarr) watches. Encodes are handed off here." error={errors()["download.handoff_path"]}>
              <div class="av-field-row">
                <Input id="library-handoff" mono autocomplete="off" spellcheck={false} invalid={!!errors()["download.handoff_path"]} value={download().handoff_path} onInput={(event) => setDownload({ handoff_path: event.currentTarget.value }, "handoff_path")} />
                <Button icon="folder" onClick={() => setBrowsing("handoff_path")}>
                  Browse
                </Button>
              </div>
            </Field>
            <div class="av-grid-2">
              <Field label="Stable for" htmlFor="library-stable-for" hint="How long a download must stay unchanged before it counts as complete when no completion event was seen. A duration such as 45s; empty uses the default." error={errors()["download.stable_for"]}>
                <Input id="library-stable-for" mono autocomplete="off" spellcheck={false} invalid={!!errors()["download.stable_for"]} value={download().stable_for} onInput={(event) => setDownload({ stable_for: event.currentTarget.value.trim() }, "stable_for")} />
              </Field>
              <Field label="Package mode" htmlFor="library-package-mode" hint="Whether a download is one file or a whole release folder. Auto decides per download." error={errors()["download.package_mode"]}>
                <Select
                  id="library-package-mode"
                  value={download().package_mode}
                  invalid={!!errors()["download.package_mode"]}
                  options={[
                    { value: "auto", label: "Auto" },
                    { value: "directory", label: "Directory" },
                    { value: "file", label: "File" },
                  ]}
                  onChange={(event) => setDownload({ package_mode: event.currentTarget.value as DownloadOptions["package_mode"] }, "package_mode")}
                />
              </Field>
            </div>
            <Field label="Handoff mode" hint={download().handoff_mode === "move" ? "The encode is moved into the handoff folder. Use this when the downloader no longer needs the download." : "The encode is copied into the handoff folder, for example while the download is still seeding."} error={errors()["download.handoff_mode"]}>
              <div>
                <Segmented
                  label="Handoff mode"
                  value={download().handoff_mode}
                  onChange={(mode) => setDownload({ handoff_mode: mode as DownloadOptions["handoff_mode"] }, "handoff_mode")}
                  options={[
                    { value: "move", label: "Move" },
                    { value: "copy", label: "Copy" },
                  ]}
                />
              </div>
            </Field>
            <Field inline label="Preserve relative path" htmlFor="library-preserve-path" hint="Keeps the download's folder structure below the handoff folder.">
              <Switch id="library-preserve-path" label="Preserve relative path" checked={download().preserve_relative_path} onChange={(value) => setDownload({ preserve_relative_path: value }, "preserve_relative_path")} />
            </Field>
            <Field inline label="Clean up source media" htmlFor="library-cleanup" hint="Deletes the downloaded media after a successful handoff, and only then.">
              <Switch id="library-cleanup" label="Clean up source media" checked={download().cleanup_source_media} onChange={(value) => setDownload({ cleanup_source_media: value }, "cleanup_source_media")} />
            </Field>
            <Field inline label="Prune empty folders" htmlFor="library-prune" hint="Removes release folders the cleanup left empty. It has no effect without source cleanup.">
              <Switch id="library-prune" label="Prune empty folders" checked={download().prune_empty_dirs} onChange={(value) => setDownload({ prune_empty_dirs: value }, "prune_empty_dirs")} />
            </Field>
            <Field label="Ignorable globs" htmlFor="library-globs" hint="Files matching these globs are not media and never hold a download back. With cleanup and pruning on, they may be deleted after a handoff. An empty list uses the built-in one." error={errors()["download.ignorable_globs"]}>
              <TokenInput id="library-globs" mono placeholder="*.nfo" invalid={!!errors()["download.ignorable_globs"]} value={download().ignorable_globs} onChange={(globs) => setDownload({ ignorable_globs: globs }, "ignorable_globs")} />
            </Field>
          </FormSection>
        </Show>

        <FormSection danger title="Delete library" description="anvil forgets this library, its rules and its job history. No media file is deleted or changed.">
          <div>
            <Button variant="danger" icon="trash-2" class="av-btn-danger-outline" onClick={() => setDeleting(true)}>
              Delete library
            </Button>
          </div>
        </FormSection>
      </div>
      <FormBar dirty={draft.dirty()} saving={saving()} saveLabel="Save settings" onSave={save} onDiscard={() => (setErrors({}), draft.reset())} />
      <FolderPicker
        open={browsing() !== null}
        onClose={() => setBrowsing(null)}
        initialPath={(browsing() === "handoff_path" ? download().handoff_path : form().path) || undefined}
        onPick={(path) => {
          if (browsing() === "handoff_path") setDownload({ handoff_path: path }, "handoff_path");
          else set({ path }, "path");
          setBrowsing(null);
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

/** Asks for the library's name before deleting it, and keeps the daemon's refusal on screen. */
function DeleteLibraryDialog(props: { library: Library; onClose: () => void; onDeleted: () => void }) {
  const navigate = useNavigate();
  const { libraries } = useAppData();
  const [typed, setTyped] = createSignal("");
  const [refusal, setRefusal] = createSignal<string | null>(null);
  const [busy, setBusy] = createSignal(false);
  const name = () => props.library.name;
  const stats = () => props.library.stats;

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
      // 409: jobs are active or protected. The daemon says which, so its words stay in the dialog.
      setRefusal(errorMessage(error));
      setBusy(false);
      return;
    }
    props.onDeleted();
    // Leave before the list refetches: the page of a library that is gone has nothing to show.
    if (next) navigate(next, { replace: true });
    await invalidate(keys.libraries, keys.flows, keys.arrs, keys.jobs, keys.status);
    if (!next) navigate("/", { replace: true });
    toast.success(`Deleted the library ${deleted}. Its files were not touched.`);
  };

  return (
    <Dialog
      open
      size="md"
      dismissible={!busy()}
      onClose={props.onClose}
      title={`Delete the library ${name()}?`}
      description={`anvil forgets this library, its rules and the history of its ${plural(stats().files, "file")}. No media file is deleted or changed: everything in ${props.library.path} stays as it is.`}
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
