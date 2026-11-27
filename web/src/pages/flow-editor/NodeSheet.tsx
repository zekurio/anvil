// The options of the selected node, in the side sheet. Everything here is
// rendered from the node type's catalog entry: sections, fields, controls,
// hints. Edits go straight into the draft; problems come from the live check.

import { Dynamic } from "@solidjs/web";
import { createEffect, createMemo, createSignal, For, Show } from "solid-js";

import type { Field as CatalogField, GraphNode, Issue, OptionValue } from "../../api";
import { Button, Field, Icon, Input, NODE_KINDS, SheetSection, SideSheet } from "../../ui";
import { CONTROLS } from "./controls";
import type { Editor } from "./editor";
import { effectiveOptions, fieldsOf, isGoDuration, isVisible } from "./graph";

/** A request to bring one option of a node into view and put the cursor in it. */
export interface FieldFocus {
  nodeId: string;
  field: string;
  /** Distinguishes two requests for the same field. */
  serial: number;
}

interface Row {
  key: string;
  fields: CatalogField[];
}

/** Fields in catalog order, two consecutive `half` fields sharing a row. */
function rowsOf(fields: CatalogField[]): Row[] {
  const rows: Row[] = [];
  for (const field of fields) {
    const last = rows[rows.length - 1];
    if (field.half && last && last.fields.length === 1 && last.fields[0]?.half) last.fields.push(field);
    else rows.push({ key: field.key, fields: [field] });
  }
  return rows.map((row) => ({ key: row.fields.map((field) => field.key).join("+"), fields: row.fields }));
}

const controlId = (nodeId: string, key: string) => `fe-option-${nodeId}-${key}`;

function focusControl(id: string): void {
  const el = document.getElementById(id);
  if (!el) return;
  const target = el.matches("input, select, textarea, button") ? el : el.querySelector<HTMLElement>("input, select, textarea, button");
  target?.focus();
  el.scrollIntoView({ block: "nearest" });
}

export function NodeSheet(props: { editor: Editor; focus: FieldFocus | null; onClose: () => void }) {
  let served = 0;
  const take = (serial: number) => {
    if (serial <= served) return false;
    served = serial;
    return true;
  };
  return (
    <Show when={props.editor.selectedId()} keyed>
      {(id) => <NodeOptions editor={props.editor} id={id} focus={props.focus} take={take} onClose={props.onClose} />}
    </Show>
  );
}

function NodeOptions(props: { editor: Editor; id: string; focus: FieldFocus | null; take: (serial: number) => boolean; onClose: () => void }) {
  const editor = props.editor;
  const draft = editor.draft;
  const id = props.id;

  const node = () => draft.node(id) as GraphNode;
  const type = createMemo(() => editor.typeOf(node()));
  const values = createMemo(() => {
    const current = type();
    return current ? effectiveOptions(current, node()) : new Map<string, OptionValue>();
  });
  const shown = (field: CatalogField) => isVisible(field, values());
  /** What the control shows: the stored value even when it is not valid, so it can be corrected; a missing key shows the default. */
  const stored = (field: CatalogField): OptionValue => {
    const own = node().options?.[field.key];
    return own === undefined ? field.default : own;
  };

  const fields = createMemo(() => {
    const current = type();
    return current ? fieldsOf(current) : [];
  });
  const issues = () => editor.check.issuesOf(id);
  const shownKeys = createMemo(() => new Set(fields().filter(shown).map((field) => field.key)));
  const fieldError = (field: CatalogField): string | undefined => {
    const reported = issues().find((issue) => issue.field === field.key && issue.severity === "error")?.message;
    if (reported) return reported;
    // Known before the daemon answers: the same words it uses.
    const value = stored(field);
    return field.type === "duration" && typeof value === "string" && !isGoDuration(value) ? 'Must be a duration such as "20s".' : undefined;
  };
  /** Problems that have no field on screen to sit under: the node's wiring, or an option its type does not have. */
  const nodeIssues = createMemo(() => issues().filter((issue) => !issue.field || !shownKeys().has(issue.field)));
  /** An option the node stores but its type does not have. Only removing it fixes that. */
  const isStray = (issue: Issue) => !!issue.field && node().options?.[issue.field] !== undefined && !fields().some((field) => field.key === issue.field);

  /* A collapsed section opens, and stays open, once something in it needs attention. */
  const [opened, setOpened] = createSignal<ReadonlySet<string>>(new Set());
  const open = (title: string) => setOpened((current) => (current.has(title) ? current : new Set([...current, title])));
  const sectionOf = (key: string) => type()?.sections.find((section) => section.fields.some((field) => field.key === key))?.title;
  createEffect(
    () =>
      (type()?.sections ?? [])
        .filter((section) => section.fields.some((field) => shown(field) && fieldError(field)))
        .map((section) => section.title)
        .join("\n"),
    (titles) => {
      for (const title of titles ? titles.split("\n") : []) open(title);
    },
  );
  createEffect(
    () => {
      const request = props.focus;
      return request && request.nodeId === id ? { field: request.field, section: sectionOf(request.field), serial: request.serial } : null;
    },
    (request) => {
      // Each request is served once: selecting the node again later must not jump back to the field.
      if (!request || !props.take(request.serial)) return;
      if (request.section) open(request.section);
      // After the section has rendered its fields.
      requestAnimationFrame(() => focusControl(controlId(id, request.field)));
    },
  );

  const removable = createMemo(() => (draft.order(), editor.canRemove(id)));
  const copyable = createMemo(() => (draft.order(), editor.canDuplicate(id)));

  return (
    <SideSheet
      title={editor.titleOf(node())}
      subtitle={`${editor.kindLabel(node())} · node ${draft.order().indexOf(id) + 1} of ${draft.order().length}`}
      icon={NODE_KINDS[editor.kindOf(node())].icon}
      onClose={() => props.onClose()}
      footer={
        <Show when={removable() || copyable()}>
          <Show when={removable()}>
            <Button variant="danger" size="sm" icon="trash-2" onClick={() => editor.removeNode(id)}>
              Delete node
            </Button>
          </Show>
          <span class="av-spacer" />
          <Show when={copyable()}>
            <Button variant="ghost" size="sm" icon="copy" onClick={() => editor.duplicate(id)}>
              Duplicate
            </Button>
          </Show>
        </Show>
      }
    >
      <Show when={nodeIssues().length > 0}>
        <ul class="fe-issues fe-issues-node" aria-label="Problems with this node">
          <For each={nodeIssues()} keyed={false}>
            {(issue) => (
              <li class={["fe-issue", `fe-issue-${issue().severity}`]}>
                <Icon name={issue().severity === "error" ? "circle-x" : "triangle-alert"} size={14} label={issue().severity === "error" ? "Error" : "Warning"} />
                <span class="fe-issue-text">{issue().message}</span>
                <Show when={isStray(issue())}>
                  <Button size="sm" onClick={() => draft.removeOption(id, issue().field as string)}>
                    Remove option
                  </Button>
                </Show>
              </li>
            )}
          </For>
        </ul>
      </Show>

      <section class="av-sec fe-about">
        <div class="fe-about-type">{type()?.label ?? node().type}</div>
        <p class="fe-about-text">{type()?.description ?? "Unknown node type. Delete it or replace it with one from the palette."}</p>
        <Field label="Label" htmlFor={controlId(id, "label")}>
          <Input id={controlId(id, "label")} autocomplete="off" placeholder={type()?.label} value={node().label ?? ""} onInput={(event) => draft.setLabel(id, event.currentTarget.value)} />
        </Field>
      </section>

      <For each={type()?.sections ?? []} keyed={(section) => section.title}>
        {(section) => (
          <Show when={section().fields.some(shown)}>
            <SheetSection title={section().title} collapsible={section().collapsible} defaultOpen={!section().collapsed || opened().has(section().title)}>
              <For each={rowsOf(section().fields.filter(shown))} keyed={(row) => row.key}>
                {(row) => (
                  <div class={{ "av-grid-2": row().fields[0]?.half }}>
                    <For each={row().fields} keyed={(field) => field.key}>
                      {(field) => (
                        <Field label={field().label} htmlFor={controlId(id, field().key)} hint={field().hint} error={fieldError(field())} inline={field().type === "bool"}>
                          <Dynamic
                            component={CONTROLS[field().type]}
                            field={field()}
                            value={stored(field())}
                            id={controlId(id, field().key)}
                            invalid={!!fieldError(field())}
                            half={row().fields.length > 1 || !!field().half}
                            onChange={(value: OptionValue) => draft.setOption(id, field().key, value)}
                          />
                        </Field>
                      )}
                    </For>
                  </div>
                )}
              </For>
            </SheetSection>
          </Show>
        )}
      </For>
    </SideSheet>
  );
}
