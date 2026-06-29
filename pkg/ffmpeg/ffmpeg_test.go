package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/marker"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
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
	if got, want := plan.TrackTitleMode, domain.TrackTitleModeStrip; got != want {
		t.Fatalf("TrackTitleMode = %q, want %q", got, want)
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
	if !containsPair(args, "-metadata:s", "title=") {
		t.Fatalf("Args() = %v, missing stream title strip", args)
	}
	if containsPair(args, "-map", "0") {
		t.Fatalf("Args() = %v, did not expect global stream map", args)
	}
	for _, pair := range [][2]string{{"-metadata:s:v:0", "anvil.processed=true"}, {"-metadata:s:v:0", "anvil.encoded=true"}, {"-metadata:s:v:0", "anvil.profile=default-av1"}, {"-metadata:s:v:0", "anvil.video.action=encode"}, {"-metadata:s:v:0", "anvil.video.crf=24"}} {
		if !containsPair(args, pair[0], pair[1]) {
			t.Fatalf("Args() = %v, missing Anvil marker %v", args, pair)
		}
	}
}

func TestArgsCanPreserveTrackTitles(t *testing.T) {
	profile := testProfile()
	profile.Metadata.TrackTitles = domain.TrackTitleModePreserve
	plan, err := BuildPlan(profile, "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, &domain.SearchResult{CRF: 24}, nil, domain.JobMetadata{})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	args := Args(plan)
	if containsPair(args, "-metadata:s", "title=") {
		t.Fatalf("Args() = %v, did not expect stream title strip", args)
	}
	if containsPair(args, "-metadata:s:a", "title=Audio") {
		t.Fatalf("Args() = %v, did not expect standardized audio title", args)
	}
}

func TestArgsCanStandardizeTrackTitles(t *testing.T) {
	profile := testProfile()
	profile.Metadata.TrackTitles = domain.TrackTitleModeStandardize
	plan, err := BuildPlan(profile, "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, &domain.SearchResult{CRF: 24}, nil, domain.JobMetadata{})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	args := Args(plan)
	for _, pair := range [][2]string{
		{"-metadata:s:v", "title=Video"},
		{"-metadata:s:a", "title=Audio"},
		{"-metadata:s:s", "title=Subtitle"},
	} {
		if !containsPair(args, pair[0], pair[1]) {
			t.Fatalf("Args() = %v, missing %v", args, pair)
		}
	}
}

func TestArgsStandardizesTrackTitlesFromProbeStreams(t *testing.T) {
	profile := testProfile()
	profile.Metadata.TrackTitles = domain.TrackTitleModeStandardize
	audio := &domain.AudioSelection{StreamIndexes: []int{2, 1}}
	probe := &domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 0, Type: "video", Codec: "hevc", Width: 1920, Height: 800, ColorTransfer: "smpte2084"},
		{Index: 1, Type: "audio", Codec: "eac3", Language: "eng", Channels: 6, BitRate: 640000},
		{Index: 2, Type: "audio", Codec: "aac", Language: "jpn", Channels: 2, BitRate: 128000},
		{Index: 3, Type: "subtitle", Codec: "hdmv_pgs_subtitle", Language: "eng", Disposition: map[string]bool{"forced": true}},
		{Index: 4, Type: "subtitle", Codec: "subrip", Language: "deu", Disposition: map[string]bool{"hearing_impaired": true}},
	}}
	plan, err := BuildPlanWithProbe(profile, "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, &domain.SearchResult{CRF: 24}, audio, domain.JobMetadata{}, probe)
	if err != nil {
		t.Fatalf("BuildPlanWithProbe() error = %v", err)
	}
	args := Args(plan)
	for _, pair := range [][2]string{
		{"-metadata:s", "title="},
		{"-metadata:s:v:0", "title=1080p HDR10 AV1"},
		{"-metadata:s:a:0", "title=Japanese AAC Stereo 128 kb/s"},
		{"-metadata:s:a:1", "title=English E-AC-3 5.1 640 kb/s"},
		{"-metadata:s:s:0", "title=English Forced PGS Subtitle"},
		{"-metadata:s:s:1", "title=German Full SDH SRT Subtitle"},
	} {
		if !containsPair(args, pair[0], pair[1]) {
			t.Fatalf("Args() = %v, missing %v", args, pair)
		}
	}
	if containsPair(args, "-metadata:s:a", "title=Audio") {
		t.Fatalf("Args() = %v, did not expect type-wide generic audio title", args)
	}
}

func TestCodecLabelUsesBitstreamNameForEncoderNames(t *testing.T) {
	tests := map[string]string{
		"av1_qsv":     "AV1",
		"av1_nvenc":   "AV1",
		"libsvtav1":   "AV1",
		"h264_qsv":    "H.264",
		"hevc_vaapi":  "HEVC",
		"libx265":     "HEVC",
		"mystery_enc": "mystery_enc",
	}

	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			if got := codecLabel(input); got != want {
				t.Fatalf("codecLabel(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestResolutionLabelUsesStandardBuckets(t *testing.T) {
	tests := map[string]struct {
		stream domain.MediaStream
		want   string
	}{
		"cropped_1080p_widescreen": {stream: domain.MediaStream{Width: 1920, Height: 800}, want: "1080p"},
		"tall_1440_width_1080p":    {stream: domain.MediaStream{Width: 1440, Height: 1072}, want: "1080p"},
		"720p":                     {stream: domain.MediaStream{Width: 1280, Height: 720}, want: "720p"},
		"cropped_2160p_widescreen": {stream: domain.MediaStream{Width: 3840, Height: 1600}, want: "2160p"},
		"height_only":              {stream: domain.MediaStream{Height: 1072}, want: "1080p"},
		"missing_dimensions":       {stream: domain.MediaStream{}, want: ""},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := resolutionLabel(tt.stream); got != tt.want {
				t.Fatalf("resolutionLabel(%+v) = %q, want %q", tt.stream, got, tt.want)
			}
		})
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

func TestArgsIncludesCustomFFmpegArgs(t *testing.T) {
	profile := testProfile()
	profile.Video.FFmpegArgs = []string{"-svtav1-params", "film-grain=8"}
	plan, err := BuildPlan(profile, "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, &domain.SearchResult{CRF: 24}, nil, domain.JobMetadata{})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	args := Args(plan)
	if !containsPair(args, "-svtav1-params", "film-grain=8") {
		t.Fatalf("Args() = %v, missing custom ffmpeg args", args)
	}
}

func TestBuildPlanUsesDolbyVisionEncoderOverride(t *testing.T) {
	profile := testProfile()
	profile.Video.FFmpegArgs = []string{"-base", "1"}
	profile.Video.DolbyVision = domain.DolbyVisionProfile{
		Mode:        domain.DolbyVisionModeAuto,
		Codec:       "hevc_qsv",
		Preset:      "medium",
		PixelFormat: "p010le",
		FFmpegArgs:  []string{"-global_quality", "24"},
	}
	plan, err := BuildPlan(profile, "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, &domain.SearchResult{CRF: 24}, nil, domain.JobMetadata{
		HDR: domain.HDRMetadata{
			DolbyVision:                &domain.DolbyVisionMetadata{Profile: 8},
			DolbyVisionToolAvailable:   true,
			DolbyVisionEncoderSelected: true,
		},
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if got, want := plan.VideoCodec, "hevc_qsv"; got != want {
		t.Fatalf("VideoCodec = %q, want %q", got, want)
	}
	if got, want := plan.VideoSource, domain.VideoSourceDolbyVision; got != want {
		t.Fatalf("VideoSource = %q, want %q", got, want)
	}
	args := Args(plan)
	for _, pair := range [][2]string{
		{"-c:v", "hevc_qsv"},
		{"-preset", "medium"},
		{"-pix_fmt", "p010le"},
		{"-base", "1"},
		{"-global_quality", "24"},
	} {
		if !containsPair(args, pair[0], pair[1]) {
			t.Fatalf("Args() = %v, missing %v", args, pair)
		}
	}
}

func TestDolbyVisionBlockRepairsHEVCMKVOutput(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.mkv")
	outputPath := filepath.Join(dir, "output.mkv")
	if err := os.WriteFile(inputPath, []byte("input"), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}
	if err := os.WriteFile(outputPath, []byte("encoded"), 0o600); err != nil {
		t.Fatalf("write output: %v", err)
	}
	runner := &doviFakeRunner{}
	job := &pipeline.JobContext{
		InputPath:  inputPath,
		OutputPath: outputPath,
		StagingDir: dir,
		Profile: domain.Profile{
			Video: domain.VideoProfile{
				DolbyVision: domain.DolbyVisionProfile{
					Mode:            domain.DolbyVisionModeAuto,
					Codec:           "hevc_qsv",
					RemoveHDR10Plus: true,
				},
			},
		},
		Metadata: domain.JobMetadata{HDR: domain.HDRMetadata{
			DolbyVision:                &domain.DolbyVisionMetadata{Profile: 8},
			DolbyVisionEncoderSelected: true,
		}},
		EncodePlan: &domain.EncodePlan{VideoCodec: "hevc_qsv"},
	}
	if err := (DolbyVisionBlock{Runner: runner}).Run(context.Background(), job); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read fixed output: %v", err)
	}
	if string(got) != "fixed-mkv" {
		t.Fatalf("output content = %q, want fixed-mkv", got)
	}
	if !runner.hasCommand("dovi_tool", "--drop-hdr10plus", "--crop", "--mode", "2", "extract-rpu") {
		t.Fatalf("commands = %v, want dovi_tool extract-rpu with crop/mode/drop", runner.commands)
	}
	if !runner.hasCommand("mkvextract", "tracks", outputPath) {
		t.Fatalf("commands = %v, want mkvextract tracks", runner.commands)
	}
	if !runner.hasCommand("dovi_tool", "--drop-hdr10plus", "--crop", "--mode", "2", "inject-rpu") {
		t.Fatalf("commands = %v, want dovi_tool inject-rpu with crop/mode/drop", runner.commands)
	}
	if !runner.hasCommand("mkvmerge", "--default-duration", "0:23.976fps", "--fix-bitstream-timing-information", "0") {
		t.Fatalf("commands = %v, want mkvmerge fps fix", runner.commands)
	}
}

func TestDolbyVisionBlockNoopsForNonDolbyVisionJobs(t *testing.T) {
	runner := &doviFakeRunner{}
	err := (DolbyVisionBlock{Runner: runner}).Run(context.Background(), &pipeline.JobContext{
		EncodePlan: &domain.EncodePlan{VideoCodec: "libsvtav1"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("commands = %v, want none", runner.commands)
	}
}

func TestArgsCopiesVideoWhenInputHasCompatibleAnvilMarker(t *testing.T) {
	plan, err := BuildPlan(testProfile(), "/in.mkv", "/out.mkv", domain.ResourceAllocation{Threads: 2}, nil, nil, domain.JobMetadata{
		VideoAlreadyEncoded: true,
		CropFilter:          "crop=1920:800:0:140",
		AnvilTags: map[string]string{
			"anvil.encoded":            "true",
			"anvil.profile":            "default-av1",
			"anvil.video.codec":        "libsvtav1",
			"anvil.video.pixel_format": "yuv420p10le",
			"anvil.video.crf":          "29",
		},
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
	if !containsPair(args, "-metadata:s:v:0", marker.TagVideoAction+"=copy") {
		t.Fatalf("Args() = %v, missing video copy marker", args)
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

type doviFakeRunner struct {
	commands [][]string
}

func (r *doviFakeRunner) Run(_ context.Context, command process.Command) (process.Result, error) {
	args := command.ArgsWithName()
	r.commands = append(r.commands, args)
	switch filepath.Base(command.Name) {
	case "dovi_tool":
		if output := argValue(command.Args, "-o"); output != "" {
			if err := os.WriteFile(output, []byte("rpu"), 0o600); err != nil {
				return process.Result{Command: args}, err
			}
		}
		if output := argValue(command.Args, "--output"); output != "" {
			if err := os.WriteFile(output, []byte("fixed-hevc"), 0o600); err != nil {
				return process.Result{Command: args}, err
			}
		}
	case "mkvextract":
		for _, arg := range command.Args {
			if _, path, ok := strings.Cut(arg, "0:"); ok {
				if err := os.WriteFile(path, []byte("converted-hevc"), 0o600); err != nil {
					return process.Result{Command: args}, err
				}
			}
		}
	case "mkvinfo":
		return process.Result{Command: args, Stdout: []byte("Default duration: 23.976 frames/fields per second")}, nil
	case "mkvmerge":
		output := argValue(command.Args, "-o")
		if output != "" {
			if err := os.WriteFile(output, []byte("fixed-mkv"), 0o600); err != nil {
				return process.Result{Command: args}, err
			}
		}
	}
	return process.Result{Command: args}, nil
}

func (r *doviFakeRunner) hasCommand(name string, tokens ...string) bool {
	for _, command := range r.commands {
		if len(command) == 0 || filepath.Base(command[0]) != name {
			continue
		}
		if containsAll(command, tokens...) {
			return true
		}
	}
	return false
}

func containsAll(values []string, tokens ...string) bool {
	for _, token := range tokens {
		if !containsArg(values, token) {
			return false
		}
	}
	return true
}

func argValue(args []string, name string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}
