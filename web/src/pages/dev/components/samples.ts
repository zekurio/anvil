// Sample data for the gallery, taken from the design package's previews so
// the gallery can be compared with them side by side.

import type { Rule } from "../../../api";
import type { FlowCanvasEdge, FlowCanvasNode, JobStatus, PaletteItem, SidebarItem } from "../../../ui";

export interface SampleRow {
  id: number;
  path: string;
  container: string;
  video: string;
  res: string;
  audio: string;
  size: string;
  status: JobStatus;
  flow: string;
}

export const ROWS: SampleRow[] = [
  { id: 1, path: "/media/movies/Arrival (2016)/Arrival (2016).mkv", container: "mkv", video: "h264", res: "1080p", audio: "eac3 5.1", size: "12.4 GB", status: "queued", flow: "Default" },
  { id: 2, path: "/media/movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017).mkv", container: "mkv", video: "hevc", res: "2160p", audio: "truehd 7.1", size: "58.2 GB", status: "skipped", flow: "Default" },
  { id: 3, path: "/media/movies/Dune (2021)/Dune (2021).mp4", container: "mp4", video: "h264", res: "1080p", audio: "aac 2.0", size: "9.8 GB", status: "running", flow: "Default" },
  { id: 4, path: "/media/movies/Interstellar (2014)/Interstellar (2014).mkv", container: "mkv", video: "hevc", res: "1080p", audio: "dts 5.1", size: "6.1 GB", status: "done", flow: "Default" },
  { id: 5, path: "/media/movies/Moon (2009)/Moon (2009).avi", container: "avi", video: "mpeg4", res: "720p", audio: "mp3 2.0", size: "1.4 GB", status: "failed", flow: "Remux only" },
  { id: 6, path: "/media/movies/Paterson (2016)/Paterson (2016).mkv", container: "mkv", video: "h264", res: "1080p", audio: "ac3 5.1", size: "8.7 GB", status: "queued", flow: "Default" },
  { id: 7, path: "/media/movies/Sicario (2015)/Sicario (2015).mkv", container: "mkv", video: "h264", res: "1080p", audio: "dts 5.1", size: "11.2 GB", status: "queued", flow: "Default" },
  { id: 8, path: "/media/movies/The Thing (1982)/The Thing (1982).mkv", container: "mkv", video: "hevc", res: "1080p", audio: "flac 1.0", size: "7.3 GB", status: "done", flow: "Default" },
];

export const NAV: SidebarItem[] = [
  { section: "Libraries" },
  { id: "movies", label: "Movies", icon: "library-big", count: 312 },
  { id: "tv", label: "TV shows", icon: "library-big", count: "1,204" },
  { id: "anime", label: "Anime", icon: "library-big", count: 0 },
  { section: "Processing" },
  { id: "flows", label: "Flows", icon: "workflow" },
  { id: "queue", label: "Queue", icon: "list-video", count: 3 },
  { section: "System" },
  { id: "settings", label: "Settings", icon: "settings" },
];

export const RULES: Rule[] = [
  { action: "deny", field: "path", op: "matches", value: "**/Extras/**" },
  { action: "deny", field: "path", op: "matches", value: "*.sample.*" },
  { action: "allow", field: "extension", op: "is", value: "mkv, mp4, avi, m2ts" },
  { action: "deny", field: "size", op: "lt", value: "200 MB" },
];

export const STATS = [
  { label: "Files", value: "4,812" },
  { label: "Total size", value: "18.2 TB" },
  { label: "Pending", value: "312", tone: "running" as const },
  { label: "Failed", value: "4", tone: "danger" as const },
  { label: "Space saved", value: "2.13 TB", tone: "ok" as const },
];

export const FLOW_NODES: FlowCanvasNode[] = [
  { id: "in", kind: "input", x: 288, y: 32, title: "Input file", summary: "*.mkv, *.mp4, *.avi" },
  { id: "cond", kind: "condition", x: 288, y: 144, title: "Video is hevc?", summary: "video.codec == hevc", outputs: ["true", "false"] },
  { id: "remux", kind: "container", x: 64, y: 256, title: "Remux to mkv", summary: "copy all streams · mkv" },
  { id: "enc", kind: "video", x: 512, y: 256, title: "Encode video", summary: "hevc_qsv · global_quality 22" },
  { id: "aud", kind: "audio", x: 512, y: 368, title: "Keep audio", summary: "eng, jpn · aac 2.0 fallback" },
  { id: "out", kind: "output", x: 288, y: 480, title: "Replace file", summary: "replace the source in place" },
];

export const FLOW_EDGES: FlowCanvasEdge[] = [
  { from: "in", to: "cond" },
  { from: "cond", port: 0, to: "remux" },
  { from: "cond", port: 1, to: "enc" },
  { from: "enc", to: "aud" },
  { from: "aud", to: "out" },
  { from: "remux", to: "out" },
];

const item = (kind: PaletteItem["kind"], label: string): PaletteItem => ({ id: label, kind, label });

export const PALETTE: { label: string; items: PaletteItem[] }[] = [
  { label: "Input", items: [item("input", "Input file")] },
  { label: "Inspect", items: [item("condition", "Check video codec"), item("condition", "Check resolution"), item("condition", "Check audio language"), item("condition", "Check file size")] },
  { label: "Video", items: [item("video", "Encode video"), item("video", "Scale"), item("video", "Tone-map HDR")] },
  { label: "Audio", items: [item("audio", "Keep audio tracks"), item("audio", "Encode audio"), item("audio", "Downmix to stereo")] },
  { label: "Subtitles", items: [item("subtitles", "Keep subtitles"), item("subtitles", "Extract to file")] },
  { label: "Container", items: [item("container", "Remux"), item("container", "Reorder streams")] },
  { label: "Output", items: [item("output", "Replace file"), item("output", "Move file"), item("output", "Skip file")] },
  { label: "Tools", items: [item("command", "Run command")] },
];

export const COMMAND = ["-hwaccel", "qsv", "-i", "{input}", "-map", "0:v:0", "-c:v", "hevc_qsv", "-preset", "slow", "-global_quality", "22", "{output}"];
