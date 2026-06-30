package crop

import (
	"context"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
)

func TestParseFilterChoosesMostFrequentCrop(t *testing.T) {
	output := []byte(`
		[Parsed_cropdetect_0] crop=1920:800:0:140
		[Parsed_cropdetect_0] crop=1918:800:2:140
		[Parsed_cropdetect_0] crop=1920:800:0:140
	`)
	if got, want := ParseFilter(output), "crop=1920:800:0:140"; got != want {
		t.Fatalf("ParseFilter() = %q, want %q", got, want)
	}
}

func TestParseFilterBreaksTiesWithLatestCrop(t *testing.T) {
	output := []byte(`
		[Parsed_cropdetect_0] crop=1920:800:0:140
		[Parsed_cropdetect_0] crop=1918:800:2:140
	`)
	if got, want := ParseFilter(output), "crop=1918:800:2:140"; got != want {
		t.Fatalf("ParseFilter() = %q, want %q", got, want)
	}
}

func TestFFmpegDetectorBuildsCommandAndStoresRawData(t *testing.T) {
	runner := &fakeRunner{stderrs: [][]byte{[]byte("[Parsed_cropdetect_0] crop=1920:800:0:140")}}
	result, err := FFmpegDetector{Runner: runner, FrameLimit: 42}.Detect(context.Background(), "/input.mkv")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if got, want := result.Filter, "crop=1920:800:0:140"; got != want {
		t.Fatalf("filter = %q, want %q", got, want)
	}
	if !contains(result.RawCommand, "ffmpeg") || !contains(result.RawCommand, "-frames:v") || !contains(result.RawCommand, "42") {
		t.Fatalf("raw command = %v, want ffmpeg cropdetect frame limit", result.RawCommand)
	}
	if !contains(result.RawCommand, "cropdetect=64:16:0") {
		t.Fatalf("raw command = %v, want HDR-safe cropdetect limit", result.RawCommand)
	}
}

func TestFFmpegDetectorSamplesMultipleOffsets(t *testing.T) {
	runner := &fakeRunner{
		stderrs: [][]byte{
			[]byte("[Parsed_cropdetect_0] crop=1920:864:0:108"),
			[]byte("[Parsed_cropdetect_0] crop=1920:816:0:132"),
			[]byte("[Parsed_cropdetect_0] crop=1920:816:0:132"),
		},
	}
	result, err := FFmpegDetector{
		Runner:      runner,
		FrameLimit:  42,
		SeekOffsets: []string{"", "00:02:00", "00:05:00"},
	}.Detect(context.Background(), "/input.mkv")
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if got, want := result.Filter, "crop=1920:816:0:132"; got != want {
		t.Fatalf("filter = %q, want %q", got, want)
	}
	if len(runner.commands) != 3 {
		t.Fatalf("commands = %v, want three cropdetect samples", runner.commands)
	}
	if contains(runner.commands[0], "-ss") {
		t.Fatalf("first command = %v, did not expect seek offset", runner.commands[0])
	}
	if !contains(runner.commands[1], "-ss") || !contains(runner.commands[1], "00:02:00") {
		t.Fatalf("second command = %v, want seek offset", runner.commands[1])
	}
}

func TestBlockStoresCropInJobMetadata(t *testing.T) {
	block := Block{Detector: staticDetector{result: domain.CropResult{Filter: "crop=1920:800:0:140"}}}
	job := &pipeline.JobContext{InputPath: "/input.mkv"}
	if err := block.Run(context.Background(), job); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if job.Crop == nil || job.Crop.Filter != "crop=1920:800:0:140" {
		t.Fatalf("job crop = %#v, want detected crop", job.Crop)
	}
	if got, want := job.Metadata.CropFilter, "crop=1920:800:0:140"; got != want {
		t.Fatalf("metadata crop = %q, want %q", got, want)
	}
}

func TestBlockSkipsDetectionForAnvilEncodedVideo(t *testing.T) {
	block := Block{Detector: failingDetector{}}
	job := &pipeline.JobContext{
		InputPath: "/input.mkv",
		Metadata: domain.JobMetadata{
			VideoAlreadyEncoded: true,
			CropFilter:          "crop=1920:800:0:140",
		},
	}
	if err := block.Run(context.Background(), job); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if job.Crop == nil || job.Crop.Filter != "crop=1920:800:0:140" {
		t.Fatalf("job crop = %#v, want marker crop", job.Crop)
	}
}

type fakeRunner struct {
	stderrs  [][]byte
	commands [][]string
}

func (f *fakeRunner) Run(_ context.Context, command process.Command) (process.Result, error) {
	index := len(f.commands)
	f.commands = append(f.commands, command.ArgsWithName())
	stderr := []byte(nil)
	if index < len(f.stderrs) {
		stderr = f.stderrs[index]
	} else if len(f.stderrs) > 0 {
		stderr = f.stderrs[len(f.stderrs)-1]
	}
	return process.Result{Command: command.ArgsWithName(), Stderr: stderr}, nil
}

type staticDetector struct {
	result domain.CropResult
}

func (s staticDetector) Detect(context.Context, string) (domain.CropResult, error) {
	return s.result, nil
}

type failingDetector struct{}

func (failingDetector) Detect(context.Context, string) (domain.CropResult, error) {
	panic("detector should not be called")
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
