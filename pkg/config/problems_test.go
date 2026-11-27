package config

import (
	"math"
	"strings"
	"testing"
	"time"
)

// TestProfileProblemsKeepCheckConfigWording pins the check-config messages for
// the rules that Problems also exposes to the flow editor.
func TestProfileProblemsKeepCheckConfigWording(t *testing.T) {
	cfg := Default()
	profile := cfg.Profiles[DefaultProfileName]
	profile.Video.Codec = "vp9"
	profile.Video.Accelerator = "cuda"
	profile.Video.BitDepth = 12
	profile.Video.CRFMin = 50
	profile.Video.CRFMax = -1
	profile.Video.SampleDuration = Duration{}
	profile.Video.FFmpegArgs = []string{"-crf", "20"}
	profile.Video.Samples = -1
	profile.Video.Metric = "xpsnr"
	profile.Video.Target = 0
	profile.Video.MinSavingsPercent = 101
	profile.Crop = CropConfig{
		SeekOffsets:        []Duration{{Duration: time.Second}, {Duration: -time.Second}},
		Samples:            1,
		Limit:              256,
		ResetCount:         -1,
		MinRetainedAreaPct: math.NaN(),
	}
	cfg.Profiles[DefaultProfileName] = profile

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate succeeded")
	}
	want := strings.Join([]string{
		"invalid config:",
		`profile "default-av1" crop.samples must be 0 (automatic) or at least 2`,
		`profile "default-av1" crop.frame_count must be at least 1`,
		`profile "default-av1" crop.seek_offsets[1] must be non-negative`,
		`profile "default-av1" crop.limit must be between 1 and 255`,
		`profile "default-av1" crop.round must be at least 1`,
		`profile "default-av1" crop.reset_count must be non-negative`,
		`profile "default-av1" crop.min_retained_area_percent must be finite, greater than 0, and at most 100`,
		`profile "default-av1" crop.min_width must be at least 1`,
		`profile "default-av1" crop.min_height must be at least 1`,
		`profile "default-av1" crop.required_alignment must be at least 1`,
		`profile "default-av1" video.codec "vp9" is invalid (must be av1, hevc, h265, h264, or avc)`,
		`profile "default-av1" video.accelerator "cuda" is invalid (must be software, qsv, vaapi, or amf)`,
		`profile "default-av1" video.bit_depth 12 is invalid (must be 8 or 10)`,
		`profile "default-av1" CRF values must be non-negative`,
		`profile "default-av1" crf_min must be less than or equal to crf_max`,
		`profile "default-av1" video.sample_duration must be positive`,
		`profile "default-av1" video.ffmpeg_args option "-crf" is managed by Anvil and cannot be used during CRF search`,
		`profile "default-av1" video.samples must be non-negative`,
		`profile "default-av1" video.target must be set when metric is "xpsnr" (typical targets are 35-50)`,
		`profile "default-av1" min_savings_percent must be between 0 and 100`,
	}, "\n- ")
	if err.Error() != want {
		t.Fatalf("Validate() =\n%s\nwant\n%s", err, want)
	}
}

func TestProblemsNameFieldAndRule(t *testing.T) {
	video := Default().Profiles[DefaultProfileName].Video
	video.CRFMin = 41
	video.FFmpegArgs = []string{"-svtav1-params", "crf=20"}

	got := video.Problems()
	want := []Problem{
		{Field: "crf_min", Rule: "must be less than or equal to crf_max"},
		{Field: "ffmpeg_args", Rule: `option "-svtav1-params": encoder param "crf" overrides a setting managed by Anvil`},
	}
	if len(got) != len(want) {
		t.Fatalf("Problems() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Field != want[i].Field || got[i].Rule != want[i].Rule {
			t.Errorf("Problems()[%d] = %q %q, want %q %q", i, got[i].Field, got[i].Rule, want[i].Field, want[i].Rule)
		}
	}
	if problems := Default().Profiles[DefaultProfileName].Crop.Problems(); len(problems) != 0 {
		t.Fatalf("default crop Problems() = %+v", problems)
	}
}
