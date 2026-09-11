package search

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/ffmpeg"
	"github.com/zekurio/anvil/pkg/probe"
	"github.com/zekurio/anvil/pkg/process"
)

type runnerFunc func(context.Context, process.Command) (process.Result, error)

func (f runnerFunc) Run(ctx context.Context, command process.Command) (process.Result, error) {
	return f(ctx, command)
}

func TestFailedCandidateCleansOnlyItsScratch(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep")
	if err := os.WriteFile(keep, []byte("other attempt"), 0o600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("encoder failed")
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
			return result, failure
		}
		return result, nil
	})
	_, err := (FFmpeg{Runner: runner}).Search(context.Background(), domain.EncodePlan{InputPath: "input.mkv", CRFMin: 20, CRFMax: 22, Metric: domain.QualityMetricVMAF, ForceEncodeOnNoFit: true}, root)
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "keep" {
		t.Fatalf("scratch = %v, %v", entries, err)
	}
}

// Run with ANVIL_MEDIA_TEST=1 inside nix develop. Uses synthetic media only.
func TestNativeSearchWithFFmpeg(t *testing.T) {
	if os.Getenv("ANVIL_MEDIA_TEST") != "1" {
		t.Skip("set ANVIL_MEDIA_TEST=1 to run real FFmpeg encodes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := filepath.Join(t.TempDir(), "space ' [clips]")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "source.mkv")
	runner := process.OSRunner{}
	_, err := runner.Run(ctx, process.Command{Name: "ffmpeg", Args: []string{
		"-hide_banner", "-nostdin", "-n", "-f", "lavfi", "-i", "testsrc2=size=192x128:rate=12:duration=3",
		"-f", "lavfi", "-i", "sine=duration=3", "-map", "1:a", "-map", "0:v", "-c:a", "pcm_s16le", "-c:v", "ffv1", "-threads", "1", input,
	}})
	if err != nil {
		t.Fatal(err)
	}
	source, err := (probe.FFProbe{}).Probe(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, metric := range []domain.QualityMetric{domain.QualityMetricVMAF, domain.QualityMetricXPSNR} {
		t.Run(string(metric), func(t *testing.T) {
			profile := domain.Profile{Container: "mkv", Video: domain.VideoProfile{Codec: "av1", Accelerator: "software", Preset: "12", BitDepth: 10, CRFMin: 22, CRFMax: 24, Samples: 2, SampleDuration: 500 * time.Millisecond, Metric: metric, Target: 0, FFmpegArgs: []string{"-svtav1-params", "lp=2"}}, Crop: domain.CropPolicy{MinWidth: 128, MinHeight: 64, MinRetainedAreaPercent: 70, RequiredAlignment: 2}}
			request := ffmpeg.BuildPlanRequest{Profile: profile, InputPath: input, OutputPath: filepath.Join(root, string(metric)+".mkv"), Probe: &source, Resources: domain.ResourceAllocation{Threads: 2}, Metadata: domain.JobMetadata{CropFilter: "crop=160:128:16:0"}}
			plan, err := ffmpeg.BuildPlanFromRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			scratch := filepath.Join(root, "scratch")
			result, err := (FFmpeg{}).Search(ctx, plan, scratch)
			if err != nil {
				t.Fatal(err)
			}
			if result.SkipVideoEncode || result.CRF != 24 || len(result.Candidates) == 0 {
				t.Fatalf("result = %+v", result)
			}
			if _, err := json.Marshal(result); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s", result.RawOutput)
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 0 {
				t.Fatalf("scratch not cleaned: %v, %v", entries, err)
			}
			request.Search = &result
			final, err := ffmpeg.BuildPlanFromRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (ffmpeg.Encoder{}).Encode(ctx, final); err != nil {
				t.Fatal(err)
			}
			frames, err := sampleFrames(ctx, runner, final.OutputPath, 2)
			if err != nil || frames != 36 {
				t.Fatalf("final frames = %d, %v", frames, err)
			}
		})
	}
}
