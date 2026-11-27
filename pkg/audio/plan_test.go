package audio

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

func audioStream(index int, codec string, channels int, layout string) domain.MediaStream {
	return domain.MediaStream{Index: index, Type: "audio", Codec: codec, Channels: channels, ChannelLayout: layout}
}

func TestPlanTrack(t *testing.T) {
	opusAll := &domain.AudioEncode{Codec: "opus", BitrateKbpsPerChannel: 64}
	eac3Lossless := &domain.AudioEncode{Codec: "eac3", BitrateKbpsPerChannel: 96, SourceCodecs: []string{"truehd", "DTS", "flac"}}
	stereoAAC := &domain.AudioDownmix{MaxChannels: 2, Codec: "aac", BitrateKbpsPerChannel: 96}
	surroundAC3 := &domain.AudioDownmix{MaxChannels: 6, Codec: "ac3", BitrateKbpsPerChannel: 80}
	tests := []struct {
		name    string
		stream  domain.MediaStream
		encode  *domain.AudioEncode
		downmix *domain.AudioDownmix
		want    domain.AudioTrackPlan
	}{
		{
			name:   "no encode or downmix copies",
			stream: audioStream(1, "truehd", 8, "7.1"),
			want:   domain.AudioTrackPlan{Action: domain.AudioTrackCopy, Codec: "truehd", Channels: 8},
		},
		{
			name:   "encode converts a 5.1(side) source to the layout libopus accepts",
			stream: audioStream(1, "ac3", 6, "5.1(side)"),
			encode: opusAll,
			want:   domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "opus", Channels: 6, Layout: "5.1", BitrateKbps: 384},
		},
		{
			name:   "encode keeps a layout the encoder already writes",
			stream: audioStream(1, "flac", 8, "7.1"),
			encode: opusAll,
			want:   domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "opus", Channels: 8, BitrateKbps: 512},
		},
		{
			name:   "a track already in the target codec is copied",
			stream: audioStream(1, "opus", 6, "5.1"),
			encode: opusAll,
			want:   domain.AudioTrackPlan{Action: domain.AudioTrackCopy, Codec: "opus", Channels: 6},
		},
		{
			name:   "source codec filter leaves other codecs alone",
			stream: audioStream(1, "ac3", 6, "5.1(side)"),
			encode: eac3Lossless,
			want:   domain.AudioTrackPlan{Action: domain.AudioTrackCopy, Codec: "ac3", Channels: 6},
		},
		{
			name:   "source codec filter matches case-insensitively",
			stream: audioStream(1, "dts", 6, "5.1(side)"),
			encode: eac3Lossless,
			want:   domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "eac3", Channels: 6, BitrateKbps: 576},
		},
		{
			name:   "eac3 carries at most six channels",
			stream: audioStream(1, "truehd", 8, "7.1"),
			encode: eac3Lossless,
			want: domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "eac3", Channels: 6, Layout: "5.1(side)", BitrateKbps: 576,
				Note: "eac3 has no 8-channel layout; downmixed to 5.1(side)"},
		},
		{
			name:    "downmix uses its own codec when encode does not match",
			stream:  audioStream(1, "ac3", 6, "5.1(side)"),
			encode:  eac3Lossless,
			downmix: stereoAAC,
			want:    domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "aac", Channels: 2, Layout: "stereo", BitrateKbps: 192},
		},
		{
			name:    "downmix takes codec and bitrate from a matching encode",
			stream:  audioStream(1, "dts", 6, "5.1(side)"),
			encode:  eac3Lossless,
			downmix: stereoAAC,
			want:    domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "eac3", Channels: 2, Layout: "stereo", BitrateKbps: 192},
		},
		{
			name:    "downmix re-encodes a track already in the encode codec",
			stream:  audioStream(1, "opus", 8, "7.1"),
			encode:  opusAll,
			downmix: surroundAC3,
			want:    domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "opus", Channels: 6, Layout: "5.1", BitrateKbps: 384},
		},
		{
			name:    "a track at the downmix limit is left to encode or copied",
			stream:  audioStream(1, "aac", 2, "stereo"),
			downmix: stereoAAC,
			want:    domain.AudioTrackPlan{Action: domain.AudioTrackCopy, Codec: "aac", Channels: 2},
		},
		{
			name:    "ac3 downmix bitrate stops at the codec's ceiling",
			stream:  audioStream(1, "truehd", 8, "7.1"),
			downmix: &domain.AudioDownmix{MaxChannels: 6, Codec: "ac3", BitrateKbpsPerChannel: 128},
			want: domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "ac3", Channels: 6, Layout: "5.1(side)", BitrateKbps: 640,
				Note: "128 kb/s per channel is outside what ac3 encodes; using 640 kb/s"},
		},
		{
			name:   "opus bitrate stops at what libopus opens",
			stream: audioStream(1, "flac", 2, "stereo"),
			encode: &domain.AudioEncode{Codec: "opus", BitrateKbpsPerChannel: 320},
			want: domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "opus", Channels: 2, BitrateKbps: 512,
				Note: "320 kb/s per channel is outside what opus encodes; using 512 kb/s"},
		},
		{
			name:   "dolby bitrate is raised clear of bit allocation failure",
			stream: audioStream(1, "flac", 6, "5.1"),
			encode: &domain.AudioEncode{Codec: "eac3", BitrateKbpsPerChannel: 8},
			want: domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "eac3", Channels: 6, Layout: "5.1(side)", BitrateKbps: 192,
				Note: "8 kb/s per channel is outside what eac3 encodes; using 192 kb/s"},
		},
		{
			name:   "zero bitrate leaves the encoder default",
			stream: audioStream(1, "dts", 6, "5.1(side)"),
			encode: &domain.AudioEncode{Codec: "aac"},
			want:   domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "aac", Channels: 6, Layout: "5.1"},
		},
		{
			name:   "flac ignores bitrate and keeps the source layout",
			stream: audioStream(1, "truehd", 6, "5.1(side)"),
			encode: &domain.AudioEncode{Codec: "FLAC", BitrateKbpsPerChannel: 256},
			want:   domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "flac", Channels: 6},
		},
		{
			name:    "flac downmix names the target layout",
			stream:  audioStream(1, "truehd", 8, "7.1"),
			downmix: &domain.AudioDownmix{MaxChannels: 2, Codec: "flac"},
			want:    domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "flac", Channels: 2, Layout: "stereo"},
		},
		{
			name:   "aac has no seven-channel layout",
			stream: audioStream(1, "dts", 7, "6.1"),
			encode: &domain.AudioEncode{Codec: "aac", BitrateKbpsPerChannel: 64},
			want: domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "aac", Channels: 6, Layout: "5.1", BitrateKbps: 384,
				Note: "aac has no 7-channel layout; downmixed to 5.1"},
		},
		{
			name:   "an unnamed layout is converted to the standard one",
			stream: audioStream(1, "dts", 6, ""),
			encode: opusAll,
			want:   domain.AudioTrackPlan{Action: domain.AudioTrackEncode, Codec: "opus", Channels: 6, Layout: "5.1", BitrateKbps: 384},
		},
		{
			name:   "unknown channel count falls back to copy",
			stream: audioStream(1, "dts", 0, ""),
			encode: opusAll,
			want:   domain.AudioTrackPlan{Action: domain.AudioTrackCopy, Codec: "dts", Note: "source channel count is unknown; track copied"},
		},
		{
			name:    "unknown channel count is not downmixed",
			stream:  audioStream(1, "aac", 0, ""),
			downmix: stereoAAC,
			want:    domain.AudioTrackPlan{Action: domain.AudioTrackCopy, Codec: "aac", Note: "source channel count is unknown; track copied"},
		},
		{
			name:   "unknown channel count without a claim stays quiet",
			stream: audioStream(1, "ac3", 0, ""),
			encode: eac3Lossless,
			want:   domain.AudioTrackPlan{Action: domain.AudioTrackCopy, Codec: "ac3"},
		},
		{
			name:   "more channels than any encoder takes are copied",
			stream: audioStream(1, "pcm_s24le", 16, ""),
			encode: opusAll,
			want:   domain.AudioTrackPlan{Action: domain.AudioTrackCopy, Codec: "pcm_s24le", Channels: 16, Note: "tracks with more than 8 channels are copied"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.want.StreamIndex = tt.stream.Index
			tt.want.SourceCodec = tt.stream.Codec
			tt.want.SourceChannels = tt.stream.Channels
			got := planTracks([]domain.MediaStream{tt.stream}, domain.AudioProfile{Encode: tt.encode, Downmix: tt.downmix})
			if len(got) != 1 || got[0] != tt.want {
				t.Fatalf("plan = %+v\nwant   %+v", got, tt.want)
			}
		})
	}
}

func TestValidateOutput(t *testing.T) {
	tests := []struct {
		name    string
		profile domain.AudioProfile
		want    string
	}{
		{"nothing configured", domain.AudioProfile{}, ""},
		{"supported codecs", domain.AudioProfile{
			Encode:  &domain.AudioEncode{Codec: "opus"},
			Downmix: &domain.AudioDownmix{MaxChannels: 2, Codec: "aac"},
		}, ""},
		{"encode for every track supplies the downmix codec", domain.AudioProfile{
			Encode:  &domain.AudioEncode{Codec: "opus"},
			Downmix: &domain.AudioDownmix{MaxChannels: 6},
		}, ""},
		{"unknown encode codec", domain.AudioProfile{Encode: &domain.AudioEncode{Codec: "mp3"}}, `audio encode codec "mp3" is not supported`},
		{"filtered encode needs a downmix codec", domain.AudioProfile{
			Encode:  &domain.AudioEncode{Codec: "opus", SourceCodecs: []string{"dts"}},
			Downmix: &domain.AudioDownmix{MaxChannels: 2},
		}, `audio downmix codec "" is not supported`},
		{"downmix limit", domain.AudioProfile{Downmix: &domain.AudioDownmix{Codec: "aac"}}, "audio downmix channel limit 0 must be at least 1"},
		{"negative bitrate", domain.AudioProfile{Encode: &domain.AudioEncode{Codec: "aac", BitrateKbpsPerChannel: -1}}, "must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOutput(tt.profile)
			if tt.want == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSelectRules(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 0, Type: "video", Codec: "hevc"},
		{Index: 1, Type: "audio", Codec: "truehd", Channels: 8, ChannelLayout: "7.1", Language: "eng"},
		{Index: 2, Type: "audio", Codec: "ac3", Channels: 6, ChannelLayout: "5.1(side)", Language: "deu"},
	}}
	tests := []struct {
		name       string
		profile    domain.AudioProfile
		metadata   domain.JobMetadata
		wantRule   domain.StreamSelectionRule
		wantReason domain.StreamDecisionReason
		wantKept   []int
		wantReport string
	}{
		{
			name:       "no languages keeps every track quietly",
			profile:    domain.AudioProfile{},
			wantRule:   domain.StreamSelectionRuleKeepAll,
			wantReason: domain.StreamKeptKeepAll,
			wantKept:   []int{1, 2},
		},
		{
			name:       "missing original language still disables cleanup",
			profile:    domain.AudioProfile{LanguagesToKeep: []string{"orig"}},
			metadata:   domain.JobMetadata{StreamCleanupDisabled: true, StreamCleanupDisabledReason: "original language metadata is unavailable"},
			wantRule:   domain.StreamSelectionRuleCleanupDisabled,
			wantReason: domain.StreamKeptCleanupDisabled,
			wantKept:   []int{1, 2},
			wantReport: "original language metadata is unavailable",
		},
		{
			name:       "original token without an original language disables cleanup",
			profile:    domain.AudioProfile{LanguagesToKeep: []string{"orig"}},
			wantRule:   domain.StreamSelectionRuleCleanupDisabled,
			wantReason: domain.StreamKeptCleanupDisabled,
			wantKept:   []int{1, 2},
			wantReport: "original language is unavailable",
		},
		{
			name:       "language filter",
			profile:    domain.AudioProfile{LanguagesToKeep: []string{"eng"}},
			wantRule:   domain.StreamSelectionRuleLanguageFilter,
			wantReason: domain.StreamKeptLanguageMatch,
			wantKept:   []int{1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection, err := (Selector{}).Select(probe, tt.profile, tt.metadata)
			if err != nil {
				t.Fatal(err)
			}
			decision := selection.Decision
			if decision.Rule != tt.wantRule || decision.CleanupDisabledReason != tt.wantReport {
				t.Fatalf("rule = %q, cleanup reason = %q", decision.Rule, decision.CleanupDisabledReason)
			}
			if !reflect.DeepEqual(selection.StreamIndexes, tt.wantKept) {
				t.Fatalf("kept = %v, want %v", selection.StreamIndexes, tt.wantKept)
			}
			for _, stream := range decision.Streams {
				if stream.Kept && stream.Reason != tt.wantReason {
					t.Fatalf("stream %d reason = %q, want %q", stream.Index, stream.Reason, tt.wantReason)
				}
			}
			if len(selection.Tracks) != len(tt.wantKept) {
				t.Fatalf("tracks = %+v, want one per kept stream", selection.Tracks)
			}
		})
	}
}

func TestSelectPlansKeptTracksAndReportsThem(t *testing.T) {
	job := &pipeline.JobContext{
		Probe: &domain.ProbeResult{Streams: []domain.MediaStream{
			{Index: 1, Type: "audio", Codec: "truehd", Channels: 8, ChannelLayout: "7.1", Language: "eng"},
			{Index: 2, Type: "audio", Codec: "ac3", Channels: 6, ChannelLayout: "5.1(side)", Language: "eng"},
			{Index: 3, Type: "audio", Codec: "dts", Channels: 6, ChannelLayout: "5.1(side)", Language: "deu"},
		}},
		Profile: domain.Profile{Audio: domain.AudioProfile{
			LanguagesToKeep: []string{"eng"},
			Encode:          &domain.AudioEncode{Codec: "opus", BitrateKbpsPerChannel: 64, SourceCodecs: []string{"truehd"}},
			Downmix:         &domain.AudioDownmix{MaxChannels: 6, Codec: "aac"},
		}},
	}
	if err := (Block{}).Run(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(job.Audio.StreamIndexes, []int{1, 2}) {
		t.Fatalf("kept = %v", job.Audio.StreamIndexes)
	}
	report, ok := (Block{}).Artifact(job)
	if !ok || report.Name != OutputArtifact {
		t.Fatalf("report = %+v, %v", report, ok)
	}
	if want := "1:downmix:truehd:8ch->opus:6ch:384k, 2:copy:ac3:6ch"; report.Message != want {
		t.Fatalf("message = %q, want %q", report.Message, want)
	}

	job.Profile.Audio.Encode, job.Profile.Audio.Downmix = nil, nil
	if _, ok := (Block{}).Artifact(job); ok {
		t.Fatal("a profile that copies every track reported an output plan")
	}
}

func TestSelectRejectsUnsupportedCodecBeforeDeciding(t *testing.T) {
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{{Index: 1, Type: "audio", Codec: "ac3", Channels: 6}}}
	selection, err := (Selector{}).Select(probe, domain.AudioProfile{Encode: &domain.AudioEncode{Codec: "vorbis"}}, domain.JobMetadata{})
	if err == nil || selection.Decision != nil {
		t.Fatalf("selection = %+v, error = %v", selection, err)
	}
}
