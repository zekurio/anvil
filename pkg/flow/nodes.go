package flow

import (
	"fmt"
	"path"
	"slices"
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
	typeOutputReplace      = "output.replace"
	typeOutputMove         = "output.move"
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
// test, apply, and outcome is set, except on the input node, which has none,
// and on a publishing output node, which also applies: a condition tests, an
// action applies, and an output node ends the walk. The only part of the
// profile an output node writes is Publish, so it never undoes an action.
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
		outputReplaceNode(),
		outputMoveNode(),
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
			Description: "Where each queued file enters. A flow has one input.",
			Outputs:     []string{""},
			Sections:    []Section{},
		},
		summary: fixedSummary("every queued file"),
	}
}

func outputReplaceNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeOutputReplace,
			Kind:        KindOutput,
			Group:       groupOutput,
			Label:       "Replace file",
			Description: "Swaps the source for the result in place.",
			Outputs:     []string{},
			Sections:    []Section{},
		},
		summary: fixedSummary("replace the source in place"),
		apply: func(_ options, profile *domain.Profile) {
			profile.Publish = domain.PublishPolicy{Mode: domain.PublishReplace}
		},
		outcome: OutcomeProcess,
	}
}

func outputMoveNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeOutputMove,
			Kind:        KindOutput,
			Group:       groupOutput,
			Label:       "Move file",
			Description: "Publishes the result to another folder, such as the one an importer watches, and deletes the source.",
			Outputs:     []string{},
			Sections: []Section{{Title: "Destination", Fields: []Field{
				{
					Key:         "destination",
					Label:       "Folder",
					Type:        FieldPath,
					Default:     "",
					Mono:        true,
					Placeholder: "/srv/imports/tv",
				},
				{
					Key:     "preserve_structure",
					Label:   "Keep folder structure",
					Type:    FieldBool,
					Hint:    "Recreates the folders below the library root. Off puts every file directly in the folder.",
					Default: true,
				},
				{
					Key:     "keep_source",
					Label:   "Keep source",
					Type:    FieldBool,
					Hint:    "Leaves the source in place, for a downloader that still seeds it.",
					Default: false,
				},
				{
					Key:         "prune_empty_dirs",
					Label:       "Remove emptied folders",
					Type:        FieldBool,
					Hint:        "Deletes the folders the source leaves behind, with residue that matches the library's ignorable globs.",
					Default:     false,
					VisibleWhen: when("keep_source", false),
				},
			}}},
		},
		summary: func(o options) string {
			destination := o.text("destination")
			if destination == "" {
				destination = "no folder set"
			}
			parts := []string{destination}
			if o.on("keep_source") {
				parts = append(parts, "keep source")
			} else {
				parts = append(parts, "delete source")
			}
			return strings.Join(parts, dot)
		},
		check: func(o options) []optionProblem {
			destination := o.text("destination")
			switch {
			case destination == "":
				return []optionProblem{{Field: "destination", Message: "Enter the folder to move the result to, as an absolute path such as /srv/imports/tv."}}
			case !path.IsAbs(destination):
				return []optionProblem{{Field: "destination", Message: fmt.Sprintf("%q is not an absolute path. Enter one that starts with /, such as /srv/imports/tv.", destination)}}
			case destination == "/":
				return []optionProblem{{Field: "destination", Message: "The folder cannot be the filesystem root. Enter a folder such as /srv/imports/tv."}}
			}
			return nil
		},
		apply: func(o options, profile *domain.Profile) {
			profile.Publish = domain.PublishPolicy{
				Mode:              domain.PublishMove,
				Destination:       o.text("destination"),
				KeepSource:        o.on("keep_source"),
				PreserveStructure: o.on("preserve_structure"),
				PruneEmptyDirs:    !o.on("keep_source") && o.on("prune_empty_dirs"),
			}
		},
		outcome: OutcomeProcess,
	}
}

// MoveTarget is the folder one move output of a graph publishes below.
type MoveTarget struct {
	NodeID string
	// Folder is cleaned; it is never empty.
	Folder string
}

// MoveTargets lists the move outputs of a graph that name a folder, in node
// order. Callers check the folders against library folders and the daemon's
// confinement; the graph itself cannot.
func MoveTargets(g Graph) []MoveTarget {
	var targets []MoveTarget
	for _, node := range g.Nodes {
		if node.Type != typeOutputMove {
			continue
		}
		decoded, _ := decodeOptions(nodeDefsByID[typeOutputMove], node.Options)
		if folder := decoded.text("destination"); folder != "" {
			targets = append(targets, MoveTarget{NodeID: node.ID, Folder: folder})
		}
	}
	return targets
}

// PublishPolicies lists, without repeats and in node order, every way a
// graph can publish a result: one policy per output node that publishes.
// Which one a file gets depends on its facts; preflight, which does not probe
// files, uses this to say what could happen.
func PublishPolicies(g Graph) []domain.PublishPolicy {
	var policies []domain.PublishPolicy
	for _, node := range g.Nodes {
		def, known := nodeDefsByID[node.Type]
		if !known || def.outcome != OutcomeProcess || def.apply == nil {
			continue
		}
		decoded, _ := decodeOptions(def, node.Options)
		var profile domain.Profile
		def.apply(decoded, &profile)
		if !slices.Contains(policies, profile.Publish) {
			policies = append(policies, profile.Publish)
		}
	}
	return policies
}

// MoveDestinations lists the folders of MoveTargets without repeats.
func MoveDestinations(g Graph) []string {
	var destinations []string
	for _, target := range MoveTargets(g) {
		if !slices.Contains(destinations, target.Folder) {
			destinations = append(destinations, target.Folder)
		}
	}
	return destinations
}

func outputSkipNode() nodeDef {
	return nodeDef{
		NodeType: NodeType{
			ID:          typeOutputSkip,
			Kind:        KindOutput,
			Group:       groupOutput,
			Label:       "Skip file",
			Description: "Leaves the file untouched and marks the job skipped.",
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
