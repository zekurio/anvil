package flow

import (
	"slices"
	"sort"
	"strconv"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/video"
)

// Canvas geometry of generated graphs. A node is 240x64 px and every
// position sits on the 16 px grid.
const (
	// layoutColumn is the x of the main column, which the input, the shared
	// nodes, and the output share.
	layoutColumn = 288
	layoutTop    = 32
	// layoutRow is the y distance between one row of nodes and the next.
	layoutRow = 112
	// layoutBranch is how far a condition's true branch sits to its left and
	// its false branch to its right, which puts the two 448 px apart.
	layoutBranch = 224
)

// Default returns the flow a fresh installation starts with. It does what
// the built-in "default-av1" TOML profile does at its defaults: search for
// an AV1 encode at the default quality target, crop black bars, keep every
// audio and subtitle track, and apply the default metadata policy.
func Default() Graph {
	defaults, _ := config.Default().FindProfile(config.DefaultProfileName)
	return FromProfile(defaults.ToDomain())
}

// FromProfile converts a resolved TOML profile into a flow that resolves to
// the same profile, laid out top to bottom. Each per-codec video override
// becomes a check.video_codec condition whose true branch carries the base
// video settings with the override applied; the branches rejoin ahead of the
// shared audio, subtitle, and container nodes.
//
// Three things do not carry over. No node sets the validation tolerance, so
// the flow uses the default. A flow never crops copied video, so crop
// settings are dropped where the profile skips the encode. And an override
// is dropped when its key is not a codec check.video_codec offers.
func FromProfile(p domain.Profile) Graph {
	overrides := overrideBranches(p)
	encodes := []bool{!p.Video.SkipEncode}
	for _, override := range overrides {
		encodes = append(encodes, !override.settings.SkipEncode)
	}
	crop := !p.Crop.Disabled && slices.Contains(encodes, true)
	// Cropping the copied branches too would only earn a warning, so a
	// profile that copies some codecs crops on the branches that encode.
	cropPerBranch := crop && slices.Contains(encodes, false)

	l := layout{}
	l.place(Node{ID: "input", Type: typeInput, X: layoutColumn, Y: layoutTop})
	x, y := layoutColumn, layoutTop+layoutRow
	var rejoin []Edge
	for i, override := range overrides {
		check := "is-" + override.family
		l.place(Node{
			ID:      check,
			Type:    typeCheckVideoCodec,
			Label:   "Video is " + override.family + "?",
			X:       x,
			Y:       y,
			Options: map[string]any{"codecs": []string{override.family}},
		})
		l.open = []Edge{{From: check, Port: 0}}
		l.placeVideo("-"+override.family, override.settings, p.Crop, cropPerBranch, x-layoutBranch, y+layoutRow)
		rejoin = append(rejoin, l.open...)
		l.open = []Edge{{From: check, Port: 1}}
		// The next condition needs room for this one's true branch beside
		// its own; the base video node, last on the false side, does not.
		if i == len(overrides)-1 {
			x += layoutBranch
		} else {
			x += 2 * layoutBranch
		}
		y += layoutRow
	}
	l.placeVideo("", p.Video, p.Crop, cropPerBranch, x, y)
	l.open = append(rejoin, l.open...)

	y = l.bottom
	shared := func(id, nodeType string, values map[string]any) {
		y += layoutRow
		l.place(Node{ID: id, Type: nodeType, X: layoutColumn, Y: y, Options: values})
	}
	if crop && !cropPerBranch {
		shared("crop", typeVideoCrop, cropOptions(p.Crop))
	}
	shared("audio", typeAudioKeep, map[string]any{
		"languages":           optionList(p.Audio.LanguagesToKeep),
		"fallback":            valueOr(string(p.Audio.Fallback), config.DefaultStreamFallback),
		"keep_commentary":     p.Audio.KeepCommentary,
		"unknown_as_original": p.Audio.UnknownAsOriginal,
	})
	if encode := p.Audio.Encode; encode != nil {
		values := audioTargetOptions(encode.Codec, encode.BitrateKbpsPerChannel)
		values["source_codecs"] = optionList(encode.SourceCodecs)
		shared("audio-encode", typeAudioEncode, values)
	}
	if downmix := p.Audio.Downmix; downmix != nil {
		values := audioTargetOptions(downmix.Codec, downmix.BitrateKbpsPerChannel)
		values["max_channels"] = strconv.Itoa(downmix.MaxChannels)
		shared("audio-downmix", typeAudioDownmix, values)
	}
	shared("subtitles", typeSubtitlesKeep, map[string]any{
		"languages":           optionList(p.Subtitles.LanguagesToKeep),
		"fallback":            valueOr(string(p.Subtitles.Fallback), config.DefaultStreamFallback),
		"keep_forced":         p.Subtitles.KeepForced,
		"keep_sdh":            p.Subtitles.KeepSDH,
		"keep_commentary":     p.Subtitles.KeepCommentary,
		"unknown_as_original": p.Subtitles.UnknownAsOriginal,
	})
	if p.Subtitles.DropBitmap {
		shared("subtitles-images", typeSubtitlesDropImage, nil)
	}
	if p.Subtitles.RemoveAll {
		shared("subtitles-remove", typeSubtitlesRemoveAll, nil)
	}
	shared("metadata", typeContainerMetadata, map[string]any{
		"metadata": valueOr(string(p.Metadata.Mode), config.DefaultMetadataMode),
		// pkg/ffmpeg reads an unset track-title mode as strip.
		"track_titles": valueOr(string(p.Metadata.TrackTitles), config.DefaultTrackTitleMode),
		"attachments":  valueOr(string(p.Attachments.Mode), config.DefaultMetadataMode),
		"chapters":     valueOr(string(p.Chapters.Mode), config.DefaultMetadataMode),
	})
	shared("publish", typeOutputPublish, nil)
	return l.graph
}

// layout builds a generated graph. open lists the output ports waiting for
// the next node; placing a node connects all of them to it.
type layout struct {
	graph Graph
	open  []Edge
	// bottom is the y of the lowest node placed so far.
	bottom int
}

func (l *layout) place(node Node) {
	l.graph.Nodes = append(l.graph.Nodes, node)
	l.bottom = max(l.bottom, node.Y)
	for _, edge := range l.open {
		edge.To = node.ID
		l.graph.Edges = append(l.graph.Edges, edge)
	}
	l.open = nil
	if len(nodeDefsByID[node.Type].Outputs) > 0 {
		l.open = []Edge{{From: node.ID, Port: 0}}
	}
}

// placeVideo places the node for one set of video settings, and under it a
// crop node when this branch crops for itself.
func (l *layout) placeVideo(suffix string, settings domain.VideoProfile, crop domain.CropPolicy, cropHere bool, x, y int) {
	if settings.SkipEncode {
		l.place(Node{ID: "video" + suffix, Type: typeVideoCopy, X: x, Y: y})
		return
	}
	l.place(Node{ID: "video" + suffix, Type: typeVideoEncode, X: x, Y: y, Options: encodeOptions(settings)})
	if cropHere {
		l.place(Node{ID: "crop" + suffix, Type: typeVideoCrop, X: x, Y: y + layoutRow, Options: cropOptions(crop)})
	}
}

// overrideBranch is one per-codec video override as a branch of the flow.
type overrideBranch struct {
	// family is the codec as check.video_codec lists it.
	family string
	// settings are the base video settings with the override applied.
	settings domain.VideoProfile
}

// overrideBranches lists a profile's video overrides in key order. Keys are
// the canonical codec names pkg/config resolves them to.
func overrideBranches(p domain.Profile) []overrideBranch {
	keys := make([]string, 0, len(p.Video.Overrides))
	for key := range p.Video.Overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	offered := nodeDefsByID[typeCheckVideoCodec].Sections[0].Fields[0]
	var branches []overrideBranch
	for _, key := range keys {
		family := codecFamily(key)
		taken := slices.ContainsFunc(branches, func(branch overrideBranch) bool { return branch.family == family })
		if taken || !hasChoice(offered, family) {
			continue
		}
		branches = append(branches, overrideBranch{
			family:   family,
			settings: domain.EffectiveVideoProfile(p, domain.JobMetadata{}, key),
		})
	}
	return branches
}

// The option builders below write every option that is in effect, so a
// generated flow keeps its meaning if a catalog default ever changes.

func encodeOptions(settings domain.VideoProfile) map[string]any {
	values := map[string]any{
		"codec":       video.CanonicalCodec(settings.Codec),
		"accelerator": video.ResolveAccelerator(settings.Accelerator),
		"preset":      settings.Preset,
		"bit_depth":   strconv.Itoa(video.NormalizeBitDepth(settings.BitDepth)),
		"ffmpeg_args": optionList(settings.FFmpegArgs),
	}
	if settings.QualityMode == domain.VideoQualityFixed {
		values["quality_mode"] = string(domain.VideoQualityFixed)
		values["crf"] = settings.CRF
		return values
	}
	values["quality_mode"] = string(domain.VideoQualitySearch)
	values["metric"] = string(settings.Metric)
	values["target"] = settings.Target
	values["crf_min"] = settings.CRFMin
	values["crf_max"] = settings.CRFMax
	values["min_savings_percent"] = settings.MinSavingsPercent
	values["on_no_fit"] = "copy"
	if settings.ForceEncodeOnNoFit {
		values["on_no_fit"] = "encode"
	}
	values["samples"] = settings.Samples
	values["sample_duration"] = settings.SampleDuration.String()
	return values
}

func cropOptions(policy domain.CropPolicy) map[string]any {
	offsets := make([]string, 0, len(policy.SeekOffsets))
	for _, offset := range policy.SeekOffsets {
		offsets = append(offsets, offset.String())
	}
	return map[string]any{
		"min_retained_area_percent": policy.MinRetainedAreaPercent,
		"samples":                   policy.Samples,
		"frame_count":               policy.FrameCount,
		"seek_offsets":              offsets,
		"limit":                     policy.Limit,
		"round":                     policy.Round,
		"reset_count":               policy.ResetCount,
		"required_alignment":        policy.RequiredAlignment,
		"min_width":                 policy.MinWidth,
		"min_height":                policy.MinHeight,
	}
}

// audioTargetOptions writes the codec and bitrate options audio.encode and
// audio.downmix share. FLAC hides the bitrate, so it is left out.
func audioTargetOptions(codec string, bitrateKbpsPerChannel int) map[string]any {
	values := map[string]any{"codec": codec}
	if codec != "flac" {
		values["bitrate_kbps_per_channel"] = bitrateKbpsPerChannel
	}
	return values
}

// optionList copies a list for a node option. The result is never nil, so it
// encodes as [] rather than null.
func optionList(entries []string) []string {
	return append([]string{}, entries...)
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
