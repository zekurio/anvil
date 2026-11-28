import { createSignal, createStore, Show } from "solid-js";

import { api, keys, type ApiError, type Arr, type ArrWrite } from "../../api";
import { run } from "../../lib/mutate";
import { Banner, Button, Checkbox, Dialog, Field, Input, Segmented } from "../../ui";

interface Draft {
  name: string;
  type: Arr["type"];
  base_url: string;
  /** Write-only: never filled from the server. */
  api_key: string;
  /** Edit only: clear the stored key instead of keeping it. */
  remove_key: boolean;
}

const NAME = /^[a-z0-9_-]{1,64}$/;

/** Scheme, host and port of a URL, the way the daemon compares them; the text itself when it is not a URL. */
function origin(url: string): string {
  try {
    return new URL(url.trim()).origin;
  } catch {
    return url.trim();
  }
}

/**
 * Adds an Arr connection, or edits `arr`. The key is write-only: the daemon
 * never returns it, so an empty field keeps what is stored, and removing the
 * stored key is its own explicit choice.
 */
export function ArrDialog(props: { arr?: Arr; onClose: () => void }) {
  // Read once on purpose: the dialog is mounted per open, and a poll must not reset what is being typed.
  const editing = props.arr;
  const [draft, setDraft] = createStore<Draft>({ name: editing?.name ?? "", type: editing?.type ?? "radarr", base_url: editing?.base_url ?? "", api_key: "", remove_key: false });
  const [errors, setErrors] = createStore<Record<string, string | undefined>>({});
  const [general, setGeneral] = createSignal<string | null>(null);
  const [saving, setSaving] = createSignal(false);

  const edit = (field: string, change: (draft: Draft) => void) => {
    setDraft(change);
    setErrors((current) => void (current[field] = undefined));
    setGeneral(null);
  };

  // The daemon keeps a stored key only for the server it was entered for: a new scheme, host or port needs it again.
  const keyNeeded = () => !!editing && (editing.api_key_set || editing.api_key_file !== "") && !draft.remove_key && draft.api_key === "" && origin(draft.base_url) !== origin(editing.base_url);

  const validate = (): boolean => {
    const next: Record<string, string | undefined> = {
      name: editing || NAME.test(draft.name) ? undefined : 'Must be 1 to 64 characters of a-z, 0-9, "-" and "_".',
      base_url: /^https?:\/\/\S+$/.test(draft.base_url.trim()) ? undefined : "Must be a URL starting with http:// or https://.",
    };
    setErrors(() => next);
    return Object.values(next).every((message) => message === undefined);
  };

  const submit = async (event: SubmitEvent) => {
    event.preventDefault();
    if (saving() || !validate()) return;
    // null keeps the stored key, "" clears it, anything else replaces it.
    const api_key = draft.remove_key ? "" : draft.api_key !== "" ? draft.api_key : null;
    const write: ArrWrite = { name: draft.name, type: draft.type, base_url: draft.base_url.trim(), api_key };
    setSaving(true);
    setGeneral(null);
    const saved = await run(() => (editing ? api.arrs.update(editing.name, write) : api.arrs.create(write)), {
      invalidate: [keys.arrs],
      success: (arr) => (editing ? `Saved the connection ${arr.name}.` : `Added the connection ${arr.name}.`),
      onError: (error: ApiError) => {
        if (error.field && ["name", "type", "base_url", "api_key"].includes(error.field)) setErrors((current) => void (current[error.field as string] = error.message));
        else setGeneral(error.message);
        return true;
      },
    });
    setSaving(false);
    if (saved) props.onClose();
  };

  return (
    <Dialog
      open
      dismissible={!saving()}
      onClose={props.onClose}
      title={editing ? `Edit ${editing.name}` : "Add Arr connection"}
      footer={
        <>
          <Button disabled={saving()} onClick={props.onClose}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" form="arr-form" busy={saving()}>
            {editing ? "Save connection" : "Add connection"}
          </Button>
        </>
      }
    >
      <form id="arr-form" class="av-stack" novalidate onSubmit={submit}>
        <Show when={general()}>{(message) => <Banner tone="danger">{message()}</Banner>}</Show>
        <Field label="Name" htmlFor="arr-name" hint={editing ? undefined : "a-z, 0-9, - and _. Cannot be changed later."} error={errors.name}>
          <Show when={!editing} fallback={<p class="av-readonly">{draft.name}</p>}>
            <Input id="arr-name" mono autocomplete="off" spellcheck={false} placeholder="radarr" invalid={!!errors.name} value={draft.name} onInput={(event) => edit("name", (d) => void (d.name = event.currentTarget.value))} />
          </Show>
        </Field>
        <Field label="Type" error={errors.type}>
          <div>
            <Segmented
              label="Type"
              value={draft.type}
              onChange={(type) => edit("type", (d) => void (d.type = type as Arr["type"]))}
              options={[
                { value: "radarr", label: "Radarr" },
                { value: "sonarr", label: "Sonarr" },
              ]}
            />
          </div>
        </Field>
        <Field label="Base URL" htmlFor="arr-url" error={errors.base_url}>
          <Input id="arr-url" mono type="url" autocomplete="off" spellcheck={false} placeholder={draft.type === "sonarr" ? "http://sonarr.lan:8989" : "http://radarr.lan:7878"} invalid={!!errors.base_url} value={draft.base_url} onInput={(event) => edit("base_url", (d) => void (d.base_url = event.currentTarget.value))} />
        </Field>
        <Field
          label="API key"
          htmlFor="arr-key"
          hint={draft.remove_key ? "Removed on save." : keyNeeded() ? "The address changed. Enter the key again." : editing?.api_key_set ? "Leave empty to keep the stored key." : editing?.api_key_file ? "Replaces the key file below." : undefined}
          error={errors.api_key}
        >
          <Input id="arr-key" mono type="password" autocomplete="new-password" spellcheck={false} disabled={draft.remove_key} invalid={!!errors.api_key} value={draft.api_key} onInput={(event) => edit("api_key", (d) => void (d.api_key = event.currentTarget.value))} />
        </Field>
        <Show when={editing?.api_key_set}>
          <label class="av-check-row">
            <Checkbox
              checked={draft.remove_key}
              onChange={(event) =>
                edit("api_key", (d) => {
                  d.remove_key = event.currentTarget.checked;
                  if (d.remove_key) d.api_key = "";
                })
              }
            />
            Remove stored key
          </label>
        </Show>
        <Show when={editing?.api_key_file}>
          {(path) => (
            <Field label="API key file" hint="Imported from anvil.toml.">
              <p class="av-readonly">{path()}</p>
            </Field>
          )}
        </Show>
      </form>
    </Dialog>
  );
}
