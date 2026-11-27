// The result of "Test on a file": what the file is, which way it went
// through the flow, and what anvil would run. A dry run; nothing is encoded.

import { For, Show } from "solid-js";

import type { FileFacts, FlowTest, StreamFacts } from "../../api";
import { formatBitrate, formatBytes, formatDuration, joinDot } from "../../lib/format";
import { Banner, Button, CommandPreview, Icon, LoadingState, PathCell, SheetSection, SideSheet, StatusBadge, toast } from "../../ui";
import type { Editor } from "./editor";

const LAYOUTS: Record<number, string> = { 1: "1.0", 2: "2.0", 6: "5.1", 8: "7.1" };

const fileName = (path: string) => path.slice(path.lastIndexOf("/") + 1);

/** A track the way ffprobe would list it: index, codec, layout, language, then what sets it apart. */
function trackLine(track: StreamFacts): string {
  return joinDot(`#${track.index}`, track.codec || "unknown", track.channels ? (LAYOUTS[track.channels] ?? `${track.channels} ch`) : "", track.language || "und", track.default && "default", track.forced && "forced");
}

function Tracks(props: { label: string; tracks: StreamFacts[] }) {
  return (
    <div class="fe-fact">
      <dt>{props.label}</dt>
      <dd>
        <For each={props.tracks} keyed={false} fallback={<span class="av-muted">none</span>}>
          {(track) => (
            <div class="fe-track">
              <span>{trackLine(track())}</span>
              <Show when={track().title}>
                <span class="fe-track-title">{track().title}</span>
              </Show>
            </div>
          )}
        </For>
      </dd>
    </div>
  );
}

function Facts(props: { file: FileFacts }) {
  return (
    <dl class="fe-facts">
      <div class="fe-fact">
        <dt>Video</dt>
        <dd>{joinDot(props.file.video_codec || "unknown", `${props.file.width}x${props.file.height}`, props.file.hdr || "sdr")}</dd>
      </div>
      <div class="fe-fact">
        <dt>File</dt>
        <dd>{joinDot(props.file.container, formatBytes(props.file.size_bytes), formatDuration(props.file.duration_seconds), props.file.bitrate_kbps > 0 && formatBitrate(props.file.bitrate_kbps))}</dd>
      </div>
      <Tracks label="Audio" tracks={props.file.audio} />
      <Tracks label="Subtitles" tracks={props.file.subtitles} />
    </dl>
  );
}

function Result(props: { result: FlowTest; stale: boolean; onRerun: () => void }) {
  const copy = () => {
    navigator.clipboard.writeText(["ffmpeg", ...props.result.command].join(" ")).then(
      () => toast.success("Command copied"),
      () => toast.error("The command could not be copied", { description: "The browser refused access to the clipboard." }),
    );
  };
  return (
    <>
      <Show when={props.stale}>
        <Banner tone="warning" action={<Button size="sm" onClick={() => props.onRerun()}>Run again</Button>}>
          The flow changed since this test.
        </Banner>
      </Show>
      <SheetSection title="Outcome">
        <div class="fe-outcome">
          <StatusBadge status={props.result.outcome === "process" ? "done" : "skipped"}>{props.result.outcome === "process" ? "Process" : "Skip"}</StatusBadge>
          <span>{props.result.outcome === "process" ? "The file is processed and the result published." : "The file is left untouched."}</span>
        </div>
        <Show when={props.result.decisions.length > 0}>
          <ol class="fe-lines" aria-label="Decisions">
            <For each={props.result.decisions} keyed={false}>{(line) => <li>{line()}</li>}</For>
          </ol>
        </Show>
      </SheetSection>
      <SheetSection title="Plan">
        <ul class="fe-lines" aria-label="Plan">
          <For each={props.result.plan} keyed={false}>{(line) => <li>{line()}</li>}</For>
        </ul>
      </SheetSection>
      <SheetSection title="File">
        <div class="fe-test-path">
          <PathCell path={props.result.file.path} />
        </div>
        <Facts file={props.result.file} />
      </SheetSection>
      <Show when={props.result.command.length > 0}>
        <SheetSection title="Command">
          <CommandPreview prefix="ffmpeg" args={props.result.command} />
          <div>
            <Button size="sm" icon="copy" onClick={copy}>
              Copy command
            </Button>
          </div>
        </SheetSection>
      </Show>
    </>
  );
}

export function TestSheet(props: { editor: Editor; onChoose: () => void }) {
  const editor = props.editor;
  const rerun = () => {
    const state = editor.test();
    if (state) editor.runTest(state.path);
  };
  const failed = () => {
    const state = editor.test();
    return state?.status === "failed" ? state : undefined;
  };
  const done = () => {
    const state = editor.test();
    return state?.status === "done" ? state : undefined;
  };
  return (
    <Show when={editor.test()}>
      {(state) => (
        <SideSheet
          title="Test on a file"
          subtitle={fileName(state().path)}
          icon="play"
          onClose={() => editor.clearTest()}
          footer={
            <>
              <Button variant="ghost" size="sm" onClick={() => props.onChoose()}>
                Choose another file
              </Button>
              <span class="av-spacer" />
              <Button size="sm" icon="refresh-cw" disabled={state().status === "running"} onClick={rerun}>
                Run again
              </Button>
            </>
          }
        >
          <Show when={state().status === "running"}>
            <LoadingState label="Testing the flow" />
          </Show>
          <Show when={failed()}>
            {(state) => (
              <div class="fe-test-failed" role="alert">
                <Icon name="circle-alert" size={16} />
                <div>
                  <div class="fe-test-failed-title">The test could not run</div>
                  <p>{state().message}</p>
                  <p class="av-mono">{state().path}</p>
                </div>
              </div>
            )}
          </Show>
          <Show when={done()}>{(state) => <Result result={state().result} stale={editor.testStale()} onRerun={rerun} />}</Show>
        </SideSheet>
      )}
    </Show>
  );
}
