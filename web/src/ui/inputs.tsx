import type { JSX } from "@solidjs/web";
import { NumberInput as NumberRoot, NumberInputControl, NumberInputGroup } from "@ui-sdk/solid/number-input";
import { For, omit, Show } from "solid-js";

import { Icon, type IconName } from "./icon";

/* ---------- Input ---------- */

export interface InputProps extends Omit<JSX.InputHTMLAttributes<HTMLInputElement>, "size"> {
  size?: "md" | "sm";
  /** Monospace: paths, globs, codec ids, numbers, ffmpeg arguments. */
  mono?: boolean;
  icon?: IconName;
  /** Unit inside the right edge, e.g. "kb/s". */
  suffix?: string;
  invalid?: boolean;
}

/** Room for a unit inside the right edge of an input: 12px mono is about 8px a character. */
function suffixPadding(suffix: string | undefined): JSX.CSSProperties | undefined {
  return suffix ? { "padding-right": `${20 + 8 * suffix.length}px` } : undefined;
}

/** A single-line text input. Units go in `suffix`, never in the value. */
export function Input(props: InputProps) {
  const rest = omit(props, "size", "mono", "icon", "suffix", "invalid", "class", "style");
  return (
    <div class="av-input-wrap">
      <Show when={props.icon}>{(name) => <Icon name={name()} size={16} />}</Show>
      <input
        {...rest}
        class={["av-input", props.class, { "av-input-sm": props.size === "sm", "av-mono": props.mono, "av-input-has-icon": !!props.icon }]}
        aria-invalid={props.invalid ? "true" : undefined}
        style={suffixPadding(props.suffix)}
      />
      <Show when={props.suffix}>
        <span class="av-input-suffix">{props.suffix}</span>
      </Show>
    </div>
  );
}

/* ---------- Textarea ---------- */

export interface TextareaProps extends JSX.TextareaHTMLAttributes<HTMLTextAreaElement> {
  mono?: boolean;
  invalid?: boolean;
}

/** A multi-line text input. Resizes vertically only. */
export function Textarea(props: TextareaProps) {
  const rest = omit(props, "mono", "invalid", "class");
  return <textarea rows={3} {...rest} class={["av-input", "av-textarea", props.class, { "av-mono": props.mono }]} aria-invalid={props.invalid ? "true" : undefined} />;
}

/* ---------- NumberInput ---------- */

export interface NumberInputProps {
  /** null is an empty field. */
  value: number | null;
  /** Runs as a typed number becomes valid. */
  onChange?: (value: number | null) => void;
  /** Runs once typing is committed (Enter or blur), after clamping to min and max. */
  onCommit?: (value: number | null) => void;
  min?: number;
  max?: number;
  step?: number;
  /** Whole numbers only. */
  integer?: boolean;
  suffix?: string;
  size?: "md" | "sm";
  invalid?: boolean;
  disabled?: boolean;
  placeholder?: string;
  id?: string;
  /** Required unless a Field label points at `id`. */
  label?: string;
}

/**
 * A numeric input: mono, plain digits, clamped to min and max on commit.
 * Arrow keys step; there are no spinner buttons.
 */
export function NumberInput(props: NumberInputProps) {
  return (
    <NumberRoot
      value={props.value}
      onValueChange={(value) => props.onChange?.(value)}
      onValueCommit={(value) => props.onCommit?.(value)}
      min={props.min}
      max={props.max}
      step={props.step ?? (props.integer ? 1 : undefined)}
      disabled={props.disabled}
      locale="en-US"
      format={{ useGrouping: false, maximumFractionDigits: props.integer ? 0 : 3 }}
    >
      <NumberInputGroup class="av-input-wrap">
        <NumberInputControl
          id={props.id}
          class={["av-input", "av-mono", { "av-input-sm": props.size === "sm" }]}
          aria-label={props.label}
          aria-invalid={props.invalid ? "true" : undefined}
          placeholder={props.placeholder}
          style={suffixPadding(props.suffix)}
        />
        <Show when={props.suffix}>
          <span class="av-input-suffix">{props.suffix}</span>
        </Show>
      </NumberInputGroup>
    </NumberRoot>
  );
}

/* ---------- Select ---------- */

export type SelectOption = string | { value: string; label: string; disabled?: boolean };

export interface SelectProps extends Omit<JSX.SelectHTMLAttributes<HTMLSelectElement>, "size" | "value"> {
  options: SelectOption[];
  value?: string;
  /** The option selected at first, when uncontrolled. */
  defaultValue?: string;
  size?: "md" | "sm";
  invalid?: boolean;
}

const optionValue = (option: SelectOption) => (typeof option === "string" ? option : option.value);
const optionLabel = (option: SelectOption) => (typeof option === "string" ? option : option.label);
const optionDisabled = (option: SelectOption) => typeof option !== "string" && !!option.disabled;

/** A native select styled like Input. Native on purpose: keyboard, mobile and screen readers work without extra code. */
export function Select(props: SelectProps) {
  const rest = omit(props, "options", "value", "defaultValue", "size", "invalid", "class");
  const current = () => props.value ?? props.defaultValue;
  return (
    <div class="av-select-wrap">
      <select {...rest} class={["av-select", props.class, { "av-select-sm": props.size === "sm" }]} aria-invalid={props.invalid ? "true" : undefined}>
        <For each={props.options} keyed={optionValue}>
          {(option) => (
            <option value={optionValue(option())} disabled={optionDisabled(option())} selected={optionValue(option()) === current()}>
              {optionLabel(option())}
            </option>
          )}
        </For>
      </select>
      <Icon name="chevron-down" size={16} />
    </div>
  );
}

/* ---------- Checkbox ---------- */

export interface CheckboxProps extends Omit<JSX.InputHTMLAttributes<HTMLInputElement>, "type"> {
  indeterminate?: boolean;
}

/** A native checkbox. Give it an aria-label when no visible label points at it. */
export function Checkbox(props: CheckboxProps) {
  const rest = omit(props, "indeterminate", "class");
  return <input type="checkbox" {...rest} class={["av-check", props.class]} prop:indeterminate={!!props.indeterminate} />;
}
