package search

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
	videocodec "github.com/zekurio/anvil/pkg/video"
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
	scratchDir := searchScratchDir(plan)
	if scratchDir != "" {
		if err := os.MkdirAll(scratchDir, 0o750); err != nil {
			return domain.SearchResult{}, fmt.Errorf("prepare ab-av1 scratch dir: %w", err)
		}
	}
	command := process.Command{Name: binary, Args: args}
	if scratchDir != "" {
		command.Dir = scratchDir
		command.Env = []string{
			"TMPDIR=" + scratchDir,
			"TEMP=" + scratchDir,
			"TMP=" + scratchDir,
			"XDG_CACHE_HOME=" + filepath.Join(scratchDir, ".cache"),
		}
	}
	result, err := runner.Run(ctx, command)
	if err != nil {
		output := combinedOutput(result)
		if fatalSearchFailure(output) {
			return domain.SearchResult{}, fmt.Errorf("ab-av1 crf-search failed: %w%s", err, outputHint(result))
		}
		if search, ok := ParseNoFitResult(output, plan); ok {
			search.RawOutput = combinedOutput(result)
			search.RawCommand = result.Command
			return search, nil
		}
		return domain.SearchResult{}, fmt.Errorf("ab-av1 crf-search failed: %w%s", err, outputHint(result))
	}
	search, err := ParseResultForPlan(result.Stdout, plan)
	if err != nil {
		return domain.SearchResult{}, fmt.Errorf("%w%s", err, outputHint(result))
	}
	search.RawOutput = string(result.Stdout)
	search.RawCommand = result.Command
	return search, nil
}

func searchScratchDir(plan domain.EncodePlan) string {
	if strings.TrimSpace(plan.OutputPath) == "" {
		return ""
	}
	dir := filepath.Dir(plan.OutputPath)
	if dir == "." || dir == string(filepath.Separator) {
		return ""
	}
	return dir
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
	if pixelFormat := searchPixelFormat(plan); pixelFormat != "" {
		args = append(args, "--pix-format", pixelFormat)
	}
	if filter := searchVideoFilter(plan); filter != "" {
		args = append(args, "--vfilter", filter)
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
	inputVideoCodec, inputWidth, inputHeight := inputVideo(job.Probe)
	video := domain.EffectiveVideoProfile(job.Profile, job.Metadata, inputVideoCodec)
	return domain.EncodePlan{
		InputPath:          job.InputPath,
		OutputPath:         job.OutputPath,
		VideoCodec:         videocodec.ResolveEncoder(video.Codec, video.Accelerator),
		InputVideoCodec:    inputVideoCodec,
		InputWidth:         inputWidth,
		InputHeight:        inputHeight,
		Accelerator:        videocodec.ResolveAccelerator(video.Accelerator),
		VideoSource:        domain.EffectiveVideoSource(job.Metadata),
		Preset:             video.Preset,
		BitDepth:           videocodec.NormalizeBitDepth(video.BitDepth),
		PixelFormat:        videocodec.SoftwarePixelFormat(video.BitDepth),
		CRFMin:             video.CRFMin,
		CRFMax:             video.CRFMax,
		TargetVMAF:         video.TargetVMAF,
		MinSavingsPercent:  video.MinSavingsPercent,
		ForceEncodeOnNoFit: video.ForceEncodeOnNoFit,
		Threads:            job.Resources.Threads,
		Container:          job.Profile.Container,
		CropFilter:         job.Metadata.CropFilter,
		ABAV1Args:          append([]string(nil), video.ABAV1Args...),
		HDR:                job.Metadata.HDR,
	}
}

func searchVideoFilter(plan domain.EncodePlan) string {
	if strings.TrimSpace(plan.CropFilter) == "" || videocodec.NoOpCrop(plan.CropFilter, plan.InputWidth, plan.InputHeight) {
		return ""
	}
	return plan.CropFilter
}

func searchPixelFormat(plan domain.EncodePlan) string {
	if plan.BitDepth != 0 {
		return videocodec.SoftwarePixelFormat(plan.BitDepth)
	}
	return strings.TrimSpace(plan.PixelFormat)
}

func inputVideo(probe *domain.ProbeResult) (string, int, int) {
	if probe == nil {
		return "", 0, 0
	}
	stream, ok := domain.PrimaryVideoStream(probe.Streams)
	if !ok {
		return "", 0, 0
	}
	return stream.Codec, stream.Width, stream.Height
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
	crfPattern         = regexp.MustCompile(`(?i)\bcrf\b[^0-9]*(\d{1,3})`)
	vmafPattern        = regexp.MustCompile(`(?i)\bvmaf\b[^0-9]*(\d+(?:\.\d+)?)`)
	noGoodCRFPattern   = regexp.MustCompile(`(?i)(failed to find a suitable crf|no suitable crf|no good crf|not worth (?:av1 )?encoding)`)
	fatalSearchPattern = regexp.MustCompile(`(?i)(panicked at|failed to create temp-dir|permission denied|invalid value|unknown option|unrecognized option)`)
)

func ParseResult(output []byte) (domain.SearchResult, error) {
	return ParseResultForPlan(output, domain.EncodePlan{})
}

func ParseResultForPlan(output []byte, plan domain.EncodePlan) (domain.SearchResult, error) {
	text := string(output)
	if search, ok := ParseNoFitResult(text, plan); ok {
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
	return ParseNoFitResult(text, domain.EncodePlan{})
}

func ParseNoFitResult(text string, plan domain.EncodePlan) (domain.SearchResult, bool) {
	if !noGoodCRFPattern.MatchString(text) {
		return domain.SearchResult{}, false
	}
	if plan.ForceEncodeOnNoFit {
		crf, vmaf, ok := lowestCRFObservation(text)
		if !ok {
			crf = crfMin(plan)
		}
		if crf > 0 {
			return domain.SearchResult{
				CRF:                     crf,
				VMAF:                    vmaf,
				ForcedVideoEncodeReason: forceNoGoodCRFReason(text, crf),
			}, true
		}
	}
	return domain.SearchResult{
		SkipVideoEncode:       true,
		VideoEncodeSkipReason: noGoodCRFReason(text),
	}, true
}

func fatalSearchFailure(text string) bool {
	return fatalSearchPattern.MatchString(text)
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

func lowestCRFObservation(text string) (int, float64, bool) {
	bestCRF := 0
	bestVMAF := 0.0
	found := false
	for _, line := range strings.Split(text, "\n") {
		matches := crfPattern.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			continue
		}
		lineVMAF, _ := lastFloatMatch(vmafPattern, line)
		for _, match := range matches {
			value, err := strconv.Atoi(match[1])
			if err != nil {
				continue
			}
			if !found || value < bestCRF {
				bestCRF = value
				bestVMAF = lineVMAF
				found = true
			}
		}
	}
	return bestCRF, bestVMAF, found
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

func forceNoGoodCRFReason(text string, crf int) string {
	return fmt.Sprintf("%s; forcing encode with lowest tested CRF %d", noGoodCRFReason(text), crf)
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
