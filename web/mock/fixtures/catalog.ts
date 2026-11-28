// The node catalog served by GET /node-types: a port of pkg/flow/nodes.go,
// conditions.go and actions.go, with the encoder variants of the video.encode
// node built from the registry in pkg/video/encoders.go. Ids, option keys,
// defaults, outputs, groups, labels and hints are the daemon's; change them
// there first.

import type { Choice, Field, FieldVariant, NodeType, OptionValue, Section, Visibility } from "../../src/api/types.ts";

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

/* ---------- video.encode (pkg/flow/actions.go, pkg/video/encoders.go) ---------- */

interface MockSpeed {
  /** The ffmpeg option the preset goes to; empty when the encoder has none. */
  option: string;
  default: string;
  choices?: string[];
  min?: number;
  max?: number;
}

interface MockQuality {
  label: string;
  min: number;
  max: number;
  default: number;
  searchMin: number;
  searchMax: number;
}

interface MockEncoder {
  name: string;
  codec: string;
  backend: string;
  speed: MockSpeed;
  quality: MockQuality;
  bitDepths: number[];
  /** The encoder's parameter bundle, without the leading "-". */
  paramsOption: string;
  tuning: string[];
}

const x26xSpeed = (): MockSpeed => ({ option: "-preset", default: "medium", choices: ["ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow", "placebo"] });
const qsvSpeed = (): MockSpeed => ({ option: "-preset", default: "medium", choices: ["veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"] });
const amfSpeed = (): MockSpeed => ({ option: "-quality", default: "balanced", choices: ["speed", "balanced", "quality"] });
const amfAV1Speed = (): MockSpeed => ({ option: "-quality", default: "balanced", choices: ["speed", "balanced", "quality", "high_quality"] });
const noSpeed = (): MockSpeed => ({ option: "", default: "" });

const crf = (): MockQuality => ({ label: "CRF", min: 0, max: 51, default: 23, searchMin: 18, searchMax: 36 });

/** The encoder registry of pkg/video/encoders.go: same order, same choices, same limits. */
const MOCK_ENCODERS: MockEncoder[] = [
  { name: "libsvtav1", codec: "av1", backend: "software", speed: { option: "-preset", default: "6", min: 0, max: 13 }, quality: { label: "CRF", min: 1, max: 63, default: 28, searchMin: 18, searchMax: 40 }, bitDepths: [8, 10], paramsOption: "svtav1-params", tuning: ["g"] },
  { name: "libx265", codec: "hevc", backend: "software", speed: x26xSpeed(), quality: crf(), bitDepths: [8, 10], paramsOption: "x265-params", tuning: ["g", "bf", "refs", "tune", "profile"] },
  { name: "libx264", codec: "h264", backend: "software", speed: x26xSpeed(), quality: crf(), bitDepths: [8, 10], paramsOption: "x264-params", tuning: ["g", "keyint_min", "bf", "refs", "sc_threshold", "tune", "profile", "level", "aq-mode", "aq-strength", "rc-lookahead", "b_strategy"] },
  { name: "av1_qsv", codec: "av1", backend: "qsv", speed: qsvSpeed(), quality: { label: "ICQ", min: 1, max: 51, default: 23, searchMin: 18, searchMax: 36 }, bitDepths: [8, 10], paramsOption: "", tuning: ["g", "bf", "refs", "profile", "low_power", "async_depth", "b_strategy", "extbrc", "look_ahead_depth", "adaptive_i", "adaptive_b"] },
  { name: "hevc_qsv", codec: "hevc", backend: "qsv", speed: qsvSpeed(), quality: { label: "ICQ", min: 1, max: 51, default: 23, searchMin: 18, searchMax: 36 }, bitDepths: [8, 10], paramsOption: "", tuning: ["g", "bf", "refs", "profile", "low_power", "async_depth", "b_strategy", "extbrc", "look_ahead_depth", "adaptive_i", "adaptive_b"] },
  { name: "h264_qsv", codec: "h264", backend: "qsv", speed: qsvSpeed(), quality: { label: "ICQ", min: 1, max: 51, default: 23, searchMin: 18, searchMax: 36 }, bitDepths: [8], paramsOption: "", tuning: ["g", "bf", "refs", "profile", "low_power", "async_depth", "b_strategy", "extbrc", "look_ahead_depth", "adaptive_i", "adaptive_b", "look_ahead"] },
  { name: "av1_vaapi", codec: "av1", backend: "vaapi", speed: noSpeed(), quality: { label: "QP", min: 1, max: 255, default: 110, searchMin: 70, searchMax: 170 }, bitDepths: [8, 10], paramsOption: "", tuning: ["g", "bf", "refs", "profile", "level", "low_power", "async_depth"] },
  { name: "hevc_vaapi", codec: "hevc", backend: "vaapi", speed: noSpeed(), quality: { label: "QP", min: 1, max: 51, default: 23, searchMin: 18, searchMax: 36 }, bitDepths: [8, 10], paramsOption: "", tuning: ["g", "bf", "refs", "profile", "level", "low_power", "async_depth"] },
  { name: "h264_vaapi", codec: "h264", backend: "vaapi", speed: noSpeed(), quality: { label: "QP", min: 1, max: 51, default: 23, searchMin: 18, searchMax: 36 }, bitDepths: [8], paramsOption: "", tuning: ["g", "bf", "refs", "profile", "level", "low_power", "async_depth", "quality"] },
  { name: "av1_amf", codec: "av1", backend: "amf", speed: amfAV1Speed(), quality: { label: "QP", min: 0, max: 255, default: 110, searchMin: 70, searchMax: 170 }, bitDepths: [8, 10], paramsOption: "", tuning: ["g", "profile", "level", "async_depth"] },
  { name: "hevc_amf", codec: "hevc", backend: "amf", speed: amfSpeed(), quality: { label: "QP", min: 0, max: 51, default: 23, searchMin: 18, searchMax: 36 }, bitDepths: [8, 10], paramsOption: "", tuning: ["g", "profile", "level", "async_depth"] },
  { name: "h264_amf", codec: "h264", backend: "amf", speed: amfSpeed(), quality: { label: "QP", min: 0, max: 51, default: 23, searchMin: 18, searchMax: 36 }, bitDepths: [8], paramsOption: "", tuning: ["g", "profile", "level", "async_depth", "bf", "refs"] },
];

type EncoderFieldKey = "preset" | "bit_depth" | "crf" | "crf_min" | "crf_max" | "ffmpeg_args";
/** One encoder's answer about a field, before its condition is attached. */
type EncoderVariant = Omit<FieldVariant, "when">;

const DEFAULT_BIT_DEPTH = 10;

/** pkg/flow/actions.go encoderFieldVariant: what one encoder changes about one field. */
function encoderVariant(key: EncoderFieldKey, encoder: MockEncoder): EncoderVariant {
  const quality = encoder.quality;
  switch (key) {
    case "preset": {
      const choices = (encoder.speed.choices ?? []).map((value) => choice(value, value));
      if (encoder.speed.option !== "" && choices.length === 0) for (let n = encoder.speed.min ?? 0; n <= (encoder.speed.max ?? 0); n++) choices.push(choice(String(n), String(n)));
      return { choices, default: encoder.speed.default, ...(encoder.speed.option === "" ? { hidden: true } : {}) };
    }
    case "bit_depth": {
      const choices = encoder.bitDepths.map((depth) => choice(String(depth), `${depth}-bit`));
      const depth = encoder.bitDepths.includes(DEFAULT_BIT_DEPTH) ? DEFAULT_BIT_DEPTH : (encoder.bitDepths[0] ?? 8);
      return { choices, default: String(depth), ...(choices.length === 1 ? { hidden: true } : {}) };
    }
    case "crf":
      return { label: quality.label, min: quality.min, max: quality.max, default: quality.default };
    case "crf_min":
      return { label: `Lowest ${quality.label}`, min: quality.min, max: quality.max, default: quality.searchMin };
    case "crf_max":
      return { label: `Highest ${quality.label}`, min: quality.min, max: quality.max, default: quality.searchMax };
    case "ffmpeg_args": {
      const accepts = [...(encoder.paramsOption === "" ? [] : [`-${encoder.paramsOption}`]), ...encoder.tuning.map((option) => `-${option}`)];
      const first = accepts[0];
      if (first === undefined) return { hidden: true };
      return { placeholder: first, hint: `Enter each option and value separately. Accepted options: ${accepts.join(", ")}.` };
    }
  }
}

/** pkg/flow/actions.go applyVariant, over a copy. */
function applyVariant(field: Field, variant: EncoderVariant): Field {
  const next = { ...field };
  if (variant.label !== undefined) next.label = variant.label;
  if (variant.hint !== undefined) next.hint = variant.hint;
  if (variant.choices !== undefined) next.choices = variant.choices;
  if (variant.min !== undefined) next.min = variant.min;
  if (variant.max !== undefined) next.max = variant.max;
  if (variant.default !== undefined) next.default = variant.default;
  if (variant.placeholder !== undefined) next.placeholder = variant.placeholder;
  return next;
}

/** A field whose choices, bounds and default follow the encoder: the fallback is applied over the base, then one variant per encoder. */
function encoderField(key: EncoderFieldKey, base: Field): Field {
  const fallback = MOCK_ENCODERS[0];
  const resolved = fallback ? applyVariant(base, encoderVariant(key, fallback)) : base;
  return {
    ...resolved,
    variants: MOCK_ENCODERS.map((encoder) => ({ ...encoderVariant(key, encoder), when: [when("codec", encoder.codec), when("accelerator", encoder.backend)] })),
  };
}

const videoEncoderFields = () =>
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
    encoderField("preset", { key: "preset", label: "Preset", type: "enum", default: "", mono: true, half: true }),
    encoderField("bit_depth", { key: "bit_depth", label: "Bit depth", type: "enum", default: "", segmented: true, half: true }),
  ]);


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
    { key: "megabytes", label: "Size", type: "int", default: 1024, min: 1, max: 16777216, suffix: "MiB", mono: true, half: true },
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
    description: "With a quality target, anvil picks the highest encoder setting that meets it.",
    outputs: [""],
    sections: [
      videoEncoderFields(),
      section("Quality", [
        { key: "quality_mode", label: "Mode", type: "enum", default: "search", choices: [choice("search", "Quality target"), choice("fixed", "Fixed quality")], segmented: true },
        encoderField("crf", { key: "crf", label: "CRF", type: "int", hint: "Lower is better quality and a larger file.", default: 0, mono: true, visible_when: when("quality_mode", "fixed") }),
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
        encoderField("crf_min", { key: "crf_min", label: "Lowest CRF", type: "int", default: 0, mono: true, half: true, visible_when: search() }),
        encoderField("crf_max", { key: "crf_max", label: "Highest CRF", type: "int", default: 0, mono: true, half: true, visible_when: search() }),
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
          label: "When no setting fits",
          type: "enum",
          hint: "A setting must meet both the quality target and the minimum saving.",
          default: "copy",
          choices: [choice("copy", "Copy the video"), choice("encode", "Encode anyway")],
          segmented: true,
          visible_when: search(),
        },
      ]),
      section(
        "Advanced",
        [
          { key: "samples", label: "Samples per quality value", type: "int", hint: "0 uses one sample per 12 minutes of runtime.", default: 0, min: 0, max: 100, mono: true, half: true, visible_when: search() },
          { key: "sample_duration", label: "Sample length", type: "duration", default: "20s", mono: true, half: true, visible_when: search() },
          encoderField("ffmpeg_args", { key: "ffmpeg_args", label: "Extra encoder arguments", type: "string_list", default: [], mono: true }),
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
          { key: "samples", label: "Sample windows", type: "int", hint: "0 uses one window per 3 minutes of runtime, between 4 and 24.", default: 0, min: 0, max: 100, mono: true, half: true },
          { key: "frame_count", label: "Frames per window", type: "int", default: 300, min: 1, max: 10000, mono: true, half: true },
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
    id: "output.replace",
    kind: "output",
    group: "Output",
    label: "Replace file",
    description: "Swaps the source for the result in place.",
    outputs: [],
    sections: [],
  },
  {
    id: "output.move",
    kind: "output",
    group: "Output",
    label: "Move file",
    description: "Publishes the result to another folder, such as the one an importer watches, and deletes the source.",
    outputs: [],
    sections: [
      section("Destination", [
        { key: "destination", label: "Folder", type: "path", default: "", mono: true, placeholder: "/srv/imports/tv" },
        { key: "preserve_structure", label: "Keep folder structure", type: "bool", hint: "Recreates the folders below the library root. Off puts every file directly in the folder.", default: true },
        { key: "keep_source", label: "Keep source", type: "bool", hint: "Leaves the source in place, for a downloader that still seeds it.", default: false },
        {
          key: "prune_empty_dirs",
          label: "Remove emptied folders",
          type: "bool",
          hint: "Deletes the folders the source leaves behind, with residue that matches the library's ignorable globs.",
          default: false,
          visible_when: when("keep_source", false),
        },
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
    sections: [],
  },
];

export const NODE_TYPE_BY_ID = new Map(NODE_TYPES.map((t) => [t.id, t]));
