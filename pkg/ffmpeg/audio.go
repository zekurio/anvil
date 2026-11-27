package ffmpeg

import (
	"fmt"
	"strconv"

	"github.com/zekurio/anvil/pkg/domain"
)

// audioEncoders maps a planned audio codec to the ffmpeg encoder that writes
// it. Opus goes through libopus because ffmpeg's own opus encoder is still
// experimental; the others are the native encoders every build carries.
var audioEncoders = map[string]string{
	"opus": "libopus",
	"aac":  "aac",
	"ac3":  "ac3",
	"eac3": "eac3",
	"flac": "flac",
}

// encodedAudio is a re-encoded track and its output audio index: the position
// of its stream among the mapped audio streams, which is what ffmpeg's
// per-stream options (-c:a:N) count.
type encodedAudio struct {
	output int
	domain.AudioTrackPlan
}

func encodedAudioTracks(plan domain.EncodePlan) []encodedAudio {
	if !plan.AudioSelectionApplied {
		return nil
	}
	var encoded []encodedAudio
	for output, index := range plan.AudioStreamIndexes {
		for _, track := range plan.AudioTracks {
			if track.StreamIndex == index && track.Action == domain.AudioTrackEncode {
				encoded = append(encoded, encodedAudio{output: output, AudioTrackPlan: track})
				break
			}
		}
	}
	return encoded
}

// validateAudioTracks rejects a plan Args could only turn into a failing
// command. The audio block never produces one; a hand-built plan might.
func validateAudioTracks(plan domain.EncodePlan) error {
	for _, track := range encodedAudioTracks(plan) {
		if _, ok := audioEncoders[track.Codec]; !ok {
			return fmt.Errorf("audio stream %d: codec %q has no encoder", track.StreamIndex, track.Codec)
		}
		if track.Channels <= 0 {
			return fmt.Errorf("audio stream %d: re-encoding needs a channel count", track.StreamIndex)
		}
	}
	return nil
}

// audioArgs copies every audio stream and then overrides the re-encoded ones
// by output index; ffmpeg applies the last matching per-stream option.
//
// Channels are set with an aformat filter and never with -ac. -ac picks the
// first layout of that width the encoder lists, or ffmpeg's default when it
// lists none: -ac 3 turns a 2.1 source into 3.0(back) for ac3, and -ac 4
// hands libopus a 4.0 layout it refuses to open.
func audioArgs(plan domain.EncodePlan) []string {
	args := []string{"-c:a", "copy"}
	for _, track := range encodedAudioTracks(plan) {
		stream := ":a:" + strconv.Itoa(track.output)
		args = append(args, "-c"+stream, audioEncoders[track.Codec])
		if track.BitrateKbps > 0 {
			args = append(args, "-b"+stream, strconv.Itoa(track.BitrateKbps)+"k")
		}
		if track.Layout != "" {
			args = append(args, "-filter"+stream, "aformat=channel_layouts="+track.Layout)
		}
	}
	return args
}

// outputAudioStream describes a selected audio stream as it leaves the
// encode, so standardized titles name the output codec and channels.
func outputAudioStream(plan domain.EncodePlan, stream domain.MediaStream) domain.MediaStream {
	for _, track := range plan.AudioTracks {
		if track.StreamIndex != stream.Index || track.Action != domain.AudioTrackEncode {
			continue
		}
		stream.Codec = track.Codec
		stream.Channels = track.Channels
		if track.Layout != "" {
			stream.ChannelLayout = track.Layout
		}
		break
	}
	return stream
}
