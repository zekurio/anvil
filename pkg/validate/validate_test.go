package validate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

type fixedProber struct{ result domain.ProbeResult }

func (p fixedProber) Probe(context.Context, string) (domain.ProbeResult, error) {
	return p.result, nil
}

func TestValidateChecksReencodedAudioAgainstThePlan(t *testing.T) {
	output := filepath.Join(t.TempDir(), "out.mkv")
	if err := os.WriteFile(output, []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 0, Type: "video", Codec: "hevc"},
		{Index: 1, Type: "audio", Codec: "truehd", Channels: 8},
		{Index: 2, Type: "audio", Codec: "ac3", Channels: 6},
		{Index: 3, Type: "audio", Codec: "dts", Channels: 6},
	}}
	plan := &domain.EncodePlan{
		VideoCodec: "libsvtav1", AudioSelectionApplied: true, AudioStreamIndexes: []int{1, 3},
		AudioTracks: []domain.AudioTrackPlan{
			{StreamIndex: 1, Action: domain.AudioTrackEncode, SourceCodec: "truehd", SourceChannels: 8, Codec: "eac3", Channels: 6},
			{StreamIndex: 3, Action: domain.AudioTrackCopy, SourceCodec: "dts", SourceChannels: 6, Codec: "dts", Channels: 6},
		},
	}
	audio := func(codec string, channels int) domain.MediaStream {
		return domain.MediaStream{Type: "audio", Codec: codec, Channels: channels}
	}
	tests := []struct {
		name   string
		output []domain.MediaStream
		want   []string
	}{
		{"output matches the plan", []domain.MediaStream{audio("eac3", 6), audio("dts", 6)}, nil},
		{"track was copied instead of encoded", []domain.MediaStream{audio("truehd", 8), audio("dts", 6)}, []string{
			`output audio track 0 (source stream 1) codec "truehd" does not match expected "eac3"`,
			"output audio track 0 (source stream 1) has 8 channels, expected 6",
		}},
		{"downmix did not happen", []domain.MediaStream{audio("eac3", 8), audio("dts", 6)}, []string{
			"output audio track 0 (source stream 1) has 8 channels, expected 6",
		}},
		{"copied tracks are not second-guessed", []domain.MediaStream{audio("eac3", 6), audio("dts", 5)}, nil},
		{"a missing track is a count finding only", []domain.MediaStream{audio("eac3", 6)}, []string{
			"output audio stream count 1 does not match expected 2",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probed := domain.ProbeResult{Streams: append([]domain.MediaStream{{Type: "video", Codec: "av1"}}, tt.output...)}
			validator := Validator{Prober: fixedProber{result: probed}}
			result, err := validator.Validate(context.Background(), Request{SourceProbe: source, OutputPath: output, EncodePlan: plan})
			if err != nil && !errors.Is(err, ErrValidationFailed) {
				t.Fatal(err)
			}
			var findings []string
			for _, finding := range result.Errors {
				if strings.Contains(finding, "audio") {
					findings = append(findings, finding)
				}
			}
			if !reflect.DeepEqual(findings, tt.want) {
				t.Fatalf("audio findings = %q\nwant             %q", findings, tt.want)
			}
		})
	}
}
