package search

import (
	"context"
	"errors"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/process"
)

type outputRunner struct{ err error }

func (r outputRunner) Run(_ context.Context, command process.Command) (process.Result, error) {
	return process.Result{Command: command.ArgsWithName(), ExitCode: 1, Stderr: []byte("Failed to find a suitable crf")}, r.err
}

func TestNoFitDoesNotHideOutputOrContextFailure(t *testing.T) {
	for _, failure := range []error{process.ErrOutputCapture, process.ErrOutputLog, context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			_, err := (ABAV1{Runner: outputRunner{err: errors.Join(errors.New("process exited"), failure)}}).Search(context.Background(), domain.EncodePlan{InputPath: "input.mkv"}, "")
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v, want %v", err, failure)
			}
		})
	}
}
