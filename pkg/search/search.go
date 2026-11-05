package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/bits"
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
	if !finite(plan.Target) || plan.Target < 0 || plan.Target > 100 {
		return errors.New("search quality target must be finite and between 0 and 100")
	}
	if !finite(plan.MinSavingsPercent) || plan.MinSavingsPercent < 0 || plan.MinSavingsPercent > 100 {
		return errors.New("search savings target must be finite and between 0 and 100")
	}
	return ffmpeg.ValidateEncoderArgs(plan.FFmpegArgs)
}

const noFitReason = "CRF search found no candidate satisfying quality and size constraints"

// crfSearch is one CRF search over a fixed set of samples. Each CRF is
// measured at most once; Candidates keeps the measurement order.
type crfSearch struct {
	plan     domain.EncodePlan
	measure  func(context.Context, int) (domain.SearchCandidate, error)
	measured map[int]domain.SearchCandidate
	result   domain.SearchResult
	// estimatesLeft caps interpolated (non-midpoint) quality trials.
	estimatesLeft int
}

func searchCRF(ctx context.Context, plan domain.EncodePlan, measure func(context.Context, int) (domain.SearchCandidate, error)) (domain.SearchResult, error) {
	s := &crfSearch{
		plan:     plan,
		measure:  measure,
		measured: make(map[int]domain.SearchCandidate),
		result:   domain.SearchResult{Metric: plan.Metric},
	}
	if err := s.findQualityEdge(ctx); err != nil {
		return s.result, err
	}
	maxPercent := 100 - plan.MinSavingsPercent
	chosen, ok := s.highestFit(maxPercent)
	if !ok {
		if !plan.ForceEncodeOnNoFit {
			s.result.SkipVideoEncode, s.result.VideoEncodeSkipReason = true, noFitReason
			return s.result, nil
		}
		var err error
		if chosen, err = s.forcedChoice(ctx, maxPercent); err != nil {
			return s.result, err
		}
	}
	s.result.CRF = chosen.CRF
	if plan.Metric == domain.QualityMetricXPSNR {
		s.result.XPSNR = chosen.Score
	} else {
		s.result.VMAF = chosen.Score
	}
	return s.result, nil
}

func (s *crfSearch) evaluate(ctx context.Context, crf int) (domain.SearchCandidate, error) {
	if err := ctx.Err(); err != nil {
		return domain.SearchCandidate{}, err
	}
	if candidate, ok := s.measured[crf]; ok {
		return candidate, nil
	}
	candidate, err := s.measure(ctx, crf)
	if err != nil {
		return candidate, err
	}
	if err := ctx.Err(); err != nil {
		return candidate, err
	}
	if !finite(candidate.Score) || !finite(candidate.EncodedPercent) || candidate.EncodedPercent <= 0 {
		return candidate, errors.New("search candidate has invalid quality or size measurements")
	}
	candidate.CRF = crf
	s.measured[crf] = candidate
	s.result.Candidates = append(s.result.Candidates, candidate)
	s.result.RawOutput += fmt.Sprintf("crf %d %s %.4f encoded %.2f%%\n", crf, s.plan.Metric, candidate.Score, candidate.EncodedPercent)
	return candidate, nil
}

// findQualityEdge searches for the highest CRF that still meets the target.
//
// Caveat: search assumes quality falls and size shrinks as CRF rises. A sweep
// is needed to guarantee an optimum for non-monotonic encoders.
func (s *crfSearch) findQualityEdge(ctx context.Context) error {
	plan := s.plan
	endpoint, err := s.evaluate(ctx, plan.CRFMax)
	if err != nil {
		return err
	}
	low, high := plan.CRFMin, plan.CRFMax-1
	if endpoint.Score >= plan.Target {
		low = plan.CRFMax
	}
	// Allow local estimates to approach the boundary without requiring each
	// trial to halve the interval. Cap non-midpoint trials at the number of
	// bisections the original interval needs, then finish with bisection.
	// Including the endpoint, quality search takes at most 1+2*bits.Len(span)
	// measurements even when the score curve makes interpolation unhelpful.
	s.estimatesLeft = bits.Len(uint(plan.CRFMax - plan.CRFMin))
	for low <= high {
		candidate, err := s.evaluate(ctx, s.nextQualityTrial(low, high))
		if err != nil {
			return err
		}
		if candidate.Score >= plan.Target {
			low = candidate.CRF + 1
		} else {
			high = candidate.CRF - 1
		}
	}
	return nil
}

// nextQualityTrial picks the next CRF in [low, high]: a linear estimate from
// the last two measurements while the estimate budget lasts, else the midpoint.
func (s *crfSearch) nextQualityTrial(low, high int) int {
	mid := low + (high-low)/2
	n := len(s.result.Candidates)
	if n < 2 || s.estimatesLeft <= 0 {
		return mid
	}
	a, b := s.result.Candidates[n-2], s.result.Candidates[n-1]
	if a.CRF > b.CRF {
		a, b = b, a
	}
	// Flat or reversed slopes give no usable estimate.
	if a.Score <= b.Score {
		return mid
	}
	estimate := float64(a.CRF) + (a.Score-s.plan.Target)*float64(b.CRF-a.CRF)/(a.Score-b.Score)
	if !finite(estimate) {
		return mid
	}
	next := int(math.Floor(max(float64(low), min(float64(high), estimate))))
	if next != mid {
		s.estimatesLeft--
	}
	return next
}

// highestFit is the highest measured CRF meeting both the quality target and
// the size limit.
func (s *crfSearch) highestFit(maxPercent float64) (domain.SearchCandidate, bool) {
	var chosen domain.SearchCandidate
	found := false
	for _, candidate := range s.result.Candidates {
		if candidate.Score >= s.plan.Target && candidate.EncodedPercent <= maxPercent && (!found || candidate.CRF > chosen.CRF) {
			chosen, found = candidate, true
		}
	}
	return chosen, found
}

// forcedChoice runs when nothing fits and the profile forces an encode. It
// finds the quality-favoring edge of the size limit, then picks the best
// measured candidate. A failed process or score never becomes a no-fit result.
func (s *crfSearch) forcedChoice(ctx context.Context, maxPercent float64) (domain.SearchCandidate, error) {
	low, high := s.plan.CRFMin, s.plan.CRFMax
	for low <= high {
		mid := low + (high-low)/2
		candidate, err := s.evaluate(ctx, mid)
		if err != nil {
			return domain.SearchCandidate{}, err
		}
		if candidate.EncodedPercent <= maxPercent {
			high = mid - 1
		} else {
			low = mid + 1
		}
	}
	// Include the highest-quality endpoint even if no tested size fits.
	if _, err := s.evaluate(ctx, s.plan.CRFMin); err != nil {
		return domain.SearchCandidate{}, err
	}
	chosen := s.result.Candidates[0]
	for _, candidate := range s.result.Candidates[1:] {
		if preferForced(candidate, chosen, maxPercent) {
			chosen = candidate
		}
	}
	// The additional probes can find a passing candidate with irregular scores.
	if chosen.Score < s.plan.Target || chosen.EncodedPercent > maxPercent {
		s.result.ForcedVideoEncodeReason = fmt.Sprintf("%s; forcing encode with best tested CRF %d", noFitReason, chosen.CRF)
	}
	return chosen, nil
}

// preferForced orders forced candidates: fitting the size limit first, then
// higher quality, then smaller size.
func preferForced(candidate, best domain.SearchCandidate, maxPercent float64) bool {
	fits, bestFits := candidate.EncodedPercent <= maxPercent, best.EncodedPercent <= maxPercent
	if fits != bestFits {
		return fits
	}
	if candidate.Score != best.Score {
		return candidate.Score > best.Score
	}
	return candidate.EncodedPercent < best.EncodedPercent
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
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
