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
	runner := fakeRunner{stderr: []byte("[Parsed_cropdetect_0] crop=1920:800:0:140")}
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

type fakeRunner struct {
	stderr []byte
}

func (f fakeRunner) Run(_ context.Context, command process.Command) (process.Result, error) {
	return process.Result{Command: command.ArgsWithName(), Stderr: f.stderr}, nil
}

type staticDetector struct {
	result domain.CropResult
}

func (s staticDetector) Detect(context.Context, string) (domain.CropResult, error) {
	return s.result, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
