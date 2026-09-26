package search

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/process"
)

func containsPair(args []string, first, second string) bool {
	for i := range args[:max(len(args)-1, 0)] {
		if args[i] == first && args[i+1] == second {
			return true
		}
	}
	return false
}

func TestCopySampleArgsAllowsTimestampFixup(t *testing.T) {
	args := copySampleArgs(domain.EncodePlan{InputPath: "input.mkv", VideoStreamIndex: 1}, sampleWindow{offset: 4, duration: 3}, "out.mkv")
	// -xerror turns the CLI's non-monotonic DTS fixup into a fatal error on
	// open-GOP sources, which is most HEVC content.
	if slices.Contains(args, "-xerror") {
		t.Fatalf("args = %v", args)
	}
	if !containsPair(args, "-c:v", "copy") || !containsPair(args, "-avoid_negative_ts", "make_zero") {
		t.Fatalf("args = %v", args)
	}
}

func framesRunner(stdout, stderr string, err error) runnerFunc {
	return func(_ context.Context, command process.Command) (process.Result, error) {
		if !command.RequireFullStdout {
			return process.Result{}, errors.New("frame count must capture full stdout")
		}
		return process.Result{Command: command.ArgsWithName(), Stdout: []byte(stdout), Stderr: []byte(stderr)}, err
	}
}

func TestSampleFramesIgnoresDecoderNoise(t *testing.T) {
	runner := framesRunner(`{"streams":[{"nb_read_frames":"82"}]}`, "[h264 @ 0x1] mmco: unref short failure\n", nil)
	frames, err := sampleFrames(context.Background(), runner, "ffprobe", "sample.mkv", 2)
	if err != nil || frames != 82 {
		t.Fatalf("frames = %d, %v", frames, err)
	}
}

func TestSampleFramesRejectsUnusableProbes(t *testing.T) {
	failure := errors.New("ffprobe failed")
	for name, runner := range map[string]runnerFunc{
		"process":     framesRunner(`{"streams":[{"nb_read_frames":"82"}]}`, "", failure),
		"invalidJSON": framesRunner("not json", "", nil),
		"noFrames":    framesRunner(`{"streams":[{"nb_read_frames":"0"}]}`, "", nil),
		"twoStreams":  framesRunner(`{"streams":[{"nb_read_frames":"82"},{"nb_read_frames":"82"}]}`, "", nil),
	} {
		t.Run(name, func(t *testing.T) {
			frames, err := sampleFrames(context.Background(), runner, "ffprobe", "sample.mkv", 2)
			if err == nil || frames != 0 {
				t.Fatalf("frames = %d, %v", frames, err)
			}
		})
	}
}

func TestCandidateRejectsFrameMismatchBeforeScoring(t *testing.T) {
	for _, frames := range []int{23, 25, 26, 27} {
		t.Run(fmt.Sprint(frames), func(t *testing.T) {
			scored := false
			runner := runnerFunc(func(_ context.Context, command process.Command) (process.Result, error) {
				var result process.Result
				switch {
				case command.Name == "ffprobe":
					result.Stdout = fmt.Appendf(nil, `{"streams":[{"nb_read_frames":"%d"}]}`, frames)
				case slices.Contains(command.Args, "-filter_complex"):
					scored = true
				default:
					if err := os.WriteFile(command.Args[len(command.Args)-1], []byte("encoded"), 0o600); err != nil {
						return result, err
					}
				}
				return result, nil
			})
			refs := []sample{{path: "reference.mkv", frames: 24, bytes: 100}}
			_, err := measureCandidate(context.Background(), runner, domain.EncodePlan{}, refs, t.TempDir(), 28, tools{})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("has %d frames, expected 24", frames)) || scored {
				t.Fatalf("error = %v, scored = %v", err, scored)
			}
		})
	}
}
