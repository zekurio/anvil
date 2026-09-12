package ffmpeg

import (
	"slices"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestSampleAndFinalVideoSettingsMatch(t *testing.T) {
	for _, encoder := range []string{"libsvtav1", "hevc_qsv", "av1_vaapi", "h264_amf"} {
		t.Run(encoder, func(t *testing.T) {
			plan := domain.EncodePlan{InputPath: "input.mkv", OutputPath: "output.mkv", VideoCodec: encoder, InputVideoCodec: "hevc", CRF: 0, Preset: "6", BitDepth: 10, PixelFormat: "yuv420p10le", Threads: 2, FFmpegArgs: []string{"-g", "120"}}
			shared := videoOutputArgs(plan, "")
			for _, args := range [][]string{Args(plan), SampleArgs(plan)} {
				start := slices.Index(args, "-c:v")
				if start < 0 || !slices.Equal(args[start:start+len(shared)], shared) {
					t.Fatalf("video args differ: %q", args)
				}
			}
			quality := qualityArgs(plan)
			if len(quality) == 0 || quality[len(quality)-1] != "0" {
				t.Fatalf("CRF zero lost: %q", quality)
			}
		})
	}
	if got := selectedCRF(domain.VideoProfile{CRFMin: 18}, false, &domain.SearchResult{CRF: 0}); got != 0 {
		t.Fatalf("selected CRF = %d", got)
	}
}

func TestSearchOmitsUnsafeReferenceCrop(t *testing.T) {
	if filter := ReferenceFilter(unsafeCropPlan()); filter != "" {
		t.Fatalf("unsafe reference filter = %q", filter)
	}
	args := SampleArgs(unsafeCropPlan())
	if i := slices.Index(args, "-vf"); i >= 0 && strings.Contains(args[i+1], "crop") {
		t.Fatal("sample applies unsafe crop")
	}
}

func TestOnlySamplesNormalizeTimestamps(t *testing.T) {
	plan := domain.EncodePlan{VideoCodec: "hevc_qsv", InputVideoCodec: "hevc", Accelerator: "qsv", InputWidth: 1920, InputHeight: 1080, BitDepth: 10, CropFilter: "crop=1920:1000:0:40"}
	args := SampleArgs(plan)
	i := slices.Index(args, "-vf")
	if i < 0 || !strings.HasPrefix(args[i+1], "setpts=") || !strings.Contains(args[i+1], ",vpp_qsv=") {
		t.Fatalf("sample timestamp/crop filters = %q", args)
	}
	if !slices.Contains(args, "-xerror") || strings.Contains(strings.Join(Args(plan), " "), "setpts=") {
		t.Fatal("lost strict sample error handling or changed final encode timestamps")
	}
}

func TestEncoderArgumentsCannotOverrideSearch(t *testing.T) {
	for _, args := range [][]string{{"-crf", "20"}, {"-vf", "scale=128:128"}, {"-filter:v:0", "fps=24"}, {"-i", "other.mkv"}, {"output.mkv"}, {"-tune"}, {"-c:v", "copy"}, {"-r", "25"}} {
		if err := ValidateEncoderArgs(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	if err := ValidateEncoderArgs([]string{"-svtav1-params", "tune=0:film-grain=8", "-g", "120"}); err != nil {
		t.Fatal(err)
	}
}
