package ffmpeg

import (
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestBuildPlanUsesSearchCRF(t *testing.T) {
	plan, err := BuildPlan(testProfile(), "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 6}, &domain.SearchResult{CRF: 29}, nil, "")
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
	plan, _ := BuildPlan(testProfile(), "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, &domain.SearchResult{CRF: 24}, nil, "")
	args := Args(plan)
	want := []string{"-c:v", "libsvtav1", "-crf", "24", "-threads", "2", "-c:a", "copy", "-c:s", "copy", "-map_metadata", "-1", "-map_chapters", "-1", "/out.mkv"}
	for _, token := range want {
		if !containsArg(args, token) {
			t.Fatalf("Args() = %v, missing %q", args, token)
		}
	}
	for _, pair := range [][2]string{{"-map", "0:v?"}, {"-map", "0:a?"}, {"-map", "0:s?"}} {
		if !containsPair(args, pair[0], pair[1]) {
			t.Fatalf("Args() = %v, missing %v", args, pair)
		}
	}
	if containsPair(args, "-map", "0") {
		t.Fatalf("Args() = %v, did not expect global stream map", args)
	}
}

func TestArgsMapsSelectedAudioAndAppliesCrop(t *testing.T) {
	audio := &domain.AudioSelection{StreamIndexes: []int{2, 4}}
	plan, err := BuildPlan(testProfile(), "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, &domain.SearchResult{CRF: 24}, audio, "crop=1920:800:0:140")
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if !plan.AudioSelectionApplied {
		t.Fatal("AudioSelectionApplied = false, want true")
	}
	args := Args(plan)
	for _, pair := range [][2]string{{"-map", "0:2"}, {"-map", "0:4"}, {"-vf", "crop=1920:800:0:140"}} {
		if !containsPair(args, pair[0], pair[1]) {
			t.Fatalf("Args() = %v, missing %v", args, pair)
		}
	}
	if containsPair(args, "-map", "0:a?") {
		t.Fatalf("Args() = %v, did not expect all-audio stream map", args)
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

func containsPair(args []string, key string, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}
