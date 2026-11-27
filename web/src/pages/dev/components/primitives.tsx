import { createSignal, createUniqueId, For } from "solid-js";

import { Button, Checkbox, Field, Icon, ICONS, Input, LinkButton, Logo, NumberInput, Segmented, Select, Switch, Tabs, Textarea, TokenInput } from "../../../ui";
import { Item, Section } from "./parts";

const COLORS = ["bg", "surface", "surface-raised", "surface-sunken", "surface-hover", "line", "line-control", "text", "text-muted", "text-disabled", "accent", "accent-hover", "accent-soft", "ok", "ok-soft", "danger", "danger-soft", "warn", "warn-soft", "neutral-soft"];

const TYPE = [
  ["heading", "Movies"],
  ["title", "Encode video"],
  ["body", "Files matching a Deny rule are never queued."],
  ["body-strong", "Scan library"],
  ["small", "Arrival (2016).mkv"],
  ["label", "Global quality"],
  ["caption", "Slower presets give smaller files at the same quality."],
  ["mono", "/media/movies/Arrival (2016)/Arrival (2016).mkv"],
  ["mono-small", "libsvtav1 · vmaf 95 · preset 6"],
] as const;

export function Foundations() {
  return (
    <Section id="foundations" title="Foundations">
      <Item title="Logo">
        <div class="av-row" style={{ gap: "var(--space-5)" }}>
          <Logo variant="wordmark" size={28} />
          <Logo size={28} />
          <span style={{ color: "var(--accent)", display: "flex" }}>
            <Logo size={28} />
          </span>
        </div>
      </Item>
      <Item title="Color">
        <div class="gal-swatches">
          <For each={COLORS}>
            {(name) => (
              <div>
                <div class="gal-swatch" style={{ background: `var(--${name})` }} />
                {name}
              </div>
            )}
          </For>
        </div>
      </Item>
      <Item title="Type">
        <div class="av-stack" style={{ gap: "var(--space-2)" }}>
          <For each={TYPE}>
            {([style, sample]) => (
              <div class="av-row" style={{ "flex-wrap": "nowrap" }}>
                <span class="mono-small av-muted" style={{ width: "96px", flex: "none" }}>
                  {style}
                </span>
                <span class={style}>{sample}</span>
              </div>
            )}
          </For>
        </div>
      </Item>
      <Item title="Icon">
        <div class="gal-icons">
          <For each={ICONS}>
            {(name) => (
              <div>
                <Icon name={name} />
                {name}
              </div>
            )}
          </For>
        </div>
      </Item>
    </Section>
  );
}

export function Primitives() {
  const id = createUniqueId();
  const [quality, setQuality] = createSignal<number | null>(22);
  const [languages, setLanguages] = createSignal(["eng", "jpn"]);
  const [args, setArgs] = createSignal(["-g 240", "-svtav1-params tune=0"]);
  const [tab, setTab] = createSignal("files");
  const [checked, setChecked] = createSignal(true);

  return (
    <Section id="primitives" title="Primitives">
      <Item title="Button">
        <div class="av-stack">
          <div class="av-row">
            <Button variant="primary" icon="play">
              Process 312 files
            </Button>
            <Button icon="refresh-cw">Scan library</Button>
            <Button variant="ghost" icon="play">
              Test on a file
            </Button>
            <Button variant="danger" icon="trash-2">
              Delete node
            </Button>
            <Button icon="ellipsis" label="More actions" />
            <LinkButton href="#primitives" icon="plus">
              New flow
            </LinkButton>
          </div>
          <div class="av-row">
            <Button size="sm" variant="primary">
              Queue 2 files
            </Button>
            <Button size="sm" icon="play">
              Queue
            </Button>
            <Button size="sm" variant="ghost" icon="copy">
              Duplicate
            </Button>
            <Button size="sm" variant="danger" icon="trash-2">
              Delete node
            </Button>
            <Button size="sm" variant="ghost" icon="x" label="Close" />
          </div>
          <div class="av-row">
            <Button variant="primary" disabled>
              Save flow
            </Button>
            <Button disabled>Discard</Button>
            <Button variant="ghost" disabled>
              Test on a file
            </Button>
            <Button variant="primary" busy>
              Saving flow
            </Button>
            <Button busy icon="refresh-cw">
              Scan library
            </Button>
          </div>
        </div>
      </Item>

      <Item title="Input, Textarea, NumberInput, Select">
        <div class="gal-grid">
          <Field label="Label" htmlFor={`${id}-label`}>
            <Input id={`${id}-label`} value="Encode video" />
          </Field>
          <Field label="Filter" htmlFor={`${id}-filter`}>
            <Input id={`${id}-filter`} icon="search" placeholder="Filter by name or path" />
          </Field>
          <Field label="Bitrate" htmlFor={`${id}-bitrate`} hint="Units go in the suffix, never in the value.">
            <Input id={`${id}-bitrate`} mono suffix="kb/s" value="6000" />
          </Field>
          <Field label="Glob" htmlFor={`${id}-glob`} error="Must be a glob such as **/Extras/**.">
            <Input id={`${id}-glob`} mono invalid value="**/Extras/[" />
          </Field>
          <Field label="Global quality" htmlFor={`${id}-quality`} hint={`1–51. Lower is better quality and larger files. Value: ${quality() ?? "empty"}`}>
            <NumberInput id={`${id}-quality`} integer min={1} max={51} value={quality()} onChange={setQuality} />
          </Field>
          <Field label="Smallest saving" htmlFor={`${id}-saving`}>
            <NumberInput id={`${id}-saving`} min={0} max={90} suffix="%" value={5} />
          </Field>
          <Field label="Preset" htmlFor={`${id}-preset`} hint="Slower presets give smaller files at the same quality.">
            <Select id={`${id}-preset`} defaultValue="slow" options={["veryfast", "faster", "medium", "slow", "veryslow"]} />
          </Field>
          <Field label="Codec" htmlFor={`${id}-codec`}>
            <Select id={`${id}-codec`} invalid defaultValue="av1" options={[{ value: "av1", label: "AV1" }, { value: "hevc", label: "HEVC (H.265)" }, { value: "h264", label: "H.264", disabled: true }]} />
          </Field>
          <Field label="Compact" htmlFor={`${id}-sm`}>
            <div class="av-row av-row-nowrap">
              <Input id={`${id}-sm`} size="sm" mono value="mkv, mp4" />
              <Select size="sm" aria-label="Operator" options={["is", "is not"]} />
            </div>
          </Field>
          <Field label="Disabled" htmlFor={`${id}-disabled`}>
            <Input id={`${id}-disabled`} disabled value="/media/movies" />
          </Field>
          <Field label="Description" htmlFor={`${id}-description`}>
            <Textarea id={`${id}-description`} value="Encodes everything that is not AV1 yet." />
          </Field>
        </div>
      </Item>

      <Item title="TokenInput">
        <div class="gal-grid">
          <Field label="Languages" htmlFor={`${id}-languages`} hint="ISO 639-2 tags. Enter or a comma adds one.">
            <TokenInput id={`${id}-languages`} mono separators={[","]} value={languages()} onChange={setLanguages} placeholder="eng" />
          </Field>
          <Field label="Extra ffmpeg arguments" htmlFor={`${id}-args`} hint="Enter adds one. Commas are kept.">
            <TokenInput id={`${id}-args`} mono repeats value={args()} onChange={setArgs} placeholder="-g" />
          </Field>
          <Field label="Empty and invalid" htmlFor={`${id}-empty`} error="List at least one extension.">
            <TokenInput id={`${id}-empty`} invalid value={[]} onChange={() => {}} placeholder="mkv" />
          </Field>
        </div>
      </Item>

      <Item title="Switch, Checkbox, Segmented, Field inline">
        <div class="gal-grid">
          <div class="av-stack">
            <Field inline label="Hardware decoding" hint="Only applies to supported codecs.">
              <Switch label="Hardware decoding" defaultChecked />
            </Field>
            <Field inline label="Tone-map HDR to SDR">
              <Switch label="Tone-map HDR to SDR" />
            </Field>
            <Field inline label="Paused" hint="Disabled.">
              <Switch label="Paused" disabled />
            </Field>
          </div>
          <div class="av-stack">
            <label class="av-row">
              <Checkbox checked={checked()} onChange={(event) => setChecked(event.currentTarget.checked)} /> Checked
            </label>
            <label class="av-row">
              <Checkbox /> Unchecked
            </label>
            <label class="av-row">
              <Checkbox indeterminate /> Indeterminate
            </label>
          </div>
          <div class="av-stack">
            <Segmented label="Rate control" defaultValue="q" options={[{ value: "q", label: "Constant quality" }, { value: "br", label: "Target bitrate" }]} />
            <Segmented size="sm" label="Status" options={[{ value: "all", label: "All" }, { value: "pending", label: "Pending" }, { value: "failed", label: "Failed" }, { value: "done", label: "Done" }]} />
            <Segmented size="sm" label="Action" defaultValue="deny" options={[{ value: "allow", label: "Allow", icon: "check", class: "av-allow" }, { value: "deny", label: "Deny", icon: "ban", class: "av-deny" }]} />
          </div>
        </div>
      </Item>

      <Item title="Tabs" flush>
        <div style={{ "padding-top": "var(--space-2)" }}>
          <Tabs
            label="Views of movies"
            value={tab()}
            onChange={setTab}
            tabs={[
              { id: "files", label: "Files", count: "4,812" },
              { id: "rules", label: "Rules", count: 5 },
              { id: "history", label: "History" },
              { id: "settings", label: "Settings" },
            ]}
          />
        </div>
      </Item>
    </Section>
  );
}
