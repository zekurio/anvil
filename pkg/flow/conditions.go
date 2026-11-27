package flow

import (
	"fmt"
	"slices"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/language"
)

// conditionOutputs are the ports of every condition node: port 0 is taken
// when the test holds, port 1 when it does not.
func conditionOutputs() []string {
	return []string{"true", "false"}
}

func comparisonField(first, firstLabel, second, secondLabel string) Field {
	return Field{
		Key:       "op",
		Label:     "Compare",
		Type:      FieldEnum,
		Default:   first,
		Choices:   []Choice{{Value: first, Label: firstLabel}, {Value: second, Label: secondLabel}},
		Segmented: true,
		Half:      true,
	}
}

func checkVideoCodecNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeCheckVideoCodec,
			Kind:        KindCondition,
			Group:       groupInspect,
			Label:       "Check video codec",
			Description: "Branches on the codec of the main video stream.",
			Outputs:     conditionOutputs(),
			// The list is free-form rather than a fixed set of choices: a
			// condition has to be able to name any codec ffprobe reports, or
			// a file in a codec nobody listed here could not be told apart
			// from the ones a flow re-encodes.
			Sections: []Section{{Title: "Condition", Fields: []Field{{
				Key:         "codecs",
				Label:       "Video codecs",
				Type:        FieldStringList,
				Tokens:      true,
				Hint:        "ffprobe names, such as h264, hevc, av1 or vp9. True when the video uses any of them.",
				Default:     []string{"av1"},
				Mono:        true,
				Placeholder: "hevc",
			}}}},
		},
		check: func(o options) []optionProblem {
			if problems := requireEntries(o, "codecs", "Add at least one codec."); problems != nil {
				return problems
			}
			return blankEntries(o, "codecs")
		},
		summary: func(o options) string {
			return "video.codec in " + listOr(codecFamilies(o.list("codecs")), "nothing")
		},
		test: func(o options, facts Facts) (bool, string) {
			stream, ok := domain.PrimaryVideoStream(facts.Probe.Streams)
			if !ok {
				return false, "no video stream"
			}
			family := codecFamily(stream.Codec)
			if family == "" {
				return false, "video codec is unknown"
			}
			return slices.Contains(codecFamilies(o.list("codecs")), family), "video codec is " + family
		},
	}
}

func checkResolutionNode() nodeDef {
	tiers := make([]Choice, 0, len(resolutionTiers))
	for _, tier := range resolutionTiers {
		tiers = append(tiers, Choice{Value: tier.name, Label: tier.name})
	}
	return nodeDef{
		NodeType: NodeType{
			ID:          typeCheckResolution,
			Kind:        KindCondition,
			Group:       groupInspect,
			Label:       "Check resolution",
			Description: "Branches on the resolution class of the main video stream.",
			Outputs:     conditionOutputs(),
			Sections: []Section{{Title: "Condition", Fields: []Field{
				comparisonField("at_least", "At least", "at_most", "At most"),
				{
					Key:     "resolution",
					Label:   "Resolution",
					Type:    FieldEnum,
					Hint:    "A frame reaches a class by width or by height, so 1920x800 counts as 1080p.",
					Default: "1080p",
					Choices: tiers,
					Mono:    true,
					Half:    true,
				},
			}}},
		},
		summary: func(o options) string {
			return strings.ReplaceAll(o.text("op"), "_", " ") + " " + o.text("resolution")
		},
		test: func(o options, facts Facts) (bool, string) {
			stream, ok := domain.PrimaryVideoStream(facts.Probe.Streams)
			if !ok {
				return false, "no video stream"
			}
			if stream.Width <= 0 && stream.Height <= 0 {
				return false, "video size is unknown"
			}
			rank := resolutionRank(stream.Width, stream.Height)
			class := "below 480p"
			if rank > 0 {
				class = resolutionTiers[rank-1].name
			}
			wanted := 1 + slices.IndexFunc(resolutionTiers, func(tier resolutionTier) bool {
				return tier.name == o.text("resolution")
			})
			reason := fmt.Sprintf("video is %dx%d, %s", stream.Width, stream.Height, class)
			if o.text("op") == "at_most" {
				return rank <= wanted, reason
			}
			return rank >= wanted, reason
		},
	}
}

func checkHDRNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeCheckHDR,
			Kind:        KindCondition,
			Group:       groupInspect,
			Label:       "Check HDR",
			Description: "Branches on whether the main video stream is HDR, and which kind.",
			Outputs:     conditionOutputs(),
			Sections: []Section{{Title: "Condition", Fields: []Field{{
				Key:     "kinds",
				Label:   "HDR formats",
				Type:    FieldEnumList,
				Hint:    "True when the video is any of these. Dolby Vision on an HDR10 or HLG base layer counts as both.",
				Default: []string{"hdr10", "hlg", "dolby_vision"},
				Choices: []Choice{
					{Value: "hdr10", Label: "HDR10"},
					{Value: "hlg", Label: "HLG"},
					{Value: "dolby_vision", Label: "Dolby Vision"},
				},
			}}}},
		},
		check: func(o options) []optionProblem {
			return requireEntries(o, "kinds", "Choose at least one HDR format.")
		},
		summary: func(o options) string {
			return "video.hdr in " + listOr(o.list("kinds"), "nothing")
		},
		test: func(o options, facts Facts) (bool, string) {
			stream, ok := domain.PrimaryVideoStream(facts.Probe.Streams)
			if !ok {
				return false, "no video stream"
			}
			kinds := hdrKinds(stream)
			if len(kinds) == 0 {
				return false, "video is sdr"
			}
			wanted := o.list("kinds")
			matched := slices.ContainsFunc(kinds, func(kind string) bool { return slices.Contains(wanted, kind) })
			return matched, "video is " + strings.Join(kinds, ", ")
		},
	}
}

func checkBitrateNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeCheckBitrate,
			Kind:        KindCondition,
			Group:       groupInspect,
			Label:       "Check bitrate",
			Description: "Branches on the overall bitrate of the file.",
			Outputs:     conditionOutputs(),
			Sections: []Section{{Title: "Condition", Fields: []Field{
				comparisonField("above", "Above", "below", "Below"),
				{
					Key:     "kbps",
					Label:   "Bitrate",
					Type:    FieldInt,
					Hint:    "All streams together. False when the duration is unknown.",
					Default: 8000,
					Min:     bound(1),
					Suffix:  "kb/s",
					Mono:    true,
					Half:    true,
				},
			}}},
		},
		summary: func(o options) string {
			return fmt.Sprintf("%s %d kb/s", o.text("op"), o.whole("kbps"))
		},
		test: func(o options, facts Facts) (bool, string) {
			kbps, known := overallBitrateKbps(facts.Probe)
			switch {
			case facts.Probe.DurationSeconds <= 0:
				return false, "duration is unknown"
			case !known:
				return false, "file size is unknown"
			}
			reason := fmt.Sprintf("overall bitrate is %d kb/s", kbps)
			if o.text("op") == "below" {
				return kbps < int64(o.whole("kbps")), reason
			}
			return kbps > int64(o.whole("kbps")), reason
		},
	}
}

func checkFileSizeNode() nodeDef {
	const mebibyte = 1 << 20
	return nodeDef{
		NodeType: NodeType{
			ID:          typeCheckFileSize,
			Kind:        KindCondition,
			Group:       groupInspect,
			Label:       "Check file size",
			Description: "Branches on the size of the file.",
			Outputs:     conditionOutputs(),
			Sections: []Section{{Title: "Condition", Fields: []Field{
				comparisonField("above", "Above", "below", "Below"),
				{
					Key:     "megabytes",
					Label:   "Size",
					Type:    FieldInt,
					Default: 1024,
					Min:     bound(1),
					Suffix:  "MiB",
					Mono:    true,
					Half:    true,
				},
			}}},
		},
		summary: func(o options) string {
			return fmt.Sprintf("%s %d MiB", o.text("op"), o.whole("megabytes"))
		},
		test: func(o options, facts Facts) (bool, string) {
			size := facts.Probe.SizeBytes
			if size <= 0 {
				return false, "file size is unknown"
			}
			limit := int64(o.whole("megabytes")) * mebibyte
			reason := fmt.Sprintf("file is %.0f MiB", float64(size)/mebibyte)
			if o.text("op") == "below" {
				return size < limit, reason
			}
			return size > limit, reason
		},
	}
}

func checkAudioLanguageNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeCheckAudioLanguage,
			Kind:        KindCondition,
			Group:       groupInspect,
			Label:       "Check audio language",
			Description: "Branches on the languages of the audio tracks.",
			Outputs:     conditionOutputs(),
			Sections: []Section{{Title: "Condition", Fields: []Field{{
				Key:         "languages",
				Label:       "Languages",
				Type:        FieldStringList,
				Tokens:      true,
				Hint:        "Tags such as eng or jpn, or orig for the original language. True when any track matches.",
				Default:     []string{"eng"},
				Mono:        true,
				Placeholder: "eng",
			}}}},
		},
		check: func(o options) []optionProblem {
			if problems := requireEntries(o, "languages", "Add at least one language."); problems != nil {
				return problems
			}
			return blankEntries(o, "languages")
		},
		summary: func(o options) string {
			return "audio.language in " + listOr(cleanList(o.list("languages")), "nothing")
		},
		test: func(o options, facts Facts) (bool, string) {
			tracks := streamsOfType(facts.Probe, "audio")
			if len(tracks) == 0 {
				return false, "no audio tracks"
			}
			original := language.Normalize(facts.Metadata.OriginalLanguage)
			originalMissing := false
			var wanted []string
			for _, token := range cleanList(o.list("languages")) {
				switch {
				case token != originalLanguageToken:
					wanted = append(wanted, language.Normalize(token))
				case original == "":
					originalMissing = true
				default:
					wanted = append(wanted, original)
				}
			}
			matched := false
			var found []string
			for _, track := range tracks {
				tag := language.Normalize(track.Language)
				// An untagged track normalizes to "", which no wanted
				// language is, so it never matches.
				matched = matched || (tag != "" && slices.Contains(wanted, tag))
				if tag == "" {
					tag = "und"
				}
				if !slices.Contains(found, tag) {
					found = append(found, tag)
				}
			}
			reason := "audio languages are " + strings.Join(found, ", ")
			if len(found) == 1 {
				reason = "audio language is " + found[0]
			}
			if originalMissing {
				reason += "; original language is unknown"
			}
			return matched, reason
		},
	}
}

func checkAudioCodecNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeCheckAudioCodec,
			Kind:        KindCondition,
			Group:       groupInspect,
			Label:       "Check audio codec",
			Description: "Branches on the codecs of the audio tracks.",
			Outputs:     conditionOutputs(),
			Sections: []Section{{Title: "Condition", Fields: []Field{{
				Key:         "codecs",
				Label:       "Audio codecs",
				Type:        FieldStringList,
				Tokens:      true,
				Hint:        "ffprobe names, such as truehd, dts, eac3 or aac. True when any track matches.",
				Default:     []string{"truehd", "dts"},
				Mono:        true,
				Placeholder: "truehd",
			}}}},
		},
		check: func(o options) []optionProblem {
			if problems := requireEntries(o, "codecs", "Add at least one codec."); problems != nil {
				return problems
			}
			return blankEntries(o, "codecs")
		},
		summary: func(o options) string {
			return "audio.codec in " + listOr(cleanList(o.list("codecs")), "nothing")
		},
		test: func(o options, facts Facts) (bool, string) {
			tracks := streamsOfType(facts.Probe, "audio")
			if len(tracks) == 0 {
				return false, "no audio tracks"
			}
			wanted := cleanList(o.list("codecs"))
			matched := false
			var found []string
			for _, track := range tracks {
				codec := strings.ToLower(strings.TrimSpace(track.Codec))
				matched = matched || slices.Contains(wanted, codec)
				if codec == "" {
					codec = "unknown"
				}
				if !slices.Contains(found, codec) {
					found = append(found, codec)
				}
			}
			reason := "audio codecs are " + strings.Join(found, ", ")
			if len(found) == 1 {
				reason = "audio codec is " + found[0]
			}
			return matched, reason
		},
	}
}

func checkAudioChannelsNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeCheckAudioChannels,
			Kind:        KindCondition,
			Group:       groupInspect,
			Label:       "Check audio channels",
			Description: "Branches on how many channels the audio tracks have.",
			Outputs:     conditionOutputs(),
			Sections: []Section{{Title: "Condition", Fields: []Field{{
				Key:       "more_than",
				Label:     "More channels than",
				Type:      FieldEnum,
				Default:   "2",
				Choices:   []Choice{{Value: "2", Label: "Stereo"}, {Value: "6", Label: "5.1"}},
				Segmented: true,
			}}}},
		},
		summary: func(o options) string {
			return "audio.channels above " + o.text("more_than")
		},
		test: func(o options, facts Facts) (bool, string) {
			tracks := streamsOfType(facts.Probe, "audio")
			if len(tracks) == 0 {
				return false, "no audio tracks"
			}
			most := 0
			for _, track := range tracks {
				most = max(most, track.Channels)
			}
			return most > channelCount(o.text("more_than")), fmt.Sprintf("largest audio track has %d channels", most)
		},
	}
}

// channelCount reads a channel-layout choice. The only layouts offered are
// stereo ("2") and 5.1 ("6").
func channelCount(value string) int {
	if value == "2" {
		return 2
	}
	return 6
}

func layoutName(channels int) string {
	if channels == 2 {
		return "stereo"
	}
	return "5.1"
}
