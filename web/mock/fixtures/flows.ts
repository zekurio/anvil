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

const av1Archive: Graph = {
  nodes: [
    node("in", "input", 288, 32),
    node("is_av1", "check.video_codec", 288, 144, "Video is AV1?", { codecs: ["av1"] }),
    node("skip", "output.skip", 32, 256, "Skip, already AV1", { reason: "Already AV1" }),
    node("is_dv", "check.hdr", 544, 256, "Dolby Vision profile 5?", { formats: ["dolby_vision"], dv_profiles: ["5"] }),
    node("copy", "video.copy", 288, 368, "Copy video"),
    node("crop", "video.crop", 800, 368),
    node("encode", "video.encode", 800, 480, "Encode to AV1", { target_vmaf: 95, preset: 6 }),
    node("audio", "audio.keep", 544, 592, "", { languages: ["eng", "jpn"] }),
    node("lossless", "audio.encode", 544, 704, "Shrink lossless audio"),
    node("subs", "subtitles.keep", 544, 816, "", { languages: ["eng"] }),
    node("out", "output.publish", 544, 928),
  ],
  edges: [
    edge("in", "is_av1"),
    edge("is_av1", "skip", 0),
    edge("is_av1", "is_dv", 1),
    edge("is_dv", "copy", 0),
    edge("is_dv", "crop", 1),
    edge("crop", "encode"),
    edge("copy", "audio"),
    edge("encode", "audio"),
    edge("audio", "lossless"),
    edge("lossless", "subs"),
    edge("subs", "out"),
  ],
};

const remuxOnly: Graph = {
  nodes: [
    node("in", "input", 288, 32),
    node("meta", "container.metadata", 288, 144),
    node("remux", "container.remux", 288, 256, "Remux to mkv"),
    node("out", "output.publish", 288, 368, "", { only_if_smaller: false }),
  ],
  edges: [edge("in", "meta"), edge("meta", "remux"), edge("remux", "out")],
};

// Carries a warning on purpose: "bitmap" is not connected to the input.
const travel: Graph = {
  nodes: [
    node("in", "input", 288, 32),
    node("is_large", "check.resolution", 288, 144, "Taller than 1080p?", { op: "gt", height: 1080 }),
    node("scale", "video.scale", 32, 256, "Scale to 1080p"),
    node("encode", "video.encode", 288, 368, "Encode to AV1", { rate_control: "crf", crf: 32, preset: 8 }),
    node("stereo", "audio.downmix", 288, 480, "", { mode: "replace", codec: "opus", bitrate: 128 }),
    node("out", "output.publish", 288, 592),
    node("bitmap", "subtitles.drop_bitmap", 608, 256),
  ],
  edges: [edge("in", "is_large"), edge("is_large", "scale", 0), edge("is_large", "encode", 1), edge("scale", "encode"), edge("encode", "stereo"), edge("stereo", "out")],
};

export function initialFlows(now: number): StoredFlow[] {
  const at = (daysAgo: number) => new Date(now - daysAgo * 86_400_000).toISOString();
  return [
    { id: 1, name: "AV1 archive", description: "Encodes everything that is not AV1 yet and keeps English and Japanese audio.", revision: 7, graph: av1Archive, created_at: at(41), updated_at: at(3) },
    { id: 2, name: "Remux only", description: "Cleans metadata and remuxes to mkv without touching the streams.", revision: 2, graph: remuxOnly, created_at: at(30), updated_at: at(22) },
    { id: 3, name: "1080p for travel", description: "Small stereo copies for a tablet.", revision: 4, graph: travel, created_at: at(12), updated_at: at(1) },
  ];
}
