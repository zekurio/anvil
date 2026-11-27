import { useNavigate } from "@solidjs/router";
import { createSignal, createStore, Show } from "solid-js";

import { api, keys, type ApiError, type LibraryKind, type LibraryWrite } from "../api";
import { run } from "../lib/mutate";
import { Button, Dialog, Field, FolderPicker, Input, Segmented, Select } from "../ui";
import { useAppData } from "./data";

const [open, setOpen] = createSignal(false);

/** Opens the Add library dialog. The app shell hosts the dialog itself. */
export function openAddLibrary(): void {
  setOpen(true);
}

const NAME = /^[a-z0-9_-]{1,64}$/;

interface Draft {
  name: string;
  kind: LibraryKind;
  path: string;
  handoff_path: string;
  /** "" until the person chooses; the first flow is used then. */
  flow_id: string;
}

const EMPTY: Draft = { name: "", kind: "media", path: "", handoff_path: "", flow_id: "" };

/** What a new library starts with beyond the form's fields. Everything else is edited on its Settings tab. */
function toWrite(draft: Draft, flowId: number): LibraryWrite {
  return {
    name: draft.name,
    kind: draft.kind,
    path: draft.path,
    flow_id: flowId,
    paused: false,
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
  const [browsing, setBrowsing] = createSignal<"path" | "handoff_path" | null>(null);
  const [saving, setSaving] = createSignal(false);

  const flowId = () => Number(draft.flow_id) || flows()[0]?.id;

  /** Checks what the browser can check; the daemon has the last word. */
  const validate = (): boolean => {
    const next: Record<string, string | undefined> = {
      name: NAME.test(draft.name) ? undefined : 'Must be 1 to 64 characters of a-z, 0-9, "-" and "_".',
      path: draft.path.startsWith("/") ? undefined : "Must be an absolute path.",
      "download.handoff_path": draft.kind !== "download" || draft.handoff_path.startsWith("/") ? undefined : "Must be an absolute path.",
      flow_id: flowId() === undefined ? "Create a flow first." : undefined,
    };
    setErrors(() => next);
    return Object.values(next).every((message) => message === undefined);
  };

  const submit = async (event: SubmitEvent) => {
    event.preventDefault();
    const flow = flowId();
    if (!validate() || flow === undefined) return;
    setSaving(true);
    const library = await run(() => api.libraries.create(toWrite(draft, flow)), {
      invalidate: [keys.libraries, keys.flows],
      success: (created) => `Added the library ${created.name}.`,
      failure: "The library could not be added.",
      onError: (error: ApiError) => {
        if (!error.field) return false;
        setErrors((current) => {
          current[error.field as string] = error.message;
        });
        return true;
      },
    });
    setSaving(false);
    if (!library) return;
    setOpen(false);
    navigate(`/libraries/${encodeURIComponent(library.name)}`);
  };

  return (
    <Dialog
      open
      onClose={() => setOpen(false)}
      title="Add library"
      description="A library is a folder anvil scans. Every file in it runs through the library's flow."
      footer={
        <>
          <Button onClick={() => setOpen(false)}>Cancel</Button>
          <Button variant="primary" type="submit" form="add-library" busy={saving()}>
            Add library
          </Button>
        </>
      }
    >
      <form id="add-library" class="av-stack" novalidate onSubmit={submit}>
        <Field label="Name" htmlFor="add-library-name" hint="Lowercase letters, digits, - and _. It cannot change later." error={errors.name}>
          <Input id="add-library-name" mono autocomplete="off" spellcheck={false} placeholder="movies" invalid={!!errors.name} value={draft.name} onInput={(event) => setDraft((d) => void (d.name = event.currentTarget.value))} />
        </Field>
        <Field label="Kind" hint={draft.kind === "media" ? "Files are encoded in place." : "Finished downloads are encoded, then handed to an importer."}>
          <Segmented
            label="Kind"
            value={draft.kind}
            onChange={(kind) => setDraft((d) => void (d.kind = kind as LibraryKind))}
            options={[
              { value: "media", label: "Media library" },
              { value: "download", label: "Download folder" },
            ]}
          />
        </Field>
        <Field label="Folder" htmlFor="add-library-path" error={errors.path}>
          <div class="av-row av-row-nowrap">
            <Input id="add-library-path" mono autocomplete="off" spellcheck={false} placeholder="/media/movies" invalid={!!errors.path} value={draft.path} onInput={(event) => setDraft((d) => void (d.path = event.currentTarget.value))} />
            <Button icon="folder" onClick={() => setBrowsing("path")}>
              Browse
            </Button>
          </div>
        </Field>
        <Show when={draft.kind === "download"}>
          <Field label="Handoff folder" htmlFor="add-library-handoff" hint="The folder your importer watches." error={errors["download.handoff_path"]}>
            <div class="av-row av-row-nowrap">
              <Input id="add-library-handoff" mono autocomplete="off" spellcheck={false} placeholder="/downloads/import" invalid={!!errors["download.handoff_path"]} value={draft.handoff_path} onInput={(event) => setDraft((d) => void (d.handoff_path = event.currentTarget.value))} />
              <Button icon="folder" onClick={() => setBrowsing("handoff_path")}>
                Browse
              </Button>
            </div>
          </Field>
        </Show>
        <Field label="Flow" htmlFor="add-library-flow" hint="What happens to each file. It can be changed at any time." error={errors.flow_id}>
          <Select id="add-library-flow" value={String(flowId() ?? "")} invalid={!!errors.flow_id} options={flows().map((flow) => ({ value: String(flow.id), label: flow.name }))} onChange={(event) => setDraft((d) => void (d.flow_id = event.currentTarget.value))} />
        </Field>
      </form>
      <FolderPicker
        open={browsing() !== null}
        onClose={() => setBrowsing(null)}
        initialPath={(browsing() === "handoff_path" ? draft.handoff_path : draft.path) || undefined}
        onPick={(path) => {
          const target = browsing();
          setDraft((d) => {
            if (target === "handoff_path") d.handoff_path = path;
            else d.path = path;
          });
          setBrowsing(null);
        }}
      />
    </Dialog>
  );
}
