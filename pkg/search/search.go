package search

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
)

type Searcher interface {
	Search(ctx context.Context, plan domain.EncodePlan) (domain.SearchResult, error)
}

type ABAV1 struct {
	Runner process.Runner
	Binary string
}

func (s ABAV1) Search(ctx context.Context, plan domain.EncodePlan) (domain.SearchResult, error) {
	if plan.InputPath == "" {
		return domain.SearchResult{}, errors.New("search input path is required")
	}
	runner := s.Runner
	if runner == nil {
		runner = process.OSRunner{}
	}
	binary := s.Binary
	if binary == "" {
		binary = "ab-av1"
	}
	args := SearchArgs(plan)
	result, err := runner.Run(ctx, process.Command{Name: binary, Args: args})
	if err != nil {
		if search, ok := ParseSkipResult(combinedOutput(result)); ok {
			search.RawOutput = combinedOutput(result)
			search.RawCommand = result.Command
			return search, nil
		}
		return domain.SearchResult{}, fmt.Errorf("ab-av1 crf-search failed: %w%s", err, outputHint(result))
	}
	search, err := ParseResult(result.Stdout)
	if err != nil {
		return domain.SearchResult{}, fmt.Errorf("%w%s", err, outputHint(result))
	}
	search.RawOutput = string(result.Stdout)
	search.RawCommand = result.Command
	return search, nil
}

func SearchArgs(plan domain.EncodePlan) []string {
	args := []string{
		"crf-search",
		"-i", plan.InputPath,
		"--min-crf", strconv.Itoa(crfMin(plan)),
		"--max-crf", strconv.Itoa(crfMax(plan)),
	}
	if plan.TargetVMAF > 0 {
		args = append(args, "--min-vmaf", strconv.FormatFloat(plan.TargetVMAF, 'f', -1, 64))
	}
	if plan.MinSavingsPercent > 0 {
		maxEncodedPercent := 100 - plan.MinSavingsPercent
		if maxEncodedPercent < 0 {
			maxEncodedPercent = 0
		}
		args = append(args, "--max-encoded-percent", strconv.FormatFloat(maxEncodedPercent, 'f', -1, 64))
	}
	if plan.VideoCodec != "" {
		args = append(args, "--encoder", plan.VideoCodec)
	}
	if plan.Preset != "" {
		args = append(args, "--preset", plan.Preset)
	}
	if plan.PixelFormat != "" {
		args = append(args, "--pix-format", plan.PixelFormat)
	}
	if plan.CropFilter != "" {
		args = append(args, "--vfilter", plan.CropFilter)
	}
	if plan.Threads > 0 {
		threads := strconv.Itoa(plan.Threads)
		args = append(args, "--enc", "threads="+threads, "--vmaf", "n_threads="+threads)
	}
	if len(plan.ABAV1Args) > 0 {
		args = append(args, plan.ABAV1Args...)
	}
	return args
}

type Block struct {
	Searcher Searcher
}

func (Block) Name() string {
	return "crf-search"
}

func (b Block) Run(ctx context.Context, job *pipeline.JobContext) error {
	if job.Metadata.VideoAlreadyEncoded {
		result := domain.SearchResult{
			SkipVideoEncode:       true,
			VideoEncodeSkipReason: "compatible Anvil video marker",
			RawOutput:             "skipped: compatible Anvil video marker",
		}
		job.Search = &result
		return nil
	}
	searcher := b.Searcher
	if searcher == nil {
		searcher = ABAV1{}
	}
	plan := searchPlan(job)
	result, err := searcher.Search(ctx, plan)
	if err != nil {
		return err
	}
	job.Search = &result
	return nil
}

func searchPlan(job *pipeline.JobContext) domain.EncodePlan {
	video := domain.EffectiveVideoProfile(job.Profile, job.Metadata)
	return domain.EncodePlan{
		InputPath:         job.InputPath,
		OutputPath:        job.OutputPath,
		VideoCodec:        video.Codec,
		VideoSource:       domain.EffectiveVideoSource(job.Metadata),
		Preset:            video.Preset,
		PixelFormat:       video.PixelFormat,
		CRFMin:            video.CRFMin,
		CRFMax:            video.CRFMax,
		TargetVMAF:        video.TargetVMAF,
		MinSavingsPercent: video.MinSavingsPercent,
		Threads:           job.Resources.Threads,
		Container:         job.Profile.Container,
		CropFilter:        job.Metadata.CropFilter,
		SubtitleMode:      job.Profile.Subtitles.Mode,
		ABAV1Args:         append([]string(nil), video.ABAV1Args...),
		HDR:               job.Metadata.HDR,
	}
}

func crfMin(plan domain.EncodePlan) int {
	if plan.CRFMin > 0 {
		return plan.CRFMin
	}
	return max(plan.CRF, 0)
}

func crfMax(plan domain.EncodePlan) int {
	if plan.CRFMax > 0 {
		return plan.CRFMax
	}
	return max(plan.CRF, 0)
}

var (
	crfPattern       = regexp.MustCompile(`(?i)\bcrf\b[^0-9]*(\d{1,3})`)
	vmafPattern      = regexp.MustCompile(`(?i)\bvmaf\b[^0-9]*(\d+(?:\.\d+)?)`)
	noGoodCRFPattern = regexp.MustCompile(`(?i)(failed to find a suitable crf|no suitable crf|no good crf|not worth (?:av1 )?encoding)`)
)

func ParseResult(output []byte) (domain.SearchResult, error) {
	text := string(output)
	if search, ok := ParseSkipResult(text); ok {
		return search, nil
	}
	crf, ok := lastIntMatch(crfPattern, text)
	if !ok {
		return domain.SearchResult{}, fmt.Errorf("parse ab-av1 output: CRF not found")
	}
	vmaf, _ := lastFloatMatch(vmafPattern, text)
	return domain.SearchResult{
		CRF:  crf,
		VMAF: vmaf,
	}, nil
}

func ParseSkipResult(text string) (domain.SearchResult, bool) {
	if !noGoodCRFPattern.MatchString(text) {
		return domain.SearchResult{}, false
	}
	return domain.SearchResult{
		SkipVideoEncode:       true,
		VideoEncodeSkipReason: noGoodCRFReason(text),
	}, true
}

func lastIntMatch(pattern *regexp.Regexp, text string) (int, bool) {
	matches := pattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return 0, false
	}
	value, err := strconv.Atoi(matches[len(matches)-1][1])
	return value, err == nil
}

func lastFloatMatch(pattern *regexp.Regexp, text string) (float64, bool) {
	matches := pattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return 0, false
	}
	value, err := strconv.ParseFloat(matches[len(matches)-1][1], 64)
	return value, err == nil
}

func noGoodCRFReason(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if noGoodCRFPattern.MatchString(line) {
			return "ab-av1 did not find a CRF satisfying VMAF/size constraints: " + line
		}
	}
	return "ab-av1 did not find a CRF satisfying VMAF/size constraints"
}

func combinedOutput(result process.Result) string {
	stdout := strings.TrimSpace(string(result.Stdout))
	stderr := strings.TrimSpace(string(result.Stderr))
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	default:
		return stdout + "\n" + stderr
	}
}

func outputHint(result process.Result) string {
	text := combinedOutput(result)
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\n", " ")
	if len(text) > 500 {
		text = text[:500] + "..."
	}
	return ": " + text
}
