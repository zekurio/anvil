package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// TestSearchUsesConfiguredBinaries checks that search routes every ffmpeg and
// ffprobe invocation through the configured executables.
func TestSearchUsesConfiguredBinaries(t *testing.T) {
	root := t.TempDir()
	used := make(map[string]bool)
	failure := errors.New("stop after sample encode")
	runner := runnerFunc(func(_ context.Context, command process.Command) (process.Result, error) {
		used[command.Name] = true
		result := process.Result{Command: command.ArgsWithName()}
		switch {
		case command.Name == "custom-probe" && slices.Contains(command.Args, "-show_format"):
			result.Stdout = []byte(`{"format":{"duration":"2","size":"1000"},"streams":[{"index":0,"codec_type":"video","codec_name":"h264"}]}`)
		case command.Name == "custom-probe":
			result.Stdout = []byte(`{"streams":[{"nb_read_frames":"24"}]}`)
		case command.Name == "custom-ffmpeg" && slices.Contains(command.Args, "copy"):
			path := command.Args[len(command.Args)-1]
			if err := os.WriteFile(path, []byte("sample"), 0o600); err != nil {
				return result, err
			}
		default:
			result.ExitCode, result.Stderr = 1, []byte("stop")
			return result, failure
		}
		return result, nil
	})
	plan := domain.EncodePlan{InputPath: "input.mkv", CRFMin: 20, CRFMax: 22, Metric: domain.QualityMetricVMAF, ForceEncodeOnNoFit: true}
	if _, err := (FFmpeg{Runner: runner, Binary: "custom-ffmpeg", ProbeBinary: "custom-probe"}).Search(context.Background(), plan, root); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	if used["ffmpeg"] || used["ffprobe"] {
		t.Fatalf("used default binaries: %v", used)
	}
	if !used["custom-ffmpeg"] || !used["custom-probe"] {
		t.Fatalf("configured binaries unused: %v", used)
	}
}

// ffmpegSupports reports whether the ffmpeg build lists name in the given
// capability listing, such as -encoders or -filters.
func ffmpegSupports(ctx context.Context, t *testing.T, listing, name string) bool {
	t.Helper()
	result, err := (process.OSRunner{}).Run(ctx, process.Command{Name: "ffmpeg", Args: []string{"-hide_banner", listing}, RequireFullStdout: true})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(result.Stdout), name)
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
			if metric == domain.QualityMetricVMAF && !ffmpegSupports(ctx, t, "-filters", "libvmaf") {
				t.Skip("ffmpeg build has no libvmaf filter")
			}
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
			frames, err := sampleFrames(ctx, runner, "ffprobe", final.OutputPath, 2)
			if err != nil || frames != 36 {
				t.Fatalf("final frames = %d, %v", frames, err)
			}
		})
	}
}

// Run with ANVIL_MEDIA_TEST=1 inside nix develop. x265 defaults to open GOP,
// so copying a sample leaves leading B-frames whose DTS precedes the seek
// point; the extraction must survive the CLI's timestamp fixup.
func TestSearchExtractsOpenGOPSamples(t *testing.T) {
	if os.Getenv("ANVIL_MEDIA_TEST") != "1" {
		t.Skip("set ANVIL_MEDIA_TEST=1 to run real FFmpeg encodes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if !ffmpegSupports(ctx, t, "-encoders", "libx265") {
		t.Skip("ffmpeg build has no libx265 encoder")
	}
	root := t.TempDir()
	input := filepath.Join(root, "opengop.mkv")
	_, err := (process.OSRunner{}).Run(ctx, process.Command{Name: "ffmpeg", Args: []string{
		"-hide_banner", "-nostdin", "-n", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=12",
		"-c:v", "libx265", "-preset", "veryfast", "-x265-params", "keyint=24:min-keyint=24:log-level=error",
		"-pix_fmt", "yuv420p", input,
	}})
	if err != nil {
		t.Fatal(err)
	}
	source, err := (probe.FFProbe{}).Probe(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	profile := domain.Profile{Container: "mkv", Video: domain.VideoProfile{
		Codec: "av1", Accelerator: "software", Preset: "12", BitDepth: 8,
		CRFMin: 30, CRFMax: 32, Samples: 2, SampleDuration: 2 * time.Second,
		Metric: domain.QualityMetricXPSNR, Target: 0, ForceEncodeOnNoFit: true,
		FFmpegArgs: []string{"-svtav1-params", "lp=2"},
	}}
	plan, err := ffmpeg.BuildPlanFromRequest(ffmpeg.BuildPlanRequest{Profile: profile, InputPath: input, OutputPath: filepath.Join(root, "final.mkv"), Probe: &source, Resources: domain.ResourceAllocation{Threads: 2}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (FFmpeg{}).Search(ctx, plan, filepath.Join(root, "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	// The point is that extraction and scoring complete on open-GOP input, not
	// that the already-compressed source fits the savings target.
	if result.SkipVideoEncode || len(result.Candidates) == 0 {
		t.Fatalf("result = %+v", result)
	}
	t.Log(result.RawOutput)
}

// Exercise packet-copy cuts inside reordered H.264 GOPs without the original
// episodes. QSV runs opt in separately because they require an Intel device.
func TestSearchSamplesWithOpenGOPH264(t *testing.T) {
	if os.Getenv("ANVIL_MEDIA_TEST") != "1" && os.Getenv("ANVIL_QSV_TEST") != "1" {
		t.Skip("set ANVIL_MEDIA_TEST=1 or ANVIL_QSV_TEST=1 to run real FFmpeg encodes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if !ffmpegSupports(ctx, t, "-encoders", "libx264") {
		t.Skip("ffmpeg build has no libx264 encoder")
	}
	root := t.TempDir()
	input := filepath.Join(root, "opengop.mkv")
	runner := process.OSRunner{}
	_, err := runner.Run(ctx, process.Command{Name: "ffmpeg", Args: []string{
		"-hide_banner", "-nostdin", "-n", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=24000/1001:duration=12",
		"-c:v", "libx264", "-preset", "veryfast",
		"-x264-params", "open-gop=1:keyint=48:min-keyint=48:scenecut=0:bframes=3",
		"-pix_fmt", "yuv420p", "-threads", "2", input,
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan := domain.EncodePlan{
		InputPath: input, InputVideoCodec: "h264", InputPixelFormat: "yuv420p",
		InputWidth: 320, InputHeight: 240, CropFilter: "crop=320:192:0:24",
		VideoCodec: "libx264", BitDepth: 8, CRF: 28, Preset: "veryfast",
		Threads: 2, Metric: domain.QualityMetricXPSNR,
	}
	refs, err := prepareSamples(ctx, runner, plan, []sampleWindow{
		{offset: 4.25, duration: 2}, {offset: 8.125, duration: 2},
	}, root, tools{})
	if err != nil {
		t.Fatal(err)
	}
	for _, encoder := range []string{"libx264", "hevc_qsv"} {
		for _, depth := range []int{8, 10} {
			t.Run(fmt.Sprintf("%s-%d", encoder, depth), func(t *testing.T) {
				if encoder == "hevc_qsv" && os.Getenv("ANVIL_QSV_TEST") != "1" {
					t.Skip("set ANVIL_QSV_TEST=1 to run QSV sample encodes")
				}
				plan := plan
				plan.VideoCodec, plan.BitDepth = encoder, depth
				if encoder == "hevc_qsv" {
					plan.Accelerator, plan.Preset = "qsv", "veryslow"
				}
				for i, ref := range refs {
					plan.InputPath = ref.path
					plan.OutputPath = filepath.Join(root, fmt.Sprintf("%s-%d-%d.mkv", encoder, depth, i))
					if _, err := runner.Run(ctx, process.Command{Name: "ffmpeg", Args: ffmpeg.SampleArgs(plan)}); err != nil {
						t.Fatal(err)
					}
					frames, err := sampleFrames(ctx, runner, "ffprobe", plan.OutputPath, plan.Threads)
					if err != nil || frames != ref.frames {
						t.Fatalf("sample %d frames = %d, expected %d: %v", i+1, frames, ref.frames, err)
					}
					encoded, err := (probe.FFProbe{}).Probe(ctx, plan.OutputPath)
					if err != nil {
						t.Fatal(err)
					}
					video, ok := domain.PrimaryVideoStream(encoded.Streams)
					wantFormat := "yuv420p"
					if depth == 10 {
						wantFormat = "yuv420p10le"
					}
					if !ok || video.Width != 320 || video.Height != 192 || video.PixelFormat != wantFormat {
						t.Fatalf("encoded video = %+v, expected 320x192 %s", video, wantFormat)
					}
					if _, err := measureQuality(ctx, runner, plan, ref, plan.OutputPath, root, "ffmpeg"); err != nil {
						t.Fatalf("score sample %d: %v", i+1, err)
					}
				}
			})
		}
	}
}

// Run with ANVIL_QSV_TEST=1 and an FFmpeg/driver pair that supports QSV.
func TestQSVSearchWithTimestampGaps(t *testing.T) {
	if os.Getenv("ANVIL_QSV_TEST") != "1" {
		t.Skip("set ANVIL_QSV_TEST=1 to run the QSV timestamp regression")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := t.TempDir()
	input := filepath.Join(root, "gapped.mkv")
	runner := process.OSRunner{}
	// Reproduce the sparse tail left by copying part of a reordered GOP:
	// all 123 frames exist, but the last two timestamps have three-frame gaps.
	_, err := runner.Run(ctx, process.Command{Name: "ffmpeg", Args: []string{
		"-hide_banner", "-nostdin", "-n", "-f", "lavfi", "-i", "testsrc2=size=192x128:rate=24000/1001",
		"-frames:v", "123", "-vf", "setpts='PTS+if(gte(N,121),(N-120)*3/(24000/1001)/TB,0)'",
		"-fps_mode", "passthrough", "-c:v", "ffv1", "-pix_fmt", "yuv420p10le", "-threads", "1", input,
	}})
	if err != nil {
		t.Fatal(err)
	}
	frames, err := sampleFrames(ctx, runner, "ffprobe", input, 4)
	if err != nil || frames != 123 {
		t.Fatalf("source frames = %d, %v", frames, err)
	}
	source, err := (probe.FFProbe{}).Probe(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	profile := domain.Profile{Container: "mkv", Video: domain.VideoProfile{
		Codec: "hevc", Accelerator: "qsv", Preset: "veryslow", BitDepth: 10,
		CRFMin: 24, CRFMax: 24, Samples: 1, Metric: domain.QualityMetricVMAF,
		FFmpegArgs: []string{"-extbrc", "1", "-look_ahead_depth", "40", "-adaptive_i", "1", "-adaptive_b", "1", "-b_strategy", "1", "-bf", "7"},
	}}
	plan, err := ffmpeg.BuildPlanFromRequest(ffmpeg.BuildPlanRequest{Profile: profile, InputPath: input, OutputPath: filepath.Join(root, "final.mkv"), Probe: &source, Resources: domain.ResourceAllocation{Threads: 4}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (FFmpeg{}).Search(ctx, plan, filepath.Join(root, "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	if result.SkipVideoEncode || result.CRF != 24 || len(result.Candidates) != 1 || result.VMAF <= 0 {
		t.Fatalf("result = %+v", result)
	}
	t.Log(result.RawOutput)
}
