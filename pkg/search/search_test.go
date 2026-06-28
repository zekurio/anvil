package search

import (
	"context"
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
		InputPath:   "/input.mkv",
		VideoCodec:  "libsvtav1",
		Preset:      "6",
		CRFMin:      18,
		CRFMax:      40,
		TargetVMAF:  95,
		Threads:     4,
		PixelFormat: "yuv420p10le",
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
}

func (f fakeRunner) Run(_ context.Context, command process.Command) (process.Result, error) {
	return process.Result{Command: command.ArgsWithName(), Stdout: f.stdout}, nil
}

type failingSearcher struct{}

func (failingSearcher) Search(context.Context, domain.EncodePlan) (domain.SearchResult, error) {
	panic("searcher should not be called")
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
