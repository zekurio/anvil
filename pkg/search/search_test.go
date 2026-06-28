package search

import (
	"context"
	"errors"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
)

func TestParseResultUsesLastCRFAndVMAF(t *testing.T) {
	result, err := ParseResult([]byte("sample crf 22 vmaf 94.1\nselected CRF: 27 VMAF: 95.3\n"))
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if result.CRF != 27 {
		t.Fatalf("CRF = %d, want 27", result.CRF)
	}
	if result.VMAF != 95.3 {
		t.Fatalf("VMAF = %f, want 95.3", result.VMAF)
	}
}

func TestABAV1BuildsCommandAndParsesOutput(t *testing.T) {
	runner := fakeRunner{stdout: []byte("crf 30 vmaf 96.2")}
	result, err := ABAV1{Runner: runner}.Search(context.Background(), domain.EncodePlan{
		InputPath:         "/input.mkv",
		VideoCodec:        "libsvtav1",
		Preset:            "6",
		CRFMin:            18,
		CRFMax:            40,
		TargetVMAF:        95,
		Threads:           4,
		PixelFormat:       "yuv420p10le",
		MinSavingsPercent: 20,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if result.CRF != 30 {
		t.Fatalf("CRF = %d, want 30", result.CRF)
	}
	if len(result.RawCommand) == 0 || result.RawCommand[0] != "ab-av1" {
		t.Fatalf("raw command = %v, want ab-av1 command", result.RawCommand)
	}
	if !containsArg(result.RawCommand, "--min-vmaf") {
		t.Fatalf("raw command = %v, want --min-vmaf", result.RawCommand)
	}
	if !containsPair(result.RawCommand, "--max-encoded-percent", "80") {
		t.Fatalf("raw command = %v, want --max-encoded-percent 80", result.RawCommand)
	}
	if containsArg(result.RawCommand, "--threads") {
		t.Fatalf("raw command = %v, did not expect unsupported --threads", result.RawCommand)
	}
	if !containsArg(result.RawCommand, "threads=4") {
		t.Fatalf("raw command = %v, want ffmpeg encoder thread budget", result.RawCommand)
	}
}

func TestSearchArgsIncludesCropFilter(t *testing.T) {
	args := SearchArgs(domain.EncodePlan{
		InputPath:   "/input.mkv",
		CRFMin:      18,
		CRFMax:      40,
		CropFilter:  "crop=1920:800:0:140",
		TargetVMAF:  95,
		VideoCodec:  "libsvtav1",
		PixelFormat: "yuv420p10le",
	})
	if !containsPair(args, "--vfilter", "crop=1920:800:0:140") {
		t.Fatalf("SearchArgs() = %v, want crop vfilter", args)
	}
}

func TestSearchArgsMapsMinimumSavingsToMaxEncodedPercent(t *testing.T) {
	args := SearchArgs(domain.EncodePlan{
		InputPath:         "/input.mkv",
		CRFMin:            18,
		CRFMax:            40,
		MinSavingsPercent: 12.5,
	})
	if !containsPair(args, "--max-encoded-percent", "87.5") {
		t.Fatalf("SearchArgs() = %v, want --max-encoded-percent 87.5", args)
	}
}

func TestSearchArgsIncludesCustomABAV1Args(t *testing.T) {
	args := SearchArgs(domain.EncodePlan{
		InputPath: "/input.mkv",
		CRFMin:    18,
		CRFMax:    40,
		ABAV1Args: []string{
			"--enc", "lookahead=120",
		},
	})
	if !containsPair(args, "--enc", "lookahead=120") {
		t.Fatalf("SearchArgs() = %v, want custom ab-av1 args", args)
	}
}

func TestBlockSearchPlanUsesDolbyVisionOverride(t *testing.T) {
	searcher := captureSearcher{result: domain.SearchResult{CRF: 24}}
	job := &pipeline.JobContext{
		InputPath: "/input.mkv",
		Profile: domain.Profile{
			Video: domain.VideoProfile{
				Codec:     "libsvtav1",
				Preset:    "6",
				CRFMin:    18,
				CRFMax:    40,
				ABAV1Args: []string{"--enc", "normal=1"},
				DolbyVision: domain.DolbyVisionProfile{
					Mode:      domain.DolbyVisionModeAuto,
					Codec:     "hevc_qsv",
					Preset:    "medium",
					ABAV1Args: []string{"--enc", "low_power=1"},
				},
			},
		},
		Metadata: domain.JobMetadata{HDR: domain.HDRMetadata{DolbyVisionEncoderSelected: true}},
	}
	if err := (Block{Searcher: &searcher}).Run(context.Background(), job); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := searcher.plan.VideoCodec, "hevc_qsv"; got != want {
		t.Fatalf("search plan codec = %q, want %q", got, want)
	}
	if got, want := searcher.plan.Preset, "medium"; got != want {
		t.Fatalf("search plan preset = %q, want %q", got, want)
	}
	if got, want := searcher.plan.ABAV1Args, []string{"--enc", "normal=1", "--enc", "low_power=1"}; !sameStrings(got, want) {
		t.Fatalf("search plan ABAV1Args = %v, want %v", got, want)
	}
}

func TestParseResultReturnsSkipForNoSuitableCRF(t *testing.T) {
	result, err := ParseResult([]byte("crf 18 vmaf 94.7 (103%)\nError: Failed to find a suitable crf\n"))
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if !result.SkipVideoEncode {
		t.Fatal("SkipVideoEncode = false, want true")
	}
	if result.CRF != 0 {
		t.Fatalf("CRF = %d, want 0", result.CRF)
	}
	if result.VideoEncodeSkipReason == "" {
		t.Fatal("VideoEncodeSkipReason is empty")
	}
}

func TestABAV1ConvertsExpectedNoSuitableCRFErrorToSkip(t *testing.T) {
	runner := fakeRunner{
		stdout: []byte("crf 18 vmaf 94.7 (103%)\n"),
		stderr: []byte("Error: Failed to find a suitable crf\n"),
		err:    errors.New("exit status 1"),
	}
	result, err := ABAV1{Runner: runner}.Search(context.Background(), domain.EncodePlan{
		InputPath: "/input.mkv",
		CRFMin:    18,
		CRFMax:    40,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if !result.SkipVideoEncode {
		t.Fatal("SkipVideoEncode = false, want true")
	}
	if result.RawOutput == "" || len(result.RawCommand) == 0 {
		t.Fatalf("result = %#v, want captured output and command", result)
	}
}

func TestABAV1KeepsUnrelatedFailuresFatal(t *testing.T) {
	runner := fakeRunner{
		stderr: []byte("Error: Invalid --min-crf & --max-crf\n"),
		err:    errors.New("exit status 2"),
	}
	_, err := ABAV1{Runner: runner}.Search(context.Background(), domain.EncodePlan{
		InputPath: "/input.mkv",
		CRFMin:    40,
		CRFMax:    18,
	})
	if err == nil {
		t.Fatal("Search() error = nil, want fatal error")
	}
}

func TestBlockSkipsSearchForAnvilEncodedVideo(t *testing.T) {
	job := &pipeline.JobContext{
		InputPath: "/input.mkv",
		Metadata:  domain.JobMetadata{VideoAlreadyEncoded: true},
	}
	if err := (Block{Searcher: failingSearcher{}}).Run(context.Background(), job); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if job.Search == nil || job.Search.RawOutput == "" {
		t.Fatalf("job search = %#v, want skipped search result", job.Search)
	}
}

type fakeRunner struct {
	stdout []byte
	stderr []byte
	err    error
}

func (f fakeRunner) Run(_ context.Context, command process.Command) (process.Result, error) {
	exitCode := 0
	if f.err != nil {
		exitCode = 1
	}
	return process.Result{
		Command:  command.ArgsWithName(),
		Stdout:   f.stdout,
		Stderr:   f.stderr,
		ExitCode: exitCode,
	}, f.err
}

type failingSearcher struct{}

func (failingSearcher) Search(context.Context, domain.EncodePlan) (domain.SearchResult, error) {
	panic("searcher should not be called")
}

type captureSearcher struct {
	plan   domain.EncodePlan
	result domain.SearchResult
}

func (s *captureSearcher) Search(_ context.Context, plan domain.EncodePlan) (domain.SearchResult, error) {
	s.plan = plan
	return s.result, nil
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

func sameStrings(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
