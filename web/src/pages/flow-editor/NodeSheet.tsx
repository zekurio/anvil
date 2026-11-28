// The options of the selected node, in the side sheet. Everything here is
// rendered from the node type's catalog entry: sections, fields, controls,
// hints. Edits go straight into the draft; problems come from the live check.

import { Dynamic } from "@solidjs/web";
import { createEffect, createMemo, createSignal, For, Show } from "solid-js";

import { api, createQuery, keys, type EncoderCapability, type Field as CatalogField, type GraphNode, type Issue, type OptionValue } from "../../api";
import { Button, Field, Icon, Input, NODE_KINDS, SheetSection, SideSheet } from "../../ui";
import { CONTROLS } from "./controls";
import type { Editor } from "./editor";
import { dependentOptions, effectiveOptions, fieldsOf, isGoDuration, isVisible, resolveField, sameOptionValue, type ResolvedField } from "./graph";

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
  /** The host's encoder report, asked only for the one node type whose options depend on it. */
  const host = type()?.id === "video.encode" ? createQuery(() => keys.encoders, api.encoders) : undefined;
  const resolvedOf = (field: CatalogField): ResolvedField => resolveField(field, values());
  /**
   * A field is on screen while `visible_when` allows it and its variant does
   * not hide it. A hidden field is shown anyway when the node stores a value
   * there: the daemon still validates that value, so the operator needs a way
   * to fix or reset it.
   */
  const shown = (field: CatalogField) => {
    const resolved = resolvedOf(field);
    return isVisible(resolved, values()) && (!resolved.hidden || node().options?.[field.key] !== undefined);
  };
  /** What the control shows: the stored value even when it is not valid, so it can be corrected; a missing key shows the resolved default. */
  const stored = (field: ResolvedField): OptionValue => {
    const own = node().options?.[field.key];
    return own === undefined ? field.default : own;
  };

  /** The encoder behind the selected codec and backend, when the host's report knows it. */
  const selectedEncoder = (): EncoderCapability | undefined => {
    const report = host?.();
    if (report?.state !== "ready") return undefined;
    const codec = values().get("codec");
    const backend = values().get("accelerator");
    return report.encoders.find((encoder) => encoder.codec === codec && encoder.backend === backend);
  };
  /** Marks a backend whose encoder for this codec is unavailable here. The choice stays selectable: a flow may run elsewhere. */
  const withAvailability = (field: ResolvedField): ResolvedField => {
    const report = host?.();
    if (field.key !== "accelerator" || report?.state !== "ready") return field;
    const codec = values().get("codec");
    return {
      ...field,
      choices: (field.choices ?? []).map((choice) => {
        const encoder = report.encoders.find((candidate) => candidate.codec === codec && candidate.backend === choice.value);
        return encoder && !encoder.available ? { ...choice, label: `${choice.label} (not available here)` } : choice;
      }),
    };
  };
  /** Why the selected encoder or bit depth cannot run here, in the probe's own words. */
  const availabilityWarning = (field: ResolvedField): string | undefined => {
    const report = host?.();
    if (report?.state !== "ready") return undefined;
    const encoder = selectedEncoder();
    if (!encoder) return undefined;
    if (!encoder.available) return field.key === "accelerator" ? (encoder.reason ?? "This encoder is not available on this host.") : undefined;
    if (field.key !== "bit_depth") return undefined;
    const depth = Number(values().get("bit_depth"));
    const capability = encoder.bit_depths.find((candidate) => candidate.bit_depth === depth);
    return capability && !capability.available ? (capability.reason ?? "This bit depth is not available on this host.") : undefined;
  };

  const fields = createMemo(() => {
    const current = type();
    return current ? fieldsOf(current) : [];
  });
  const issues = () => editor.check.issuesOf(id);
  const shownKeys = createMemo(() => new Set(fields().filter(shown).map((field) => field.key)));
  const fieldError = (field: ResolvedField): string | undefined => {
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
  /** Writes one option, dropping the stored options its new value invalidates, in the same undo step. */
  const change = (field: CatalogField, value: OptionValue) => {
    if (sameOptionValue(values().get(field.key), value)) return;
    const current = type();
    draft.setOption(id, field.key, value, current ? dependentOptions(current, field.key) : []);
  };

  /* A collapsed section opens, and stays open, once something in it needs attention. */
  const [opened, setOpened] = createSignal<ReadonlySet<string>>(new Set());
  const open = (title: string) => setOpened((current) => (current.has(title) ? current : new Set([...current, title])));
  const sectionOf = (key: string) => type()?.sections.find((section) => section.fields.some((field) => field.key === key))?.title;
  createEffect(
    () =>
      (type()?.sections ?? [])
        .filter((section) => section.fields.some((field) => shown(field) && fieldError(resolvedOf(field))))
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
                      {(field) => {
                        const resolved = () => resolvedOf(field());
                        return (
                          <Field
                            label={resolved().label}
                            htmlFor={controlId(id, field().key)}
                            hint={resolved().hint}
                            warning={availabilityWarning(resolved())}
                            error={fieldError(resolved())}
                            inline={resolved().type === "bool"}
                          >
                            <Dynamic
                              component={CONTROLS[resolved().type]}
                              field={withAvailability(resolved())}
                              value={stored(resolved())}
                              id={controlId(id, field().key)}
                              invalid={!!fieldError(resolved())}
                              half={row().fields.length > 1 || !!resolved().half}
                              onChange={(value: OptionValue) => change(field(), value)}
                            />
                            <Show when={resolved().hidden && node().options?.[field().key] !== undefined}>
                              <Button variant="ghost" size="sm" class="fe-field-reset" onClick={() => draft.removeOption(id, field().key)}>
                                Reset
                              </Button>
                            </Show>
                          </Field>
                        );
                      }}
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
