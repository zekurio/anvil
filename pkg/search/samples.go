package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/ffmpeg"
	"github.com/zekurio/anvil/pkg/process"
)

type sampleWindow struct{ offset, duration float64 }
type sample struct {
	path   string
	bytes  int64
	frames int
}

func sampleWindows(duration float64, count int, length time.Duration) ([]sampleWindow, error) {
	if math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		return nil, errors.New("search requires a finite, positive input duration")
	}
	if count < 0 || length < 0 {
		return nil, errors.New("search sample count and duration must be non-negative")
	}
	if length == 0 {
		length = 20 * time.Second
	}
	if count == 0 {
		inferred := math.Ceil(duration / (12 * 60))
		if inferred > 10000 {
			return nil, errors.New("search sample count exceeds 10000")
		}
		count = max(1, int(inferred))
	}
	seconds := length.Seconds()
	if float64(count)*seconds >= duration*0.85 {
		return []sampleWindow{{duration: duration}}, nil
	}
	// Guard absurd configurations before allocating or creating scratch files.
	if count > 10000 {
		return nil, errors.New("search sample count exceeds 10000")
	}
	windows := make([]sampleWindow, count)
	for i := range windows {
		windows[i] = sampleWindow{offset: (float64(i)+0.5)*duration/float64(count) - seconds/2, duration: seconds}
	}
	return windows, nil
}

func copySampleArgs(plan domain.EncodePlan, window sampleWindow, output string) []string {
	// Copy from a seekable keyframe, retaining compressed bytes for the savings
	// estimate. Clips may include keyframe preroll and partial GOPs; reference
	// counting, candidate encoding, and scoring must use the same decoder.
	// No -xerror here: seeking into an open GOP — x265's default — leaves the
	// leading B-frames with non-monotonic DTS, and the CLI must stay free to
	// fix those up instead of aborting. Frame counts are verified separately,
	// and the sample encode that follows still runs with -xerror.
	return []string{"-hide_banner", "-nostdin", "-n", "-ss", seconds(window.offset), "-i", plan.InputPath,
		"-t", seconds(window.duration), "-map", "0:" + strconv.Itoa(plan.VideoStreamIndex), "-c:v", "copy",
		"-an", "-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1", "-avoid_negative_ts", "make_zero", "-f", "matroska", output}
}

func prepareSamples(ctx context.Context, runner process.Runner, plan domain.EncodePlan, windows []sampleWindow, dir string, bin tools) ([]sample, error) {
	samples := make([]sample, 0, len(windows))
	for i, window := range windows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, fmt.Sprintf("reference-%d.mkv", i))
		if _, err := runner.Run(ctx, process.Command{Name: bin.ffmpegName(), Args: copySampleArgs(plan, window, path)}); err != nil {
			return nil, fmt.Errorf("extract search sample %d at %.3fs: %w", i+1, window.offset, err)
		}
		frames, err := sampleFrames(ctx, runner, bin.ffprobeName(), path, plan.Threads)
		if err != nil {
			return nil, fmt.Errorf("inspect reference sample %d: %w", i+1, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat reference sample: %w", err)
		}
		if info.Size() <= 0 {
			return nil, errors.New("reference sample is empty")
		}
		samples = append(samples, sample{path: path, bytes: info.Size(), frames: frames})
	}
	return samples, nil
}

func measureCandidate(ctx context.Context, runner process.Runner, plan domain.EncodePlan, samples []sample, dir string, crf int, bin tools) (domain.SearchCandidate, error) {
	// evaluate in search.go stamps the CRF on the returned candidate.
	var candidate domain.SearchCandidate
	var inputBytes, encodedBytes int64
	for i, ref := range samples {
		if err := ctx.Err(); err != nil {
			return candidate, err
		}
		encoded := filepath.Join(dir, fmt.Sprintf("candidate-%d-%d.mkv", crf, i))
		samplePlan := plan
		samplePlan.InputPath, samplePlan.OutputPath, samplePlan.CRF = ref.path, encoded, crf
		samplePlan.Threads = max(plan.Threads, 1)
		if _, err := runner.Run(ctx, process.Command{Name: bin.ffmpegName(), Args: ffmpeg.SampleArgs(samplePlan)}); err != nil {
			return candidate, fmt.Errorf("encode sample %d at CRF %d: %w", i+1, crf, err)
		}
		frames, err := sampleFrames(ctx, runner, bin.ffprobeName(), encoded, plan.Threads)
		if err != nil {
			return candidate, fmt.Errorf("inspect encoded sample %d at CRF %d: %w", i+1, crf, err)
		}
		if frames != ref.frames {
			return candidate, fmt.Errorf("sample %d at CRF %d has %d frames, expected %d", i+1, crf, frames, ref.frames)
		}
		score, err := measureQuality(ctx, runner, plan, ref, encoded, dir, bin.ffmpegName())
		if err != nil {
			return candidate, fmt.Errorf("score sample %d at CRF %d: %w", i+1, crf, err)
		}
		info, err := os.Stat(encoded)
		if err != nil {
			return candidate, fmt.Errorf("stat encoded sample: %w", err)
		}
		if info.Size() <= 0 {
			return candidate, errors.New("encoded sample is empty")
		}
		inputBytes += ref.bytes
		encodedBytes += info.Size()
		candidate.Score += score / float64(len(samples))
		if err := os.Remove(encoded); err != nil {
			return candidate, fmt.Errorf("remove encoded sample: %w", err)
		}
	}
	candidate.EncodedPercent = 100 * float64(encodedBytes) / float64(inputBytes)
	return candidate, nil
}

func sampleFrameArgs(path string, threads int) []string {
	return []string{"-v", "error", "-threads", strconv.Itoa(max(threads, 1)), "-select_streams", "v:0", "-count_frames", "-show_entries", "stream=nb_read_frames", "-of", "json", path}
}

func sampleFrames(ctx context.Context, runner process.Runner, binary string, path string, threads int) (int, error) {
	result, err := runner.Run(ctx, process.Command{Name: binary, Args: sampleFrameArgs(path, threads), RequireFullStdout: true})
	if err != nil {
		return 0, err
	}
	// Decoders log recoverable errors — h264 reports "mmco: unref short failure"
	// on open-GOP clips — while still decoding every frame, so the exit status
	// and the frame count below decide whether the sample is usable.
	var data struct {
		Streams []struct {
			Frames string `json:"nb_read_frames"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(result.Stdout, &data); err != nil {
		return 0, fmt.Errorf("parse sample frame count: %w", err)
	}
	if len(data.Streams) != 1 {
		return 0, errors.New("sample must contain one video stream")
	}
	frames, err := strconv.Atoi(data.Streams[0].Frames)
	if err != nil || frames <= 0 {
		return 0, fmt.Errorf("invalid sample frame count %q", data.Streams[0].Frames)
	}
	return frames, nil
}
func seconds(value float64) string { return strconv.FormatFloat(value, 'f', 6, 64) }
