package ffmpeg

import (
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestBuildPlanUsesSearchCRF(t *testing.T) {
	plan, err := BuildPlan(testProfile(), "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 6}, &domain.SearchResult{CRF: 29})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if plan.CRF != 29 {
		t.Fatalf("CRF = %d, want 29", plan.CRF)
	}
	if plan.Threads != 6 {
		t.Fatalf("threads = %d, want 6", plan.Threads)
	}
}

func TestArgsPreserveStreamsAndStripMetadata(t *testing.T) {
	plan, _ := BuildPlan(testProfile(), "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, &domain.SearchResult{CRF: 24})
	args := Args(plan)
	want := []string{"-map", "0", "-c:v", "libsvtav1", "-crf", "24", "-threads", "2", "-c:a", "copy", "-c:s", "copy", "-map_metadata", "-1", "-map_chapters", "-1", "/out.mkv"}
	for _, token := range want {
		if !containsArg(args, token) {
			t.Fatalf("Args() = %v, missing %q", args, token)
		}
	}
}

func testProfile() domain.Profile {
	return domain.Profile{
		Video: domain.VideoProfile{
			Codec:       "libsvtav1",
			Preset:      "6",
			PixelFormat: "yuv420p10le",
			CRFMin:      18,
			CRFMax:      40,
			TargetVMAF:  95,
		},
		Audio:     domain.AudioProfile{Mode: domain.StreamPolicyPreserve},
		Subtitles: domain.SubtitleProfile{Mode: domain.StreamPolicyPreserve},
		Metadata:  domain.MetadataPolicy{Mode: domain.MetadataModeStrip},
		Chapters:  domain.ChapterPolicy{Mode: domain.MetadataModeStrip},
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
