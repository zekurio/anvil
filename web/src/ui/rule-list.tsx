import { createSignal, For, Show } from "solid-js";

import type { Rule, RuleAction, RuleField, RuleOp } from "../api/types";
import { Button } from "./button";
import { Segmented } from "./controls";
import { Icon } from "./icon";
import { Input, Select } from "./inputs";

// Fields and their operators, as pkg/webapi documents them on Rule.
const FIELDS: { value: RuleField; label: string; ops: { value: RuleOp; label: string }[]; placeholder: string }[] = [
  { value: "path", label: "Path", ops: [{ value: "matches", label: "matches glob" }, { value: "regex", label: "matches regex" }], placeholder: "**/Extras/**" },
  { value: "extension", label: "Extension", ops: [{ value: "is", label: "is" }, { value: "is_not", label: "is not" }], placeholder: "mkv, mp4" },
  { value: "size", label: "File size", ops: [{ value: "gt", label: "greater than" }, { value: "lt", label: "less than" }], placeholder: "200 MB" },
];

const fieldOf = (field: RuleField) => FIELDS.find((f) => f.value === field) ?? (FIELDS[0] as (typeof FIELDS)[number]);

export interface RuleListProps {
  rules: Rule[];
  onChange: (rules: Rule[]) => void;
  /** What happens to a file no rule matches. */
  fallback?: RuleAction;
  /** Problems the daemon found, keyed by the rule's index. Each shows under its row. */
  errors?: Record<number, string>;
  disabled?: boolean;
}

/**
 * Which files of a library anvil may touch: rules run top to bottom and the
 * first match wins. Rows reorder by dragging the grip, or with the arrow keys
 * while the grip has focus.
 */
export function RuleList(props: RuleListProps) {
  const [dragging, setDragging] = createSignal<number | null>(null);
  const rows: HTMLDivElement[] = [];
  const grips: HTMLButtonElement[] = [];

  const update = (index: number, patch: Partial<Rule>) => props.onChange(props.rules.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)));

  const move = (from: number, to: number): boolean => {
    if (to < 0 || to >= props.rules.length || to === from) return false;
    const next = [...props.rules];
    const [rule] = next.splice(from, 1);
    if (!rule) return false;
    next.splice(to, 0, rule);
    props.onChange(next);
    return true;
  };

  const drag = (start: PointerEvent, from: number) => {
    start.preventDefault();
    let at = from;
    setDragging(at);
    const onMove = (event: PointerEvent) => {
      // Rows are index-keyed, so their boxes stay put while the rules move through them.
      const over = rows.slice(0, props.rules.length).findIndex((row) => {
        const box = row.getBoundingClientRect();
        return event.clientY >= box.top && event.clientY < box.bottom;
      });
      if (over < 0 || !move(at, over)) return;
      at = over;
      setDragging(at);
    };
    const onUp = () => {
      setDragging(null);
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onUp);
  };

  return (
    <div class="av-rules">
      {/* Index-keyed: rows keep their DOM (and input focus) while values change. */}
      <For each={props.rules} keyed={false}>
        {(rule, index) => (
          <div ref={(el) => (rows[index] = el)} class={["av-rule", { "av-rule-dragging": dragging() === index }]}>
            <button
              ref={(el) => (grips[index] = el)}
              type="button"
              class="av-rule-grip"
              aria-label={`Reorder rule ${index + 1}. Use the arrow keys.`}
              title="Drag to reorder"
              disabled={props.disabled}
              onPointerDown={(event) => drag(event, index)}
              onKeyDown={(event) => {
                const to = event.key === "ArrowUp" ? index - 1 : event.key === "ArrowDown" ? index + 1 : null;
                if (to === null) return;
                event.preventDefault();
                if (move(index, to)) requestAnimationFrame(() => grips[to]?.focus());
              }}
            >
              <Icon name="grip-vertical" size={14} />
            </button>
            <span class="av-rule-n">{index + 1}</span>
            <Segmented
              size="sm"
              label="Action"
              value={rule().action}
              disabled={props.disabled}
              onChange={(action) => update(index, { action: action as RuleAction })}
              options={[
                { value: "allow", label: "Allow", icon: "check", class: "av-allow" },
                { value: "deny", label: "Deny", icon: "ban", class: "av-deny" },
              ]}
            />
            <Select
              size="sm"
              aria-label="Field"
              value={rule().field}
              options={FIELDS}
              disabled={props.disabled}
              onChange={(event) => {
                const field = event.currentTarget.value as RuleField;
                update(index, { field, op: (fieldOf(field).ops[0] as { value: RuleOp }).value });
              }}
            />
            <Select size="sm" aria-label="Operator" value={rule().op} options={fieldOf(rule().field).ops} disabled={props.disabled} onChange={(event) => update(index, { op: event.currentTarget.value as RuleOp })} />
            <Input
              size="sm"
              mono
              aria-label="Value"
              value={rule().value}
              placeholder={fieldOf(rule().field).placeholder}
              invalid={!!props.errors?.[index]}
              disabled={props.disabled}
              onInput={(event) => update(index, { value: event.currentTarget.value })}
            />
            <Button size="sm" variant="ghost" icon="trash-2" label={`Remove rule ${index + 1}`} disabled={props.disabled} onClick={() => props.onChange(props.rules.filter((_, i) => i !== index))} />
            <Show when={props.errors?.[index]}>
              <div class="av-rule-error av-field-error" role="alert">
                <Icon name="triangle-alert" size={12} />
                {props.errors?.[index]}
              </div>
            </Show>
          </div>
        )}
      </For>
      <div class="av-rule-fallback">
        <Icon name="arrow-up-down" size={12} />
        <span>
          Rules run top to bottom, first match wins. No match: <b>{props.fallback === "deny" ? "deny" : "allow"}</b>.
        </span>
      </div>
    </div>
  );
}
