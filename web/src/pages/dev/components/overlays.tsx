import { createSignal, Show } from "solid-js";

import { Banner, Button, confirm, Dialog, EmptyState, ErrorState, Field, FolderPicker, Input, LoadingRows, LoadingState, Menu, Spinner, StatusBadge, toast, Tooltip } from "../../../ui";
import { Item, Section } from "./parts";

export function Overlays() {
  const [dialog, setDialog] = createSignal(false);
  const [picker, setPicker] = createSignal<"folder" | "file" | null>(null);
  const [picked, setPicked] = createSignal("");
  const [answer, setAnswer] = createSignal("");

  const ask = async () => {
    const yes = await confirm({
      title: 'Delete the library "movies"?',
      description: "Its 4,812 files stay on disk. The job history of the library is removed.",
      confirmLabel: "Delete library",
      danger: true,
    });
    setAnswer(yes ? "Confirmed" : "Canceled");
  };

  return (
    <Section id="overlays" title="Overlays">
      <Item title="Dialog, ConfirmDialog, FolderPicker">
        <div class="av-row">
          <Button onClick={() => setDialog(true)}>Rename flow</Button>
          <Button variant="danger" icon="trash-2" onClick={ask}>
            Delete library
          </Button>
          <Button icon="folder" onClick={() => setPicker("folder")}>
            Choose a folder
          </Button>
          <Button icon="file-video" onClick={() => setPicker("file")}>
            Choose a file
          </Button>
          <span class="mono av-muted">{[answer(), picked()].filter(Boolean).join(" · ")}</span>
        </div>
        <Dialog
          open={dialog()}
          onClose={() => setDialog(false)}
          title="Rename flow"
          description="Libraries that use this flow keep using it."
          footer={
            <>
              <Button onClick={() => setDialog(false)}>Cancel</Button>
              <Button variant="primary" onClick={() => setDialog(false)}>
                Rename flow
              </Button>
            </>
          }
        >
          <Field label="Name" htmlFor="gallery-rename">
            <Input id="gallery-rename" value="AV1 archive" />
          </Field>
        </Dialog>
        <FolderPicker
          open={picker() !== null}
          files={picker() === "file"}
          initialPath="/media"
          onClose={() => setPicker(null)}
          onPick={(path) => {
            setPicked(path);
            setPicker(null);
          }}
        />
      </Item>
      <Item title="Menu, Tooltip">
        <div class="av-row">
          <Menu
            label="Actions for movies"
            items={[
              { id: "scan", label: "Scan library", icon: "refresh-cw", onSelect: () => toast("Scan started") },
              { id: "pause", label: "Pause library", icon: "pause", onSelect: () => toast("Paused movies") },
              { id: "copy", label: "Copy path", icon: "copy", disabled: true, onSelect: () => {} },
              { separator: true },
              { id: "delete", label: "Delete library", icon: "trash-2", danger: true, onSelect: ask },
            ]}
          />
          <Menu size="sm" label="Row actions" items={[{ id: "retry", label: "Retry job", onSelect: () => toast("Queued 1 job") }, { id: "cancel", label: "Cancel job", danger: true, onSelect: () => {} }]}>
            Actions
          </Menu>
          <Tooltip content="Denied by rule 3">
            <StatusBadge status="skipped" />
          </Tooltip>
          <Tooltip content="Scans the folder for new and changed files" render={(trigger) => <Button {...trigger} icon="refresh-cw" label="Scan library" />} />
        </div>
      </Item>
      <Item title="Toast">
        <div class="av-row">
          <Button onClick={() => toast("Scan started")}>Plain</Button>
          <Button onClick={() => toast.success("Saved the flow AV1 archive", { description: "3 libraries and 312 pending files use it." })}>Success</Button>
          <Button onClick={() => toast.error("The flow could not be saved.", { description: "The flow changed since it was opened (revision 8)." })}>Error</Button>
          <Button onClick={() => toast.warning("The encode was larger than the source.")}>Warning</Button>
          <Button onClick={() => toast("Canceled 4 jobs", { action: { label: "Undo", onAction: () => toast("Queued 4 jobs") } })}>With an action</Button>
        </div>
      </Item>
    </Section>
  );
}

export function Feedback() {
  const [failed, setFailed] = createSignal(true);
  return (
    <Section id="feedback" title="Feedback">
      <Item title="Banner" flush>
        <Banner>The library is paused. Scans still run; no new job starts.</Banner>
        <Banner tone="warning">The anvil daemon is not answering. What you see is the last known state.</Banner>
        <Banner tone="danger" action={<Button size="sm">Open the queue</Button>}>
          4 jobs failed in the last hour.
        </Banner>
      </Item>
      <Item title="EmptyState" flush>
        <EmptyState icon="library-big" title="No libraries yet" description="Add a folder to scan.">
          <Button variant="primary" icon="plus">
            Add library
          </Button>
        </EmptyState>
      </Item>
      <Item title="ErrorState" flush>
        <Show when={failed()} fallback={<EmptyState title="Loaded" description="Retry ran." />}>
          <ErrorState title="The files could not be loaded" error={new Error("Cannot reach the anvil daemon.")} onRetry={() => setFailed(false)} />
        </Show>
      </Item>
      <Item title="Spinner, LoadingState, LoadingRows" flush>
        <div class="av-pad av-row">
          <Spinner /> <Spinner size={24} label="Loading files" />
        </div>
        <LoadingState label="Loading files" />
        <LoadingRows rows={4} />
      </Item>
    </Section>
  );
}
