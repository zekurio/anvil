package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/ffmpeg"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/probe"
	"github.com/zekurio/anvil/pkg/process"
)

type Searcher interface {
	Search(ctx context.Context, plan domain.EncodePlan, scratchDir string) (domain.SearchResult, error)
}

// FFmpeg owns sample selection, CRF selection, and quality/size acceptance.
// Only the actual encoding and metric calculation are delegated to FFmpeg.
// Binary and ProbeBinary override the ffmpeg and ffprobe executables, matching
// the Binary fields on ffmpeg.Encoder, crop.Detector, and probe.FFProbe.
type FFmpeg struct {
	Runner      process.Runner
	Binary      string
	ProbeBinary string
}

// tools names the external programs search invokes. Empty fields fall back to
// the default PATH lookup.
type tools struct {
	ffmpeg  string
	ffprobe string
}

func (s FFmpeg) tools() tools {
	return tools{ffmpeg: s.Binary, ffprobe: s.ProbeBinary}
}

func (t tools) ffmpegName() string {
	if t.ffmpeg == "" {
		return "ffmpeg"
	}
	return t.ffmpeg
}

func (t tools) ffprobeName() string {
	if t.ffprobe == "" {
		return "ffprobe"
	}
	return t.ffprobe
}

func (s FFmpeg) Search(ctx context.Context, plan domain.EncodePlan, scratchDir string) (result domain.SearchResult, err error) {
	if err := validatePlan(plan); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	runner := s.Runner
	if runner == nil {
		runner = process.OSRunner{}
	}
	bin := s.tools()
	plan.InputPath, err = filepath.Abs(plan.InputPath)
	if err != nil {
		return result, fmt.Errorf("resolve search input: %w", err)
	}
	source, err := (probe.FFProbe{Runner: runner, Binary: bin.ffprobeName()}).Probe(ctx, plan.InputPath)
	if err != nil {
		return result, err
	}
	stream, ok := domain.PrimaryVideoStream(source.Streams)
	if !ok {
		return result, errors.New("search input has no video stream")
	}
	if plan.VideoSelectionApplied && plan.VideoStreamIndex != stream.Index {
		return result, errors.New("search input video stream changed since probing")
	}
	plan.VideoSelectionApplied, plan.VideoStreamIndex = true, stream.Index
	plan.InputPixelFormat = stream.PixelFormat
	windows, err := sampleWindows(source.DurationSeconds, plan.SearchSamples, plan.SearchSampleDuration)
	if err != nil {
		return result, err
	}
	if scratchDir != "" {
		if err := os.MkdirAll(scratchDir, 0o750); err != nil {
			return result, fmt.Errorf("prepare search scratch directory: %w", err)
		}
	}
	dir, err := os.MkdirTemp(scratchDir, "crf-search-")
	if err != nil {
		return result, fmt.Errorf("create search scratch directory: %w", err)
	}
	// Only this invocation's private directory is removed, also on cancellation.
	defer func() {
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("clean search scratch directory: %w", cleanupErr))
		}
	}()
	dir, err = filepath.Abs(dir)
	if err != nil {
		return result, fmt.Errorf("resolve search scratch directory: %w", err)
	}
	samples, err := prepareSamples(ctx, runner, plan, windows, dir, bin)
	if err != nil {
		return result, err
	}
	return searchCRF(ctx, plan, func(ctx context.Context, crf int) (domain.SearchCandidate, error) {
		return measureCandidate(ctx, runner, plan, samples, dir, crf, bin)
	})
}

func validatePlan(plan domain.EncodePlan) error {
	if strings.TrimSpace(plan.InputPath) == "" {
		return errors.New("search input path is required")
	}
	if plan.CRFMin < 0 || plan.CRFMax < plan.CRFMin || plan.CRFMax > 255 {
		return errors.New("search CRF range must be ordered and between 0 and 255")
	}
	if plan.Metric != domain.QualityMetricVMAF && plan.Metric != domain.QualityMetricXPSNR {
		return fmt.Errorf("unsupported search metric %q", plan.Metric)
	}
	if math.IsNaN(plan.Target) || math.IsInf(plan.Target, 0) || plan.Target < 0 || plan.Target > 100 {
		return errors.New("search quality target must be finite and between 0 and 100")
	}
	if math.IsNaN(plan.MinSavingsPercent) || math.IsInf(plan.MinSavingsPercent, 0) || plan.MinSavingsPercent < 0 || plan.MinSavingsPercent > 100 {
		return errors.New("search savings target must be finite and between 0 and 100")
	}
	return ffmpeg.ValidateEncoderArgs(plan.FFmpegArgs)
}

func searchCRF(ctx context.Context, plan domain.EncodePlan, measure func(context.Context, int) (domain.SearchCandidate, error)) (domain.SearchResult, error) {
	result := domain.SearchResult{Metric: plan.Metric}
	measured := make(map[int]domain.SearchCandidate)
	evaluate := func(crf int) (domain.SearchCandidate, error) {
		if err := ctx.Err(); err != nil {
			return domain.SearchCandidate{}, err
		}
		if candidate, ok := measured[crf]; ok {
			return candidate, nil
		}
		candidate, err := measure(ctx, crf)
		if err != nil {
			return candidate, err
		}
		if err := ctx.Err(); err != nil {
			return candidate, err
		}
		if math.IsNaN(candidate.Score) || math.IsInf(candidate.Score, 0) || math.IsNaN(candidate.EncodedPercent) || math.IsInf(candidate.EncodedPercent, 0) || candidate.EncodedPercent <= 0 {
			return candidate, errors.New("search candidate has invalid quality or size measurements")
		}
		candidate.CRF = crf
		measured[crf] = candidate
		result.Candidates = append(result.Candidates, candidate)
		result.RawOutput += fmt.Sprintf("crf %d %s %.4f encoded %.2f%%\n", crf, plan.Metric, candidate.Score, candidate.EncodedPercent)
		return candidate, nil
	}
	// Caveat: search assumes quality falls and size shrinks as CRF rises.
	// A sweep is needed to guarantee an optimum for non-monotonic encoders.
	endpoint, err := evaluate(plan.CRFMax)
	if err != nil {
		return result, err
	}
	low, high := plan.CRFMin, plan.CRFMax-1
	if endpoint.Score >= plan.Target {
		low = plan.CRFMax
	}
	previousWidth := 0
	for low <= high {
		width := high - low + 1
		mid := low + (high-low)/2
		// Estimate the quality boundary from the last two scores. Only do so
		// after halving the interval, so poor estimates fall back to bisection.
		if n := len(result.Candidates); n >= 2 && width <= previousWidth/2 {
			a, b := result.Candidates[n-2], result.Candidates[n-1]
			if a.CRF > b.CRF {
				a, b = b, a
			}
			if a.Score > b.Score {
				estimate := float64(a.CRF) + (a.Score-plan.Target)*float64(b.CRF-a.CRF)/(a.Score-b.Score)
				if !math.IsNaN(estimate) && !math.IsInf(estimate, 0) {
					mid = int(math.Floor(max(float64(low), min(float64(high), estimate))))
				}
			}
		}
		previousWidth = width
		candidate, err := evaluate(mid)
		if err != nil {
			return result, err
		}
		if candidate.Score >= plan.Target {
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	maxPercent := 100 - plan.MinSavingsPercent
	var chosen *domain.SearchCandidate
	for _, candidate := range result.Candidates {
		if candidate.Score >= plan.Target && candidate.EncodedPercent <= maxPercent && (chosen == nil || candidate.CRF > chosen.CRF) {
			chosen = &candidate
		}
	}
	reason := "CRF search found no candidate satisfying quality and size constraints"
	if chosen == nil && !plan.ForceEncodeOnNoFit {
		result.SkipVideoEncode, result.VideoEncodeSkipReason = true, reason
		return result, nil
	}
	if chosen == nil {
		// Find the quality-favoring edge of the size limit before selecting a
		// forced result. A failed process or score never becomes a no-fit result.
		low, high = plan.CRFMin, plan.CRFMax
		for low <= high {
			mid := low + (high-low)/2
			candidate, err := evaluate(mid)
			if err != nil {
				return result, err
			}
			if candidate.EncodedPercent <= maxPercent {
				high = mid - 1
			} else {
				low = mid + 1
			}
		}
		// Include the highest-quality endpoint even if no tested size fits.
		if _, err := evaluate(plan.CRFMin); err != nil {
			return result, err
		}
		for _, candidate := range result.Candidates {
			fits := candidate.EncodedPercent <= maxPercent
			bestFits := chosen != nil && chosen.EncodedPercent <= maxPercent
			if chosen == nil || (fits && !bestFits) || (fits == bestFits && (candidate.Score > chosen.Score || (candidate.Score == chosen.Score && candidate.EncodedPercent < chosen.EncodedPercent))) {
				chosen = &candidate
			}
		}
		// The additional probes can find a passing candidate with irregular scores.
		if chosen.Score < plan.Target || chosen.EncodedPercent > maxPercent {
			result.ForcedVideoEncodeReason = fmt.Sprintf("%s; forcing encode with best tested CRF %d", reason, chosen.CRF)
		}
	}
	result.CRF = chosen.CRF
	if plan.Metric == domain.QualityMetricXPSNR {
		result.XPSNR = chosen.Score
	} else {
		result.VMAF = chosen.Score
	}
	return result, nil
}

type Block struct{ Searcher Searcher }

func (Block) Name() string { return "crf-search" }
func (b Block) Run(ctx context.Context, job *pipeline.JobContext) error {
	plan, err := ffmpeg.BuildPlanFromRequest(ffmpeg.BuildPlanRequest{
		Profile: job.Profile, InputPath: job.InputPath, OutputPath: job.OutputPath,
		Resources: job.Resources, Metadata: job.Metadata, Probe: job.Probe,
	})
	if err != nil {
		return err
	}
	if plan.VideoCopy {
		job.Search = &domain.SearchResult{Metric: plan.Metric, SkipVideoEncode: true, VideoEncodeSkipReason: plan.VideoCopyReason}
		return nil
	}
	searcher := b.Searcher
	if searcher == nil {
		searcher = FFmpeg{}
	}
	result, err := searcher.Search(ctx, plan, job.StagingDir)
	if err != nil {
		return err
	}
	job.Search = &result
	return nil
}
