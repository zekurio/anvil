// The library settings the Add library dialog and the Settings tab share:
// whether a file is queued again, and how a scan takes in what a downloader
// writes. The daemon checks the values (pkg/config LibraryConfig.Problems).

import type { LibraryWrite, PackageMode } from "../../api";
import { Field, Input, Select, Switch, TokenInput } from "../../ui";

export type IntakeValues = Pick<LibraryWrite, "stable_for" | "package_mode" | "ignorable_globs">;
export type IntakeField = keyof IntakeValues;

const PACKAGE_MODES: { value: PackageMode; label: string }[] = [
  { value: "file", label: "Each file on its own" },
  { value: "directory", label: "Each top-level folder" },
  { value: "auto", label: "Auto" },
];

export function ReprocessField(props: { id: string; checked: boolean; error?: string; onChange: (checked: boolean) => void }) {
  return (
    <Field inline label="Reprocess files" htmlFor={props.id} hint="Queues a file that changed or was written again at a path seen before. Download folders need this." error={props.error}>
      <Switch id={props.id} label="Reprocess files" checked={props.checked} onChange={(checked) => props.onChange(checked)} />
    </Field>
  );
}

/** Quiet period, package mode and ignorable globs. `ids` names the control of each request field. */
export function IntakeFields(props: { ids: Record<IntakeField, string>; value: IntakeValues; errors: Partial<Record<string, string>>; onChange: (change: Partial<IntakeValues>, field: IntakeField) => void }) {
  return (
    <>
      <div class="av-grid-2">
        <Field label="Quiet period" htmlFor={props.ids.stable_for} hint="Time without changes before a file is queued, when no finished write was seen. Empty uses 5m, 0s queues at once." error={props.errors.stable_for}>
          <Input
            id={props.ids.stable_for}
            mono
            autocomplete="off"
            spellcheck={false}
            placeholder="5m"
            invalid={!!props.errors.stable_for}
            value={props.value.stable_for}
            onInput={(event) => props.onChange({ stable_for: event.currentTarget.value.trim() }, "stable_for")}
          />
        </Field>
        <Field label="Package mode" htmlFor={props.ids.package_mode} hint="A package is one download. Auto works like Each top-level folder." error={props.errors.package_mode}>
          <Select
            id={props.ids.package_mode}
            value={props.value.package_mode}
            invalid={!!props.errors.package_mode}
            options={PACKAGE_MODES}
            onChange={(event) => props.onChange({ package_mode: event.currentTarget.value as PackageMode }, "package_mode")}
          />
        </Field>
      </div>
      <Field label="Ignorable globs" htmlFor={props.ids.ignorable_globs} hint="Files that never hold a download back. Removing emptied folders may delete them. Empty uses the built-in list." error={props.errors.ignorable_globs}>
        <TokenInput id={props.ids.ignorable_globs} mono placeholder="**/*.nfo" invalid={!!props.errors.ignorable_globs} value={props.value.ignorable_globs} onChange={(globs) => props.onChange({ ignorable_globs: globs }, "ignorable_globs")} />
      </Field>
    </>
  );
}
