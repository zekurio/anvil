package crop

import (
	"math"
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

func TestParseFilterPreservesAllObservedPictureBounds(t *testing.T) {
	output := []byte(strings.Join([]string{
		"[Parsed_cropdetect_0 @ 0x1] crop=1920:800:0:140",
		"[Parsed_cropdetect_0 @ 0x1] crop=1440:1080:240:0",
		"[Parsed_cropdetect_0 @ 0x1] crop=1920:800:0:140",
		"[Parsed_cropdetect_0 @ 0x1] crop=1440:1080:240:0",
	}, "\n"))
	if got := ParseFilter(output); got != "crop=1920:1080:0:0" {
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

func TestApplySafetyPolicyRejectsWhiplashCrop(t *testing.T) {
	result := ApplySafetyPolicy(domain.CropResult{Filter: "crop=1616:752:254:2"}, videoProbe(1920, 804), domain.CropPolicy{})
	if result.Filter != "" || !strings.Contains(result.RejectionReason, "uneven borders") || result.RetainedAreaPercent < 70 {
		t.Fatalf("unsafe crop accepted: %#v", result)
	}
}

func samplesFor(filters ...string) []domain.CropSample {
	samples := make([]domain.CropSample, 0, len(filters))
	for _, filter := range filters {
		samples = append(samples, domain.CropSample{Filter: filter})
	}
	return samples
}

func TestSelectSamples(t *testing.T) {
	tests := []struct {
		name    string
		samples []domain.CropSample
		want    string
		reason  string
	}{
		{"letterbox", samplesFor("crop=1920:800:0:140", "crop=1920:800:0:140"), "crop=1920:800:0:140", ""},
		{"pillarbox", samplesFor("crop=1440:1080:240:0", "crop=1440:1080:240:0"), "crop=1440:1080:240:0", ""},
		{"rounding", samplesFor("crop=1920:800:0:140", "crop=1920:804:0:138"), "crop=1920:804:0:138", ""},
		// Dark scenes report rectangles inside the picture. The windows that saw
		// more must win, not the ones that agree most.
		{"dark scenes", samplesFor("crop=1696:576:96:228", "crop=1856:480:60:318", "crop=1824:560:0:2", "crop=1616:752:254:2", "crop=1920:800:0:2"), "crop=1920:802:0:2", ""},
		{"full frame evidence", samplesFor("crop=1920:800:0:140", "crop=1920:1080:0:0"), "crop=1920:1080:0:0", ""},
		{
			"failed window",
			[]domain.CropSample{{Filter: "crop=1920:800:0:140"}, {Filter: "crop=1920:800:0:140", Error: "decode failed"}, {Filter: "crop=1920:798:0:142"}},
			"crop=1920:800:0:140",
			"crop sample failed",
		},
		{"no evidence", samplesFor("", ""), "", "no crop sample contains picture evidence"},
		{"one sample", samplesFor("crop=1920:800:0:140", ""), "crop=1920:800:0:140", "fewer than two crop samples contain picture evidence"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := selectSamples(tt.samples)
			if got != tt.want || reason != tt.reason {
				t.Fatalf("selectSamples = %q, %q; want %q, %q", got, reason, tt.want, tt.reason)
			}
			result := ApplySafetyPolicy(domain.CropResult{CandidateFilter: got, SelectionReason: reason}, videoProbe(1920, 1080), domain.CropPolicy{})
			if reason != "" && (result.Filter != "" || result.RejectionReason != reason) {
				t.Fatalf("lost rejection: %#v", result)
			}
		})
	}
}

// Windows recorded by the Dead City E07 job: five dark scenes and one bright
// scene that shows the real 2:1 letterbox. The crop must survive them.
func TestSelectSamplesKeepsLetterboxFromDarkWindows(t *testing.T) {
	samples := samplesFor("crop=384:400:450:438", "crop=1920:662:0:60", "crop=1824:724:4:134", "crop=1920:952:0:60", "crop=1536:208:382:60", "crop=1920:960:0:60")
	candidate, reason := selectSamples(samples)
	if candidate != "crop=1920:960:0:60" || reason != "" {
		t.Fatalf("selectSamples = %q, %q", candidate, reason)
	}
	result := ApplySafetyPolicy(domain.CropResult{CandidateFilter: candidate, SelectionReason: reason, Samples: samples}, videoProbe(1920, 1080), domain.CropPolicy{})
	if result.Filter != "crop=1920:960:0:60" || result.RejectionReason != "" {
		t.Fatalf("crop rejected: %#v", result)
	}
}

func TestSpreadOffsets(t *testing.T) {
	explicit := spreadOffsets(300, 4)
	want := []time.Duration{37500 * time.Millisecond, 112500 * time.Millisecond, 187500 * time.Millisecond, 262500 * time.Millisecond}
	if !slices.Equal(explicit, want) {
		t.Fatalf("explicit = %v, want %v", explicit, want)
	}
	durations := []struct {
		name     string
		duration float64
		count    int
		first    time.Duration
		last     time.Duration
		spacing  time.Duration
	}{
		{"episode", (45 * time.Minute).Seconds(), 15, 90 * time.Second, 2610 * time.Second, 180 * time.Second},
		{"feature", (4 * time.Hour).Seconds(), 24, 300 * time.Second, 14100 * time.Second, 600 * time.Second},
		{"short clip", (10 * time.Minute).Seconds(), 4, 75 * time.Second, 525 * time.Second, 150 * time.Second},
	}
	for _, tt := range durations {
		t.Run(tt.name, func(t *testing.T) {
			offsets := spreadOffsets(tt.duration, 0)
			if len(offsets) != tt.count {
				t.Fatalf("windows = %d, want %d", len(offsets), tt.count)
			}
			if offsets[0] != tt.first || offsets[len(offsets)-1] != tt.last {
				t.Fatalf("offsets span %v..%v, want %v..%v", offsets[0], offsets[len(offsets)-1], tt.first, tt.last)
			}
			for i := 1; i < len(offsets); i++ {
				if offsets[i]-offsets[i-1] != tt.spacing {
					t.Fatalf("spacing %v, want %v", offsets[i]-offsets[i-1], tt.spacing)
				}
			}
		})
	}
	for _, duration := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if offsets := spreadOffsets(duration, 0); offsets != nil {
			t.Fatalf("spreadOffsets(%v) = %v, want none", duration, offsets)
		}
	}
}

func TestSeekOffsets(t *testing.T) {
	configured := []time.Duration{time.Minute, 2 * time.Minute}
	got := seekOffsets(domain.CropPolicy{SeekOffsets: configured, Samples: 3}, (45 * time.Minute).Seconds())
	if !slices.Equal(got, configured) {
		t.Fatalf("seekOffsets = %v, want configured offsets %v", got, configured)
	}
	got[0] = time.Hour
	if configured[0] != time.Minute {
		t.Fatal("seekOffsets aliases profile offsets")
	}
	if offsets := seekOffsets(domain.CropPolicy{}, 0); !slices.Equal(offsets, fallbackSeekOffsets) {
		t.Fatalf("seekOffsets without duration = %v, want fallback %v", offsets, fallbackSeekOffsets)
	}
}

func TestParseFilterDoesNotVoteAwayWiderPicture(t *testing.T) {
	output := strings.Repeat("[Parsed_cropdetect_0 @ 0x1] crop=1616:752:254:2\n", 212) + strings.Repeat("[Parsed_cropdetect_0 @ 0x1] crop=1920:800:0:2\n", 191)
	if got := ParseFilter([]byte(output)); got != "crop=1920:800:0:2" {
		t.Fatalf("got %q", got)
	}
}
