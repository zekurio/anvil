package process

import (
	"context"
	"errors"
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

func TestOSRunnerReportsOutputToContextLogger(t *testing.T) {
	logger := &fakeLogger{}
	ctx := WithStep(WithLogger(context.Background(), logger), "encode")
	result, err := OSRunner{}.Run(ctx, Command{
		Name: "sh",
		Args: []string{"-c", "printf hello; printf nope >&2; exit 7"},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want failure")
	}
	if logger.calls != 1 {
		t.Fatalf("logger calls = %d, want 1", logger.calls)
	}
	if logger.step != "encode" {
		t.Fatalf("logger step = %q, want encode", logger.step)
	}
	if string(logger.result.Stdout) != "hello" || string(logger.result.Stderr) != "nope" {
		t.Fatalf("logged result stdout/stderr = %q/%q", logger.result.Stdout, logger.result.Stderr)
	}
	if !errors.Is(logger.err, err) && logger.err.Error() != err.Error() {
		t.Fatalf("logged error = %v, want %v", logger.err, err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", result.ExitCode)
	}
}

type fakeLogger struct {
	calls  int
	step   string
	result Result
	err    error
}

func (f *fakeLogger) LogProcess(ctx context.Context, _ Command, result Result, err error) error {
	f.calls++
	f.step = Step(ctx)
	f.result = result
	f.err = err
	return nil
}
