import { createSignal, createStore, Show } from "solid-js";

import { api, keys, type ApiError, type LibraryWrite } from "../api";
import { scanLibrary } from "../features/libraries/library";
import { IntakeFields, ReprocessField, type IntakeField, type IntakeValues } from "../features/libraries/LibraryFields";
import { useAppNavigate } from "../lib/base";
import { run } from "../lib/mutate";
import { Banner, Button, Dialog, Field, FolderPicker, Input, Select, Switch } from "../ui";
import { useAppData } from "./data";

const [open, setOpen] = createSignal(false);

/** Opens the Add library dialog. The app shell hosts the dialog itself. */
export function openAddLibrary(): void {
  setOpen(true);
}

// webapi.LibraryWrite.Name: 1-64 characters of a-z, 0-9, "-", and "_".
const NAME = /^[a-z0-9_-]{1,64}$/;

interface Draft extends IntakeValues {
  name: string;
  path: string;
  /** "" until the person chooses; the first flow is used then. */
  flow_id: string;
  reprocess: boolean;
  /** Off by default: a new library starts paused, so nothing is rewritten before the operator has looked at it. */
  start: boolean;
}

// The daemon's defaults: a stable_for of "" is 5m, and an empty glob list is the built-in one.
const EMPTY: Draft = { name: "", path: "", flow_id: "", reprocess: false, stable_for: "", package_mode: "file", ignorable_globs: [], start: false };

const START_HINT = {
  off: "Starts paused. Files are queued, but nothing changes until you resume.",
  on: "Processing starts after the first scan.",
};

/** What a new library starts with beyond the form's fields. Everything else is edited on its Settings tab. */
function toWrite(draft: Draft, flowId: number): LibraryWrite {
  return {
    name: draft.name,
    path: draft.path,
    flow_id: flowId,
    paused: !draft.start,
    priority: 0,
    concurrency_limit: 0,
    scan_interval: "",
    arr: "",
    rules: [],
    rule_fallback: "allow",
    reprocess: draft.reprocess,
    stable_for: draft.stable_for,
    package_mode: draft.package_mode,
    ignorable_globs: [...draft.ignorable_globs],
  };
}

export function AddLibraryDialog() {
  return (
    <Show when={open()}>
      <AddLibraryForm />
    </Show>
  );
}

function AddLibraryForm() {
  const { flows } = useAppData();
  const navigate = useAppNavigate();
  const [draft, setDraft] = createStore<Draft>({ ...EMPTY, ignorable_globs: [] });
  const [errors, setErrors] = createStore<Record<string, string | undefined>>({});
  const [general, setGeneral] = createSignal<string | null>(null);
  const [browsing, setBrowsing] = createSignal(false);
  const [intakeOpen, setIntakeOpen] = createSignal(false);
  const [saving, setSaving] = createSignal(false);

  const flowId = () => Number(draft.flow_id) || flows()[0]?.id;
  const edit = (field: string, change: (draft: Draft) => void) => {
    setDraft(change);
    setErrors((current) => void (current[field] = undefined));
    setGeneral(null);
  };

  /** Checks what the browser can check; the daemon has the last word. */
  const validate = (): boolean => {
    const next: Record<string, string | undefined> = {
      name: NAME.test(draft.name) ? undefined : draft.name === "" ? "Give the library a name." : 'Must be 1 to 64 characters of a-z, 0-9, - and _.',
      path: draft.path.startsWith("/") ? undefined : "Must be an absolute path.",
      flow_id: flowId() === undefined ? "Create a flow under Flows first." : undefined,
    };
    setErrors(() => next);
    return Object.values(next).every((message) => message === undefined);
  };

  const submit = async (event: SubmitEvent) => {
    event.preventDefault();
    const flow = flowId();
    if (saving() || !validate() || flow === undefined) return;
    setSaving(true);
    setGeneral(null);
    const library = await run(() => api.libraries.create(toWrite(draft, flow)), {
      invalidate: [keys.libraries, keys.flows],
      success: (created) => `Added ${created.name}${created.paused ? ", paused" : ""}. Scanning now.`,
      onError: (error: ApiError) => {
        // A field the form shows gets the message; a list entry ("ignorable_globs.2") is shown on its list.
        // Anything else stays in the dialog, above the fields.
        const field = error.field?.replace(/\.\d+$/, "");
        if (field && field in FIELD_IDS) {
          setErrors((current) => void (current[field] = error.message));
          if (INTAKE.includes(field as IntakeField)) setIntakeOpen(true);
        } else setGeneral(error.message);
        return true;
      },
    });
    setSaving(false);
    if (!library) return;
    setOpen(false);
    navigate(`/libraries/${encodeURIComponent(library.name)}`);
    // The first scan runs by itself: a library without files has nothing to show. Its result arrives as its own toast.
    void scanLibrary(library.name);
  };

  return (
    <Dialog
      open
      dismissible={!saving()}
      onClose={() => setOpen(false)}
      title="Add library"
      footer={
        <>
          <Button disabled={saving()} onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" form="add-library" busy={saving()}>
            Add library
          </Button>
        </>
      }
    >
      <form id="add-library" class="av-stack" novalidate onSubmit={submit}>
        <Show when={general()}>{(message) => <Banner tone="danger">{message()}</Banner>}</Show>
        <Field label="Name" htmlFor={FIELD_IDS.name} hint="a-z, 0-9, - and _. Cannot be changed later." error={errors.name}>
          <Input id={FIELD_IDS.name} mono autocomplete="off" spellcheck={false} placeholder="movies" invalid={!!errors.name} value={draft.name} onInput={(event) => edit("name", (d) => void (d.name = event.currentTarget.value))} />
        </Field>
        <Field label="Folder" htmlFor={FIELD_IDS.path} error={errors.path}>
          <div class="av-row av-row-nowrap">
            <Input id={FIELD_IDS.path} mono autocomplete="off" spellcheck={false} placeholder="/media/movies" invalid={!!errors.path} value={draft.path} onInput={(event) => edit("path", (d) => void (d.path = event.currentTarget.value))} />
            <Button icon="folder" onClick={() => setBrowsing(true)}>
              Browse
            </Button>
          </div>
        </Field>
        <Field label="Flow" htmlFor={FIELD_IDS.flow_id} hint="Also decides whether each result replaces its file or moves to another folder." error={errors.flow_id}>
          <Select id={FIELD_IDS.flow_id} value={String(flowId() ?? "")} invalid={!!errors.flow_id} options={flows().map((flow) => ({ value: String(flow.id), label: flow.name }))} onChange={(event) => edit("flow_id", (d) => void (d.flow_id = event.currentTarget.value))} />
        </Field>
        <ReprocessField id={FIELD_IDS.reprocess} checked={draft.reprocess} error={errors.reprocess} onChange={(reprocess) => edit("reprocess", (d) => void (d.reprocess = reprocess))} />
        <Field inline label="Start processing right away" htmlFor="add-library-start" hint={START_HINT[draft.start ? "on" : "off"]}>
          <Switch id="add-library-start" label="Start processing right away" checked={draft.start} onChange={(start) => edit("paused", (d) => void (d.start = start))} />
        </Field>
        <div>
          <Button variant="ghost" size="sm" iconRight={intakeOpen() ? "chevron-up" : "chevron-down"} aria-expanded={intakeOpen() ? "true" : "false"} aria-controls="add-library-intake" onClick={() => setIntakeOpen(!intakeOpen())}>
            Intake settings
          </Button>
        </div>
        <Show when={intakeOpen()}>
          <div id="add-library-intake" class="av-stack">
            <IntakeFields
              ids={FIELD_IDS}
              value={draft}
              errors={errors}
              onChange={(change, field) =>
                edit(field, (d) => {
                  Object.assign(d, change);
                })
              }
            />
          </div>
        </Show>
      </form>
      <FolderPicker
        open={browsing()}
        onClose={() => setBrowsing(false)}
        initialPath={draft.path || undefined}
        onPick={(path) => {
          edit("path", (d) => void (d.path = path));
          setBrowsing(false);
        }}
      />
    </Dialog>
  );
}

const INTAKE: readonly IntakeField[] = ["stable_for", "package_mode", "ignorable_globs"];

// The request fields this form has a control for, and the id of each control.
const FIELD_IDS = {
  name: "add-library-name",
  path: "add-library-path",
  flow_id: "add-library-flow",
  reprocess: "add-library-reprocess",
  stable_for: "add-library-stable-for",
  package_mode: "add-library-package-mode",
  ignorable_globs: "add-library-globs",
} as const;
