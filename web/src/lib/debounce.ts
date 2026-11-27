import { createSignal, onSettled, type Accessor } from "solid-js";

/**
 * A text filter that queries only once typing pauses. `text` follows every
 * keystroke and belongs in the input; `settled` follows `delay` ms later and
 * belongs in the query key.
 */
export function createDebounced(delay = 250): { text: Accessor<string>; settled: Accessor<string>; set: (value: string) => void } {
  const [text, setText] = createSignal("");
  const [settled, setSettled] = createSignal("");
  let timer: ReturnType<typeof setTimeout> | undefined;

  onSettled(() => () => clearTimeout(timer));

  return {
    text,
    settled,
    set: (value) => {
      setText(value);
      clearTimeout(timer);
      // Clearing the field should not wait.
      if (value.trim() === "") return void setSettled("");
      timer = setTimeout(() => setSettled(value.trim()), delay);
    },
  };
}
