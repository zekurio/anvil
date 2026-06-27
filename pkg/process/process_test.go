package process

import (
	"context"
	"testing"
)

func TestOSRunnerCapturesOutput(t *testing.T) {
	result, err := OSRunner{}.Run(context.Background(), Command{
		Name: "sh",
		Args: []string{"-c", "printf hello"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if string(result.Stdout) != "hello" {
		t.Fatalf("stdout = %q, want hello", result.Stdout)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}
}

func TestOSRunnerReportsExitCode(t *testing.T) {
	result, err := OSRunner{}.Run(context.Background(), Command{
		Name: "sh",
		Args: []string{"-c", "printf nope >&2; exit 7"},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want failure")
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", result.ExitCode)
	}
	if string(result.Stderr) != "nope" {
		t.Fatalf("stderr = %q, want nope", result.Stderr)
	}
}
