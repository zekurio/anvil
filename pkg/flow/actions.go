package flow

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/video"
)

// The defaults below come from pkg/config, so a node left at its defaults
// behaves like the same section of the built-in TOML profile.

func videoEncodeNode() nodeDef {
	search := func() *Visibility {
		return when("quality_mode", string(domain.VideoQualitySearch))
	}
	return nodeDef{
		NodeType: NodeType{
			ID:    typeVideoEncode,
			Kind:  KindVideo,
			Group: groupVideo,
			Label: "Encode video",
			Description: "Re-encodes the video stream. With a quality target, Anvil measures samples of each file " +
				"and picks the highest CRF that still meets it.",
			Outputs: []string{""},
			Sections: []Section{
				{Title: "Encoder", Fields: []Field{
					{
						Key:     "codec",
						Label:   "Codec",
						Type:    FieldEnum,
						Default: "av1",
						Choices: []Choice{
							{Value: "av1", Label: "AV1"},
							{Value: "hevc", Label: "HEVC (H.265)"},
							{Value: "h264", Label: "H.264"},
						},
						Half: true,
					},
					{
						Key:     "accelerator",
						Label:   "Encoder backend",
						Type:    FieldEnum,
						Default: video.AcceleratorSoftware,
						Choices: []Choice{
							{Value: video.AcceleratorSoftware, Label: "Software"},
							{Value: video.AcceleratorQSV, Label: "Intel Quick Sync"},
							{Value: video.AcceleratorVAAPI, Label: "VAAPI"},
							{Value: video.AcceleratorAMF, Label: "AMD AMF"},
						},
						Half: true,
					},
					{
						Key:     "preset",
						Label:   "Preset",
						Type:    FieldString,
						Hint:    "Passed to the encoder as written. libsvtav1 takes 0 to 13, and lower is slower and smaller.",
						Default: "6",
						Mono:    true,
						Half:    true,
					},
					{
						Key:       "bit_depth",
						Label:     "Bit depth",
						Type:      FieldEnum,
						Default:   strconv.Itoa(video.DefaultBitDepth),
						Choices:   []Choice{{Value: "10", Label: "10-bit"}, {Value: "8", Label: "8-bit"}},
						Segmented: true,
						Half:      true,
					},
				}},
				{Title: "Quality", Fields: []Field{
					{
						Key:     "quality_mode",
						Label:   "Quality",
						Type:    FieldEnum,
						Default: string(domain.VideoQualitySearch),
						Choices: []Choice{
							{Value: string(domain.VideoQualitySearch), Label: "Quality target"},
							{Value: string(domain.VideoQualityFixed), Label: "Fixed CRF"},
						},
						Segmented: true,
					},
					{
						Key:         "crf",
						Label:       "CRF",
						Type:        FieldInt,
						Hint:        "Lower is better quality and a larger file. No samples are measured and no savings are required.",
						Default:     28,
						Min:         bound(0),
						Max:         bound(255),
						Mono:        true,
						VisibleWhen: when("quality_mode", string(domain.VideoQualityFixed)),
					},
					{
						Key:         "metric",
						Label:       "Metric",
						Type:        FieldEnum,
						Default:     string(domain.QualityMetricVMAF),
						Choices:     []Choice{{Value: "vmaf", Label: "VMAF"}, {Value: "xpsnr", Label: "XPSNR"}},
						Half:        true,
						VisibleWhen: search(),
					},
					{
						Key:         "target",
						Label:       "Target",
						Type:        FieldFloat,
						Hint:        "Lowest score to accept. VMAF targets are usually 93 to 96, XPSNR targets 35 to 50.",
						Default:     float64(config.DefaultTargetVMAF),
						Min:         bound(0),
						Max:         bound(100),
						Mono:        true,
						Half:        true,
						VisibleWhen: search(),
					},
					{
						Key:         "crf_min",
						Label:       "Lowest CRF",
						Type:        FieldInt,
						Default:     config.DefaultCRFMin,
						Min:         bound(0),
						Max:         bound(255),
						Mono:        true,
						Half:        true,
						VisibleWhen: search(),
					},
					{
						Key:         "crf_max",
						Label:       "Highest CRF",
						Type:        FieldInt,
						Default:     config.DefaultCRFMax,
						Min:         bound(0),
						Max:         bound(255),
						Mono:        true,
						Half:        true,
						VisibleWhen: search(),
					},
					{
						Key:         "min_savings_percent",
						Label:       "Smallest saving worth encoding",
						Type:        FieldFloat,
						Hint:        "Estimated from the samples, for the video stream only.",
						Default:     float64(config.DefaultMinSavingsPct),
						Min:         bound(0),
						Max:         bound(100),
						Suffix:      "%",
						Mono:        true,
						VisibleWhen: search(),
					},
					{
						Key:     "on_no_fit",
						Label:   "When no CRF fits",
						Type:    FieldEnum,
						Hint:    "What happens when no CRF in the range meets both the target and the saving.",
						Default: "copy",
						Choices: []Choice{
							{Value: "copy", Label: "Copy the video"},
							{Value: "encode", Label: "Encode anyway"},
						},
						Segmented:   true,
						VisibleWhen: search(),
					},
				}},
				{Title: "Advanced", Collapsible: true, Collapsed: true, Fields: []Field{
					{
						Key:         "samples",
						Label:       "Samples per CRF",
						Type:        FieldInt,
						Hint:        "0 uses one sample per 12 minutes of runtime.",
						Default:     0,
						Min:         bound(0),
						Mono:        true,
						Half:        true,
						VisibleWhen: search(),
					},
					{
						Key:         "sample_duration",
						Label:       "Sample length",
						Type:        FieldDuration,
						Default:     "20s",
						Mono:        true,
						Half:        true,
						VisibleWhen: search(),
					},
					{
						Key:   "ffmpeg_args",
						Label: "Extra encoder arguments",
						Type:  FieldStringList,
						Hint: "Option and value pairs, one per entry, used for the search and the final encode. " +
							"Stream mapping, filters, timing, and rate control stay with Anvil.",
						Default:     []string{},
						Mono:        true,
						Placeholder: "-svtav1-params",
					},
				}},
			},
		},
		check: func(o options) []optionProblem {
			settings := encodeSettings(o)
			problems := ruleProblems(config.VideoConfig{
				Codec:             settings.Codec,
				Accelerator:       settings.Accelerator,
				Preset:            settings.Preset,
				BitDepth:          settings.BitDepth,
				CRFMin:            settings.CRFMin,
				CRFMax:            settings.CRFMax,
				Samples:           settings.Samples,
				SampleDuration:    config.Duration{Duration: settings.SampleDuration},
				Metric:            string(settings.Metric),
				Target:            settings.Target,
				MinSavingsPercent: settings.MinSavingsPercent,
				FFmpegArgs:        settings.FFmpegArgs,
			}.Problems())
			return append(problems, qualityRangeProblems(settings)...)
		},
		warn: func(o options) []optionProblem {
			settings := encodeSettings(o)
			if settings.QualityMode == domain.VideoQualityFixed || settings.Metric != domain.QualityMetricXPSNR || settings.Target <= xpsnrPlausibleMax {
				return nil
			}
			return []optionProblem{{
				Field: "target",
				Message: fmt.Sprintf("An XPSNR target of %s dB is higher than encodes reach, so no CRF will meet it. Typical targets are 35 to 50.",
					formatNumber(settings.Target)),
			}}
		},
		summary: func(o options) string {
			settings := encodeSettings(o)
			parts := []string{video.ResolveEncoder(settings.Codec, settings.Accelerator)}
			if settings.QualityMode == domain.VideoQualityFixed {
				parts = append(parts, "crf "+strconv.Itoa(settings.CRF))
			} else {
				parts = append(parts, string(settings.Metric)+" "+formatNumber(settings.Target))
			}
			if settings.Preset != "" {
				parts = append(parts, "preset "+settings.Preset)
			}
			return strings.Join(parts, dot)
		},
		apply: func(o options, profile *domain.Profile) {
			profile.Video = encodeSettings(o)
		},
	}
}

// xpsnrPlausibleMax is the highest XPSNR target, in dB, that is not flagged.
// Visually lossless encodes score around 50; the field still accepts up to
// 100 because the VMAF scale shares it.
const xpsnrPlausibleMax = 60

// qualityRangeProblems checks the CRF values in effect against what the
// encoder the node resolves to accepts. Outside that range an encoder either
// refuses to start or silently substitutes its default.
func qualityRangeProblems(settings domain.VideoProfile) []optionProblem {
	if !slices.Contains([]string{"av1", "hevc", "h264"}, settings.Codec) {
		// The codec already has its own problem, and no encoder to ask.
		return nil
	}
	encoder := video.ResolveEncoder(settings.Codec, settings.Accelerator)
	lowest, highest := video.QualityRange(encoder)
	fields := []struct {
		key   string
		value int
	}{{"crf_min", settings.CRFMin}, {"crf_max", settings.CRFMax}}
	if settings.QualityMode == domain.VideoQualityFixed {
		fields = fields[:1]
		fields[0].key, fields[0].value = "crf", settings.CRF
	}
	var problems []optionProblem
	for _, field := range fields {
		if field.value < lowest || field.value > highest {
			problems = append(problems, optionProblem{
				Field:   field.key,
				Message: fmt.Sprintf("Must be between %d and %d for %s.", lowest, highest, encoder),
			})
		}
	}
	return problems
}

// encodeSettings reads a video.encode node. Search mode leaves QualityMode
// at its zero value, which means search, so the result equals what the TOML
// profile path produces for the same settings.
func encodeSettings(o options) domain.VideoProfile {
	settings := domain.VideoProfile{
		Codec:              o.text("codec"),
		Accelerator:        o.text("accelerator"),
		Preset:             strings.TrimSpace(o.text("preset")),
		BitDepth:           8,
		CRFMin:             o.whole("crf_min"),
		CRFMax:             o.whole("crf_max"),
		Samples:            o.whole("samples"),
		SampleDuration:     o.span("sample_duration"),
		Metric:             domain.QualityMetric(o.text("metric")),
		Target:             o.number("target"),
		MinSavingsPercent:  o.number("min_savings_percent"),
		ForceEncodeOnNoFit: o.text("on_no_fit") == "encode",
		FFmpegArgs:         cloneList(o.list("ffmpeg_args")),
	}
	if o.text("bit_depth") == "10" {
		settings.BitDepth = 10
	}
	if o.text("quality_mode") == string(domain.VideoQualityFixed) {
		settings.QualityMode = domain.VideoQualityFixed
		settings.CRF = o.whole("crf")
	}
	return settings
}

func videoCopyNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeVideoCopy,
			Kind:        KindVideo,
			Group:       groupVideo,
			Label:       "Copy video",
			Description: "Keeps the source video stream as it is. Put it on a branch that must not re-encode.",
			Outputs:     []string{""},
			Sections:    []Section{},
		},
		summary: fixedSummary("keep the source video stream"),
		apply: func(_ options, profile *domain.Profile) {
			profile.Video = baseProfile().Video
		},
	}
}

func videoCropNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:    typeVideoCrop,
			Kind:  KindVideo,
			Group: groupVideo,
			Label: "Crop black bars",
			Description: "Detects black bars and crops them away during the encode. " +
				"Cropping needs a re-encode, so nothing is cropped where the video is copied.",
			Outputs: []string{""},
			Sections: []Section{
				{Title: "Safety", Fields: []Field{{
					Key:     "min_retained_area_percent",
					Label:   "Smallest picture to keep",
					Type:    FieldFloat,
					Hint:    "A detected crop that keeps less of the frame is discarded and the file is encoded uncropped.",
					Default: float64(config.DefaultCropMinRetainedAreaPct),
					Min:     bound(0),
					Max:     bound(100),
					Suffix:  "%",
					Mono:    true,
				}}},
				{Title: "Advanced", Collapsible: true, Collapsed: true, Fields: []Field{
					{
						Key:     "samples",
						Label:   "Sample windows",
						Type:    FieldInt,
						Hint:    "0 uses one window per 3 minutes of runtime, between 4 and 24.",
						Default: 0,
						Min:     bound(0),
						Mono:    true,
						Half:    true,
					},
					{
						Key:     "frame_count",
						Label:   "Frames per window",
						Type:    FieldInt,
						Default: config.DefaultCropFrameCount,
						Min:     bound(1),
						Mono:    true,
						Half:    true,
					},
					{
						Key:         "seek_offsets",
						Label:       "Sample at these offsets",
						Type:        FieldStringList,
						Hint:        "Durations such as 5m or 1h10m. Leave empty to spread the windows across the file.",
						Default:     []string{},
						Mono:        true,
						Placeholder: "5m",
					},
					{
						Key:     "limit",
						Label:   "Black threshold",
						Type:    FieldInt,
						Hint:    "The cropdetect limit. Higher treats brighter pixels as black.",
						Default: config.DefaultCropDetectLimit,
						Min:     bound(1),
						Max:     bound(255),
						Mono:    true,
						Half:    true,
					},
					{
						Key:     "round",
						Label:   "Round to a multiple of",
						Type:    FieldInt,
						Default: config.DefaultCropDetectRound,
						Min:     bound(1),
						Suffix:  "px",
						Mono:    true,
						Half:    true,
					},
					{
						Key:     "reset_count",
						Label:   "Reset after frames",
						Type:    FieldInt,
						Hint:    "0 never resets the largest area seen.",
						Default: config.DefaultCropDetectResetCount,
						Min:     bound(0),
						Mono:    true,
						Half:    true,
					},
					{
						Key:     "required_alignment",
						Label:   "Align to a multiple of",
						Type:    FieldInt,
						Default: config.DefaultCropRequiredAlignment,
						Min:     bound(1),
						Suffix:  "px",
						Mono:    true,
						Half:    true,
					},
					{
						Key:     "min_width",
						Label:   "Narrowest crop",
						Type:    FieldInt,
						Default: config.DefaultCropMinWidth,
						Min:     bound(1),
						Suffix:  "px",
						Mono:    true,
						Half:    true,
					},
					{
						Key:     "min_height",
						Label:   "Shortest crop",
						Type:    FieldInt,
						Default: config.DefaultCropMinHeight,
						Min:     bound(1),
						Suffix:  "px",
						Mono:    true,
						Half:    true,
					},
				}},
			},
		},
		check: func(o options) []optionProblem {
			var problems []optionProblem
			policy, bad := cropPolicy(o)
			if bad != "" {
				problems = append(problems, optionProblem{
					Field:   "seek_offsets",
					Message: fmt.Sprintf("Entries must be durations such as 5m or 1h10m. %q is not one.", bad),
				})
			}
			offsets := make([]config.Duration, 0, len(policy.SeekOffsets))
			for _, offset := range policy.SeekOffsets {
				offsets = append(offsets, config.Duration{Duration: offset})
			}
			return append(problems, ruleProblems(config.CropConfig{
				SeekOffsets:        offsets,
				Samples:            policy.Samples,
				FrameCount:         policy.FrameCount,
				Limit:              policy.Limit,
				Round:              policy.Round,
				ResetCount:         policy.ResetCount,
				MinRetainedAreaPct: policy.MinRetainedAreaPercent,
				MinWidth:           policy.MinWidth,
				MinHeight:          policy.MinHeight,
				RequiredAlignment:  policy.RequiredAlignment,
			}.Problems())...)
		},
		summary: func(o options) string {
			return fmt.Sprintf("keep at least %s%%%slimit %d",
				formatNumber(o.number("min_retained_area_percent")), dot, o.whole("limit"))
		},
		apply: func(o options, profile *domain.Profile) {
			profile.Crop, _ = cropPolicy(o)
		},
	}
}

// cropPolicy reads a video.crop node. bad is the first seek offset that is
// not a duration; such entries are left out of the policy.
func cropPolicy(o options) (policy domain.CropPolicy, bad string) {
	policy = domain.CropPolicy{
		Samples:                o.whole("samples"),
		FrameCount:             o.whole("frame_count"),
		Limit:                  o.whole("limit"),
		Round:                  o.whole("round"),
		ResetCount:             o.whole("reset_count"),
		MinRetainedAreaPercent: o.number("min_retained_area_percent"),
		MinWidth:               o.whole("min_width"),
		MinHeight:              o.whole("min_height"),
		RequiredAlignment:      o.whole("required_alignment"),
	}
	for _, entry := range o.list("seek_offsets") {
		offset, err := time.ParseDuration(strings.TrimSpace(entry))
		if err != nil {
			if bad == "" {
				bad = entry
			}
			continue
		}
		policy.SeekOffsets = append(policy.SeekOffsets, offset)
	}
	return policy, bad
}

func languagesField() Field {
	return Field{
		Key:         "languages",
		Label:       "Languages to keep",
		Type:        FieldStringList,
		Hint:        "Language tags such as eng or jpn, or orig for the original language. Leave empty to keep every track.",
		Default:     []string{},
		Mono:        true,
		Placeholder: "eng",
	}
}

func fallbackField() Field {
	return Field{
		Key:     "fallback",
		Label:   "When no track matches",
		Type:    FieldEnum,
		Hint:    "What to keep when no track is in one of the languages.",
		Default: config.DefaultStreamFallback,
		Choices: []Choice{
			{Value: string(domain.StreamFallbackKeepAll), Label: "Keep every track"},
			{Value: string(domain.StreamFallbackKeepFirst), Label: "Keep the first track"},
			{Value: string(domain.StreamFallbackFailJob), Label: "Fail the job"},
		},
	}
}

func unknownAsOriginalField() Field {
	return Field{
		Key:     "unknown_as_original",
		Label:   "Count untagged tracks as the original language",
		Type:    FieldBool,
		Hint:    "Applies when the library's Arr connection knows the original language.",
		Default: false,
	}
}

// keepSummary is the summary of the two keep nodes. The fallback only
// matters once languages are listed, so it is left out otherwise.
func keepSummary(o options) string {
	languages := cleanList(o.list("languages"))
	if len(languages) == 0 {
		return "every language"
	}
	return strings.Join(languages, ", ") + dot + o.text("fallback") + " fallback"
}

func audioKeepNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeAudioKeep,
			Kind:        KindAudio,
			Group:       groupAudio,
			Label:       "Keep audio tracks",
			Description: "Chooses which audio tracks stay in the file. Tracks in other languages are removed.",
			Outputs:     []string{""},
			Sections: []Section{
				{Title: "Languages", Fields: []Field{
					languagesField(),
					fallbackField(),
				}},
				{Title: "Filters", Fields: []Field{
					{
						Key:     "keep_commentary",
						Label:   "Keep commentary tracks",
						Type:    FieldBool,
						Default: false,
					},
					unknownAsOriginalField(),
				}},
			},
		},
		check: func(o options) []optionProblem {
			return blankEntries(o, "languages")
		},
		summary: keepSummary,
		apply: func(o options, profile *domain.Profile) {
			profile.Audio.LanguagesToKeep = cloneList(o.list("languages"))
			profile.Audio.KeepCommentary = o.on("keep_commentary")
			profile.Audio.Fallback = domain.StreamFallback(o.text("fallback"))
			profile.Audio.UnknownAsOriginal = o.on("unknown_as_original")
		},
	}
}

func audioCodecField() Field {
	return Field{
		Key:     "codec",
		Label:   "Codec",
		Type:    FieldEnum,
		Default: "eac3",
		Choices: []Choice{
			{Value: "opus", Label: "Opus"},
			{Value: "aac", Label: "AAC"},
			{Value: "ac3", Label: "Dolby Digital (AC-3)"},
			{Value: "eac3", Label: "Dolby Digital Plus (E-AC-3)"},
			{Value: "flac", Label: "FLAC"},
		},
		Half: true,
	}
}

func audioBitrateField() Field {
	return Field{
		Key:     "bitrate_kbps_per_channel",
		Label:   "Bitrate per channel",
		Type:    FieldInt,
		Hint:    "A 5.1 track gets six times this. 0 uses the encoder's default.",
		Default: 96,
		Min:     bound(0),
		Max:     bound(512),
		Suffix:  "kb/s",
		Mono:    true,
		Half:    true,
		// FLAC is lossless and takes no bitrate.
		VisibleWhen: when("codec", "opus", "aac", "ac3", "eac3"),
	}
}

// audioTarget reads the codec and bitrate fields shared by audio.encode and
// audio.downmix. The bitrate field is hidden for flac, where it is 0.
func audioTarget(o options) (codec string, bitrateKbpsPerChannel int) {
	codec = o.text("codec")
	if codec == "flac" {
		return codec, 0
	}
	return codec, o.whole("bitrate_kbps_per_channel")
}

func audioEncodeNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:    typeAudioEncode,
			Kind:  KindAudio,
			Group: groupAudio,
			Label: "Encode audio",
			Description: "Re-encodes kept audio tracks to one codec. " +
				"A track already in that codec is copied, and tracks no rule claims are copied too.",
			Outputs: []string{""},
			Sections: []Section{
				{Title: "Target", Fields: []Field{audioCodecField(), audioBitrateField()}},
				{Title: "Filter", Fields: []Field{{
					Key:         "source_codecs",
					Label:       "Only these source codecs",
					Type:        FieldStringList,
					Hint:        "Codec names as ffprobe prints them, such as truehd, dts, flac, or pcm_s24le. Leave empty to encode every kept track.",
					Default:     []string{"truehd", "dts"},
					Mono:        true,
					Placeholder: "truehd",
				}}},
			},
		},
		check: func(o options) []optionProblem {
			return blankEntries(o, "source_codecs")
		},
		summary: func(o options) string {
			codec, bitrate := audioTarget(o)
			summary := listOr(cleanList(o.list("source_codecs")), "every track") + " → " + codec
			switch {
			case codec == "flac":
				return summary
			case bitrate == 0:
				return summary + dot + "default bitrate"
			default:
				return fmt.Sprintf("%s%s%d kb/s per channel", summary, dot, bitrate)
			}
		},
		apply: func(o options, profile *domain.Profile) {
			codec, bitrate := audioTarget(o)
			profile.Audio.Encode = &domain.AudioEncode{
				Codec:                 codec,
				BitrateKbpsPerChannel: bitrate,
				SourceCodecs:          cleanList(o.list("source_codecs")),
			}
		},
	}
}

func audioDownmixNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:    typeAudioDownmix,
			Kind:  KindAudio,
			Group: groupAudio,
			Label: "Downmix audio",
			Description: "Reduces tracks that have more channels than a layout down to that layout. " +
				"Tracks at or below it are left alone.",
			Outputs: []string{""},
			Sections: []Section{
				{Title: "Layout", Fields: []Field{{
					Key:       "max_channels",
					Label:     "Largest layout to keep",
					Type:      FieldEnum,
					Hint:      "Tracks with more channels are downmixed to this layout.",
					Default:   "6",
					Choices:   []Choice{{Value: "6", Label: "5.1"}, {Value: "2", Label: "Stereo"}},
					Segmented: true,
				}}},
				{Title: "Encoding", Fields: []Field{audioCodecField(), audioBitrateField()}},
			},
		},
		summary: func(o options) string {
			layout := layoutName(channelCount(o.text("max_channels")))
			return "above " + layout + " → " + layout + dot + o.text("codec")
		},
		apply: func(o options, profile *domain.Profile) {
			codec, bitrate := audioTarget(o)
			profile.Audio.Downmix = &domain.AudioDownmix{
				MaxChannels:           channelCount(o.text("max_channels")),
				Codec:                 codec,
				BitrateKbpsPerChannel: bitrate,
			}
		},
	}
}

func subtitlesKeepNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeSubtitlesKeep,
			Kind:        KindSubtitles,
			Group:       groupSubtitles,
			Label:       "Keep subtitles",
			Description: "Chooses which subtitle tracks stay in the file. Subtitles are always copied, never converted.",
			Outputs:     []string{""},
			Sections: []Section{
				{Title: "Languages", Fields: []Field{
					languagesField(),
					fallbackField(),
				}},
				{Title: "Filters", Fields: []Field{
					{
						Key:     "keep_forced",
						Label:   "Keep forced tracks",
						Type:    FieldBool,
						Default: false,
					},
					{
						Key:     "keep_sdh",
						Label:   "Keep SDH tracks",
						Type:    FieldBool,
						Hint:    "Subtitles for the deaf and hard of hearing.",
						Default: false,
					},
					{
						Key:     "keep_commentary",
						Label:   "Keep commentary tracks",
						Type:    FieldBool,
						Default: false,
					},
					unknownAsOriginalField(),
				}},
			},
		},
		check: func(o options) []optionProblem {
			return blankEntries(o, "languages")
		},
		summary: keepSummary,
		apply: func(o options, profile *domain.Profile) {
			profile.Subtitles.LanguagesToKeep = cloneList(o.list("languages"))
			profile.Subtitles.KeepForced = o.on("keep_forced")
			profile.Subtitles.KeepSDH = o.on("keep_sdh")
			profile.Subtitles.KeepCommentary = o.on("keep_commentary")
			profile.Subtitles.Fallback = domain.StreamFallback(o.text("fallback"))
			profile.Subtitles.UnknownAsOriginal = o.on("unknown_as_original")
		},
	}
}

func subtitlesDropImageNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:    typeSubtitlesDropImage,
			Kind:  KindSubtitles,
			Group: groupSubtitles,
			Label: "Remove image subtitles",
			Description: "Removes image-based subtitle tracks (PGS, VobSub, DVB) before the language rules run. " +
				"Text subtitles stay.",
			Outputs:  []string{""},
			Sections: []Section{},
		},
		summary: fixedSummary("hdmv_pgs_subtitle, dvd_subtitle, dvb_subtitle"),
		apply: func(_ options, profile *domain.Profile) {
			profile.Subtitles.DropBitmap = true
		},
	}
}

func subtitlesRemoveAllNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeSubtitlesRemoveAll,
			Kind:        KindSubtitles,
			Group:       groupSubtitles,
			Label:       "Remove all subtitles",
			Description: "Removes every subtitle track, whatever a Keep subtitles node on the same path says.",
			Outputs:     []string{""},
			Sections:    []Section{},
		},
		summary: fixedSummary("every subtitle track"),
		apply: func(_ options, profile *domain.Profile) {
			profile.Subtitles.RemoveAll = true
		},
	}
}

func preserveOrStripField(key, label string) Field {
	return Field{
		Key:     key,
		Label:   label,
		Type:    FieldEnum,
		Default: config.DefaultMetadataMode,
		Choices: []Choice{
			{Value: string(domain.MetadataModePreserve), Label: "Preserve"},
			{Value: string(domain.MetadataModeStrip), Label: "Strip"},
		},
		Segmented: true,
		Half:      true,
	}
}

func containerMetadataNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeContainerMetadata,
			Kind:        KindContainer,
			Group:       groupContainer,
			Label:       "Clean metadata",
			Description: "Decides what happens to container metadata, track titles, attachments, and chapters.",
			Outputs:     []string{""},
			Sections: []Section{{Title: "Metadata", Fields: []Field{
				preserveOrStripField("metadata", "Container metadata"),
				{
					Key:     "track_titles",
					Label:   "Track titles",
					Type:    FieldEnum,
					Hint:    "Standardize rewrites each title from the track's codec, channels, and language.",
					Default: config.DefaultTrackTitleMode,
					Choices: []Choice{
						{Value: string(domain.TrackTitleModePreserve), Label: "Preserve"},
						{Value: string(domain.TrackTitleModeStrip), Label: "Strip"},
						{Value: string(domain.TrackTitleModeStandardize), Label: "Standardize"},
					},
					Segmented: true,
					Half:      true,
				},
				preserveOrStripField("attachments", "Attachments"),
				preserveOrStripField("chapters", "Chapters"),
			}}},
		},
		summary: func(o options) string {
			parts := []string{"metadata " + o.text("metadata"), "track_titles " + o.text("track_titles")}
			for _, key := range []string{"attachments", "chapters"} {
				if o.text(key) == string(domain.MetadataModeStrip) {
					parts = append(parts, key+" strip")
				}
			}
			return strings.Join(parts, dot)
		},
		apply: func(o options, profile *domain.Profile) {
			profile.Metadata = domain.MetadataPolicy{
				Mode:        domain.MetadataMode(o.text("metadata")),
				TrackTitles: domain.TrackTitleMode(o.text("track_titles")),
			}
			profile.Attachments.Mode = domain.MetadataMode(o.text("attachments"))
			profile.Chapters.Mode = domain.MetadataMode(o.text("chapters"))
		},
	}
}

// ruleProblems turns pkg/config rule violations into option problems. The
// option keys of video.encode and video.crop are the TOML keys of the
// sections they replace, so Field carries over unchanged.
func ruleProblems(found []config.Problem) []optionProblem {
	var problems []optionProblem
	for _, problem := range found {
		converted := optionProblem{Field: problem.Field, Message: sentence(problem.Rule)}
		if !slices.Contains(problems, converted) {
			problems = append(problems, converted)
		}
	}
	return problems
}

// sentence turns a rule such as "must be 8 or 10" into UI copy: "Must be 8
// or 10."
func sentence(rule string) string {
	first, size := utf8.DecodeRuneInString(rule)
	if size == 0 {
		return rule
	}
	rule = string(unicode.ToUpper(first)) + rule[size:]
	if strings.HasSuffix(rule, ".") {
		return rule
	}
	return rule + "."
}

// cloneList copies a list; an empty one becomes nil, as the TOML profile
// path leaves it.
func cloneList(entries []string) []string {
	return append([]string(nil), entries...)
}
