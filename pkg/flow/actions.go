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

// Defaults come from pkg/config and the encoder registry in pkg/video.

func videoEncodeNode() nodeDef {
	search := func() *Visibility {
		return when("quality_mode", string(domain.VideoQualitySearch))
	}
	def := nodeDef{
		NodeType: NodeType{
			ID:          typeVideoEncode,
			Kind:        KindVideo,
			Group:       groupVideo,
			Label:       "Encode video",
			Description: "With a quality target, anvil picks the highest encoder setting that meets it.",
			Outputs:     []string{""},
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
						Key:       "preset",
						Label:     "Preset",
						Type:      FieldEnum,
						normalize: normalizePreset,
						Mono:      true,
						Half:      true,
					},
					{
						Key:   "bit_depth",
						Label: "Bit depth",
						Type:  FieldEnum,

						Segmented: true,
						Half:      true,
					},
				}},
				{Title: "Quality", Fields: []Field{
					{
						Key:     "quality_mode",
						Label:   "Mode",
						Type:    FieldEnum,
						Default: string(domain.VideoQualitySearch),
						Choices: []Choice{
							{Value: string(domain.VideoQualitySearch), Label: "Quality target"},
							{Value: string(domain.VideoQualityFixed), Label: "Fixed quality"},
						},
						Segmented: true,
					},
					{
						Key:   "crf",
						Label: "CRF",
						Type:  FieldInt,
						Hint:  "Lower is better quality and a larger file.",

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
						Hint:        "Lowest score to accept. VMAF is usually 93 to 96, XPSNR 35 to 50.",
						Default:     float64(config.DefaultTargetVMAF),
						Min:         bound(0),
						Max:         bound(100),
						Mono:        true,
						Half:        true,
						VisibleWhen: search(),
					},
					{
						Key:   "crf_min",
						Label: "Lowest CRF",
						Type:  FieldInt,

						Mono:        true,
						Half:        true,
						VisibleWhen: search(),
					},
					{
						Key:   "crf_max",
						Label: "Highest CRF",
						Type:  FieldInt,

						Mono:        true,
						Half:        true,
						VisibleWhen: search(),
					},
					{
						Key:         "min_savings_percent",
						Label:       "Smallest saving worth encoding",
						Type:        FieldFloat,
						Hint:        "Video stream only, estimated from the samples.",
						Default:     float64(config.DefaultMinSavingsPct),
						Min:         bound(0),
						Max:         bound(100),
						Suffix:      "%",
						Mono:        true,
						VisibleWhen: search(),
					},
					{
						Key:     "on_no_fit",
						Label:   "When no setting fits",
						Type:    FieldEnum,
						Hint:    "A setting must meet both the quality target and the minimum saving.",
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
						Label:       "Samples per quality value",
						Type:        FieldInt,
						Hint:        "0 uses one sample per 12 minutes of runtime.",
						Default:     0,
						Min:         bound(0),
						Max:         bound(config.MaxVideoSamples),
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

						Default: []string{},
						Mono:    true,
					},
				}},
			},
		},
		check: func(o options) []optionProblem {
			settings := encodeSettings(o)
			return ruleProblems(config.VideoConfig{
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
		},
		warn: func(o options) []optionProblem {
			settings := encodeSettings(o)
			if settings.QualityMode == domain.VideoQualityFixed || settings.Metric != domain.QualityMetricXPSNR || settings.Target <= xpsnrPlausibleMax {
				return nil
			}
			return []optionProblem{{
				Field: "target",
				Message: fmt.Sprintf("No encode reaches an XPSNR of %s dB. Typical targets are 35 to 50.",
					formatNumber(settings.Target)),
			}}
		},
		summary: func(o options) string {
			settings := encodeSettings(o)
			parts := []string{video.ResolveEncoder(settings.Codec, settings.Accelerator)}
			if settings.QualityMode == domain.VideoQualityFixed {
				encoder, _ := video.LookupEncoder(settings.Codec, settings.Accelerator)
				parts = append(parts, encoder.Quality.Label+" "+strconv.Itoa(settings.CRF))
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
	defaultEncoder, _ := video.LookupEncoder("av1", video.AcceleratorSoftware)
	encoders := video.Encoders()
	for _, section := range def.Sections {
		for i, field := range section.Fields {
			fallback, ok := encoderFieldVariant(field.Key, defaultEncoder)
			if !ok {
				continue
			}
			field = applyVariant(field, fallback)
			for _, encoder := range encoders {
				variant, _ := encoderFieldVariant(field.Key, encoder)
				variant.When = []Visibility{*when("codec", encoder.Codec), *when("accelerator", encoder.Backend)}
				field.Variants = append(field.Variants, variant)
			}
			section.Fields[i] = field
		}
	}
	return def
}

// xpsnrPlausibleMax is the highest XPSNR target, in dB, that is not flagged.
// Visually lossless encodes score around 50; the field still accepts up to
// 100 because the VMAF scale shares it.
const xpsnrPlausibleMax = 60

// normalizePreset preserves the empty preset used by legacy graphs to
// request the encoder default.
func normalizePreset(value any) (any, bool) {
	preset, ok := value.(string)
	if !ok {
		return value, true
	}
	preset = strings.TrimSpace(preset)
	if number, err := strconv.Atoi(preset); err == nil {
		preset = strconv.Itoa(number)
	}
	return preset, preset != ""
}

func encoderFieldVariant(key string, encoder video.Encoder) (FieldVariant, bool) {
	quality := encoder.Quality
	switch key {
	case "preset":
		speed := encoder.Speed
		choices := make([]Choice, 0, len(speed.Choices))
		for _, value := range speed.Choices {
			choices = append(choices, Choice{Value: value, Label: value})
		}
		if speed.Option != "" && len(choices) == 0 {
			for n := speed.Min; n <= speed.Max; n++ {
				value := strconv.Itoa(n)
				choices = append(choices, Choice{Value: value, Label: value})
			}
		}
		return FieldVariant{Choices: choices, Default: speed.Default, Hidden: speed.Option == ""}, true
	case "bit_depth":
		choices := make([]Choice, 0, len(encoder.BitDepths))
		for _, depth := range encoder.BitDepths {
			choices = append(choices, Choice{Value: strconv.Itoa(depth), Label: fmt.Sprintf("%d-bit", depth)})
		}
		depth := video.DefaultBitDepth
		if !slices.Contains(encoder.BitDepths, depth) {
			depth = encoder.BitDepths[0]
		}
		return FieldVariant{Choices: choices, Default: strconv.Itoa(depth), Hidden: len(choices) == 1}, true
	case "crf":
		return FieldVariant{Label: quality.Label, Min: bound(float64(quality.Min)), Max: bound(float64(quality.Max)), Default: quality.Default, rangeContext: encoder.Name}, true
	case "crf_min":
		return FieldVariant{Label: "Lowest " + quality.Label, Min: bound(float64(quality.Min)), Max: bound(float64(quality.Max)), Default: quality.SearchMin, rangeContext: encoder.Name}, true
	case "crf_max":
		return FieldVariant{Label: "Highest " + quality.Label, Min: bound(float64(quality.Min)), Max: bound(float64(quality.Max)), Default: quality.SearchMax, rangeContext: encoder.Name}, true
	case "ffmpeg_args":
		accepts := make([]string, 0, len(encoder.TuningOptions)+1)
		if encoder.ParamsOption != "" {
			accepts = append(accepts, "-"+encoder.ParamsOption)
		}
		for _, option := range encoder.TuningOptions {
			accepts = append(accepts, "-"+option)
		}
		if len(accepts) == 0 {
			return FieldVariant{Hidden: true}, true
		}
		return FieldVariant{Placeholder: accepts[0], Hint: "Enter each option and value separately. Accepted options: " + strings.Join(accepts, ", ") + "."}, true
	default:
		return FieldVariant{}, false
	}
}

// encodeSettings reads a video.encode node. Search mode leaves QualityMode
// at its zero value, which means search, so the result equals what the TOML
// profile path produces for the same settings.
func encodeSettings(o options) domain.VideoProfile {
	depth, err := strconv.Atoi(o.text("bit_depth"))
	if err != nil {
		depth = video.DefaultBitDepth
	}
	settings := domain.VideoProfile{
		Codec:              o.text("codec"),
		Accelerator:        o.text("accelerator"),
		Preset:             strings.TrimSpace(o.text("preset")),
		BitDepth:           depth,
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
			Description: "Keeps the source video as it is.",
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
			ID:          typeVideoCrop,
			Kind:        KindVideo,
			Group:       groupVideo,
			Label:       "Crop black bars",
			Description: "Crops black bars during the encode. Copied video is never cropped.",
			Outputs:     []string{""},
			Sections: []Section{
				{Title: "Safety", Fields: []Field{{
					Key:     "min_retained_area_percent",
					Label:   "Smallest picture to keep",
					Type:    FieldFloat,
					Hint:    "A crop that keeps less is ignored, and the file is encoded uncropped.",
					Default: float64(config.DefaultCropMinRetainedAreaPct),
					// Keeping none of the picture is not a crop: 0 is out.
					Min:          bound(0),
					MinExclusive: true,
					Max:          bound(100),
					Suffix:       "%",
					Mono:         true,
				}}},
				{Title: "Advanced", Collapsible: true, Collapsed: true, Fields: []Field{
					{
						Key:     "samples",
						Label:   "Sample windows",
						Type:    FieldInt,
						Hint:    "0 uses one window per 3 minutes of runtime, between 4 and 24.",
						Default: 0,
						Min:     bound(0),
						Max:     bound(config.MaxCropWindows),
						Mono:    true,
						Half:    true,
					},
					{
						Key:     "frame_count",
						Label:   "Frames per window",
						Type:    FieldInt,
						Default: config.DefaultCropFrameCount,
						Min:     bound(1),
						Max:     bound(config.MaxCropFrameCount),
						Mono:    true,
						Half:    true,
					},
					{
						Key:         "seek_offsets",
						Label:       "Sample at these offsets",
						Type:        FieldStringList,
						Tokens:      true,
						Hint:        "Such as 5m or 1h10m. Empty spreads the windows across the file.",
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
		Tokens:      true,
		Hint:        "Tags such as eng or jpn, or orig for the original language. Empty keeps every track.",
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
// matters once languages are listed, so it is left out otherwise. It is said
// in words, like the choices of the form: the canvas never shows an option
// value such as keep_all.
func keepSummary(o options) string {
	languages := cleanList(o.list("languages"))
	if len(languages) == 0 {
		return "every language"
	}
	fallback := "else every track"
	switch domain.StreamFallback(o.text("fallback")) {
	case domain.StreamFallbackKeepFirst:
		fallback = "else the first track"
	case domain.StreamFallbackFailJob:
		fallback = "else fail the job"
	}
	return strings.Join(languages, ", ") + dot + fallback
}

func audioKeepNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeAudioKeep,
			Kind:        KindAudio,
			Group:       groupAudio,
			Label:       "Keep audio tracks",
			Description: "Keeps audio tracks by language and removes the rest.",
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
			ID:          typeAudioEncode,
			Kind:        KindAudio,
			Group:       groupAudio,
			Label:       "Encode audio",
			Description: "Re-encodes kept audio tracks to one codec. Tracks already in it are copied.",
			Outputs:     []string{""},
			Sections: []Section{
				{Title: "Target", Fields: []Field{audioCodecField(), audioBitrateField()}},
				{Title: "Filter", Fields: []Field{{
					Key:         "source_codecs",
					Label:       "Only these source codecs",
					Type:        FieldStringList,
					Tokens:      true,
					Hint:        "ffprobe names, such as truehd, dts or flac. Empty encodes every kept track.",
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
			ID:          typeAudioDownmix,
			Kind:        KindAudio,
			Group:       groupAudio,
			Label:       "Downmix audio",
			Description: "Downmixes tracks that have more channels than the chosen layout.",
			Outputs:     []string{""},
			Sections: []Section{
				{Title: "Layout", Fields: []Field{{
					Key:       "max_channels",
					Label:     "Largest layout to keep",
					Type:      FieldEnum,
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
			Description: "Keeps subtitle tracks by language. Subtitles are copied, never converted.",
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
			ID:          typeSubtitlesDropImage,
			Kind:        KindSubtitles,
			Group:       groupSubtitles,
			Label:       "Remove image subtitles",
			Description: "Removes PGS, VobSub and DVB subtitles. Text subtitles stay.",
			Outputs:     []string{""},
			Sections:    []Section{},
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
			Description: "Removes every subtitle track, overriding Keep subtitles.",
			Outputs:     []string{""},
			Sections:    []Section{},
		},
		summary: fixedSummary("every subtitle track"),
		apply: func(_ options, profile *domain.Profile) {
			profile.Subtitles.RemoveAll = true
		},
	}
}

// preserveOrStripField is a two-way choice short enough to share a row.
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

// fullRow gives a field a row of its own.
func fullRow(field Field) Field {
	field.Half = false
	return field
}

func containerMetadataNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeContainerMetadata,
			Kind:        KindContainer,
			Group:       groupContainer,
			Label:       "Clean metadata",
			Description: "Keeps or strips metadata, track titles, attachments and chapters.",
			Outputs:     []string{""},
			Sections: []Section{{Title: "Metadata", Fields: []Field{
				// Three choices need the full row of the sheet, so the two
				// fields above the pair each get one.
				fullRow(preserveOrStripField("metadata", "Container metadata")),
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
				},
				preserveOrStripField("attachments", "Attachments"),
				preserveOrStripField("chapters", "Chapters"),
			}}},
		},
		summary: func(o options) string {
			// In the words of the form, not its option keys: the summary is
			// what the canvas shows of the node.
			titles := map[string]string{
				string(domain.TrackTitleModePreserve):    "keep titles",
				string(domain.TrackTitleModeStrip):       "strip titles",
				string(domain.TrackTitleModeStandardize): "standardize titles",
			}
			parts := []string{"keep metadata", titles[o.text("track_titles")]}
			if o.text("metadata") == string(domain.MetadataModeStrip) {
				parts[0] = "strip metadata"
			}
			for _, key := range []string{"attachments", "chapters"} {
				if o.text(key) == string(domain.MetadataModeStrip) {
					parts = append(parts, "no "+key)
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

// VideoEncodeSettings resolves the settings of a single valid encode node.
func (n Node) VideoEncodeSettings() (domain.VideoProfile, bool) {
	if n.Type != typeVideoEncode {
		return domain.VideoProfile{}, false
	}
	decoded, problems := decodeOptions(nodeDefsByID[typeVideoEncode], n.Options)
	if len(problems) != 0 {
		return domain.VideoProfile{}, false
	}
	return encodeSettings(decoded), true
}
