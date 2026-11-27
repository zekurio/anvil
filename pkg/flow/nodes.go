package flow

import (
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
)

// Node type IDs. They are stored in graphs, so they never change.
const (
	typeInput              = "input"
	typeCheckVideoCodec    = "check.video_codec"
	typeCheckResolution    = "check.resolution"
	typeCheckHDR           = "check.hdr"
	typeCheckBitrate       = "check.bitrate"
	typeCheckFileSize      = "check.file_size"
	typeCheckAudioLanguage = "check.audio_language"
	typeCheckAudioCodec    = "check.audio_codec"
	typeCheckAudioChannels = "check.audio_channels"
	typeVideoEncode        = "video.encode"
	typeVideoCopy          = "video.copy"
	typeVideoCrop          = "video.crop"
	typeAudioKeep          = "audio.keep"
	typeAudioEncode        = "audio.encode"
	typeAudioDownmix       = "audio.downmix"
	typeSubtitlesKeep      = "subtitles.keep"
	typeSubtitlesDropImage = "subtitles.drop_bitmap"
	typeSubtitlesRemoveAll = "subtitles.remove_all"
	typeContainerMetadata  = "container.metadata"
	typeOutputPublish      = "output.publish"
	typeOutputSkip         = "output.skip"
)

// Palette headings, in palette order.
const (
	groupInput     = "Input"
	groupInspect   = "Inspect"
	groupVideo     = "Video"
	groupAudio     = "Audio"
	groupSubtitles = "Subtitles"
	groupContainer = "Container"
	groupOutput    = "Output"
)

// originalLanguageToken stands for the file's original language in a
// language list, as it does in pkg/audio and pkg/subtitle.
const originalLanguageToken = "orig"

// dot joins the parts of a node summary.
const dot = " · "

// nodeDef is a catalog entry plus the behavior behind it. Exactly one of
// test, apply, and outcome is set, except on the input node, which has none:
// a condition tests, an action applies, and an output node ends the walk.
type nodeDef struct {
	NodeType
	// summary renders the one-line canvas summary. It is never empty.
	summary func(o options) string
	// check reports option problems the field definitions cannot express.
	check func(o options) []optionProblem
	// warn reports options that are valid but will not do what they say.
	warn func(o options) []optionProblem
	// test evaluates a condition; reason completes "... → true (reason)".
	test func(o options, facts Facts) (ok bool, reason string)
	// apply writes the one part of the profile this node type owns.
	apply func(o options, profile *domain.Profile)
	// outcome is what reaching an output node decides.
	outcome Outcome
}

// nodeDefs builds the catalog in palette order. Every call returns fresh
// values, so callers may keep or change what they get.
func nodeDefs() []nodeDef {
	return []nodeDef{
		inputNode(),
		checkVideoCodecNode(),
		checkResolutionNode(),
		checkHDRNode(),
		checkBitrateNode(),
		checkFileSizeNode(),
		checkAudioLanguageNode(),
		checkAudioCodecNode(),
		checkAudioChannelsNode(),
		videoEncodeNode(),
		videoCopyNode(),
		videoCropNode(),
		audioKeepNode(),
		audioEncodeNode(),
		audioDownmixNode(),
		subtitlesKeepNode(),
		subtitlesDropImageNode(),
		subtitlesRemoveAllNode(),
		containerMetadataNode(),
		outputPublishNode(),
		outputSkipNode(),
	}
}

var nodeDefsByID = func() map[string]nodeDef {
	byID := make(map[string]nodeDef)
	for _, def := range nodeDefs() {
		byID[def.ID] = def
	}
	return byID
}()

// Catalog returns every node type in palette order. Each call returns a
// fresh copy.
func Catalog() []NodeType {
	defs := nodeDefs()
	catalog := make([]NodeType, 0, len(defs))
	for _, def := range defs {
		catalog = append(catalog, def.NodeType)
	}
	return catalog
}

func inputNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeInput,
			Kind:        KindInput,
			Group:       groupInput,
			Label:       "Input file",
			Description: "Where every queued file of the library enters the flow. A flow has exactly one input.",
			Outputs:     []string{""},
			Sections:    []Section{},
		},
		summary: fixedSummary("every queued file"),
	}
}

func outputPublishNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:    typeOutputPublish,
			Kind:  KindOutput,
			Group: groupOutput,
			Label: "Publish result",
			Description: "Ends the flow and publishes the processed file. The library decides how: " +
				"a media library replaces the source in place, a download library hands the file off to its importer.",
			Outputs:  []string{},
			Sections: []Section{},
		},
		summary: fixedSummary("replace or hand off, as the library says"),
		outcome: OutcomeProcess,
	}
}

func outputSkipNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeOutputSkip,
			Kind:        KindOutput,
			Group:       groupOutput,
			Label:       "Skip file",
			Description: "Ends the flow without touching the file. The job is marked skipped.",
			Outputs:     []string{},
			Sections:    []Section{},
		},
		summary: fixedSummary("leave the file untouched"),
		outcome: OutcomeSkip,
	}
}

func fixedSummary(text string) func(options) string {
	return func(options) string { return text }
}

// bound makes a Field.Min or Field.Max.
func bound(value float64) *float64 {
	return &value
}

// when makes a Field.VisibleWhen.
func when(key string, values ...any) *Visibility {
	return &Visibility{Key: key, In: values}
}

// requireEntries flags an empty list the node cannot work without.
func requireEntries(o options, key, message string) []optionProblem {
	if len(o.list(key)) > 0 {
		return nil
	}
	return []optionProblem{{Field: key, Message: message}}
}

// blankEntries flags list entries that are empty or only spaces.
func blankEntries(o options, key string) []optionProblem {
	for _, entry := range o.list(key) {
		if strings.TrimSpace(entry) == "" {
			return []optionProblem{{Field: key, Message: "Entries must not be empty. Remove the blank one."}}
		}
	}
	return nil
}

// cleanList trims and lowercases the entries of a list of ids, such as
// language tags or codec names, and drops blanks. An empty list is nil.
func cleanList(entries []string) []string {
	var cleaned []string
	for _, entry := range entries {
		if entry = strings.ToLower(strings.TrimSpace(entry)); entry != "" {
			cleaned = append(cleaned, entry)
		}
	}
	return cleaned
}

// listOr joins a list for display, or returns fallback for an empty one.
func listOr(entries []string, fallback string) string {
	if len(entries) == 0 {
		return fallback
	}
	return strings.Join(entries, ", ")
}
