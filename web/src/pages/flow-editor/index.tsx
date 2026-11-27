import { useNavigate, useParams } from "@solidjs/router";
import { createMemo, createSignal, flush, isPending, onSettled, Show } from "solid-js";

import "./editor.css";

import { api, createQuery, keys, type ApiError, type FlowWrite, type Issue } from "../../api";
import { useUnsavedChanges } from "../../lib/guard";
import { run } from "../../lib/mutate";
import { useTitle } from "../../lib/title";
import { useAppData } from "../../shell/data";
import { Banner, Button, confirm, Dialog, Field, FlowNode, FolderPicker, Input, NodePalette, Textarea, toast, type PaletteDrag, type PaletteItem } from "../../ui";
import { EditorCanvas } from "./Canvas";
import { createEditor } from "./editor";
import { EditorTopbar } from "./EditorTopbar";
import type { Doc } from "./graph";
import { carryOver, graphOf, loadEditor, type Loaded } from "./load";
import { NodeSheet, type FieldFocus } from "./NodeSheet";
import { TestSheet } from "./TestSheet";

/**
 * The flow editor: /flows/new and /flows/:id. Full-bleed, without the app
 * Sidebar; the top bar links back to the list.
 */
export default function FlowEditorPage() {
  const params = useParams();
  return (
    // Keyed: another flow is another editor, with its own draft and history.
    <Show when={params.id} keyed fallback={<Session id={null} />}>
      {(id) => <Session id={Number(id)} />}
    </Show>
  );
}

function Session(props: { id: number | null }) {
  const id = props.id;
  // Reloading after a conflict asks again; the key keeps this query out of reach of invalidate(keys.flows),
  // which must not replace the draft under the person editing it.
  const [attempt, setAttempt] = createSignal(0);
  const loaded = createQuery(
    () => ["flow-editor", id, attempt()],
    () => loadEditor(id),
  );
  return (
    <>
      {/* A reload keeps the old editor on screen until the flow has arrived; this line says it is on its way. */}
      <Show when={isPending(() => loaded())}>
        <div class="fe-reloading" role="status" aria-label="Reloading the flow" />
      </Show>
      <Show when={loaded()} keyed>
        {(data) => <Editor data={data} onReload={() => setAttempt((n) => n + 1)} />}
      </Show>
    </>
  );
}

const dirname = (path: string) => path.slice(0, Math.max(path.lastIndexOf("/"), 0));

/** Grid steps an arrow key moves a node by, as [x, y]. */
const ARROWS: Record<string, readonly [number, number] | undefined> = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] };

function Editor(props: { data: Loaded; onReload: () => void }) {
  const data = props.data;
  const flowId = data.flow?.id ?? null;
  const isNew = flowId === null;
  const editor = createEditor({ flowId, doc: data.doc, saved: data.saved, catalog: data.catalog, seed: data.seed, handoff: data.handoff });
  const draft = editor.draft;
  const navigate = useNavigate();
  const { flows, libraries } = useAppData();

  let revision = data.flow?.revision ?? 0;
  const [saving, setSaving] = createSignal(false);
  const [nameError, setNameError] = createSignal<string | null>(null);
  /** Someone else saved the flow: this draft cannot be saved over their version. */
  const [outdated, setOutdated] = createSignal(false);
  /** Set once the page is left on purpose (after a save or a discard), so the guard does not ask. */
  const [leaving, setLeaving] = createSignal(false);
  const [issuesOpen, setIssuesOpen] = createSignal(false);
  const [detailsOpen, setDetailsOpen] = createSignal(false);
  const [picking, setPicking] = createSignal(false);
  const [focus, setFocus] = createSignal<FieldFocus | null>(null);

  useTitle(() => draft.name() || "Flow");
  // A new flow is unsaved from the start, but leaving it untouched loses nothing.
  useUnsavedChanges(() => !leaving() && (isNew ? draft.touched() : draft.changes() > 0));

  // The polled list knows about libraries assigned after the editor opened.
  const usedBy = createMemo(() => flows().find((flow) => flow.id === flowId)?.libraries ?? data.flow?.libraries ?? []);

  /** Where the file picker opens: beside the last tested file, else in a library this flow is for, else in any library. */
  const testFolder = () => {
    const last = editor.test()?.path || editor.lastTestPath();
    if (last) return dirname(last);
    const known = libraries();
    return (known.find((library) => library.flow_id === flowId) ?? known[0])?.path ?? "";
  };

  /* ---------- save ---------- */

  /** Offers the way out of a conflict. Reloading is the only step that loses work, so it is the one that asks. */
  const askReload = async () => {
    const agreed = await confirm({
      title: "This flow was changed elsewhere",
      description: "Someone saved it after you opened it. Reload to get their version and lose your changes, or keep editing and save as a new flow.",
      confirmLabel: "Reload flow",
      cancelLabel: "Keep editing",
      danger: true,
    });
    if (agreed) props.onReload();
  };

  const refused = (error: ApiError): boolean => {
    if (error.field === "name") {
      setNameError(error.message);
      document.querySelector<HTMLInputElement>(".fe-name-input")?.focus();
      return true;
    }
    if (error.code === "conflict") {
      setOutdated(true);
      void askReload();
      return true;
    }
    if (error.code === "invalid_flow" && error.issues.length > 0) {
      editor.check.report(error.issues);
      setIssuesOpen(true);
      toast.error("The flow could not be saved", { description: error.message });
      return true;
    }
    return false;
  };

  /** Writes the draft. `asNew` creates another flow from it instead of updating the one that is open. */
  const write = async (asNew: boolean) => {
    if (saving()) return;
    const doc: Doc = draft.current();
    const body: FlowWrite = { name: (asNew && !isNew ? `${doc.name} copy` : doc.name).trim(), description: doc.description, graph: graphOf(doc), revision };
    setSaving(true);
    setNameError(null);
    const users = usedBy();
    const flow = await run(() => (asNew || flowId === null ? api.flows.create(body) : api.flows.update(flowId, body)), {
      invalidate: [keys.flows],
      success: (saved) => (users.length > 0 && !asNew ? `Saved "${saved.name}". New jobs in ${users.join(", ")} use it.` : `Saved "${saved.name}"`),
      failure: "The flow could not be saved",
      onError: refused,
    });
    setSaving(false);
    if (!flow) return;
    if (asNew) {
      // The stored flow lives at another address. Its editor takes over what this one holds,
      // under the name the daemon gave the flow unless it was changed again meanwhile.
      const current = draft.current();
      const adopted = current.name === doc.name ? { ...current, name: flow.name } : current;
      carryOver(flow, { ...doc, name: flow.name }, { ...editor.handoff(), draft: { ...draft.snapshot(), doc: adopted } });
      setLeaving(true);
      flush();
      navigate(`/flows/${flow.id}`, { replace: true });
      return;
    }
    revision = flow.revision;
    draft.markSaved(doc);
  };

  const blocked = () => editor.check.errors().length > 0;
  const save = () => {
    if (blocked() || outdated() || !(isNew || draft.changes() > 0)) return;
    void write(isNew);
  };

  const discard = async () => {
    const agreed = await confirm({
      title: "Discard unsaved changes?",
      description: isNew ? "The new flow is not created." : undefined,
      confirmLabel: "Discard changes",
      cancelLabel: "Keep editing",
      danger: true,
    });
    if (!agreed) return;
    const saved = draft.saved();
    if (saved) {
      draft.reset(saved);
      setNameError(null);
      return;
    }
    setLeaving(true);
    flush();
    navigate("/flows");
  };

  /* ---------- test on a file ---------- */

  const test = () => {
    const path = editor.test()?.path || editor.lastTestPath();
    if (!path) return setPicking(true);
    editor.select(null);
    editor.runTest(path);
  };

  /* ---------- issues ---------- */

  let focusSerial = 0;
  const showIssue = (issue: Issue) => {
    if (!issue.node_id) return;
    editor.select({ kind: "node", id: issue.node_id });
    if (issue.field) setFocus({ nodeId: issue.node_id, field: issue.field, serial: ++focusSerial });
  };

  /* ---------- palette ---------- */

  const groups = createMemo(() => {
    // A flow has exactly one input: once it is there the palette stops offering another.
    const hasInput = draft.order().some((id) => editor.isInput(draft.node(id)));
    const result: { label: string; items: PaletteItem[] }[] = [];
    for (const type of data.catalog) {
      if (type.kind === "input" && hasInput) continue;
      const item = { id: type.id, kind: type.kind, label: type.label, description: type.description };
      const group = result.find((candidate) => candidate.label === type.group);
      if (group) group.items.push(item);
      else result.push({ label: type.group, items: [item] });
    }
    return result;
  });

  const [ghost, setGhost] = createSignal<{ item: PaletteItem; x: number; y: number; over: boolean } | null>(null);
  const overCanvas = (x: number, y: number) => {
    const hit = document.elementFromPoint(x, y);
    return !!hit && !!editor.canvasElement()?.contains(hit) && !hit.closest(".av-canvas-tools");
  };
  const paletteDrag = (drag: PaletteDrag) => {
    if (drag.phase === "move") return setGhost({ item: drag.item, x: drag.x, y: drag.y, over: overCanvas(drag.x, drag.y) });
    setGhost(null);
    if (drag.phase === "drop" && overCanvas(drag.x, drag.y)) editor.addAt(drag.item.id, drag.x, drag.y);
  };

  /* ---------- keyboard ---------- */

  onSettled(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented) return;
      const target = event.target instanceof Element ? event.target : document.body;
      // A dialog, a menu or the issues list has the keyboard while it is open.
      if (target.closest(".av-dialog, .av-menu, .fe-problems-pop")) return;
      const typing = target.matches("input, textarea, select, [contenteditable]");
      const mod = event.ctrlKey || event.metaKey;
      const key = event.key.toLowerCase();

      if (mod && key === "s") {
        event.preventDefault();
        return save();
      }
      // Undo covers every field that writes to the draft; the palette filter keeps the browser's own.
      if (mod && (key === "z" || key === "y") && !target.closest(".av-palette")) {
        event.preventDefault();
        return key === "y" || event.shiftKey ? draft.redo() : draft.undo();
      }
      if (typing) return;
      if (mod && key === "d") {
        const id = editor.selectedId();
        if (!id) return;
        event.preventDefault();
        editor.duplicate(id);
        return;
      }
      if (mod || event.altKey) return;

      // The rest acts on the canvas, so not while a control of the sheet has focus. The sheet closes itself on Escape.
      if (target.closest(".av-sheet")) return;
      if (event.key === "Escape") {
        if (editor.selection()) editor.select(null);
        else if (editor.test()) editor.clearTest();
        return;
      }
      if (event.key === "Delete" || event.key === "Backspace") {
        if (editor.removeSelection()) event.preventDefault();
        return;
      }
      const step = ARROWS[event.key];
      if (!step || !(target === document.body || target.closest(".av-canvas"))) return;
      // The node that has focus, or else the selected one.
      const id = target.closest("[data-node-id]")?.getAttribute("data-node-id") ?? editor.selectedId();
      if (!id) return;
      event.preventDefault();
      const reach = event.shiftKey ? 4 : 1;
      editor.nudge(id, step[0] * reach, step[1] * reach);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  });

  return (
    <>
      <EditorTopbar
        editor={editor}
        isNew={isNew}
        usedBy={usedBy()}
        saving={saving()}
        nameError={nameError()}
        issuesOpen={issuesOpen()}
        onIssuesOpen={setIssuesOpen}
        onIssue={showIssue}
        onName={(name) => {
          setNameError(null);
          draft.setName(name);
        }}
        onDetails={() => setDetailsOpen(true)}
        onTest={test}
        onDiscard={() => void discard()}
        onSave={save}
        outdated={outdated()}
      />
      <Show when={outdated()}>
        <Banner
          tone="warning"
          action={
            <>
              <Button size="sm" disabled={saving() || blocked()} onClick={() => void write(true)}>
                Save as a new flow
              </Button>
              <Button size="sm" onClick={() => void askReload()}>
                Reload flow
              </Button>
            </>
          }
        >
          This flow was changed elsewhere after you opened it. Your draft cannot be saved over the newer version.
        </Banner>
      </Show>
      <div class="av-workspace">
        <NodePalette groups={groups()} onPick={(item) => editor.addFromPalette(item.id)} onDrag={paletteDrag} />
        <div class="fe-stage">
          <EditorCanvas editor={editor} />
          <NodeSheet editor={editor} focus={focus()} onClose={() => editor.select(null)} />
          <Show when={!editor.selectedId()}>
            <TestSheet editor={editor} onChoose={() => setPicking(true)} />
          </Show>
        </div>
      </div>

      <Show when={ghost()}>
        {(dragged) => (
          <div class={["av-node-ghost", { "fe-ghost-off": !dragged().over }]} style={{ transform: `translate(${dragged().x}px, ${dragged().y}px) scale(${editor.view().k}) translate(-50%, -50%)` }}>
            <FlowNode static dragging kind={dragged().item.kind} title={dragged().item.label} summary={data.seed.defaults.get(dragged().item.id)} outputs={editor.types.get(dragged().item.id)?.outputs} />
          </div>
        )}
      </Show>

      <FolderPicker
        open={picking()}
        files
        title="Choose a file to test"
        confirmLabel="Test this file"
        initialPath={testFolder()}
        onClose={() => setPicking(false)}
        onPick={(path) => {
          setPicking(false);
          editor.select(null);
          editor.runTest(path);
        }}
      />

      <Dialog
        open={detailsOpen()}
        onClose={() => setDetailsOpen(false)}
        title="Name and description"
        footer={
          <Button variant="primary" onClick={() => setDetailsOpen(false)}>
            Done
          </Button>
        }
      >
        <Field label="Name" htmlFor="fe-details-name" error={nameError()}>
          <Input
            id="fe-details-name"
            maxlength={80}
            autocomplete="off"
            invalid={!!nameError()}
            value={draft.name()}
            onInput={(event) => {
              setNameError(null);
              draft.setName(event.currentTarget.value);
            }}
          />
        </Field>
        <Field label="Description" htmlFor="fe-details-description">
          <Textarea id="fe-details-description" value={draft.description()} onInput={(event) => draft.setDescription(event.currentTarget.value)} />
        </Field>
      </Dialog>
    </>
  );
}
