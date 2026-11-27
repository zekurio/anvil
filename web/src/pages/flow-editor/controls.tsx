// One control per FieldType of pkg/flow/catalog.go. The form knows no node
// type: which control a field gets, its bounds, choices, unit and hint all
// come from the catalog.

import type { Component } from "solid-js";
import { For } from "solid-js";

import type { Field as CatalogField, FieldType, OptionValue } from "../../api";
import { Checkbox, Input, NumberInput, Segmented, Select, Switch, TokenInput } from "../../ui";

export interface ControlProps {
  field: CatalogField;
  /** The stored option, or the field's default when the node has none. */
  value: OptionValue;
  /** id of the element a label points at and focus goes to. */
  id: string;
  invalid: boolean;
  /** Whether the field shares its row with another. */
  half: boolean;
  onChange: (value: OptionValue) => void;
}

const text = (value: OptionValue) => (typeof value === "string" ? value : "");
const list = (value: OptionValue) => (Array.isArray(value) ? value : []);

function TextControl(props: ControlProps) {
  return (
    <Input
      id={props.id}
      mono={props.field.mono || props.field.type === "duration"}
      placeholder={props.field.placeholder}
      suffix={props.field.suffix}
      invalid={props.invalid}
      autocomplete="off"
      spellcheck={false}
      value={text(props.value)}
      onInput={(event) => props.onChange(event.currentTarget.value)}
    />
  );
}

function NumberControl(props: ControlProps) {
  // An emptied field is stored as null, which the daemon answers with what a valid value is.
  return (
    <NumberInput
      id={props.id}
      value={typeof props.value === "number" ? props.value : null}
      min={props.field.min}
      max={props.field.max}
      integer={props.field.type === "int"}
      suffix={props.field.suffix}
      placeholder={props.field.placeholder}
      invalid={props.invalid}
      onChange={(value) => props.onChange(value)}
      onCommit={(value) => props.onChange(value)}
    />
  );
}

function BoolControl(props: ControlProps) {
  return <Switch id={props.id} label={props.field.label} checked={props.value === true} onChange={(next) => props.onChange(next)} />;
}

/** Roughly the px a segmented control needs: 13px medium text, 8px padding either side of each label. */
function segmentedWidth(labels: string[]): number {
  return labels.reduce((width, label) => width + label.length * 7.5 + 18, 6);
}

function EnumControl(props: ControlProps) {
  const choices = () => props.field.choices ?? [];
  // The sheet is 400px wide: 368px for a field on its own row, 178px for one of a pair.
  const segmented = () => props.field.segmented && segmentedWidth(choices().map((choice) => choice.label)) <= (props.half ? 178 : 368);
  return (
    <>
      {segmented() ? (
        <Segmented id={props.id} label={props.field.label} options={choices()} value={text(props.value)} onChange={(value) => props.onChange(value)} />
      ) : (
        <Select id={props.id} class={{ "av-mono": props.field.mono }} options={choices()} value={text(props.value)} invalid={props.invalid} onChange={(event) => props.onChange(event.currentTarget.value)} />
      )}
    </>
  );
}

function EnumListControl(props: ControlProps) {
  const chosen = () => list(props.value);
  // Stored in catalog order whatever order the boxes were ticked in, as the daemon reads the list.
  const toggle = (value: string) => props.onChange((props.field.choices ?? []).map((choice) => choice.value).filter((candidate) => (candidate === value ? !chosen().includes(value) : chosen().includes(candidate))));
  return (
    <div id={props.id} class={["fe-checks", { "fe-checks-invalid": props.invalid }]} role="group" aria-label={props.field.label}>
      <For each={props.field.choices ?? []} keyed={(choice) => choice.value}>
        {(choice) => (
          <label class="fe-check">
            <Checkbox checked={chosen().includes(choice().value)} onChange={() => toggle(choice().value)} />
            <span class="fe-check-label">{choice().label}</span>
            {/* The id the daemon and ffprobe use, when the label is a friendlier name for it. */}
            {props.field.mono && choice().label.toLowerCase() !== choice().value ? <span class="fe-check-id">{choice().value}</span> : null}
          </label>
        )}
      </For>
    </div>
  );
}

function StringListControl(props: ControlProps) {
  return <TokenInput id={props.id} mono={props.field.mono} placeholder={props.field.placeholder} invalid={props.invalid} value={list(props.value)} onChange={(next) => props.onChange(next)} />;
}

export const CONTROLS: Record<FieldType, Component<ControlProps>> = {
  string: TextControl,
  int: NumberControl,
  float: NumberControl,
  bool: BoolControl,
  enum: EnumControl,
  enum_list: EnumListControl,
  string_list: StringListControl,
  // A Go duration is typed as text; the form checks its shape and the daemon its range.
  duration: TextControl,
};
