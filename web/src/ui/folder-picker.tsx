import { createSignal, Errored, For, isPending, Loading, Show } from "solid-js";

import { api } from "../api/endpoints";
import { keys } from "../api/keys";
import { createQuery } from "../api/query";
import type { DirEntry } from "../api/types";
import { formatBytes } from "../lib/format";
import { Button } from "./button";
import { Dialog } from "./dialog";
import { ErrorState, LoadingState } from "./feedback";
import { Icon } from "./icon";
import { Input } from "./inputs";

export interface FolderPickerProps {
  open: boolean;
  onClose: () => void;
  /** Receives the absolute path that was chosen. The caller closes the picker. */
  onPick: (path: string) => void;
  /** Where browsing starts. Default: the daemon's root listing. */
  initialPath?: string;
  /** Also list media files and pick one of them instead of a folder. */
  files?: boolean;
  title?: string;
  /** Default "Choose folder", or "Choose file" with `files`. */
  confirmLabel?: string;
}

/** A dialog that browses the daemon's filesystem through GET /fs. */
export function FolderPicker(props: FolderPickerProps) {
  return (
    <Dialog open={props.open} onClose={props.onClose} size="lg" title={props.title ?? (props.files ? "Choose a file" : "Choose a folder")}>
      {/* Mounted per open, so each time starts at initialPath with a fresh listing. */}
      <Show when={props.open}>
        <Browser {...props} />
      </Show>
    </Dialog>
  );
}

function Browser(props: FolderPickerProps) {
  const [path, setPath] = createSignal(() => props.initialPath ?? "");
  const [file, setFile] = createSignal<string | null>(null);
  const [typed, setTyped] = createSignal<string | null>(null);

  const go = (next: string) => {
    setTyped(null);
    setFile(null);
    setPath(next);
  };

  const listing = createQuery(
    () => keys.fs(path(), !!props.files),
    () => api.fs(path(), props.files),
  );
  const chosen = () => (props.files ? file() : listing().path);

  return (
    <div class="av-picker">
      <Errored fallback={(err) => <ErrorState title="This folder could not be listed" error={err()} onRetry={() => go("")} />}>
        <Loading fallback={<LoadingState label="Listing folder" />}>
          <form
            class="av-picker-bar"
            onSubmit={(event) => {
              event.preventDefault();
              go(typed() ?? listing().path);
            }}
          >
            <Button icon="corner-left-up" label="Up one folder" disabled={listing().parent === ""} onClick={() => go(listing().parent)} />
            <Input mono aria-label="Path" value={typed() ?? listing().path} onInput={(event) => setTyped(event.currentTarget.value)} />
            <Button type="submit">Go</Button>
          </form>
          {/* Opening a folder keeps the old listing until the new one arrives; aria-busy dims it meanwhile. */}
          <ul class="av-picker-list" aria-label={`Contents of ${listing().path}`} aria-busy={isPending(() => listing()) ? "true" : undefined}>
            <For each={listing().entries} keyed={(entry: DirEntry) => entry.path} fallback={<li class="av-picker-empty">This folder has no {props.files ? "subfolders or media files" : "subfolders"}.</li>}>
              {(entry) => (
                <li>
                  <button
                    type="button"
                    class="av-picker-item"
                    aria-pressed={entry().dir ? undefined : file() === entry().path ? "true" : "false"}
                    onClick={() => (entry().dir ? go(entry().path) : setFile(entry().path))}
                    onDblClick={() => !entry().dir && props.onPick(entry().path)}
                  >
                    <Icon name={entry().dir ? "folder" : "file-video"} size={16} />
                    <span class="av-picker-name">{entry().name}</span>
                    <Show when={!entry().dir}>
                      <span class="av-picker-size">{formatBytes(entry().size_bytes ?? 0)}</span>
                    </Show>
                  </button>
                </li>
              )}
            </For>
          </ul>
          <div class="av-picker-foot">
            <span class="av-picker-chosen" title={chosen() ?? undefined}>
              {chosen() ?? "No file chosen"}
            </span>
            <Button onClick={() => props.onClose()}>Cancel</Button>
            <Button
              variant="primary"
              disabled={!chosen()}
              onClick={() => {
                const value = chosen();
                if (value) props.onPick(value);
              }}
            >
              {props.confirmLabel ?? (props.files ? "Choose file" : "Choose folder")}
            </Button>
          </div>
        </Loading>
      </Errored>
    </div>
  );
}
