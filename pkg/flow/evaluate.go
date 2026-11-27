package flow

import (
	"fmt"
	"strings"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/video"
)

// profileName names every profile a flow resolves. The caller may replace it
// with something that identifies the flow.
const profileName = "flow"

// Resolution is what a flow decided for one file.
type Resolution struct {
	Outcome Outcome
	// Path lists the IDs of the nodes the file passed through, in order,
	// starting at the input node.
	Path []string
	// Decisions holds one line per condition on the path, such as
	// "Video is hevc? → true (video codec is hevc)".
	Decisions []string
	// Profile is the effective profile. It is meaningful when Outcome is
	// OutcomeProcess.
	Profile domain.Profile
	// Plan states the result in words, one line per aspect: Video, Audio,
	// Subtitles, Container. A skipped file has a single line saying so.
	Plan []string
}

// Evaluate walks the graph for one file. Starting at the input node it
// follows the edges: a condition continues on its true or false port, an
// action sets the part of the profile its node type owns, and an output node
// ends the walk. When several nodes on the path set the same part, the last
// one wins; video.encode and video.copy share the video part. Whatever no
// node sets stays as it is in a plain remux: video, audio, and subtitles
// copied, every track kept, nothing cropped or stripped.
//
// It returns an error when the graph has error issues; see Validate.
func Evaluate(g Graph, facts Facts) (Resolution, error) {
	a := analyze(g)
	if err := a.err(); err != nil {
		return Resolution{}, err
	}

	resolution := Resolution{Path: []string{}, Decisions: []string{}, Plan: []string{}}
	profile := baseProfile()
	// A valid graph has no cycles and no dangling ports on any path from the
	// input, so the walk always arrives at an output node.
	for id := a.input; ; {
		def := nodeDefsByID[a.nodes[id].Type]
		resolution.Path = append(resolution.Path, id)
		port := 0
		switch {
		case def.test != nil:
			ok, reason := def.test(a.options[id], facts)
			if !ok {
				port = 1
			}
			resolution.Decisions = append(resolution.Decisions, fmt.Sprintf("%s → %t (%s)", a.label(id), ok, reason))
		case def.apply != nil:
			def.apply(a.options[id], &profile)
		case def.outcome != "":
			resolution.Outcome = def.outcome
		}
		if resolution.Outcome != "" {
			break
		}
		id = a.targets[id][port]
	}

	// Cropping happens in the encoder, so a copied video cannot be cropped.
	// The profile says so itself, rather than leaving a crop request the
	// pipeline would have to see through.
	cropWanted := !profile.Crop.Disabled
	if profile.Video.SkipEncode {
		profile.Crop.Disabled = true
	}
	resolution.Profile = profile
	if resolution.Outcome == OutcomeSkip {
		resolution.Plan = []string{"File: skip, leave it untouched"}
		return resolution, nil
	}
	resolution.Plan = []string{
		videoPlan(profile, cropWanted),
		audioPlan(profile.Audio),
		subtitlePlan(profile.Subtitles),
		containerPlan(profile),
	}
	return resolution, nil
}

// baseProfile is what a file gets when no node on its path asks for
// anything: a remux to mkv that copies the video, keeps every track, and
// preserves metadata, track titles, attachments, and chapters. The remaining
// values, such as the validation tolerance and the encoder settings a
// video.copy leaves unused, are the defaults of the built-in TOML profile.
func baseProfile() domain.Profile {
	defaults, _ := config.Default().FindProfile(config.DefaultProfileName)
	profile := defaults.ToDomain()
	profile.Name = profileName
	profile.Video.SkipEncode = true
	profile.Crop.Disabled = true
	profile.Metadata.TrackTitles = domain.TrackTitleModePreserve
	return profile
}

func videoPlan(profile domain.Profile, cropWanted bool) string {
	settings := profile.Video
	if settings.SkipEncode {
		if cropWanted {
			return "Video: copy; black bars are not cropped, because cropping needs a re-encode"
		}
		return "Video: copy"
	}
	parts := []string{"encode to " + settings.Codec + " with " + video.ResolveEncoder(settings.Codec, settings.Accelerator)}
	if settings.Preset != "" {
		parts = append(parts, "preset "+settings.Preset)
	}
	parts = append(parts, fmt.Sprintf("%d-bit", settings.BitDepth))
	fixed := settings.QualityMode == domain.VideoQualityFixed
	if fixed {
		parts = append(parts, fmt.Sprintf("CRF %d", settings.CRF))
	} else {
		parts = append(parts, fmt.Sprintf("%s %s target (CRF %d–%d)",
			strings.ToUpper(string(settings.Metric)), formatNumber(settings.Target), settings.CRFMin, settings.CRFMax))
	}
	plan := "Video: " + strings.Join(parts, ", ")
	if !fixed && settings.ForceEncodeOnNoFit {
		plan += "; encode anyway when no CRF fits"
	}
	if !profile.Crop.Disabled {
		plan += "; crop black bars"
	}
	return plan
}

func audioPlan(audio domain.AudioProfile) string {
	parts := []string{"keep " + listOr(cleanList(audio.LanguagesToKeep), "every track")}
	if audio.Encode == nil && audio.Downmix == nil {
		parts = append(parts, "copy")
	}
	if encode := audio.Encode; encode != nil {
		parts = append(parts, "encode "+listOr(encode.SourceCodecs, "every track")+" to "+audioTargetPlan(encode.Codec, encode.BitrateKbpsPerChannel))
	}
	if downmix := audio.Downmix; downmix != nil {
		parts = append(parts, "downmix above "+layoutName(downmix.MaxChannels)+" to "+audioTargetPlan(downmix.Codec, downmix.BitrateKbpsPerChannel))
	}
	return "Audio: " + strings.Join(parts, "; ")
}

func audioTargetPlan(codec string, bitrateKbpsPerChannel int) string {
	switch {
	case codec == "flac":
		return codec
	case bitrateKbpsPerChannel == 0:
		return codec + " at the encoder's default bitrate"
	default:
		return fmt.Sprintf("%s at %d kb/s per channel", codec, bitrateKbpsPerChannel)
	}
}

func subtitlePlan(subtitles domain.SubtitleProfile) string {
	if subtitles.RemoveAll {
		return "Subtitles: remove all"
	}
	languages := cleanList(subtitles.LanguagesToKeep)
	keep := "keep " + listOr(languages, "every track")
	// The kind filters only run once languages are listed.
	if len(languages) > 0 {
		var kinds []string
		if subtitles.KeepForced {
			kinds = append(kinds, "forced")
		}
		if subtitles.KeepSDH {
			kinds = append(kinds, "SDH")
		}
		if subtitles.KeepCommentary {
			kinds = append(kinds, "commentary")
		}
		if len(kinds) > 0 {
			keep += ", including " + strings.Join(kinds, ", ")
		}
	}
	if subtitles.DropBitmap {
		keep += "; drop image-based"
	}
	return "Subtitles: " + keep
}

func containerPlan(profile domain.Profile) string {
	parts := []string{profile.Container}
	if profile.Metadata.Mode == domain.MetadataModeStrip {
		parts = append(parts, "strip metadata")
	}
	switch profile.Metadata.TrackTitles {
	case domain.TrackTitleModeStrip:
		parts = append(parts, "strip track titles")
	case domain.TrackTitleModeStandardize:
		parts = append(parts, "standardize track titles")
	}
	if profile.Attachments.Mode == domain.MetadataModeStrip {
		parts = append(parts, "strip attachments")
	}
	if profile.Chapters.Mode == domain.MetadataModeStrip {
		parts = append(parts, "strip chapters")
	}
	if len(parts) == 1 {
		parts = append(parts, "keep metadata, track titles, attachments, and chapters")
	}
	return "Container: " + strings.Join(parts, "; ")
}
