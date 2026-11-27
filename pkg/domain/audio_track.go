package domain

import (
	"fmt"
	"strconv"
)

// AudioTrackAction says whether a kept audio track is copied or re-encoded.
type AudioTrackAction string

const (
	AudioTrackCopy   AudioTrackAction = "copy"
	AudioTrackEncode AudioTrackAction = "encode"
)

// AudioTrackPlan says how one kept audio track is written. Its JSON form is
// stored in attempt events and job checkpoints, so the field names are stable.
type AudioTrackPlan struct {
	StreamIndex    int              `json:"stream_index"`
	Action         AudioTrackAction `json:"action"`
	SourceCodec    string           `json:"source_codec,omitempty"`
	SourceChannels int              `json:"source_channels,omitempty"`
	// Codec and Channels describe the output track; a copied track repeats
	// its source. Codec is "opus", "aac", "ac3", "eac3", or "flac" for a
	// re-encoded track.
	Codec    string `json:"codec,omitempty"`
	Channels int    `json:"channels,omitempty"`
	// Layout is the ffmpeg channel layout a re-encoded track is converted to;
	// empty keeps the source layout.
	Layout string `json:"layout,omitempty"`
	// BitrateKbps is the total bitrate of a re-encoded track. 0 leaves the
	// encoder default, which is always the case for flac.
	BitrateKbps int `json:"bitrate_kbps,omitempty"`
	// Note explains an outcome the profile did not ask for outright, such as
	// a track left as it is because its channel count is unknown.
	Note string `json:"note,omitempty"`
}

// Downmixed reports whether the track is re-encoded with fewer channels than
// its source.
func (p AudioTrackPlan) Downmixed() bool {
	return p.Action == AudioTrackEncode && p.Channels < p.SourceChannels
}

// Summary renders the track as one compact, log-friendly value, such as
// "2:copy:eac3:6ch" or "3:downmix:truehd:8ch->opus:6ch:384k".
func (p AudioTrackPlan) Summary() string {
	source := fmt.Sprintf("%s:%dch", displayValue(p.SourceCodec, "unknown"), p.SourceChannels)
	if p.Action != AudioTrackEncode {
		return fmt.Sprintf("%d:copy:%s", p.StreamIndex, source)
	}
	action := "encode"
	if p.Downmixed() {
		action = "downmix"
	}
	bitrate := "default"
	if p.BitrateKbps > 0 {
		bitrate = strconv.Itoa(p.BitrateKbps) + "k"
	}
	return fmt.Sprintf("%d:%s:%s->%s:%dch:%s", p.StreamIndex, action, source, p.Codec, p.Channels, bitrate)
}
