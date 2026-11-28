// The editor's top bar: back to the list, the flow's name, the state of the
// draft, what the live check found, and the actions on the whole flow.

import { Popover, PopoverContent, PopoverTitle, PopoverTrigger } from "@ui-sdk/solid/popover";
import { For, Show } from "solid-js";

import type { Issue } from "../../api";
import { plural } from "../../lib/format";
import { Button, Icon } from "../../ui";
import type { Editor } from "./editor";
import { fieldsOf } from "./graph";

export interface EditorTopbarProps {
  editor: Editor;
  /** A flow that was never saved. */
  isNew: boolean;
  /** Libraries the flow is assigned to. */
  usedBy: string[];
  saving: boolean;
  /** Someone else saved the flow since it was opened; this draft cannot be saved over theirs. */
  outdated: boolean;
  /** What the daemon said about the name, shown in place of the draft state. */
  nameError: string | null;
  /** Whether the issues list is open. Owned by the page, which also opens it when a save is refused. */
  issuesOpen: boolean;
  onIssuesOpen: (open: boolean) => void;
  onIssue: (issue: Issue) => void;
  onName: (name: string) => void;
  onDetails: () => void;
  onTest: () => void;
  onDiscard: () => void;
  onSave: () => void;
}

/** How many nodes stand between the draft and a save, in the words the Save button uses. */
export function attention(editor: Editor): string {
  const nodes = editor.check.invalidNodes().size;
  if (nodes > 0) return `${plural(nodes, "node")} ${nodes === 1 ? "needs" : "need"} attention`;
  const errors = editor.check.errors().length;
  return errors > 0 ? plural(errors, "problem") : "";
}

function Problems(props: Pick<EditorTopbarProps, "editor" | "issuesOpen" | "onIssuesOpen" | "onIssue">) {
  const editor = props.editor;
  const errors = () => editor.check.errors().length;
  const warnings = () => editor.check.warnings().length;
  const tone = () => (errors() > 0 ? "error" : "warning");
  const summary = () => (errors() > 0 ? attention(editor) : plural(warnings(), "warning"));

  /** Where an issue is: the node's name, and the option when it is about one. */
  const place = (issue: Issue): string => {
    const node = issue.node_id ? editor.draft.find(issue.node_id) : undefined;
    if (!node) return "The flow";
    const type = editor.typeOf(node);
    const field = issue.field && type ? fieldsOf(type).find((candidate) => candidate.key === issue.field) : undefined;
    return field ? `${editor.titleOf(node)} · ${field.label}` : editor.titleOf(node);
  };

  return (
    <Show when={errors() + warnings() > 0}>
      <Popover open={props.issuesOpen} onOpenChange={(open) => props.onIssuesOpen(open)}>
        <PopoverTrigger class={["fe-problems", `fe-problems-${tone()}`]}>
          <Icon name={tone() === "error" ? "circle-x" : "triangle-alert"} size={14} />
          {summary()}
          <Icon name="chevron-down" size={12} />
        </PopoverTrigger>
        <PopoverContent class="fe-problems-pop" align="start" sideOffset={4}>
          <PopoverTitle class="fe-problems-title">{[errors() > 0 && plural(errors(), "error"), warnings() > 0 && plural(warnings(), "warning")].filter(Boolean).join(" · ")}</PopoverTitle>
          <ul class="fe-problems-list">
            <For each={editor.check.issues()} keyed={false}>
              {(issue) => (
                <li>
                  <button
                    type="button"
                    class={["fe-problem", `fe-problem-${issue().severity}`]}
                    disabled={!issue().node_id}
                    onClick={() => {
                      props.onIssuesOpen(false);
                      props.onIssue(issue());
                    }}
                  >
                    <Icon name={issue().severity === "error" ? "circle-x" : "triangle-alert"} size={14} label={issue().severity === "error" ? "Error" : "Warning"} />
                    <span class="fe-problem-place">{place(issue())}</span>
                    <span class="fe-problem-text">{issue().message}</span>
                  </button>
                </li>
              )}
            </For>
          </ul>
        </PopoverContent>
      </Popover>
    </Show>
  );
}

export function EditorTopbar(props: EditorTopbarProps) {
  const editor = props.editor;
  const draft = editor.draft;

  const dirty = () => props.isNew || draft.changes() > 0;
  const blocked = () => editor.check.errors().length > 0;
  /** Why Save does nothing right now, as its tooltip. */
  const saveNote = () => {
    if (blocked()) return `${attention(editor)}.`;
    if (props.outdated) return "Changed elsewhere. Reload it, or save as a new flow.";
    return undefined;
  };
  const state = () => {
    if (props.isNew) return "Draft · not saved";
    const changes = draft.changes();
    return changes > 0 ? `Draft · ${plural(changes, "unsaved change")}` : "No unsaved changes";
  };

  return (
    <header class="av-topbar fe-topbar">
      <a class="av-btn av-btn-ghost av-btn-icon av-topbar-back" href="/flows" aria-label="Back to flows" title="Back to flows">
        <Icon name="arrow-left" size={16} />
      </a>
      <div class="av-topbar-titles">
        <h1 class="av-sr-only">{draft.name() || "Flow"}</h1>
        {/* The hidden copy of the name sizes the grid cell, so the input is as wide as what it holds. */}
        <span class="fe-name" data-value={draft.name() || "Flow name"}>
          <input
            class="fe-name-input"
            type="text"
            size="1"
            aria-label="Flow name"
            placeholder="Flow name"
            autocomplete="off"
            spellcheck={false}
            maxlength={80}
            aria-invalid={props.nameError ? "true" : undefined}
            aria-describedby={props.nameError ? "fe-name-error" : undefined}
            value={draft.name()}
            onInput={(event) => props.onName(event.currentTarget.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" || event.key === "Escape") event.currentTarget.blur();
            }}
          />
        </span>
        <Button variant="ghost" size="sm" icon="pencil" label="Edit name and description" onClick={() => props.onDetails()} />
        <span class="av-topbar-meta fe-status">
          <Show
            when={props.nameError}
            fallback={
              <>
                <span class={{ "fe-draft": dirty() }}>{state()}</span>
                <Show when={props.usedBy.length > 0}>
                  <span class="fe-used" title="Saving applies to files queued from now on.">
                    Used by {props.usedBy.join(", ")}
                  </span>
                </Show>
              </>
            }
          >
            <span id="fe-name-error" class="fe-name-error" role="alert">
              {props.nameError}
            </span>
          </Show>
        </span>
        <Problems editor={editor} issuesOpen={props.issuesOpen} onIssuesOpen={props.onIssuesOpen} onIssue={props.onIssue} />
      </div>
      <div class="av-topbar-actions">
        <Button variant="ghost" icon="play" disabled={blocked()} title={blocked() ? "Fix the errors first." : undefined} onClick={() => props.onTest()}>
          Test on a file
        </Button>
        <Show when={dirty()}>
          <Button disabled={props.saving} onClick={() => props.onDiscard()}>
            Discard
          </Button>
        </Show>
        <Button variant="primary" busy={props.saving} disabled={!dirty() || blocked() || props.outdated} title={saveNote()} onClick={() => props.onSave()}>
          Save flow
        </Button>
      </div>
    </header>
  );
}
