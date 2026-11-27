// The three stored flows. Flow 1 is the default every new library gets.

import type { Graph, GraphEdge, GraphNode, OptionValue } from "../../src/api/types.ts";

export interface StoredFlow {
  id: number;
  name: string;
  description: string;
  revision: number;
  graph: Graph;
  created_at: string;
  updated_at: string;
}

const node = (id: string, type: string, x: number, y: number, label = "", options: Record<string, OptionValue> = {}): GraphNode => ({
  id,
  type,
  x,
  y,
  ...(label ? { label } : {}),
  ...(Object.keys(options).length ? { options } : {}),
});

const edge = (from: string, to: string, port = 0): GraphEdge => ({ from, port, to });

// Laid out the way pkg/flow/generate.go lays out a generated flow: 112 px
// rows, a condition's true branch 224 px to its left and its false branch
// 224 px to its right, branches rejoining in the column of the condition
// that split them.
const av1Archive: Graph = {
  nodes: [
    node("input", "input", 288, 32),
    node("is-av1", "check.video_codec", 288, 144, "Video is av1?", { codecs: ["av1"] }),
    node("skip", "output.skip", 64, 256, "Skip, already AV1"),
    node("is-dv", "check.hdr", 512, 256, "Dolby Vision?", { kinds: ["dolby_vision"] }),
    node("copy", "video.copy", 288, 368),
    node("crop", "video.crop", 736, 368),
    node("video", "video.encode", 736, 480, "Encode to AV1", { codec: "av1", accelerator: "software", preset: "6", quality_mode: "search", metric: "vmaf", target: 95 }),
    node("audio", "audio.keep", 512, 592, "", { languages: ["eng", "jpn", "orig"], fallback: "keep_all", keep_commentary: false }),
    node("audio-encode", "audio.encode", 512, 704, "Shrink lossless audio", { codec: "eac3", bitrate_kbps_per_channel: 96, source_codecs: ["truehd", "dts", "flac"] }),
    node("subtitles", "subtitles.keep", 512, 816, "", { languages: ["eng"], fallback: "keep_all", keep_forced: true }),
    node("metadata", "container.metadata", 512, 928),
    node("publish", "output.publish", 512, 1040),
  ],
  edges: [
    edge("input", "is-av1"),
    edge("is-av1", "skip", 0),
    edge("is-av1", "is-dv", 1),
    edge("is-dv", "copy", 0),
    edge("is-dv", "crop", 1),
    edge("crop", "video"),
    edge("copy", "audio"),
    edge("video", "audio"),
    edge("audio", "audio-encode"),
    edge("audio-encode", "subtitles"),
    edge("subtitles", "metadata"),
    edge("metadata", "publish"),
  ],
};

const remuxOnly: Graph = {
  nodes: [
    node("input", "input", 288, 32),
    node("metadata", "container.metadata", 288, 144, "", { metadata: "strip", track_titles: "standardize" }),
    node("publish", "output.publish", 288, 256),
  ],
  edges: [edge("input", "metadata"), edge("metadata", "publish")],
};

// Carries a warning on purpose: "images" is not connected to the input.
const travel: Graph = {
  nodes: [
    node("input", "input", 288, 32),
    node("is-surround", "check.audio_channels", 288, 144, "More than stereo?", { more_than: "2" }),
    node("downmix", "audio.downmix", 64, 256, "Downmix to stereo", { max_channels: "2", codec: "opus", bitrate_kbps_per_channel: 64 }),
    node("video", "video.encode", 288, 368, "Encode small", { codec: "hevc", preset: "medium", bit_depth: "8", quality_mode: "fixed", crf: 30 }),
    node("publish", "output.publish", 288, 480),
    node("images", "subtitles.drop_bitmap", 608, 256),
  ],
  edges: [edge("input", "is-surround"), edge("is-surround", "downmix", 0), edge("is-surround", "video", 1), edge("downmix", "video"), edge("video", "publish")],
};

export function initialFlows(now: number): StoredFlow[] {
  const at = (daysAgo: number) => new Date(now - daysAgo * 86_400_000).toISOString();
  return [
    { id: 1, name: "AV1 archive", description: "Encodes everything that is not AV1 yet and keeps English and Japanese audio.", revision: 7, graph: av1Archive, created_at: at(41), updated_at: at(3) },
    { id: 2, name: "Remux only", description: "Strips metadata and standardizes track titles without touching the streams.", revision: 2, graph: remuxOnly, created_at: at(30), updated_at: at(22) },
    { id: 3, name: "Small files for travel", description: "Small stereo copies for a tablet.", revision: 4, graph: travel, created_at: at(12), updated_at: at(1) },
  ];
}
