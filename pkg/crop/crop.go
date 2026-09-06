package crop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
	"github.com/zekurio/anvil/pkg/video"
)

const (
	defaultCropDetectLimit      = 64
	defaultCropDetectRound      = 16
	defaultCropDetectResetCount = 0
	defaultFrameCount           = 300
	defaultMinRetainedAreaPct   = 70
	defaultMinWidth             = 128
	defaultMinHeight            = 128
	defaultRequiredAlignment    = 2
	// Allow small edge differences caused by cropdetect rounding.
	maxBorderDifference = 16
)

// CropSelectionArtifact is the attempt-event name used for crop decisions.
const CropSelectionArtifact = "crop-selection"

var defaultSeekOffsets = []time.Duration{
	0,
	2 * time.Minute,
	5 * time.Minute,
	12 * time.Minute,
	20 * time.Minute,
	30 * time.Minute,
}

type Detector interface {
	Detect(ctx context.Context, path string) (domain.CropResult, error)
}

type FFmpegDetector struct {
	Runner           process.Runner
	Binary           string
	FrameCount       int
	SeekOffsets      []time.Duration
	Limit            int
	Round            int
	ResetCount       int
	MapVideoStream   bool
	VideoStreamIndex int
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
		samples []domain.CropSample
	)
	offsets := d.seekOffsets()
	for _, offset := range offsets {
		result, err := runner.Run(ctx, process.Command{Name: binary, Args: d.args(path, offset), RequireFullStdout: true, RequireFullStderr: true})
		command = result.Command
		sampleOutput := combinedOutput(result)
		output = appendOutput(output, sampleOutput)
		filter, count := parseBounds(sampleOutput)
		sample := domain.CropSample{Offset: offset, Filter: filter, Observations: count}
		if err != nil {
			sample.Error = err.Error()
		}
		samples = append(samples, sample)
		if err != nil {
			if errors.Is(err, process.ErrOutputCapture) || errors.Is(err, process.ErrOutputLog) ||
				errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return domain.CropResult{}, fmt.Errorf("ffmpeg cropdetect offset %s: %w", offset, err)
			}
			errs = append(errs, fmt.Errorf("offset %s: %w", offset, err))
		}
	}
	candidate, selectionReason := selectSamples(samples)
	crop := domain.CropResult{
		CandidateFilter: candidate,
		Filter:          candidate,
		RawOutput:       string(output),
		RawCommand:      command,
		Samples:         samples,
		SelectionReason: selectionReason,
	}
	if len(errs) == len(offsets) || (crop.Filter == "" && len(errs) > 0) {
		return crop, fmt.Errorf("ffmpeg cropdetect: %w", errors.Join(errs...))
	}
	return crop, nil
}

func (d FFmpegDetector) args(path string, offset time.Duration) []string {
	frames := d.FrameCount
	if frames <= 0 {
		frames = defaultFrameCount
	}
	limit := d.Limit
	if limit <= 0 {
		limit = defaultCropDetectLimit
	}
	round := d.Round
	if round <= 0 {
		round = defaultCropDetectRound
	}
	resetCount := d.ResetCount
	if resetCount < 0 {
		resetCount = defaultCropDetectResetCount
	}
	args := []string{"-hide_banner"}
	if offset > 0 {
		args = append(args, "-ss", strconv.FormatFloat(offset.Seconds(), 'f', -1, 64))
	}
	args = append(args, "-i", path)
	if d.MapVideoStream {
		args = append(args, "-map", "0:"+strconv.Itoa(d.VideoStreamIndex))
	}
	args = append(args,
		"-vf", fmt.Sprintf("cropdetect=%d:%d:%d", limit, round, resetCount),
		"-frames:v", strconv.Itoa(frames),
		"-an",
		"-sn",
		"-dn",
		"-f", "null",
		"-",
	)
	return args
}

func (d FFmpegDetector) seekOffsets() []time.Duration {
	if len(d.SeekOffsets) > 0 {
		return append([]time.Duration(nil), d.SeekOffsets...)
	}
	return append([]time.Duration(nil), defaultSeekOffsets...)
}

type Block struct {
	Detector Detector
}

func (Block) Name() string {
	return "crop-detect"
}

func (b Block) Run(ctx context.Context, job *pipeline.JobContext) error {
	detector := b.Detector
	if detector == nil {
		policy := effectivePolicy(job.Profile.Crop)
		stream, hasVideo := primaryVideo(job.Probe)
		detector = FFmpegDetector{
			FrameCount:       policy.FrameCount,
			SeekOffsets:      policy.SeekOffsets,
			Limit:            policy.Limit,
			Round:            policy.Round,
			ResetCount:       policy.ResetCount,
			MapVideoStream:   hasVideo,
			VideoStreamIndex: stream.Index,
		}
	}
	result, err := detector.Detect(ctx, job.InputPath)
	if err != nil {
		return err
	}
	result = ApplySafetyPolicy(result, job.Probe, job.Profile.Crop)
	job.Crop = &result
	job.Metadata.CropFilter = result.Filter
	return nil
}

func (Block) Artifact(job *pipeline.JobContext) (pipeline.ArtifactReport, bool) {
	if job == nil || job.Crop == nil {
		return pipeline.ArtifactReport{}, false
	}
	result := job.Crop
	message := "no crop detected; using source dimensions"
	switch {
	case result.RejectionReason != "":
		message = fmt.Sprintf("rejected %s; using no crop: %s", result.CandidateFilter, result.RejectionReason)
	case result.NoOp:
		message = fmt.Sprintf("selected %s removes only small edge strips; using source dimensions", result.CandidateFilter)
	case result.Filter != "":
		message = fmt.Sprintf("selected %s (%.2f%% retained area)", result.Filter, result.RetainedAreaPercent)
	}
	return pipeline.ArtifactReport{
		Name:    CropSelectionArtifact,
		Message: message,
		Payload: cropSelectionPayload{
			CandidateFilter:     result.CandidateFilter,
			AppliedFilter:       result.Filter,
			SourceWidth:         result.SourceWidth,
			SourceHeight:        result.SourceHeight,
			OutputWidth:         result.OutputWidth,
			OutputHeight:        result.OutputHeight,
			RetainedAreaPercent: result.RetainedAreaPercent,
			RejectionReason:     result.RejectionReason,
			NoOp:                result.NoOp,
			Samples:             result.Samples,
			SelectionReason:     result.SelectionReason,
		},
	}, true
}

type cropSelectionPayload struct {
	Samples             []domain.CropSample `json:"samples,omitempty"`
	SelectionReason     string              `json:"selection_reason,omitempty"`
	CandidateFilter     string              `json:"candidate_filter,omitempty"`
	AppliedFilter       string              `json:"applied_filter,omitempty"`
	SourceWidth         int                 `json:"source_width,omitempty"`
	SourceHeight        int                 `json:"source_height,omitempty"`
	OutputWidth         int                 `json:"output_width,omitempty"`
	OutputHeight        int                 `json:"output_height,omitempty"`
	RetainedAreaPercent float64             `json:"retained_area_percent,omitempty"`
	RejectionReason     string              `json:"rejection_reason,omitempty"`
	NoOp                bool                `json:"no_op,omitempty"`
}

// ApplySafetyPolicy revalidates a detected or cached candidate against the
// current source dimensions and profile. Rejected candidates become an
// explicit no-crop result so search and encode cannot accidentally reuse them.
func ApplySafetyPolicy(result domain.CropResult, probe *domain.ProbeResult, configured domain.CropPolicy) domain.CropResult {
	policy := effectivePolicy(configured)
	candidate := strings.TrimSpace(result.CandidateFilter)
	if candidate == "" {
		candidate = strings.TrimSpace(result.Filter)
	}
	result.CandidateFilter = candidate
	result.Filter = ""
	result.SourceWidth = 0
	result.SourceHeight = 0
	result.OutputWidth = 0
	result.OutputHeight = 0
	result.RetainedAreaPercent = 0
	result.RejectionReason = result.SelectionReason
	result.NoOp = false

	stream, hasVideo := primaryVideo(probe)
	if hasVideo {
		result.SourceWidth = stream.Width
		result.SourceHeight = stream.Height
	}
	if candidate == "" || result.SelectionReason != "" {
		if hasVideo {
			result.OutputWidth = stream.Width
			result.OutputHeight = stream.Height
			result.RetainedAreaPercent = 100
		}
		return result
	}
	if !hasVideo {
		result.RejectionReason = "source video dimensions are unavailable"
		return result
	}

	spec, retainedAreaPercent, err := video.ValidateCropFilter(
		candidate,
		stream.Width,
		stream.Height,
		policy.MinWidth,
		policy.MinHeight,
		policy.MinRetainedAreaPercent,
		policy.RequiredAlignment,
	)
	result.OutputWidth = spec.Width
	result.OutputHeight = spec.Height
	result.RetainedAreaPercent = retainedAreaPercent
	if err != nil {
		result.RejectionReason = err.Error()
		return result
	}
	right := stream.Width - spec.X - spec.Width
	bottom := stream.Height - spec.Y - spec.Height
	if abs(spec.X-right) > maxBorderDifference || abs(spec.Y-bottom) > maxBorderDifference {
		result.RejectionReason = fmt.Sprintf("uneven borders: left %d, right %d, top %d, bottom %d", spec.X, right, spec.Y, bottom)
		return result
	}
	if spec.X <= maxBorderDifference && right <= maxBorderDifference && spec.Y <= maxBorderDifference && bottom <= maxBorderDifference {
		result.NoOp = true
		return result
	}
	result.Filter = candidate
	return result
}

func primaryVideo(probe *domain.ProbeResult) (domain.MediaStream, bool) {
	if probe == nil {
		return domain.MediaStream{}, false
	}
	stream, ok := domain.PrimaryVideoStream(probe.Streams)
	return stream, ok && stream.Width > 0 && stream.Height > 0
}

func effectivePolicy(policy domain.CropPolicy) domain.CropPolicy {
	if len(policy.SeekOffsets) == 0 {
		policy.SeekOffsets = append([]time.Duration(nil), defaultSeekOffsets...)
	}
	if policy.FrameCount <= 0 {
		policy.FrameCount = defaultFrameCount
	}
	if policy.Limit <= 0 {
		policy.Limit = defaultCropDetectLimit
	}
	if policy.Round <= 0 {
		policy.Round = defaultCropDetectRound
	}
	if policy.ResetCount < 0 {
		policy.ResetCount = defaultCropDetectResetCount
	}
	if math.IsNaN(policy.MinRetainedAreaPercent) || math.IsInf(policy.MinRetainedAreaPercent, 0) || policy.MinRetainedAreaPercent <= 0 {
		policy.MinRetainedAreaPercent = defaultMinRetainedAreaPct
	}
	if policy.MinWidth <= 0 {
		policy.MinWidth = defaultMinWidth
	}
	if policy.MinHeight <= 0 {
		policy.MinHeight = defaultMinHeight
	}
	if policy.RequiredAlignment <= 0 {
		policy.RequiredAlignment = defaultRequiredAlignment
	}
	return policy
}

var cropPattern = regexp.MustCompile(`crop=\d+:\d+:\d+:\d+`)

// ParseFilter returns the rectangle that contains every observed picture area.
// Frame frequency must not let dark scenes override wider picture evidence.
func ParseFilter(output []byte) string {
	filter, _ := parseBounds(output)
	return filter
}

func parseBounds(output []byte) (string, int) {
	var bounds video.CropSpec
	count := 0
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		prefixEnd := bytes.IndexByte(line, ']')
		if len(line) == 0 || line[0] != '[' || prefixEnd < 0 ||
			!bytes.Contains(bytes.ToLower(line[1:prefixEnd]), []byte("cropdetect")) {
			continue
		}
		for _, match := range cropPattern.FindAll(line[prefixEnd+1:], -1) {
			spec, ok := video.ParseCropFilter(string(match))
			if !ok || spec.Width <= 0 || spec.Height <= 0 || spec.X > math.MaxInt-spec.Width || spec.Y > math.MaxInt-spec.Height {
				continue
			}
			bounds = unionBounds(bounds, spec)
			count++
		}
	}
	if count == 0 {
		return "", 0
	}
	return formatBounds(bounds), count
}

func unionBounds(a, b video.CropSpec) video.CropSpec {
	if a.Width == 0 {
		return b
	}
	x, y := min(a.X, b.X), min(a.Y, b.Y)
	return video.CropSpec{X: x, Y: y, Width: max(a.X+a.Width, b.X+b.Width) - x, Height: max(a.Y+a.Height, b.Y+b.Height) - y}
}

func formatBounds(spec video.CropSpec) string {
	return fmt.Sprintf("crop=%d:%d:%d:%d", spec.Width, spec.Height, spec.X, spec.Y)
}

func selectSamples(samples []domain.CropSample) (string, string) {
	var bounds video.CropSpec
	var specs []video.CropSpec
	failed := false
	for _, sample := range samples {
		if sample.Error != "" {
			failed = true
			continue
		}
		spec, ok := video.ParseCropFilter(sample.Filter)
		if !ok {
			continue
		}
		specs = append(specs, spec)
		bounds = unionBounds(bounds, spec)
	}
	candidate := ""
	if len(specs) > 0 {
		candidate = formatBounds(bounds)
	}
	if failed {
		return candidate, "crop sample failed"
	}
	if len(specs) < 2 {
		return candidate, "fewer than two crop samples contain picture evidence"
	}
	for _, spec := range specs {
		if spec.X-bounds.X > maxBorderDifference || spec.Y-bounds.Y > maxBorderDifference ||
			bounds.X+bounds.Width-spec.X-spec.Width > maxBorderDifference ||
			bounds.Y+bounds.Height-spec.Y-spec.Height > maxBorderDifference {
			return candidate, "crop samples disagree on picture bounds"
		}
	}
	return candidate, ""
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
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
