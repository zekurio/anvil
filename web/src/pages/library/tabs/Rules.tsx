import { createSignal, Show } from "solid-js";

import { api, keys, type ApiError, type Library, type Rule, type RuleAction } from "../../../api";
import { FormBar } from "../../../features/layout/Form";
import { toWrite } from "../../../features/libraries/library";
import { createDraft } from "../../../lib/draft";
import { useUnsavedChanges } from "../../../lib/guard";
import { run } from "../../../lib/mutate";
import { Banner, Button, Field, RuleList, Segmented } from "../../../ui";

interface RulesDraft {
  rules: Rule[];
  rule_fallback: RuleAction;
}

const NEW_RULE: Rule = { action: "deny", field: "path", op: "matches", value: "" };

/**
 * The Rules tab: which files a scan may queue. Rules change what anvil
 * touches, so nothing is saved until "Save rules" is pressed.
 */
export function RulesTab(props: { library: Library }) {
  const draft = createDraft<RulesDraft>(() => ({ rules: props.library.rules, rule_fallback: props.library.rule_fallback }));
  const [errors, setErrors] = createSignal<Record<number, string>>({});
  const [general, setGeneral] = createSignal<string | null>(null);
  const [saving, setSaving] = createSignal(false);
  useUnsavedChanges(draft.dirty, { title: "Discard the unsaved rules?", description: "The rule changes made since the last save are lost." });

  const clearErrors = () => {
    setErrors({});
    setGeneral(null);
  };
  const change = (patch: Partial<RulesDraft>) => {
    // A reported problem belongs to the rules as they were sent; an edit may have moved or fixed it.
    clearErrors();
    draft.patch(patch);
  };

  const save = async () => {
    setSaving(true);
    clearErrors();
    const saved = await run(() => api.libraries.update(props.library.name, { ...toWrite(props.library), ...draft.value() }), {
      invalidate: [keys.libraries],
      success: `Saved the rules of ${props.library.name}. A scan with the new rules starts by itself.`,
      failure: "The rules could not be saved.",
      onError: (error: ApiError) => {
        // The daemon names the rule at fault as "rules.<index>.<field>".
        const index = /^rules\.(\d+)(\.|$)/.exec(error.field ?? "")?.[1];
        if (index !== undefined) setErrors({ [Number(index)]: error.message });
        else if (error.field === "rules" || error.field === "rule_fallback") setGeneral(error.message);
        else return false;
        return true;
      },
    });
    setSaving(false);
    if (saved) draft.reset();
  };

  const fallback = () => draft.value().rule_fallback;
  const count = () => draft.value().rules.length;

  return (
    <div class="av-page-body av-surface">
      <div class="av-rulespage">
        <p class="av-rules-intro">A scan checks every file against these rules, top to bottom. The first rule that matches decides whether the file is queued. Paths are relative to the library folder.</p>
        <p class="av-rules-intro">
          A denied file is never queued, and the Files tab does not list it unless anvil already knew the file. A file that was waiting in the queue when a rule denied it is skipped by the next scan, with the rule as its reason. A path rule that denies a
          whole folder takes the files below it off the list. A job that has already started is not stopped.
        </p>
        <Show when={general()}>{(message) => <Banner tone="danger">{message()}</Banner>}</Show>
        <RuleList rules={draft.value().rules} fallback={fallback()} errors={errors()} disabled={saving()} onChange={(rules) => change({ rules })} />
        <div class="av-row">
          <Button icon="plus" disabled={saving()} onClick={() => change({ rules: [...draft.value().rules, { ...NEW_RULE }] })}>
            Add rule
          </Button>
        </div>
        <Field label="Files matching no rule" hint={fallbackSentence(fallback(), count())}>
          <div>
            <Segmented
              label="Files matching no rule"
              value={fallback()}
              disabled={saving()}
              onChange={(value) => change({ rule_fallback: value as RuleAction })}
              options={[
                { value: "allow", label: "Queue them", icon: "check", class: "av-allow" },
                { value: "deny", label: "Never queue them", icon: "ban", class: "av-deny" },
              ]}
            />
          </div>
        </Field>
      </div>
      <FormBar dirty={draft.dirty()} saving={saving()} saveLabel="Save rules" onSave={save} onDiscard={() => (clearErrors(), draft.reset())} />
    </div>
  );
}

/** States the outcome when nothing matches, and what that means with the rules there are. */
function fallbackSentence(fallback: RuleAction, count: number): string {
  if (fallback === "deny") return count === 0 ? "Files matching no rule are never queued. With no rule, nothing in this library is queued." : "Files matching no rule are never queued.";
  return count === 0 ? "Files matching no rule are queued. With no rule, every file a scan finds is queued." : "Files matching no rule are queued.";
}
