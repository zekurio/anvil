package audio

import (
	"fmt"
	"slices"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
)

// maxEncodeChannels is the widest source the encoders below can take. Wider
// tracks are object beds or raw multichannel PCM without a layout ffmpeg can
// downmix reliably, so they are copied.
const maxEncodeChannels = 8

type layout struct {
	name     string
	channels int
}

// target is what one output codec's ffmpeg encoder accepts. The limits were
// measured against ffmpeg 7.1 and 9.0 and exist so a plan can never ask for
// something the encoder rejects once the encode finally starts.
type target struct {
	// layouts lists the channel layouts the encoder writes as a standard
	// layout. The first entry of a channel count is the one other layouts
	// with that many channels are converted to.
	layouts []layout
	// lossless codecs take no bitrate and keep the source channels as they
	// are, even under a layout name the codec cannot signal.
	lossless bool
	// minKbpsPerChannel keeps ac3 and eac3 clear of "Bit allocation failed",
	// which they report on the first frames below roughly 12 kb/s a channel.
	minKbpsPerChannel int
	// maxKbpsPerChannel is libopus's hard limit; it refuses to open above it.
	maxKbpsPerChannel int
	// maxKbps caps the total: ac3 tops out at 640 kb/s, and eac3 refuses more
	// than 4096 kb/s at 32 kHz, the lowest sample rate it encodes.
	maxKbps int
}

// vorbisLayouts are the layouts libopus opens. It relabels 5.1(side) as 5.1
// on its own; anything else, such as quad(side), 5.0(side), or 6.1(back),
// fails with "Invalid channel layout".
var vorbisLayouts = []layout{
	{"mono", 1}, {"stereo", 2}, {"3.0", 3}, {"quad", 4}, {"5.0", 5}, {"5.1", 6}, {"6.1", 7}, {"7.1", 8},
}

// dolbyLayouts are what ac3 and eac3 encode without remixing. ffmpeg
// negotiates any other layout without a word, and 6.0 or 7.0 come out as 5.0.
var dolbyLayouts = []layout{
	{"mono", 1}, {"stereo", 2}, {"3.0", 3}, {"2.1", 3}, {"3.0(back)", 3},
	{"quad(side)", 4}, {"4.0", 4}, {"3.1", 4}, {"5.0(side)", 5}, {"4.1", 5}, {"5.1(side)", 6},
}

var targets = map[string]target{
	"opus": {layouts: vorbisLayouts, maxKbpsPerChannel: 256},
	// The native encoder signals a layout outside the standard channel
	// configurations with a program config element: ffprobe reads its
	// 5.1(side) back as an unknown layout, and players are less forgiving.
	// There is no standard seven-channel configuration.
	"aac": {layouts: []layout{
		{"mono", 1}, {"stereo", 2}, {"3.0", 3}, {"4.0", 4}, {"5.0", 5}, {"5.1", 6}, {"7.1", 8},
	}},
	"ac3":  {layouts: dolbyLayouts, minKbpsPerChannel: 32, maxKbps: 640},
	"eac3": {layouts: dolbyLayouts, minKbpsPerChannel: 32, maxKbps: 4096},
	"flac": {layouts: vorbisLayouts, lossless: true},
}

// fit returns the layout an output of at most channels channels is written
// in: the standard layout of the widest channel count the codec has.
func (t target) fit(channels int) layout {
	best := t.layouts[0]
	for _, candidate := range t.layouts {
		if candidate.channels <= channels && candidate.channels > best.channels {
			best = candidate
		}
	}
	return best
}

func (t target) writes(name string) bool {
	return slices.ContainsFunc(t.layouts, func(candidate layout) bool { return candidate.name == name })
}

// bitrate turns a per-channel rate into the total the encoder is given, kept
// inside what the encoder accepts. 0 leaves the encoder default.
func (t target) bitrate(kbpsPerChannel int, channels int) int {
	if t.lossless || kbpsPerChannel <= 0 {
		return 0
	}
	kbpsPerChannel = max(kbpsPerChannel, t.minKbpsPerChannel)
	if t.maxKbpsPerChannel > 0 {
		kbpsPerChannel = min(kbpsPerChannel, t.maxKbpsPerChannel)
	}
	total := kbpsPerChannel * channels
	if t.maxKbps > 0 {
		total = min(total, t.maxKbps)
	}
	return total
}

// validateOutput rejects an encode or downmix setting no track could be
// written with, before any selection is made.
func validateOutput(profile domain.AudioProfile) error {
	encode, downmix := profile.Encode, profile.Downmix
	if encode != nil {
		if _, ok := targets[codecName(encode.Codec)]; !ok {
			return fmt.Errorf("audio encode codec %q is not supported (opus, aac, ac3, eac3, flac)", encode.Codec)
		}
		if encode.BitrateKbpsPerChannel < 0 {
			return fmt.Errorf("audio encode bitrate %d kb/s per channel must not be negative", encode.BitrateKbpsPerChannel)
		}
	}
	if downmix == nil {
		return nil
	}
	if downmix.MaxChannels < 1 {
		return fmt.Errorf("audio downmix channel limit %d must be at least 1", downmix.MaxChannels)
	}
	if downmix.BitrateKbpsPerChannel < 0 {
		return fmt.Errorf("audio downmix bitrate %d kb/s per channel must not be negative", downmix.BitrateKbpsPerChannel)
	}
	// An encode that claims every track also supplies the downmix codec.
	if downmix.Codec == "" && encode != nil && len(encode.SourceCodecs) == 0 {
		return nil
	}
	if _, ok := targets[codecName(downmix.Codec)]; !ok {
		return fmt.Errorf("audio downmix codec %q is not supported (opus, aac, ac3, eac3, flac)", downmix.Codec)
	}
	return nil
}

// planTracks decides how each kept stream is written: downmixed when it has
// more channels than the downmix limit, re-encoded when the encode setting
// claims it and it is not in the target codec yet, and copied otherwise.
func planTracks(streams []domain.MediaStream, profile domain.AudioProfile) []domain.AudioTrackPlan {
	tracks := make([]domain.AudioTrackPlan, 0, len(streams))
	for _, stream := range streams {
		tracks = append(tracks, planTrack(stream, profile))
	}
	return tracks
}

func planTrack(stream domain.MediaStream, profile domain.AudioProfile) domain.AudioTrackPlan {
	plan := domain.AudioTrackPlan{
		StreamIndex:    stream.Index,
		Action:         domain.AudioTrackCopy,
		SourceCodec:    stream.Codec,
		SourceChannels: stream.Channels,
		Codec:          stream.Codec,
		Channels:       stream.Channels,
	}
	sourceCodec := codecName(stream.Codec)
	encode, downmix := profile.Encode, profile.Downmix
	if encode != nil && len(encode.SourceCodecs) > 0 &&
		!slices.ContainsFunc(encode.SourceCodecs, func(codec string) bool { return codecName(codec) == sourceCodec }) {
		encode = nil
	}
	wanted := downmix != nil || (encode != nil && codecName(encode.Codec) != sourceCodec)
	switch {
	case !wanted:
		return plan
	case stream.Channels <= 0:
		// Without a channel count there is no layout to encode to and no
		// telling whether the downmix limit applies.
		plan.Note = "source channel count is unknown; track copied"
		return plan
	case stream.Channels > maxEncodeChannels:
		plan.Note = fmt.Sprintf("tracks with more than %d channels are copied", maxEncodeChannels)
		return plan
	}

	channels := stream.Channels
	var codec string
	var kbpsPerChannel int
	switch {
	case downmix != nil && stream.Channels > downmix.MaxChannels:
		channels = downmix.MaxChannels
		codec, kbpsPerChannel = downmix.Codec, downmix.BitrateKbpsPerChannel
		if encode != nil {
			codec, kbpsPerChannel = encode.Codec, encode.BitrateKbpsPerChannel
		}
	case encode != nil && codecName(encode.Codec) != sourceCodec:
		codec, kbpsPerChannel = encode.Codec, encode.BitrateKbpsPerChannel
	default:
		return plan
	}

	codec = codecName(codec)
	output, ok := targets[codec]
	if !ok {
		plan.Note = fmt.Sprintf("audio codec %q is not supported; track copied", codec)
		return plan
	}
	fitted := output.fit(channels)
	var notes []string
	if fitted.channels < channels {
		notes = append(notes, fmt.Sprintf("%s has no %d-channel layout; downmixed to %s", codec, channels, fitted.name))
	}
	plan.Action = domain.AudioTrackEncode
	plan.Codec = codec
	plan.Channels = fitted.channels
	plan.Layout = fitted.name
	// A source the encoder already writes as it is needs no conversion.
	if fitted.channels == stream.Channels && (output.lossless || output.writes(strings.TrimSpace(stream.ChannelLayout))) {
		plan.Layout = ""
	}
	plan.BitrateKbps = output.bitrate(kbpsPerChannel, fitted.channels)
	if kbpsPerChannel > 0 && !output.lossless && plan.BitrateKbps != kbpsPerChannel*fitted.channels {
		notes = append(notes, fmt.Sprintf("%d kb/s per channel is outside what %s encodes; using %d kb/s", kbpsPerChannel, codec, plan.BitrateKbps))
	}
	plan.Note = strings.Join(notes, "; ")
	return plan
}

func codecName(codec string) string {
	return strings.ToLower(strings.TrimSpace(codec))
}
