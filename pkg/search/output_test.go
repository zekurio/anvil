package search

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/process"
)

// Samples are extracted and probed successfully, so the search reaches the
// candidate encode. A lost output or a cancelled context there must surface as
// an error instead of being folded into a forced or skipped no-fit result.
func TestNoFitDoesNotHideOutputOrContextFailure(t *testing.T) {
	for _, failure := range []error{process.ErrOutputCapture, process.ErrOutputLog, context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			encodeErr := errors.Join(errors.New("process exited"), failure)
			runner := runnerFunc(func(_ context.Context, command process.Command) (process.Result, error) {
				result := process.Result{Command: command.ArgsWithName()}
				switch {
				case command.Name == "ffprobe" && slices.Contains(command.Args, "-show_format"):
					result.Stdout = []byte(`{"format":{"duration":"2","size":"1000"},"streams":[{"index":1,"codec_type":"video","codec_name":"h264"}]}`)
				case command.Name == "ffprobe":
					result.Stdout = []byte(`{"streams":[{"nb_read_frames":"24"}]}`)
				case slices.Contains(command.Args, "copy"):
					path := command.Args[len(command.Args)-1]
					if err := os.WriteFile(path, []byte("sample"), 0o600); err != nil {
						return result, err
					}
				default:
					result.ExitCode, result.Stderr = 1, []byte("sample process failed")
					return result, encodeErr
				}
				return result, nil
			})
			plan := domain.EncodePlan{InputPath: "input.mkv", CRFMin: 20, CRFMax: 22, Metric: domain.QualityMetricVMAF, ForceEncodeOnNoFit: true}
			result, err := (FFmpeg{Runner: runner}).Search(context.Background(), plan, t.TempDir())
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v, want %v", err, failure)
			}
			if result.SkipVideoEncode || result.ForcedVideoEncodeReason != "" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}
