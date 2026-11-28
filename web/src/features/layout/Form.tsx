import type { JSX } from "@solidjs/web";
import { Show } from "solid-js";

import { Button } from "../../ui";

import "./layout.css";

/* ---------- FormSection ---------- */

export interface FormSectionProps {
  title: string;
  /** One or two sentences on what the section is for. */
  description?: JSX.Element;
  /** Colours the title for a section of destructive actions. */
  danger?: boolean;
  /** Lets the body use the full width, for tables. */
  wide?: boolean;
  children: JSX.Element;
}

/** One titled block of a settings page: the title and its explanation on the left, the fields on the right. */
export function FormSection(props: FormSectionProps) {
  return (
    <section class={["av-formsec", { "av-formsec-danger": props.danger, "av-formsec-wide": props.wide }]}>
      <div class="av-formsec-head">
        <h2 class="av-formsec-title">{props.title}</h2>
        <Show when={props.description}>
          <p class="av-formsec-text">{props.description}</p>
        </Show>
      </div>
      <div class="av-formsec-body">{props.children}</div>
    </section>
  );
}

/* ---------- FormBar ---------- */

export interface FormBarProps {
  dirty: boolean;
  saving: boolean;
  /** The verb of the save: "Save rules", "Save settings". */
  saveLabel: string;
  onSave: () => void;
  onDiscard: () => void;
}

/** The bar under a form that saves explicitly: whether there are changes, Discard, and Save. It stays in view while the form scrolls. */
export function FormBar(props: FormBarProps) {
  return (
    <div class="av-formbar">
      <span class={["av-formbar-state", { "av-formbar-dirty": props.dirty }]} role="status">
        {props.dirty ? "Unsaved changes" : "No unsaved changes"}
      </span>
      <span class="av-spacer" />
      <Button disabled={!props.dirty || props.saving} onClick={() => props.onDiscard()}>
        Discard changes
      </Button>
      <Button variant="primary" disabled={!props.dirty} busy={props.saving} onClick={() => props.onSave()}>
        {props.saveLabel}
      </Button>
    </div>
  );
}
