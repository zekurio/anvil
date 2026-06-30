package crop

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

const (
	defaultCropDetectLimit = 64
	defaultCropDetectRound = 16
	defaultCropDetectReset = 0
	defaultFrameLimit      = 300
)

var defaultSeekOffsets = []string{
	"",
	"00:02:00",
	"00:05:00",
	"00:12:00",
	"00:20:00",
	"00:30:00",
}

type Detector interface {
	Detect(ctx context.Context, path string) (domain.CropResult, error)
}

type FFmpegDetector struct {
	Runner      process.Runner
	Binary      string
	FrameLimit  int
	SeekOffsets []string
}

func (d FFmpegDetector) Detect(ctx context.Context, path string) (domain.CropResult, error) {
	if path == "" {
		return domain.CropResult{}, errors.New("crop detection input path is required")
	}
	runner := d.Runner
	if runner == nil {
		runner = process.OSRunner{}
	}
	binary := d.Binary
	if binary == "" {
		binary = "ffmpeg"
	}
	var (
		command []string
		output  []byte
		errs    []error
	)
	offsets := d.seekOffsets()
	for _, offset := range offsets {
		result, err := runner.Run(ctx, process.Command{Name: binary, Args: d.args(path, offset)})
		command = result.Command
		output = appendOutput(output, combinedOutput(result))
		if err != nil {
			errs = append(errs, fmt.Errorf("offset %q: %w", offset, err))
		}
	}
	crop := domain.CropResult{
		Filter:     ParseFilter(output),
		RawOutput:  string(output),
		RawCommand: command,
	}
	if len(errs) == len(offsets) || (crop.Filter == "" && len(errs) > 0) {
		return crop, fmt.Errorf("ffmpeg cropdetect: %w", errors.Join(errs...))
	}
	return crop, nil
}

func (d FFmpegDetector) args(path string, offset string) []string {
	frames := d.FrameLimit
	if frames <= 0 {
		frames = defaultFrameLimit
	}
	args := []string{
		"-hide_banner",
	}
	if offset = strings.TrimSpace(offset); offset != "" {
		args = append(args, "-ss", offset)
	}
	args = append(args,
		"-i", path,
		"-vf", fmt.Sprintf("cropdetect=%d:%d:%d", defaultCropDetectLimit, defaultCropDetectRound, defaultCropDetectReset),
		"-frames:v", strconv.Itoa(frames),
		"-an",
		"-sn",
		"-dn",
		"-f", "null",
		"-",
	)
	return args
}

func (d FFmpegDetector) seekOffsets() []string {
	if len(d.SeekOffsets) > 0 {
		return append([]string(nil), d.SeekOffsets...)
	}
	return append([]string(nil), defaultSeekOffsets...)
}

type Block struct {
	Detector Detector
}

func (Block) Name() string {
	return "crop-detect"
}

func (b Block) Run(ctx context.Context, job *pipeline.JobContext) error {
	if job.Metadata.VideoAlreadyEncoded {
		result := domain.CropResult{Filter: job.Metadata.CropFilter}
		job.Crop = &result
		return nil
	}
	detector := b.Detector
	if detector == nil {
		detector = FFmpegDetector{}
	}
	result, err := detector.Detect(ctx, job.InputPath)
	if err != nil {
		return err
	}
	job.Crop = &result
	job.Metadata.CropFilter = result.Filter
	return nil
}

var cropPattern = regexp.MustCompile(`crop=\d+:\d+:\d+:\d+`)

func ParseFilter(output []byte) string {
	matches := cropPattern.FindAll(output, -1)
	if len(matches) == 0 {
		return ""
	}
	counts := make(map[string]candidate, len(matches))
	best := ""
	for index, match := range matches {
		filter := string(match)
		next := counts[filter]
		next.count++
		next.last = index
		counts[filter] = next
		if best == "" || better(next, counts[best]) {
			best = filter
		}
	}
	return best
}

type candidate struct {
	count int
	last  int
}

func better(candidate, incumbent candidate) bool {
	return candidate.count > incumbent.count ||
		(candidate.count == incumbent.count && candidate.last > incumbent.last)
}

func combinedOutput(result process.Result) []byte {
	output := make([]byte, 0, len(result.Stdout)+len(result.Stderr)+1)
	output = append(output, result.Stdout...)
	if len(output) > 0 && len(result.Stderr) > 0 {
		output = append(output, '\n')
	}
	output = append(output, result.Stderr...)
	return output
}

func appendOutput(output []byte, next []byte) []byte {
	if len(next) == 0 {
		return output
	}
	if len(output) > 0 {
		output = append(output, '\n')
	}
	return append(output, next...)
}
