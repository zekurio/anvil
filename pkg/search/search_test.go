package search

import (
	"context"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
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

type fakeRunner struct {
	stdout []byte
}

func (f fakeRunner) Run(_ context.Context, command process.Command) (process.Result, error) {
	return process.Result{Command: command.ArgsWithName(), Stdout: f.stdout}, nil
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
