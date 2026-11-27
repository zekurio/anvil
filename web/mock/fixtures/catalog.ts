// The node catalog served by GET /node-types: a port of pkg/flow/nodes.go,
// conditions.go and actions.go. Ids, option keys, defaults, outputs, groups,
// labels and hints are the daemon's; change them there first.

import type { Choice, Field, NodeType, OptionValue, Section, Visibility } from "../../src/api/types.ts";

const choice = (value: string, label: string): Choice => ({ value, label });
const when = (key: string, ...values: OptionValue[]): Visibility => ({ key, in: values });
const section = (title: string, fields: Field[], extra: Partial<Section> = {}): Section => ({ title, ...extra, fields });

const CONDITION_OUTPUTS = ["true", "false"];

const comparison = (first: string, firstLabel: string, second: string, secondLabel: string): Field => ({
  key: "op",
  label: "Compare",
  type: "enum",
  default: first,
  choices: [choice(first, firstLabel), choice(second, secondLabel)],
  segmented: true,
  half: true,
});

const condition = (id: string, label: string, description: string, fields: Field[]): NodeType => ({
  id,
  kind: "condition",
  group: "Inspect",
  label,
  description,
  outputs: [...CONDITION_OUTPUTS],
  sections: [section("Condition", fields)],
});

export const RESOLUTION_TIERS: readonly (readonly [string, number, number])[] = [
  ["480p", 854, 480],
  ["720p", 1280, 720],
  ["1080p", 1920, 1080],
  ["1440p", 2560, 1440],
  ["2160p", 3840, 2160],
];

const search = () => when("quality_mode", "search");

const languages = (): Field => ({
  key: "languages",
  label: "Languages to keep",
  type: "string_list",
  tokens: true,
  hint: "Tags such as eng or jpn, or orig for the original language. Empty keeps every track.",
  default: [],
  mono: true,
  placeholder: "eng",
});

const fallback = (): Field => ({
  key: "fallback",
  label: "When no track matches",
  type: "enum",
  default: "keep_all",
  choices: [choice("keep_all", "Keep every track"), choice("keep_first", "Keep the first track"), choice("fail_job", "Fail the job")],
});

const unknownAsOriginal = (): Field => ({
  key: "unknown_as_original",
  label: "Count untagged tracks as the original language",
  type: "bool",
  hint: "Applies when the library's Arr connection knows the original language.",
  default: false,
});

const audioCodec = (): Field => ({
  key: "codec",
  label: "Codec",
  type: "enum",
  default: "eac3",
  choices: [choice("opus", "Opus"), choice("aac", "AAC"), choice("ac3", "Dolby Digital (AC-3)"), choice("eac3", "Dolby Digital Plus (E-AC-3)"), choice("flac", "FLAC")],
  half: true,
});

const audioBitrate = (): Field => ({
  key: "bitrate_kbps_per_channel",
  label: "Bitrate per channel",
  type: "int",
  hint: "A 5.1 track gets six times this. 0 uses the encoder's default.",
  default: 96,
  min: 0,
  max: 512,
  suffix: "kb/s",
  mono: true,
  half: true,
  // FLAC is lossless and takes no bitrate.
  visible_when: when("codec", "opus", "aac", "ac3", "eac3"),
});

const preserveOrStrip = (key: string, label: string): Field => ({
  key,
  label,
  type: "enum",
  default: "preserve",
  choices: [choice("preserve", "Preserve"), choice("strip", "Strip")],
  segmented: true,
  half: true,
});

export const NODE_TYPES: NodeType[] = [
  {
    id: "input",
    kind: "input",
    group: "Input",
    label: "Input file",
    description: "Where each queued file enters. A flow has one input.",
    outputs: [""],
    sections: [],
  },

  condition("check.video_codec", "Check video codec", "Branches on the codec of the main video stream.", [
    {
      key: "codecs",
      label: "Video codecs",
      type: "string_list",
      tokens: true,
      hint: "ffprobe names, such as h264, hevc, av1 or vp9. True when the video uses any of them.",
      default: ["av1"],
      mono: true,
      placeholder: "hevc",
    },
  ]),
  condition("check.resolution", "Check resolution", "Branches on the resolution class of the main video stream.", [
    comparison("at_least", "At least", "at_most", "At most"),
    {
      key: "resolution",
      label: "Resolution",
      type: "enum",
      hint: "A frame reaches a class by width or by height, so 1920x800 counts as 1080p.",
      default: "1080p",
      choices: RESOLUTION_TIERS.map(([name]) => choice(name, name)),
      mono: true,
      half: true,
    },
  ]),
  condition("check.hdr", "Check HDR", "Branches on whether the main video stream is HDR, and which kind.", [
    {
      key: "kinds",
      label: "HDR formats",
      type: "enum_list",
      hint: "True when the video is any of these. Dolby Vision on an HDR10 or HLG base layer counts as both.",
      default: ["hdr10", "hlg", "dolby_vision"],
      choices: [choice("hdr10", "HDR10"), choice("hlg", "HLG"), choice("dolby_vision", "Dolby Vision")],
    },
  ]),
  condition("check.bitrate", "Check bitrate", "Branches on the overall bitrate of the file.", [
    comparison("above", "Above", "below", "Below"),
    { key: "kbps", label: "Bitrate", type: "int", hint: "All streams together. False when the duration is unknown.", default: 8000, min: 1, suffix: "kb/s", mono: true, half: true },
  ]),
  condition("check.file_size", "Check file size", "Branches on the size of the file.", [
    comparison("above", "Above", "below", "Below"),
    { key: "megabytes", label: "Size", type: "int", default: 1024, min: 1, suffix: "MiB", mono: true, half: true },
  ]),
  condition("check.audio_language", "Check audio language", "Branches on the languages of the audio tracks.", [
    {
      key: "languages",
      label: "Languages",
      type: "string_list",
      tokens: true,
      hint: "Tags such as eng or jpn, or orig for the original language. True when any track matches.",
      default: ["eng"],
      mono: true,
      placeholder: "eng",
    },
  ]),
  condition("check.audio_codec", "Check audio codec", "Branches on the codecs of the audio tracks.", [
    {
      key: "codecs",
      label: "Audio codecs",
      type: "string_list",
      tokens: true,
      hint: "ffprobe names, such as truehd, dts, eac3 or aac. True when any track matches.",
      default: ["truehd", "dts"],
      mono: true,
      placeholder: "truehd",
    },
  ]),
  condition("check.audio_channels", "Check audio channels", "Branches on how many channels the audio tracks have.", [
    {
      key: "more_than",
      label: "More channels than",
      type: "enum",
      default: "2",
      choices: [choice("2", "Stereo"), choice("6", "5.1")],
      segmented: true,
    },
  ]),

  {
    id: "video.encode",
    kind: "video",
    group: "Video",
    label: "Encode video",
    description: "Re-encodes the video. With a quality target, anvil picks the highest CRF whose samples still meet it.",
    outputs: [""],
    sections: [
      section("Encoder", [
        { key: "codec", label: "Codec", type: "enum", default: "av1", choices: [choice("av1", "AV1"), choice("hevc", "HEVC (H.265)"), choice("h264", "H.264")], half: true },
        {
          key: "accelerator",
          label: "Encoder backend",
          type: "enum",
          default: "software",
          choices: [choice("software", "Software"), choice("qsv", "Intel Quick Sync"), choice("vaapi", "VAAPI"), choice("amf", "AMD AMF")],
          half: true,
        },
        { key: "preset", label: "Preset", type: "string", hint: "libsvtav1 takes 0 to 13. Lower is slower and smaller.", default: "6", mono: true, half: true },
        { key: "bit_depth", label: "Bit depth", type: "enum", default: "10", choices: [choice("10", "10-bit"), choice("8", "8-bit")], segmented: true, half: true },
      ]),
      section("Quality", [
        { key: "quality_mode", label: "Quality", type: "enum", default: "search", choices: [choice("search", "Quality target"), choice("fixed", "Fixed CRF")], segmented: true },
        {
          key: "crf",
          label: "CRF",
          type: "int",
          hint: "Lower is better quality and a larger file.",
          default: 28,
          min: 0,
          max: 255,
          mono: true,
          visible_when: when("quality_mode", "fixed"),
        },
        { key: "metric", label: "Metric", type: "enum", default: "vmaf", choices: [choice("vmaf", "VMAF"), choice("xpsnr", "XPSNR")], half: true, visible_when: search() },
        {
          key: "target",
          label: "Target",
          type: "float",
          hint: "Lowest score to accept. VMAF is usually 93 to 96, XPSNR 35 to 50.",
          default: 95,
          min: 0,
          max: 100,
          mono: true,
          half: true,
          visible_when: search(),
        },
        { key: "crf_min", label: "Lowest CRF", type: "int", default: 18, min: 0, max: 255, mono: true, half: true, visible_when: search() },
        { key: "crf_max", label: "Highest CRF", type: "int", default: 40, min: 0, max: 255, mono: true, half: true, visible_when: search() },
        {
          key: "min_savings_percent",
          label: "Smallest saving worth encoding",
          type: "float",
          hint: "Video stream only, estimated from the samples.",
          default: 20,
          min: 0,
          max: 100,
          suffix: "%",
          mono: true,
          visible_when: search(),
        },
        {
          key: "on_no_fit",
          label: "When no CRF fits",
          type: "enum",
          hint: "A CRF fits when it meets both the target and the saving.",
          default: "copy",
          choices: [choice("copy", "Copy the video"), choice("encode", "Encode anyway")],
          segmented: true,
          visible_when: search(),
        },
      ]),
      section(
        "Advanced",
        [
          { key: "samples", label: "Samples per CRF", type: "int", hint: "0 uses one sample per 12 minutes of runtime.", default: 0, min: 0, mono: true, half: true, visible_when: search() },
          { key: "sample_duration", label: "Sample length", type: "duration", default: "20s", mono: true, half: true, visible_when: search() },
          {
            key: "ffmpeg_args",
            label: "Extra encoder arguments",
            type: "string_list",
            hint: "One option and its value per entry. Only encoder parameters and value-only tuning options are allowed.",
            default: [],
            mono: true,
            placeholder: "-svtav1-params",
          },
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
    description: "Keeps the source video as it is.",
    outputs: [""],
    sections: [],
  },
  {
    id: "video.crop",
    kind: "video",
    group: "Video",
    label: "Crop black bars",
    description: "Crops black bars during the encode. Copied video is never cropped.",
    outputs: [""],
    sections: [
      section("Safety", [
        {
          key: "min_retained_area_percent",
          label: "Smallest picture to keep",
          type: "float",
          hint: "A crop that keeps less is ignored, and the file is encoded uncropped.",
          default: 70,
          min: 0,
          min_exclusive: true,
          max: 100,
          suffix: "%",
          mono: true,
        },
      ]),
      section(
        "Advanced",
        [
          { key: "samples", label: "Sample windows", type: "int", hint: "0 uses one window per 3 minutes of runtime, between 4 and 24.", default: 0, min: 0, mono: true, half: true },
          { key: "frame_count", label: "Frames per window", type: "int", default: 300, min: 1, mono: true, half: true },
          {
            key: "seek_offsets",
            label: "Sample at these offsets",
            type: "string_list",
            tokens: true,
            hint: "Such as 5m or 1h10m. Empty spreads the windows across the file.",
            default: [],
            mono: true,
            placeholder: "5m",
          },
          { key: "limit", label: "Black threshold", type: "int", hint: "The cropdetect limit. Higher treats brighter pixels as black.", default: 64, min: 1, max: 255, mono: true, half: true },
          { key: "round", label: "Round to a multiple of", type: "int", default: 16, min: 1, suffix: "px", mono: true, half: true },
          { key: "reset_count", label: "Reset after frames", type: "int", hint: "0 never resets the largest area seen.", default: 0, min: 0, mono: true, half: true },
          { key: "required_alignment", label: "Align to a multiple of", type: "int", default: 2, min: 1, suffix: "px", mono: true, half: true },
          { key: "min_width", label: "Narrowest crop", type: "int", default: 128, min: 1, suffix: "px", mono: true, half: true },
          { key: "min_height", label: "Shortest crop", type: "int", default: 128, min: 1, suffix: "px", mono: true, half: true },
        ],
        { collapsible: true, collapsed: true },
      ),
    ],
  },

  {
    id: "audio.keep",
    kind: "audio",
    group: "Audio",
    label: "Keep audio tracks",
    description: "Keeps audio tracks by language and removes the rest.",
    outputs: [""],
    sections: [section("Languages", [languages(), fallback()]), section("Filters", [{ key: "keep_commentary", label: "Keep commentary tracks", type: "bool", default: false }, unknownAsOriginal()])],
  },
  {
    id: "audio.encode",
    kind: "audio",
    group: "Audio",
    label: "Encode audio",
    description: "Re-encodes kept audio tracks to one codec. Tracks already in it are copied.",
    outputs: [""],
    sections: [
      section("Target", [audioCodec(), audioBitrate()]),
      section("Filter", [
        {
          key: "source_codecs",
          label: "Only these source codecs",
          type: "string_list",
          tokens: true,
          hint: "ffprobe names, such as truehd, dts or flac. Empty encodes every kept track.",
          default: ["truehd", "dts"],
          mono: true,
          placeholder: "truehd",
        },
      ]),
    ],
  },
  {
    id: "audio.downmix",
    kind: "audio",
    group: "Audio",
    label: "Downmix audio",
    description: "Downmixes tracks that have more channels than the chosen layout.",
    outputs: [""],
    sections: [
      section("Layout", [
        {
          key: "max_channels",
          label: "Largest layout to keep",
          type: "enum",
          default: "6",
          choices: [choice("6", "5.1"), choice("2", "Stereo")],
          segmented: true,
        },
      ]),
      section("Encoding", [audioCodec(), audioBitrate()]),
    ],
  },

  {
    id: "subtitles.keep",
    kind: "subtitles",
    group: "Subtitles",
    label: "Keep subtitles",
    description: "Keeps subtitle tracks by language. Subtitles are copied, never converted.",
    outputs: [""],
    sections: [
      section("Languages", [languages(), fallback()]),
      section("Filters", [
        { key: "keep_forced", label: "Keep forced tracks", type: "bool", default: false },
        { key: "keep_sdh", label: "Keep SDH tracks", type: "bool", hint: "Subtitles for the deaf and hard of hearing.", default: false },
        { key: "keep_commentary", label: "Keep commentary tracks", type: "bool", default: false },
        unknownAsOriginal(),
      ]),
    ],
  },
  {
    id: "subtitles.drop_bitmap",
    kind: "subtitles",
    group: "Subtitles",
    label: "Remove image subtitles",
    description: "Removes PGS, VobSub and DVB subtitles. Text subtitles stay.",
    outputs: [""],
    sections: [],
  },
  {
    id: "subtitles.remove_all",
    kind: "subtitles",
    group: "Subtitles",
    label: "Remove all subtitles",
    description: "Removes every subtitle track, overriding Keep subtitles.",
    outputs: [""],
    sections: [],
  },

  {
    id: "container.metadata",
    kind: "container",
    group: "Container",
    label: "Clean metadata",
    description: "Keeps or strips metadata, track titles, attachments and chapters.",
    outputs: [""],
    sections: [
      section("Metadata", [
        // Three choices need the full row, so the two fields above the pair each get one.
        { ...preserveOrStrip("metadata", "Container metadata"), half: false },
        {
          key: "track_titles",
          label: "Track titles",
          type: "enum",
          hint: "Standardize rewrites each title from the track's codec, channels, and language.",
          default: "strip",
          choices: [choice("preserve", "Preserve"), choice("strip", "Strip"), choice("standardize", "Standardize")],
          segmented: true,
        },
        preserveOrStrip("attachments", "Attachments"),
        preserveOrStrip("chapters", "Chapters"),
      ]),
    ],
  },

  {
    id: "output.publish",
    kind: "output",
    group: "Output",
    label: "Publish result",
    description: "Publishes the result as the library is set up, in place or by handoff.",
    outputs: [],
    sections: [],
  },
  {
    id: "output.skip",
    kind: "output",
    group: "Output",
    label: "Skip file",
    description: "Leaves the file untouched and marks the job skipped.",
    outputs: [],
    sections: [],
  },
];

export const NODE_TYPE_BY_ID = new Map(NODE_TYPES.map((t) => [t.id, t]));
