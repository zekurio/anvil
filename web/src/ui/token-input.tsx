import { createSignal, For, Show } from "solid-js";

import { Icon } from "./icon";

export interface TokenInputProps {
  /** The list being edited: language tags, globs, codec ids, ffmpeg arguments. */
  value: string[];
  onChange: (next: string[]) => void;
  /**
   * Characters that end an entry while typing, next to Enter. Default: none,
   * so an entry may hold any character (an ffmpeg argument, a glob with
   * braces). Pass [","] for lists of simple tokens such as language tags.
   */
  separators?: string[];
  /**
   * Keeps an entry that is already listed. Off by default: most lists are
   * sets. ffmpeg arguments are not, and "-g 240 -keyint_min 240" needs both.
   */
  repeats?: boolean;
  mono?: boolean;
  invalid?: boolean;
  disabled?: boolean;
  placeholder?: string;
  /** id of the text input, for a Field label. */
  id?: string;
  /** Required unless a Field label points at `id`. */
  label?: string;
}

/**
 * Edits a list of short strings. Enter or a separator adds what was typed,
 * Backspace in the empty input removes the last item, and each item has its
 * own Remove button in the tab order. Pasted text is split at line breaks,
 * and at the separators when there are any (a list of tokens also splits at
 * spaces). Built by hand: ui-sdk's Token Input is a rich-text editor, which a
 * plain string list does not need.
 */
export function TokenInput(props: TokenInputProps) {
  const [draft, setDraft] = createSignal("");
  let input: HTMLInputElement | undefined;

  const separators = () => props.separators ?? [];

  /** Splits typed text at the separators; pasted text also at line breaks, and at spaces in a list of tokens. */
  const split = (text: string, pasted = false): string[] => {
    let parts = pasted ? text.split(/\r?\n/) : [text];
    for (const separator of separators()) parts = parts.flatMap((part) => part.split(separator));
    if (pasted && separators().length > 0) parts = parts.flatMap((part) => part.split(/\s+/));
    return parts.map((part) => part.trim()).filter(Boolean);
  };

  /** Adds the entries in `text`. Ones already listed are skipped unless the list keeps repeats. */
  const add = (text: string, pasted = false) => {
    const next = [...props.value];
    for (const token of split(text, pasted)) if (props.repeats || !next.includes(token)) next.push(token);
    if (next.length !== props.value.length) props.onChange(next);
    setDraft("");
    if (input) input.value = "";
  };

  const remove = (index: number) => {
    props.onChange(props.value.filter((_, i) => i !== index));
    input?.focus();
  };

  return (
    <div
      class={["av-tokens", { "av-mono": props.mono, "av-tokens-invalid": props.invalid, "av-tokens-disabled": props.disabled }]}
      onMouseDown={(event) => {
        // A press on the padding lands in the input, as in a plain text field.
        if (event.target === event.currentTarget) {
          event.preventDefault();
          input?.focus();
        }
      }}
    >
      <For each={props.value}>
        {(token, index) => (
          <span class="av-token">
            <span class="av-token-text">{token}</span>
            <button type="button" class="av-token-x" aria-label={`Remove ${token}`} disabled={props.disabled} onClick={() => remove(index())}>
              <Icon name="x" size={12} />
            </button>
          </span>
        )}
      </For>
      <input
        ref={(el) => (input = el)}
        id={props.id}
        class="av-tokens-input"
        type="text"
        autocomplete="off"
        spellcheck={false}
        aria-label={props.label}
        aria-invalid={props.invalid ? "true" : undefined}
        disabled={props.disabled}
        placeholder={props.value.length === 0 ? props.placeholder : undefined}
        value={draft()}
        onInput={(event) => {
          const text = event.currentTarget.value;
          if (separators().some((separator) => text.includes(separator))) add(text);
          else setDraft(text);
        }}
        onPaste={(event) => {
          // A single pasted line goes into the input like typed text; several lines are several entries.
          const text = event.clipboardData?.getData("text") ?? "";
          const whole = draft() + text;
          if (!/\r?\n/.test(text) && !separators().some((separator) => text.includes(separator))) return;
          event.preventDefault();
          add(whole, true);
        }}
        onKeyDown={(event) => {
          if (event.key === "Enter" && draft().trim() !== "") {
            event.preventDefault();
            add(draft());
          } else if (event.key === "Backspace" && event.currentTarget.value === "" && props.value.length > 0) {
            event.preventDefault();
            remove(props.value.length - 1);
          }
        }}
        onBlur={() => {
          if (draft().trim() !== "") add(draft());
        }}
      />
      {/* Says how what is being typed becomes an entry; leaving the field adds it too. */}
      <Show when={draft().trim() !== ""}>
        <span class="av-tokens-hint" aria-hidden="true">
          {separators().length > 0 ? "Enter or comma adds it" : "Enter adds it"}
        </span>
      </Show>
    </div>
  );
}
