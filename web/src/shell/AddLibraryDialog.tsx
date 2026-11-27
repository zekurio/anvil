import { useNavigate } from "@solidjs/router";
import { createSignal, createStore, Show } from "solid-js";

import { api, keys, type ApiError, type LibraryKind, type LibraryWrite } from "../api";
import { scanLibrary } from "../features/libraries/library";
import { run } from "../lib/mutate";
import { Banner, Button, Dialog, Field, FolderPicker, Input, Segmented, Select, Switch } from "../ui";
import { useAppData } from "./data";

const [open, setOpen] = createSignal(false);

/** Opens the Add library dialog. The app shell hosts the dialog itself. */
export function openAddLibrary(): void {
  setOpen(true);
}

// webapi.LibraryWrite.Name: 1-64 characters of a-z, 0-9, "-", and "_".
const NAME = /^[a-z0-9_-]{1,64}$/;

interface Draft {
  name: string;
  kind: LibraryKind;
  path: string;
  handoff_path: string;
  /** "" until the person chooses; the first flow is used then. */
  flow_id: string;
  /** Off by default: a new library starts paused, so nothing is rewritten before the operator has looked at it. */
  start: boolean;
}

const EMPTY: Draft = { name: "", kind: "media", path: "", handoff_path: "", flow_id: "", start: false };

const KIND_HINT: Record<LibraryKind, string> = {
  media: "A folder of films or series. Files are encoded where they are, and each encode replaces its original.",
  download: "A folder of finished downloads. Each one is encoded and handed to the folder your importer (Sonarr, Radarr) watches.",
};

const START_HINT: Record<LibraryKind, { off: string; on: string }> = {
  media: {
    off: "Off: the library starts paused. anvil scans it and queues its files, but changes nothing until you resume it.",
    on: "On: anvil starts encoding as soon as the first scan has queued files. Each encode replaces its original, which is gone once the encode is published.",
  },
  download: {
    off: "Off: the library starts paused. anvil scans it and queues its downloads, but encodes and hands off nothing until you resume it.",
    on: "On: anvil starts encoding as soon as the first scan has queued downloads, and hands each encode to the handoff folder.",
  },
};

/** What a new library starts with beyond the form's fields. Everything else is edited on its Settings tab. */
function toWrite(draft: Draft, flowId: number): LibraryWrite {
  return {
    name: draft.name,
    kind: draft.kind,
    path: draft.path,
    flow_id: flowId,
    paused: !draft.start,
    priority: 0,
    concurrency_limit: 0,
    scan_interval: "",
    arr: "",
    rules: [],
    rule_fallback: "allow",
    media: { replacement_mode: "replace" },
    download: { handoff_path: draft.kind === "download" ? draft.handoff_path : "", stable_for: "", package_mode: "auto", handoff_mode: "move", preserve_relative_path: true, cleanup_source_media: false, prune_empty_dirs: true, ignorable_globs: [] },
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
  const navigate = useNavigate();
  const [draft, setDraft] = createStore<Draft>({ ...EMPTY });
  const [errors, setErrors] = createStore<Record<string, string | undefined>>({});
  const [general, setGeneral] = createSignal<string | null>(null);
  const [browsing, setBrowsing] = createSignal<"path" | "handoff_path" | null>(null);
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
      name: NAME.test(draft.name) ? undefined : draft.name === "" ? "Give the library a name." : 'Must be 1 to 64 characters of a-z, 0-9, "-" and "_". No spaces or capitals.',
      path: draft.path.startsWith("/") ? undefined : "Must be an absolute path, starting with /.",
      "download.handoff_path": draft.kind !== "download" || draft.handoff_path.startsWith("/") ? undefined : "Must be an absolute path, starting with /.",
      flow_id: flowId() === undefined ? "There is no flow yet. Create one under Flows first." : undefined,
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
      success: (created) => `Added the library ${created.name}${created.paused ? ", paused" : ""}. Scanning it now.`,
      onError: (error: ApiError) => {
        // A field the form shows gets the message; anything else stays in the dialog, above the fields.
        if (error.field && error.field in FIELD_IDS) setErrors((current) => void (current[error.field as string] = error.message));
        else setGeneral(error.message);
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
      description="A library is a folder anvil scans. Every file in it runs through the library's flow. The first scan starts as soon as the library is added."
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
        <Field label="Name" htmlFor={FIELD_IDS.name} hint="Lowercase letters, digits, - and _. It cannot change later." error={errors.name}>
          <Input id={FIELD_IDS.name} mono autocomplete="off" spellcheck={false} placeholder="movies" invalid={!!errors.name} value={draft.name} onInput={(event) => edit("name", (d) => void (d.name = event.currentTarget.value))} />
        </Field>
        <Field label="Kind" hint={`${KIND_HINT[draft.kind]} It cannot change later.`}>
          <div>
            <Segmented
              label="Kind"
              value={draft.kind}
              onChange={(kind) => edit("kind", (d) => void (d.kind = kind as LibraryKind))}
              options={[
                { value: "media", label: "Media library" },
                { value: "download", label: "Download folder" },
              ]}
            />
          </div>
        </Field>
        <Field label="Folder" htmlFor={FIELD_IDS.path} hint="The folder anvil scans." error={errors.path}>
          <div class="av-row av-row-nowrap">
            <Input id={FIELD_IDS.path} mono autocomplete="off" spellcheck={false} placeholder={draft.kind === "download" ? "/downloads/complete" : "/media/movies"} invalid={!!errors.path} value={draft.path} onInput={(event) => edit("path", (d) => void (d.path = event.currentTarget.value))} />
            <Button icon="folder" onClick={() => setBrowsing("path")}>
              Browse
            </Button>
          </div>
        </Field>
        <Show when={draft.kind === "download"}>
          <Field label="Handoff folder" htmlFor={FIELD_IDS["download.handoff_path"]} hint="The folder your importer watches. Encodes are handed off here." error={errors["download.handoff_path"]}>
            <div class="av-row av-row-nowrap">
              <Input
                id={FIELD_IDS["download.handoff_path"]}
                mono
                autocomplete="off"
                spellcheck={false}
                placeholder="/downloads/import"
                invalid={!!errors["download.handoff_path"]}
                value={draft.handoff_path}
                onInput={(event) => edit("download.handoff_path", (d) => void (d.handoff_path = event.currentTarget.value))}
              />
              <Button icon="folder" onClick={() => setBrowsing("handoff_path")}>
                Browse
              </Button>
            </div>
          </Field>
        </Show>
        <Field label="Flow" htmlFor={FIELD_IDS.flow_id} hint="What happens to each file. It can be changed at any time." error={errors.flow_id}>
          <Select id={FIELD_IDS.flow_id} value={String(flowId() ?? "")} invalid={!!errors.flow_id} options={flows().map((flow) => ({ value: String(flow.id), label: flow.name }))} onChange={(event) => edit("flow_id", (d) => void (d.flow_id = event.currentTarget.value))} />
        </Field>
        <Field inline label="Start processing right away" htmlFor="add-library-start" hint={START_HINT[draft.kind][draft.start ? "on" : "off"]}>
          <Switch id="add-library-start" label="Start processing right away" checked={draft.start} onChange={(start) => edit("paused", (d) => void (d.start = start))} />
        </Field>
      </form>
      <FolderPicker
        open={browsing() !== null}
        onClose={() => setBrowsing(null)}
        initialPath={(browsing() === "handoff_path" ? draft.handoff_path : draft.path) || undefined}
        onPick={(path) => {
          if (browsing() === "handoff_path") edit("download.handoff_path", (d) => void (d.handoff_path = path));
          else edit("path", (d) => void (d.path = path));
          setBrowsing(null);
        }}
      />
    </Dialog>
  );
}

// The request fields this form has a control for, and the id of each control.
const FIELD_IDS = {
  name: "add-library-name",
  path: "add-library-path",
  "download.handoff_path": "add-library-handoff",
  flow_id: "add-library-flow",
} as const;
