package search

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"

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
		return domain.SearchResult{}, err
	}
	search, err := ParseResult(result.Stdout)
	if err != nil {
		return domain.SearchResult{}, err
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
	if plan.VideoCodec != "" {
		args = append(args, "--encoder", plan.VideoCodec)
	}
	if plan.Preset != "" {
		args = append(args, "--preset", plan.Preset)
	}
	if plan.PixelFormat != "" {
		args = append(args, "--pix-format", plan.PixelFormat)
	}
	if plan.Threads > 0 {
		threads := strconv.Itoa(plan.Threads)
		args = append(args, "--enc", "threads="+threads, "--vmaf", "n_threads="+threads)
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
	return domain.EncodePlan{
		InputPath:    job.InputPath,
		OutputPath:   job.OutputPath,
		VideoCodec:   job.Profile.Video.Codec,
		Preset:       job.Profile.Video.Preset,
		PixelFormat:  job.Profile.Video.PixelFormat,
		CRFMin:       job.Profile.Video.CRFMin,
		CRFMax:       job.Profile.Video.CRFMax,
		TargetVMAF:   job.Profile.Video.TargetVMAF,
		Threads:      job.Resources.Threads,
		Container:    job.Profile.Container,
		AudioMode:    job.Profile.Audio.Mode,
		SubtitleMode: job.Profile.Subtitles.Mode,
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
	crfPattern  = regexp.MustCompile(`(?i)\bcrf\b[^0-9]*(\d{1,3})`)
	vmafPattern = regexp.MustCompile(`(?i)\bvmaf\b[^0-9]*(\d+(?:\.\d+)?)`)
)

func ParseResult(output []byte) (domain.SearchResult, error) {
	text := string(output)
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
