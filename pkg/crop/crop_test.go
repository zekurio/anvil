package crop

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestApplySafetyPolicyRejectsTinyCandidates(t *testing.T) {
	tests := []struct {
		name     string
		filter   string
		width    int
		height   int
		wantArea string
	}{
		{name: "1080p", filter: "crop=176:64:996:64", width: 1920, height: 1080, wantArea: "retained area 0.54%"},
		{name: "720p", filter: "crop=112:32:668:48", width: 1280, height: 720, wantArea: "retained area 0.39%"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ApplySafetyPolicy(
				domain.CropResult{Filter: test.filter},
				videoProbe(test.width, test.height),
				domain.CropPolicy{},
			)
			if result.CandidateFilter != test.filter {
				t.Fatalf("CandidateFilter = %q, want %q", result.CandidateFilter, test.filter)
			}
			if result.Filter != "" {
				t.Fatalf("Filter = %q, want no crop", result.Filter)
			}
			if !strings.Contains(result.RejectionReason, test.wantArea) {
				t.Fatalf("RejectionReason = %q, want %q", result.RejectionReason, test.wantArea)
			}
			if !strings.Contains(result.RejectionReason, "smaller than minimum") {
				t.Fatalf("RejectionReason = %q, want dimension rejection", result.RejectionReason)
			}
		})
	}
}

func TestApplySafetyPolicyAcceptsCommonAspectRatioCrops(t *testing.T) {
	tests := []struct {
		name   string
		filter string
	}{
		{name: "letterbox", filter: "crop=1920:800:0:140"},
		{name: "pillarbox", filter: "crop=1440:1080:240:0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ApplySafetyPolicy(
				domain.CropResult{CandidateFilter: test.filter},
				videoProbe(1920, 1080),
				domain.CropPolicy{},
			)
			if result.Filter != test.filter {
				t.Fatalf("Filter = %q, want %q (reason %q)", result.Filter, test.filter, result.RejectionReason)
			}
			if result.RetainedAreaPercent < 70 {
				t.Fatalf("RetainedAreaPercent = %.2f, want at least 70", result.RetainedAreaPercent)
			}
		})
	}
}

func TestApplySafetyPolicyChecksBoundsAndAlignmentIndependently(t *testing.T) {
	tests := []struct {
		name       string
		filter     string
		wantReason string
	}{
		{name: "bounds", filter: "crop=1920:1000:2:82", wantReason: "exceeds source dimensions"},
		{name: "alignment", filter: "crop=1919:1080:0:0", wantReason: "not aligned to 2 pixels"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ApplySafetyPolicy(
				domain.CropResult{Filter: test.filter},
				videoProbe(1920, 1080),
				domain.CropPolicy{},
			)
			if result.Filter != "" {
				t.Fatalf("Filter = %q, want no crop", result.Filter)
			}
			if !strings.Contains(result.RejectionReason, test.wantReason) {
				t.Fatalf("RejectionReason = %q, want %q", result.RejectionReason, test.wantReason)
			}
		})
	}
}

func TestApplySafetyPolicyRejectsCandidateWithoutSourceDimensions(t *testing.T) {
	result := ApplySafetyPolicy(domain.CropResult{Filter: "crop=1920:800:0:140"}, nil, domain.CropPolicy{})
	if result.Filter != "" || result.RejectionReason != "source video dimensions are unavailable" {
		t.Fatalf("result = %#v, want safe no-crop fallback", result)
	}
}

func TestApplySafetyPolicyNormalizesFullFrameCrop(t *testing.T) {
	result := ApplySafetyPolicy(
		domain.CropResult{Filter: "crop=1920:1080:0:0"},
		videoProbe(1920, 1080),
		domain.CropPolicy{},
	)
	if result.Filter != "" || !result.NoOp || result.RejectionReason != "" {
		t.Fatalf("result = %#v, want no-op crop", result)
	}
}

func TestPrimaryVideoSkipsAttachedPicture(t *testing.T) {
	stream, ok := primaryVideo(&domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 0, Type: "video", Width: 600, Height: 600, Disposition: map[string]bool{"attached_pic": true}},
		{Index: 3, Type: "video", Width: 1920, Height: 1080},
	}})
	if !ok || stream.Index != 3 {
		t.Fatalf("primaryVideo = %#v, %v", stream, ok)
	}
}

func TestDetectorArgsUseConfiguredSamplingAndVideoStream(t *testing.T) {
	detector := FFmpegDetector{
		FrameCount:       42,
		Limit:            20,
		Round:            8,
		ResetCount:       12,
		MapVideoStream:   true,
		VideoStreamIndex: 3,
	}
	got := detector.args("movie.mkv", 90*time.Second)
	want := []string{
		"-hide_banner", "-ss", "90", "-i", "movie.mkv", "-map", "0:3",
		"-vf", "cropdetect=20:8:12", "-frames:v", "42",
		"-an", "-sn", "-dn", "-f", "null", "-",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestParseFilterUsesLatestCandidateToBreakFrequencyTie(t *testing.T) {
	output := []byte(strings.Join([]string{
		"[Parsed_cropdetect_0 @ 0x1] crop=1920:800:0:140",
		"[Parsed_cropdetect_0 @ 0x1] crop=1440:1080:240:0",
		"[Parsed_cropdetect_0 @ 0x1] crop=1920:800:0:140",
		"[Parsed_cropdetect_0 @ 0x1] crop=1440:1080:240:0",
	}, "\n"))
	if got := ParseFilter(output); got != "crop=1440:1080:240:0" {
		t.Fatalf("ParseFilter = %q", got)
	}
}

func TestParseFilterIgnoresCropExpressionsOutsideCropdetectOutput(t *testing.T) {
	output := []byte("Input #0, matroska, from 'crop=1760:900:80:90.mkv':\n" +
		"  title: crop=1760:900:80:90\n" +
		"[Parsed_scale_0 @ 0x1] crop=1760:900:80:90")
	if got := ParseFilter(output); got != "" {
		t.Fatalf("ParseFilter = %q, want no candidate", got)
	}
}

func videoProbe(width, height int) *domain.ProbeResult {
	return &domain.ProbeResult{Streams: []domain.MediaStream{{Type: "video", Width: width, Height: height}}}
}
