package config

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestProfileProblemsKeepCheckConfigWording pins the check-config messages for
// the rules that Problems also exposes to the flow editor.
func TestProfileProblemsKeepCheckConfigWording(t *testing.T) {
	cfg := Default()
	profile := cfg.Legacy.Profiles[DefaultProfileName]
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
	cfg.Legacy.Profiles[DefaultProfileName] = profile

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate succeeded")
	}
	want := strings.Join([]string{
		"invalid config:",
		`profile "default-av1" crop.samples must be 0 (automatic) or between 2 and 100`,
		`profile "default-av1" crop.frame_count must be between 1 and 10000`,
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
		`profile "default-av1" video.samples must be between 0 and 100`,
		`profile "default-av1" video.target must be set when metric is "xpsnr" (typical targets are 35-50)`,
		`profile "default-av1" min_savings_percent must be between 0 and 100`,
	}, "\n- ")
	if err.Error() != want {
		t.Fatalf("Validate() =\n%s\nwant\n%s", err, want)
	}
}

func TestProblemsNameFieldAndRule(t *testing.T) {
	video := Default().Legacy.Profiles[DefaultProfileName].Video
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
	if problems := Default().Legacy.Profiles[DefaultProfileName].Crop.Problems(); len(problems) != 0 {
		t.Fatalf("default crop Problems() = %+v", problems)
	}
}

// Each crop window and search sample is an ffmpeg run, and spread crop
// windows are allocated up front, so the counts have a ceiling.
func TestProblemsBoundTheWorkOfAJob(t *testing.T) {
	profile := Default().Legacy.Profiles[DefaultProfileName]
	crop := profile.Crop
	crop.Samples = MaxCropWindows + 1
	crop.FrameCount = MaxCropFrameCount + 1
	crop.SeekOffsets = make([]Duration, MaxCropWindows+1)
	want := []Problem{
		{Field: "samples", Rule: "must be 0 (automatic) or between 2 and 100"},
		{Field: "frame_count", Rule: "must be between 1 and 10000"},
		{Field: "seek_offsets", Rule: "must not hold more than 100 offsets"},
	}
	if got := crop.Problems(); !slices.EqualFunc(got, want, sameProblem) {
		t.Fatalf("crop Problems() = %+v, want %+v", got, want)
	}
	crop.Samples, crop.FrameCount, crop.SeekOffsets = MaxCropWindows, MaxCropFrameCount, make([]Duration, MaxCropWindows)
	if got := crop.Problems(); len(got) != 0 {
		t.Fatalf("crop Problems() at the bounds = %+v", got)
	}

	video := profile.Video
	video.Samples = MaxVideoSamples + 1
	want = []Problem{{Field: "samples", Rule: "must be between 0 and 100"}}
	if got := video.Problems(); !slices.EqualFunc(got, want, sameProblem) {
		t.Fatalf("video Problems() = %+v, want %+v", got, want)
	}
	video.Samples = MaxVideoSamples
	if got := video.Problems(); len(got) != 0 {
		t.Fatalf("video Problems() at the bound = %+v", got)
	}
}

func sameProblem(a, b Problem) bool {
	return a.Field == b.Field && a.Rule == b.Rule
}

func TestEncoderProblemsUseEffectiveSettings(t *testing.T) {
	base := Default().Legacy.Profiles[DefaultProfileName].Video
	base.Preset = "6"
	base.FFmpegArgs = []string{"-svtav1-params", "lp=2"}
	override := VideoOverrideConfig{Codec: new("h264"), Accelerator: new("qsv")}
	var problems problems
	override.validate(&problems, "video.overrides.vp9", base)
	got := strings.Join(problems, "\n")
	for _, want := range []string{
		`video.overrides.vp9.preset "6" must be one of veryfast, faster, fast, medium, slow, slower, veryslow for h264_qsv`,
		"video.overrides.vp9.bit_depth 10 must be 8 for h264_qsv",
		`video.overrides.vp9.ffmpeg_args option "-svtav1-params" is not accepted by h264_qsv`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestRenderDeviceIsFileOnlyAndAbsolute(t *testing.T) {
	for _, tt := range []struct {
		device string
		valid  bool
	}{
		{"", true},
		{"/dev/dri/renderD129", true},
		{"dev/dri/renderD129", false},
		{"/dev/dri/renderD129,driver=foo", false},
	} {
		cfg := Default()
		cfg.Daemon.RenderDevice = tt.device
		err := cfg.Validate()
		if (err == nil) != tt.valid || err != nil && !strings.Contains(err.Error(), "daemon.render_device") {
			t.Errorf("Validate with render_device %q = %v", tt.device, err)
		}
	}
	if slices.ContainsFunc(DaemonKeys(), func(key DaemonKey) bool { return key.Key == "render_device" }) {
		t.Fatal("render_device is writable through daemon overrides")
	}
}
