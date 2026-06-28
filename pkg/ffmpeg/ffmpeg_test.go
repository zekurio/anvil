package ffmpeg

import (
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/marker"
)

func TestBuildPlanUsesSearchCRF(t *testing.T) {
	plan, err := BuildPlan(testProfile(), "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 6}, &domain.SearchResult{CRF: 29}, nil, domain.JobMetadata{})
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
	plan, _ := BuildPlan(testProfile(), "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, &domain.SearchResult{CRF: 24}, nil, domain.JobMetadata{})
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
	for _, pair := range [][2]string{{"-metadata:s:v:0", "anvil.encoded=true"}, {"-metadata:s:v:0", "anvil.profile=default-av1"}, {"-metadata:s:v:0", "anvil.video.crf=24"}} {
		if !containsPair(args, pair[0], pair[1]) {
			t.Fatalf("Args() = %v, missing Anvil marker %v", args, pair)
		}
	}
}

func TestArgsMarksOnlyVideoStreamWithAnvilTags(t *testing.T) {
	plan, err := BuildPlan(testProfile(), "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, &domain.SearchResult{CRF: 24}, &domain.AudioSelection{StreamIndexes: []int{1, 2}}, domain.JobMetadata{
		StreamCleanupDisabled: true,
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	args := Args(plan)
	if !containsPair(args, "-metadata:s:v:0", "anvil.encoded=true") {
		t.Fatalf("Args() = %v, missing video Anvil marker", args)
	}
	for _, unexpected := range []string{"-metadata", "-metadata:s:a:0", "-metadata:s:s:0", "-metadata:s:t:0"} {
		if containsArg(args, unexpected) {
			t.Fatalf("Args() = %v, did not expect %q", args, unexpected)
		}
	}
}

func TestArgsMapsSelectedAudioAndAppliesCrop(t *testing.T) {
	audio := &domain.AudioSelection{StreamIndexes: []int{2, 4}}
	plan, err := BuildPlan(testProfile(), "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, &domain.SearchResult{CRF: 24}, audio, domain.JobMetadata{CropFilter: "crop=1920:800:0:140"})
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

func TestArgsCopiesVideoWhenInputHasCompatibleAnvilMarker(t *testing.T) {
	plan, err := BuildPlan(testProfile(), "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, nil, nil, domain.JobMetadata{
		VideoAlreadyEncoded: true,
		CropFilter:          "crop=1920:800:0:140",
		AnvilTags:           map[string]string{"anvil.encoded": "true", "anvil.video.crf": "29"},
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if !plan.VideoCopy {
		t.Fatal("VideoCopy = false, want true")
	}
	args := Args(plan)
	if !containsPair(args, "-c:v", "copy") {
		t.Fatalf("Args() = %v, missing video copy", args)
	}
	if containsArg(args, "-crf") {
		t.Fatalf("Args() = %v, did not expect CRF for copied video", args)
	}
	if containsArg(args, "-vf") {
		t.Fatalf("Args() = %v, did not expect crop filter for copied video", args)
	}
	if !containsPair(args, "-metadata:s:v:0", "anvil.video.crf=29") {
		t.Fatalf("Args() = %v, missing preserved CRF marker", args)
	}
	if !containsPair(args, "-metadata:s:v:0", "anvil.encoded=true") {
		t.Fatalf("Args() = %v, missing preserved encoded marker", args)
	}
}

func TestArgsCopiesVideoWhenSearchSkipsEncode(t *testing.T) {
	audio := &domain.AudioSelection{StreamIndexes: []int{1}}
	reason := "ab-av1 did not find a CRF satisfying VMAF/size constraints"
	plan, err := BuildPlan(testProfile(), "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, &domain.SearchResult{
		SkipVideoEncode:       true,
		VideoEncodeSkipReason: reason,
	}, audio, domain.JobMetadata{CropFilter: "crop=1920:800:0:140"})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if !plan.VideoCopy {
		t.Fatal("VideoCopy = false, want true")
	}
	if plan.CRF != 0 {
		t.Fatalf("CRF = %d, want 0", plan.CRF)
	}
	args := Args(plan)
	if !containsPair(args, "-c:v", "copy") {
		t.Fatalf("Args() = %v, missing video copy", args)
	}
	for _, unexpected := range []string{"-crf", "-vf"} {
		if containsArg(args, unexpected) {
			t.Fatalf("Args() = %v, did not expect %q for skipped video encode", args, unexpected)
		}
	}
	if !containsPair(args, "-map", "0:1") {
		t.Fatalf("Args() = %v, missing selected audio map", args)
	}
	for _, pair := range [][2]string{
		{"-metadata:s:v:0", marker.TagProcessed + "=true"},
		{"-metadata:s:v:0", marker.TagVideoAction + "=copy"},
		{"-metadata:s:v:0", marker.TagProcessReason + "=" + reason},
	} {
		if !containsPair(args, pair[0], pair[1]) {
			t.Fatalf("Args() = %v, missing metadata %v", args, pair)
		}
	}
	if containsPair(args, "-metadata:s:v:0", marker.TagEncoded+"=true") {
		t.Fatalf("Args() = %v, did not expect encoded marker for skipped video encode", args)
	}
}

func testProfile() domain.Profile {
	return domain.Profile{
		Name: domain.ProfileName("default-av1"),
		Video: domain.VideoProfile{
			Codec:       "libsvtav1",
			Preset:      "6",
			PixelFormat: "yuv420p10le",
			CRFMin:      18,
			CRFMax:      40,
			TargetVMAF:  95,
		},
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
