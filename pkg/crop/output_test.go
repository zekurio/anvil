package crop

import (
	"context"
	"errors"
	"testing"

	"github.com/zekurio/anvil/pkg/process"
)

type outputRunner struct {
	calls   int
	failure error
}

func (r *outputRunner) Run(_ context.Context, command process.Command) (process.Result, error) {
	r.calls++
	result := process.Result{Command: command.ArgsWithName(), Stderr: []byte("crop=1920:800:0:140")}
	if r.calls == 2 {
		return result, r.failure
	}
	return result, nil
}

func TestCropDoesNotHideOutputFailureAfterGoodSample(t *testing.T) {
	for _, failure := range []error{process.ErrOutputCapture, process.ErrOutputLog, context.Canceled} {
		runner := &outputRunner{failure: failure}
		_, err := (FFmpegDetector{Runner: runner}).Detect(context.Background(), "input.mkv")
		if !errors.Is(err, failure) || runner.calls != 2 {
			t.Fatalf("error = %v, calls = %d", err, runner.calls)
		}
	}
}
