package validate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/marker"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/probe"
	videocodec "github.com/zekurio/anvil/pkg/video"
)

const defaultDurationToleranceSeconds = 2

var ErrValidationFailed = errors.New("validation failed")

type Prober interface {
	Probe(ctx context.Context, path string) (domain.ProbeResult, error)
}

type Validator struct {
	Prober                   Prober
	DurationToleranceSeconds float64
}

type Request struct {
	SourceProbe *domain.ProbeResult
	SourcePath  string
	OutputPath  string
	Profile     domain.Profile
	EncodePlan  *domain.EncodePlan
	Audio       *domain.AudioSelection
	Metadata    domain.JobMetadata
}

func RequestFromJob(job *pipeline.JobContext) Request {
	if job == nil {
		return Request{}
	}
	return Request{
		SourceProbe: job.Probe,
		SourcePath:  job.InputPath,
		OutputPath:  job.OutputPath,
		Profile:     job.Profile,
		EncodePlan:  job.EncodePlan,
		Audio:       job.Audio,
		Metadata:    job.Metadata,
	}
}

func (v Validator) Validate(ctx context.Context, request Request) (domain.ValidationResult, error) {
	result := domain.ValidationResult{OK: true}
	if request.SourceProbe == nil {
		addError(&result, "source probe is unavailable; cannot validate duration or stream counts")
	} else {
		result.SourceDurationSeconds = request.SourceProbe.DurationSeconds
	}
	if request.EncodePlan == nil {
		addError(&result, "encode plan is unavailable; cannot validate encode intent")
	}

	outputPath := strings.TrimSpace(request.OutputPath)
	if outputPath == "" {
		addError(&result, "output path is required")
		return result, ErrValidationFailed
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		addError(&result, fmt.Sprintf("output stat failed: %v", err))
		return result, fmt.Errorf("validate output: %w", err)
	}
	result.OutputSizeBytes = info.Size()
	if info.Size() <= 0 {
		addError(&result, "output file is empty")
	}
	result.SourceSizeBytes = sourceSize(request)

	prober := v.Prober
	if prober == nil {
		prober = probe.FFProbe{}
	}
	outputProbe, err := prober.Probe(ctx, outputPath)
	if err != nil {
		addError(&result, fmt.Sprintf("output probe failed: %v", err))
		return result, fmt.Errorf("validate output probe: %w", err)
	}
	result.OutputDurationSeconds = outputProbe.DurationSeconds

	v.validateDuration(request, outputProbe, &result)
	validateVideo(request, outputProbe, &result)
	validateMarker(request, outputProbe, &result)
	validateAudio(request, outputProbe, &result)
	validateSubtitles(request, outputProbe, &result)
	validateHDR(request, outputProbe, &result)
	computeSizeMetrics(&result)

	if !result.OK {
		return result, ErrValidationFailed
	}
	return result, nil
}

func (v Validator) validateDuration(request Request, outputProbe domain.ProbeResult, result *domain.ValidationResult) {
	if request.SourceProbe == nil {
		return
	}
	sourceDuration := request.SourceProbe.DurationSeconds
	outputDuration := outputProbe.DurationSeconds
	if sourceDuration <= 0 || outputDuration <= 0 {
		return
	}
	tolerance := v.DurationToleranceSeconds
	if tolerance <= 0 {
		tolerance = request.Profile.Validation.DurationToleranceSeconds
	}
	if tolerance <= 0 {
		tolerance = defaultDurationToleranceSeconds
	}
	if math.Abs(sourceDuration-outputDuration) > tolerance {
		addError(result, fmt.Sprintf("output duration %.3fs differs from source %.3fs by more than %.3fs", outputDuration, sourceDuration, tolerance))
	}
}

func validateVideo(request Request, outputProbe domain.ProbeResult, result *domain.ValidationResult) {
	outputVideos := streamsByType(outputProbe.Streams, "video")
	result.OutputVideoStreamCount = len(outputVideos)
	if len(outputVideos) == 0 {
		addError(result, "output has no video streams")
		return
	}

	outputVideo := outputVideos[0]
	result.OutputVideoCodec = outputVideo.Codec
	result.OutputVideoPixelFormat = outputVideo.PixelFormat

	expectedCodec, codecOK := expectedVideoCodec(request)
	result.ExpectedVideoCodec = expectedCodec
	if !codecOK {
		addError(result, "source video stream is unavailable for video-copy validation")
	} else if expectedCodec != "" && normalizeCodec(outputVideo.Codec) != expectedCodec {
		addError(result, fmt.Sprintf("output video codec %q does not match expected %q", outputVideo.Codec, expectedCodec))
	}

	if videoCopy(request) {
		return
	}
	expectedPixelFormat := expectedVideoPixelFormat(request)
	result.ExpectedVideoPixelFormat = expectedPixelFormat
	if expectedPixelFormat == "" {
		return
	}
	if strings.TrimSpace(outputVideo.PixelFormat) == "" {
		addError(result, fmt.Sprintf("output video pixel format is unavailable; expected %q", expectedPixelFormat))
		return
	}
	if !strings.EqualFold(strings.TrimSpace(outputVideo.PixelFormat), expectedPixelFormat) {
		addError(result, fmt.Sprintf("output video pixel format %q does not match expected %q", outputVideo.PixelFormat, expectedPixelFormat))
	}
}

func validateMarker(request Request, outputProbe domain.ProbeResult, result *domain.ValidationResult) {
	videoCodec := request.Profile.Video.Codec
	pixelFormat := request.Profile.Video.PixelFormat
	if request.EncodePlan != nil {
		videoCodec = request.EncodePlan.VideoCodec
		pixelFormat = request.EncodePlan.PixelFormat
	}
	match := marker.DetectVideo(outputProbe, request.Profile.Name, videoCodec, pixelFormat)
	processed := marker.DetectProcessed(outputProbe, request.Profile)
	result.AnvilProcessedMarkerPresent = len(processed.Tags) > 0

	if match.Compatible {
		result.AnvilMarkerCompatible = true
		return
	}
	if videoCopy(request) {
		if len(match.Tags) > 0 {
			addError(result, fmt.Sprintf("output encoded Anvil marker is incompatible with profile %q", request.Profile.Name))
			return
		}
		if len(processed.Tags) == 0 {
			addError(result, "output Anvil processed marker is missing or not truthy on the video stream")
			return
		}
		if !processed.Compatible {
			addError(result, fmt.Sprintf("output Anvil processed marker is incompatible with profile %q", request.Profile.Name))
			return
		}
		if !validVideoCopyAction(processed.Tags[marker.TagVideoAction]) {
			addError(result, fmt.Sprintf("output Anvil video action %q is incompatible with video-copy validation", processed.Tags[marker.TagVideoAction]))
			return
		}
		result.AnvilMarkerCompatible = true
		return
	}
	if len(match.Tags) > 0 {
		addError(result, fmt.Sprintf("output Anvil marker is incompatible with profile %q", request.Profile.Name))
		return
	}
	addError(result, "output Anvil marker is missing or not truthy on the video stream")
}

func validateAudio(request Request, outputProbe domain.ProbeResult, result *domain.ValidationResult) {
	if request.SourceProbe != nil {
		result.SourceAudioStreamCount = countStreams(request.SourceProbe.Streams, "audio")
	}
	result.OutputAudioStreamCount = countStreams(outputProbe.Streams, "audio")
	expected, ok := expectedAudioStreamCount(request, result.SourceAudioStreamCount)
	if !ok {
		return
	}
	result.ExpectedAudioStreamCount = expected
	if result.OutputAudioStreamCount != expected {
		addError(result, fmt.Sprintf("output audio stream count %d does not match expected %d", result.OutputAudioStreamCount, expected))
	}
}

func validateSubtitles(request Request, outputProbe domain.ProbeResult, result *domain.ValidationResult) {
	if request.SourceProbe == nil {
		return
	}
	result.SourceSubtitleStreamCount = countStreams(request.SourceProbe.Streams, "subtitle")
	result.OutputSubtitleStreamCount = countStreams(outputProbe.Streams, "subtitle")
	result.ExpectedSubtitleStreamCount = result.SourceSubtitleStreamCount
	if result.OutputSubtitleStreamCount != result.ExpectedSubtitleStreamCount {
		addError(result, fmt.Sprintf("output subtitle stream count %d does not match expected %d", result.OutputSubtitleStreamCount, result.ExpectedSubtitleStreamCount))
	}
}

func validateHDR(request Request, outputProbe domain.ProbeResult, result *domain.ValidationResult) {
	if request.SourceProbe == nil {
		return
	}
	sourceVideo, sourceOK := firstVideo(request.SourceProbe.Streams)
	outputVideo, outputOK := firstVideo(outputProbe.Streams)
	if !sourceOK || !outputOK {
		return
	}
	result.SourceHDRColorTransfer = sourceVideo.ColorTransfer
	result.OutputHDRColorTransfer = outputVideo.ColorTransfer
	result.SourceHDRColorPrimaries = sourceVideo.ColorPrimaries
	result.OutputHDRColorPrimaries = outputVideo.ColorPrimaries
	result.SourceDolbyVisionPresent = sourceVideo.DolbyVision != nil
	result.OutputDolbyVisionPresent = outputVideo.DolbyVision != nil

	for _, field := range []struct {
		name   string
		source string
		output string
	}{
		{name: "color transfer", source: sourceVideo.ColorTransfer, output: outputVideo.ColorTransfer},
		{name: "color primaries", source: sourceVideo.ColorPrimaries, output: outputVideo.ColorPrimaries},
		{name: "color space", source: sourceVideo.ColorSpace, output: outputVideo.ColorSpace},
	} {
		if strings.TrimSpace(field.source) == "" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(field.source), strings.TrimSpace(field.output)) {
			addError(result, fmt.Sprintf("output HDR %s %q does not match source %q", field.name, field.output, field.source))
		}
	}

	if sourceVideo.DolbyVision == nil || request.Profile.Video.DolbyVision.Mode == domain.DolbyVisionModeOff {
		return
	}
	if outputVideo.DolbyVision == nil {
		addError(result, "output Dolby Vision metadata is missing")
	}
}

func computeSizeMetrics(result *domain.ValidationResult) {
	if result.SourceSizeBytes <= 0 {
		return
	}
	result.SizeSavingsBytes = result.SourceSizeBytes - result.OutputSizeBytes
	result.SizeSavingsPercent = float64(result.SizeSavingsBytes) / float64(result.SourceSizeBytes) * 100
}

func sourceSize(request Request) int64 {
	if request.SourceProbe != nil && request.SourceProbe.SizeBytes > 0 {
		return request.SourceProbe.SizeBytes
	}
	sourcePath := strings.TrimSpace(request.SourcePath)
	if sourcePath == "" {
		return 0
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return 0
	}
	if info.Size() <= 0 {
		return 0
	}
	return info.Size()
}

func expectedVideoCodec(request Request) (string, bool) {
	if videoCopy(request) {
		if request.SourceProbe == nil {
			return "", false
		}
		sourceVideos := streamsByType(request.SourceProbe.Streams, "video")
		if len(sourceVideos) == 0 {
			return "", false
		}
		return normalizeCodec(sourceVideos[0].Codec), true
	}
	codec := ""
	if request.EncodePlan != nil {
		codec = request.EncodePlan.VideoCodec
	}
	if strings.TrimSpace(codec) == "" {
		codec = request.Profile.Video.Codec
	}
	if strings.TrimSpace(codec) == "" {
		codec = "av1"
	}
	return encodeIntentCodec(codec), true
}

func expectedVideoPixelFormat(request Request) string {
	pixelFormat := ""
	if request.EncodePlan != nil {
		pixelFormat = request.EncodePlan.PixelFormat
	}
	if strings.TrimSpace(pixelFormat) == "" {
		pixelFormat = request.Profile.Video.PixelFormat
	}
	return strings.TrimSpace(pixelFormat)
}

func expectedAudioStreamCount(request Request, sourceAudioCount int) (int, bool) {
	if request.EncodePlan != nil && request.EncodePlan.AudioSelectionApplied {
		return len(request.EncodePlan.AudioStreamIndexes), true
	}
	if request.EncodePlan == nil && request.Audio != nil {
		return len(request.Audio.StreamIndexes), true
	}
	if request.SourceProbe != nil {
		return sourceAudioCount, true
	}
	return 0, false
}

func videoCopy(request Request) bool {
	if request.EncodePlan != nil {
		return request.EncodePlan.VideoCopy
	}
	return request.Metadata.VideoAlreadyEncoded
}

func validVideoCopyAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", marker.VideoActionCopy, marker.VideoActionRemux:
		return true
	default:
		return false
	}
}

func encodeIntentCodec(codec string) string {
	return videocodec.CanonicalCodec(codec)
}

func normalizeCodec(codec string) string {
	codec = strings.ToLower(strings.TrimSpace(codec))
	codec = strings.ReplaceAll(codec, "_", "-")
	return codec
}

func streamsByType(streams []domain.MediaStream, streamType string) []domain.MediaStream {
	var result []domain.MediaStream
	for _, stream := range streams {
		if stream.Type == streamType {
			result = append(result, stream)
		}
	}
	return result
}

func firstVideo(streams []domain.MediaStream) (domain.MediaStream, bool) {
	for _, stream := range streams {
		if stream.Type == "video" {
			return stream, true
		}
	}
	return domain.MediaStream{}, false
}

func countStreams(streams []domain.MediaStream, streamType string) int {
	count := 0
	for _, stream := range streams {
		if stream.Type == streamType {
			count++
		}
	}
	return count
}

func addError(result *domain.ValidationResult, message string) {
	result.OK = false
	result.Errors = append(result.Errors, message)
}

type Block struct {
	Validator Validator
}

func (Block) Name() string {
	return "validate"
}

func (b Block) Run(ctx context.Context, job *pipeline.JobContext) error {
	result, err := b.Validator.Validate(ctx, RequestFromJob(job))
	job.Validation = &result
	if err != nil {
		slog.Warn("validation observations recorded; accepting encode authority", "error", err, "errors", result.Errors)
	}
	return nil
}
