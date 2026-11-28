import { SegmentedControl, SegmentedControlItem } from "@ui-sdk/solid/segmented-control";
import { Switch as SwitchRoot } from "@ui-sdk/solid/switch";
import { Tabs as TabsRoot, TabsList, TabsTab } from "@ui-sdk/solid/tabs";
import { For, Show } from "solid-js";

import { Icon, type IconName } from "./icon";

/* ---------- Switch ---------- */

export interface SwitchProps {
  checked?: boolean;
  defaultChecked?: boolean;
  onChange?: (next: boolean) => void;
  /** aria-label. Required: the visible label lives in the Field. */
  label: string;
  disabled?: boolean;
  id?: string;
}

/** An on/off setting that takes effect as soon as it flips. Place it in a `Field inline`. */
export function Switch(props: SwitchProps) {
  return (
    <SwitchRoot
      class="av-switch"
      id={props.id}
      aria-label={props.label}
      checked={props.checked}
      defaultChecked={props.defaultChecked}
      disabled={props.disabled}
      onCheckedChange={(next) => props.onChange?.(next)}
    />
  );
}

/* ---------- Segmented ---------- */

export interface SegmentedOption {
  value: string;
  label: string;
  icon?: IconName;
  /** "av-allow" / "av-deny" colour the chosen label ok / danger. */
  class?: string;
}

export interface SegmentedProps {
  options: SegmentedOption[];
  value?: string;
  defaultValue?: string;
  onChange?: (value: string) => void;
  /** Group aria-label. */
  label?: string;
  size?: "md" | "sm";
  disabled?: boolean;
  id?: string;
}

/** Two to five mutually exclusive choices, all visible. Arrow keys move between them. */
export function Segmented(props: SegmentedProps) {
  return (
    <SegmentedControl
      class={["av-seg", { "av-seg-sm": props.size === "sm" }]}
      id={props.id}
      aria-label={props.label}
      value={props.value}
      defaultValue={props.defaultValue ?? (props.value === undefined ? props.options[0]?.value : undefined)}
      disabled={props.disabled}
      onValueChange={(value) => {
        if (value !== null) props.onChange?.(value);
      }}
    >
      <For each={props.options} keyed={(option) => option.value}>
        {(option) => (
          <SegmentedControlItem value={option().value} class={option().class}>
            <Show when={option().icon}>{(name) => <Icon name={name()} size={14} />}</Show>
            {option().label}
          </SegmentedControlItem>
        )}
      </For>
    </SegmentedControl>
  );
}

/* ---------- Tabs ---------- */

export interface TabItem {
  id: string;
  label: string;
  count?: number | string;
  /** Makes the tab a link, so the selected tab lives in the URL. */
  href?: string;
}

export interface TabsProps {
  tabs: TabItem[];
  value?: string;
  defaultValue?: string;
  onChange?: (id: string) => void;
  /** aria-label of the tab list: the object whose views these are. */
  label?: string;
}

function TabBody(props: { tab: TabItem }) {
  return (
    <>
      {props.tab.label}
      <Show when={props.tab.count != null}>
        <span class="av-tab-count">{props.tab.count}</span>
      </Show>
    </>
  );
}

/**
 * Switches between views of one object. This is the tab list only; the page
 * renders the selected view. With `href` tabs, derive `value` from the route.
 */
export function Tabs(props: TabsProps) {
  return (
    <TabsRoot
      value={props.value}
      defaultValue={props.defaultValue}
      onValueChange={(id, details) => {
        if (id === null) return;
        // A tab that is a link changes the address, and `value` follows the
        // route. The press is not the change: a route guard may still refuse it.
        if (props.tabs.find((tab) => tab.id === id)?.href) return details.cancel();
        props.onChange?.(id);
      }}
    >
      <TabsList class="av-tabs" aria-label={props.label}>
        <For each={props.tabs} keyed={(tab) => tab.id}>
          {(tab) => (
            <Show
              when={tab().href}
              fallback={
                <TabsTab class="av-tab" value={tab().id}>
                  <TabBody tab={tab()} />
                </TabsTab>
              }
            >
              {(href) => (
                <TabsTab class="av-tab" value={tab().id} render={(part) => <a {...part} href={href()} />}>
                  <TabBody tab={tab()} />
                </TabsTab>
              )}
            </Show>
          )}
        </For>
      </TabsList>
    </TabsRoot>
  );
}
