// The node catalog served by GET /node-types. It exercises every FieldType,
// visible_when, sections, and all kinds and groups of pkg/flow/catalog.go, so
// the editor's generic option forms can be built against it.

import type { Choice, Field, NodeType, Section } from "../../src/api/types.ts";

const choices = (...values: (string | [string, string])[]): Choice[] =>
  values.map((v) => (typeof v === "string" ? { value: v, label: v } : { value: v[0], label: v[1] }));

const section = (title: string, fields: Field[], extra: Partial<Section> = {}): Section => ({ title, fields, ...extra });

const labelField: Field = { key: "label", label: "Label", type: "string", default: "", placeholder: "Uses the node name" };

const VIDEO_CODECS = choices(["av1", "AV1"], ["hevc", "HEVC (H.265)"], ["h264", "H.264"], ["vp9", "VP9"], ["mpeg2video", "MPEG-2"], ["mpeg4", "MPEG-4 Part 2"], ["vc1", "VC-1"]);
const AUDIO_CODECS = choices(["eac3", "Dolby Digital Plus"], ["ac3", "Dolby Digital"], ["opus", "Opus"], ["aac", "AAC"], ["flac", "FLAC"]);
const LOSSLESS = choices("truehd", "dts", "flac", "pcm_s16le", "pcm_s24le", "mlp");
const COMPARE = choices(["gt", "greater than"], ["lt", "less than"]);

const condition = (id: string, label: string, description: string, fields: Field[]): NodeType => ({
  id,
  kind: "condition",
  group: "Inspect",
  label,
  description,
  outputs: ["true", "false"],
  sections: [section("Condition", [labelField, ...fields])],
});

export const NODE_TYPES: NodeType[] = [
  {
    id: "input",
    kind: "input",
    group: "Input",
    label: "Input file",
    description: "Where every file of the library enters the flow.",
    outputs: [""],
    sections: [
      section("Input", [
        labelField,
        { key: "extensions", label: "Extensions", type: "string_list", default: ["mkv", "mp4", "avi", "m2ts"], mono: true, placeholder: "mkv", hint: "Files with another extension never enter the flow." },
        { key: "probe_timeout", label: "Probe timeout", type: "duration", default: "30s", mono: true, half: true, hint: "How long ffprobe may take." },
        { key: "detect_crop", label: "Detect black bars", type: "bool", default: true, hint: "Samples the video once so Crop nodes can use the result." },
      ]),
    ],
  },

  condition("check.video_codec", "Check video codec", "Branches on the codec of the primary video stream.", [
    { key: "codecs", label: "Video codec is one of", type: "enum_list", default: ["av1"], choices: VIDEO_CODECS, hint: "As ffprobe reports it." },
  ]),
  condition("check.resolution", "Check resolution", "Branches on the height of the primary video stream.", [
    { key: "op", label: "Height is", type: "enum", default: "gt", choices: COMPARE, segmented: true },
    { key: "height", label: "Height", type: "int", default: 1080, min: 144, max: 4320, suffix: "px", mono: true, half: true },
  ]),
  condition("check.hdr", "Check HDR", "Branches on the HDR format of the primary video stream.", [
    { key: "formats", label: "HDR format is one of", type: "enum_list", default: ["dolby_vision"], choices: choices(["hdr10", "HDR10"], ["hlg", "HLG"], ["dolby_vision", "Dolby Vision"]) },
    { key: "dv_profiles", label: "Dolby Vision profiles", type: "string_list", default: ["5"], mono: true, placeholder: "5", hint: "Leave empty to match any profile.", visible_when: { key: "formats", in: ["dolby_vision"] } },
  ]),
  condition("check.bitrate", "Check bitrate", "Branches on the overall bitrate of the file.", [
    { key: "op", label: "Bitrate is", type: "enum", default: "lt", choices: COMPARE, segmented: true },
    { key: "kbps", label: "Bitrate", type: "int", default: 2500, min: 100, max: 200000, suffix: "kb/s", mono: true, half: true },
  ]),
  condition("check.file_size", "Check file size", "Branches on the size of the file.", [
    { key: "op", label: "Size is", type: "enum", default: "lt", choices: COMPARE, segmented: true },
    { key: "gigabytes", label: "Size", type: "float", default: 1.5, min: 0.1, max: 500, suffix: "GB", mono: true, half: true },
  ]),
  condition("check.audio_language", "Check audio language", "Branches on whether an audio track in one of the languages exists.", [
    { key: "languages", label: "Languages", type: "string_list", default: ["eng"], mono: true, placeholder: "eng", hint: "ISO 639-2 tags." },
  ]),

  {
    id: "video.encode",
    kind: "video",
    group: "Video",
    label: "Encode video",
    description: "Re-encodes the primary video stream.",
    outputs: [""],
    sections: [
      section("Encoder", [
        labelField,
        { key: "codec", label: "Codec", type: "enum", default: "av1", choices: choices(["av1", "AV1"], ["hevc", "HEVC (H.265)"]), half: true },
        { key: "encoder", label: "Encoder", type: "enum", default: "libsvtav1", choices: choices("libsvtav1", "libaom-av1", "av1_qsv", "libx265", "hevc_qsv"), half: true, mono: true },
        { key: "preset", label: "Preset", type: "int", default: 6, min: 0, max: 13, mono: true, half: true, hint: "0\u201313. Lower is slower and smaller." },
        { key: "film_grain", label: "Film grain", type: "int", default: 0, min: 0, max: 50, mono: true, half: true, hint: "0 turns synthesis off." },
      ]),
      section("Quality", [
        { key: "rate_control", label: "Rate control", type: "enum", default: "search", segmented: true, choices: choices(["search", "Quality target"], ["crf", "Fixed CRF"], ["bitrate", "Target bitrate"]) },
        { key: "target_vmaf", label: "Target VMAF", type: "float", default: 95, min: 80, max: 99, mono: true, half: true, hint: "80\u201399. The CRF search picks the highest CRF that reaches it.", visible_when: { key: "rate_control", in: ["search"] } },
        { key: "max_encoded_percent", label: "Largest output", type: "int", default: 80, min: 10, max: 100, suffix: "%", mono: true, half: true, hint: "Of the source size.", visible_when: { key: "rate_control", in: ["search"] } },
        { key: "sample_duration", label: "Sample length", type: "duration", default: "20s", mono: true, half: true, visible_when: { key: "rate_control", in: ["search"] } },
        { key: "sample_count", label: "Samples", type: "int", default: 4, min: 1, max: 12, mono: true, half: true, visible_when: { key: "rate_control", in: ["search"] } },
        { key: "crf", label: "CRF", type: "int", default: 28, min: 1, max: 63, mono: true, hint: "1\u201363. Lower is better quality and larger files.", visible_when: { key: "rate_control", in: ["crf"] } },
        { key: "bitrate", label: "Bitrate", type: "int", default: 4000, min: 100, max: 100000, suffix: "kb/s", mono: true, visible_when: { key: "rate_control", in: ["bitrate"] } },
      ]),
      section(
        "Filters",
        [
          { key: "max_height", label: "Largest resolution", type: "enum", default: "keep", choices: choices(["keep", "Keep source"], ["2160", "2160p"], ["1080", "1080p"], ["720", "720p"]) },
          { key: "pix_fmt", label: "Pixel format", type: "enum", default: "yuv420p10le", choices: choices("yuv420p10le", "yuv420p"), mono: true, half: true },
          { key: "keep_hdr", label: "Keep HDR10 metadata", type: "bool", default: true, hint: "Only applies to HDR sources." },
        ],
        { collapsible: true },
      ),
      section(
        "Advanced",
        [
          { key: "svt_params", label: "Encoder parameters", type: "string_list", default: [], mono: true, placeholder: "tune=0", hint: "Passed as -svtav1-params, joined with a colon." },
          { key: "extra_args", label: "Extra ffmpeg arguments", type: "string_list", default: [], mono: true, placeholder: "-g 240", hint: "Appended after the video options." },
        ],
        { collapsible: true, collapsed: true },
      ),
    ],
  },
  {
    id: "video.copy",
    kind: "video",
    group: "Video",
    label: "Copy video",
    description: "Keeps the video stream as it is.",
    outputs: [""],
    sections: [section("Video", [labelField])],
  },
  {
    id: "video.crop",
    kind: "video",
    group: "Video",
    label: "Crop black bars",
    description: "Removes letterboxing found by crop detection.",
    outputs: [""],
    sections: [
      section("Crop", [
        labelField,
        { key: "mode", label: "Mode", type: "enum", default: "auto", segmented: true, choices: choices(["auto", "Detect"], ["manual", "Manual"]) },
        { key: "min_retained_percent", label: "Smallest kept area", type: "float", default: 70, min: 50, max: 100, suffix: "%", mono: true, hint: "A crop that keeps less than this is rejected.", visible_when: { key: "mode", in: ["auto"] } },
        { key: "filter", label: "Crop filter", type: "string", default: "", mono: true, placeholder: "crop=1920:800:0:140", visible_when: { key: "mode", in: ["manual"] } },
      ]),
    ],
  },
  {
    id: "video.scale",
    kind: "video",
    group: "Video",
    label: "Scale",
    description: "Scales the video down to a largest height.",
    outputs: [""],
    sections: [
      section("Scale", [
        labelField,
        { key: "height", label: "Largest height", type: "int", default: 1080, min: 144, max: 4320, suffix: "px", mono: true, half: true },
        { key: "algorithm", label: "Algorithm", type: "enum", default: "lanczos", choices: choices("lanczos", "bicubic", "spline"), mono: true, half: true },
      ]),
    ],
  },

  {
    id: "audio.keep",
    kind: "audio",
    group: "Audio",
    label: "Keep audio tracks",
    description: "Chooses which audio tracks survive, by language.",
    outputs: [""],
    sections: [
      section("Tracks", [
        labelField,
        { key: "languages", label: "Languages", type: "string_list", default: ["eng"], mono: true, placeholder: "eng", hint: "ISO 639-2 tags. Tracks in another language are dropped." },
        { key: "keep_original", label: "Keep the original language", type: "bool", default: true, hint: "Looked up through the library's Arr connection." },
        { key: "drop_commentary", label: "Drop commentary tracks", type: "bool", default: true },
        { key: "fallback", label: "When no track matches", type: "enum", default: "keep_all", choices: choices(["keep_all", "Keep every track"], ["keep_first", "Keep the first track"], ["fail_job", "Fail the job"]) },
      ]),
    ],
  },
  {
    id: "audio.encode",
    kind: "audio",
    group: "Audio",
    label: "Encode audio",
    description: "Re-encodes audio tracks in the listed codecs.",
    outputs: [""],
    sections: [
      section("Encoder", [
        labelField,
        { key: "from_codecs", label: "Encode tracks in", type: "enum_list", default: ["truehd", "dts", "flac"], choices: LOSSLESS, hint: "Other tracks are copied." },
        { key: "codec", label: "Codec", type: "enum", default: "eac3", choices: AUDIO_CODECS, half: true },
        { key: "bitrate_per_channel", label: "Bitrate per channel", type: "int", default: 96, min: 32, max: 320, suffix: "kb/s", mono: true, half: true },
      ]),
    ],
  },
  {
    id: "audio.downmix",
    kind: "audio",
    group: "Audio",
    label: "Downmix to stereo",
    description: "Adds or replaces a track with a stereo downmix.",
    outputs: [""],
    sections: [
      section("Downmix", [
        labelField,
        { key: "mode", label: "Stereo track", type: "enum", default: "add", segmented: true, choices: choices(["add", "Add"], ["replace", "Replace"]) },
        { key: "codec", label: "Codec", type: "enum", default: "aac", choices: choices(["aac", "AAC"], ["opus", "Opus"]), half: true },
        { key: "bitrate", label: "Bitrate", type: "int", default: 192, min: 64, max: 512, suffix: "kb/s", mono: true, half: true },
      ]),
    ],
  },

  {
    id: "subtitles.keep",
    kind: "subtitles",
    group: "Subtitles",
    label: "Keep subtitles",
    description: "Chooses which subtitle tracks survive, by language.",
    outputs: [""],
    sections: [
      section("Tracks", [
        labelField,
        { key: "languages", label: "Languages", type: "string_list", default: ["eng"], mono: true, placeholder: "eng", hint: "ISO 639-2 tags." },
        { key: "keep_forced", label: "Keep forced tracks", type: "bool", default: true },
        { key: "drop_sdh", label: "Drop SDH tracks", type: "bool", default: false },
      ]),
    ],
  },
  {
    id: "subtitles.drop_bitmap",
    kind: "subtitles",
    group: "Subtitles",
    label: "Drop bitmap subtitles",
    description: "Removes image-based subtitle tracks.",
    outputs: [""],
    sections: [
      section("Tracks", [
        labelField,
        { key: "codecs", label: "Codecs", type: "enum_list", default: ["hdmv_pgs_subtitle", "dvd_subtitle"], choices: choices(["hdmv_pgs_subtitle", "PGS"], ["dvd_subtitle", "VobSub"], ["dvb_subtitle", "DVB"]) },
      ]),
    ],
  },
  {
    id: "subtitles.remove_all",
    kind: "subtitles",
    group: "Subtitles",
    label: "Remove all subtitles",
    description: "Drops every subtitle track.",
    outputs: [""],
    sections: [section("Tracks", [labelField])],
  },

  {
    id: "container.metadata",
    kind: "container",
    group: "Container",
    label: "Clean metadata",
    description: "Removes titles, tags and attachments the encode should not carry.",
    outputs: [""],
    sections: [
      section("Metadata", [
        labelField,
        { key: "strip_title", label: "Remove the file title", type: "bool", default: true },
        { key: "strip_chapters", label: "Remove chapters", type: "bool", default: false },
        { key: "strip_attachments", label: "Remove attachments", type: "bool", default: false, hint: "Fonts for styled subtitles are attachments." },
        { key: "tags", label: "Tags to remove", type: "string_list", default: ["encoder", "comment"], mono: true, placeholder: "comment" },
      ]),
    ],
  },
  {
    id: "container.remux",
    kind: "container",
    group: "Container",
    label: "Remux",
    description: "Sets the output container.",
    outputs: [""],
    sections: [
      section("Container", [
        labelField,
        { key: "container", label: "Container", type: "enum", default: "mkv", segmented: true, choices: choices("mkv", "mp4") },
        { key: "faststart", label: "Move the index to the start", type: "bool", default: true, hint: "Lets players start before the download ends.", visible_when: { key: "container", in: ["mp4"] } },
      ]),
    ],
  },

  {
    id: "output.publish",
    kind: "output",
    group: "Output",
    label: "Publish",
    description: "Runs the pipeline and publishes the result the way the library says.",
    outputs: [],
    sections: [
      section("Output", [
        labelField,
        { key: "only_if_smaller", label: "Only publish a smaller file", type: "bool", default: true, hint: "A larger encode is discarded and the file is marked skipped." },
        { key: "min_saving_percent", label: "Smallest saving", type: "float", default: 5, min: 0, max: 90, suffix: "%", mono: true, half: true, visible_when: { key: "only_if_smaller", in: [true] } },
        { key: "validate_duration", label: "Duration tolerance", type: "duration", default: "2s", mono: true, half: true, hint: "Largest difference from the source." },
      ]),
    ],
  },
  {
    id: "output.skip",
    kind: "output",
    group: "Output",
    label: "Skip file",
    description: "Leaves the file untouched and marks the job skipped.",
    outputs: [],
    sections: [
      section("Output", [labelField, { key: "reason", label: "Reason", type: "string", default: "", placeholder: "Already AV1", hint: "Shown on the file and in the job history." }]),
    ],
  },
];

export const NODE_TYPE_BY_ID = new Map(NODE_TYPES.map((t) => [t.id, t]));
