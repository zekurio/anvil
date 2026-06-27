package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
)

type Encoder struct {
	Runner process.Runner
	Binary string
}

func (e Encoder) Encode(ctx context.Context, plan domain.EncodePlan) (process.Result, error) {
	if plan.InputPath == "" {
		return process.Result{}, errors.New("encode input path is required")
	}
	if plan.OutputPath == "" {
		return process.Result{}, errors.New("encode output path is required")
	}
	runner := e.Runner
	if runner == nil {
		runner = process.OSRunner{}
	}
	binary := e.Binary
	if binary == "" {
		binary = "ffmpeg"
	}
	return runner.Run(ctx, process.Command{Name: binary, Args: Args(plan)})
}

func BuildPlan(profile domain.Profile, inputPath string, outputPath string, allocation domain.ResourceAllocation, search *domain.SearchResult) (domain.EncodePlan, error) {
	if inputPath == "" {
		return domain.EncodePlan{}, errors.New("input path is required")
	}
	if outputPath == "" {
		return domain.EncodePlan{}, errors.New("output path is required")
	}
	crf := profile.Video.CRFMin
	if search != nil && search.CRF > 0 {
		crf = search.CRF
	}
	return domain.EncodePlan{
		InputPath:      inputPath,
		OutputPath:     outputPath,
		VideoCodec:     profile.Video.Codec,
		Preset:         profile.Video.Preset,
		PixelFormat:    profile.Video.PixelFormat,
		CRF:            crf,
		CRFMin:         profile.Video.CRFMin,
		CRFMax:         profile.Video.CRFMax,
		TargetVMAF:     profile.Video.TargetVMAF,
		Threads:        allocation.Threads,
		Container:      profile.Container,
		AudioMode:      profile.Audio.Mode,
		SubtitleMode:   profile.Subtitles.Mode,
		MetadataMode:   profile.Metadata.Mode,
		AttachmentMode: profile.Attachments.Mode,
		ChapterMode:    profile.Chapters.Mode,
	}, nil
}

func Args(plan domain.EncodePlan) []string {
	args := []string{
		"-hide_banner",
		"-y",
		"-i", plan.InputPath,
		"-map", "0",
		"-c:v", valueOr(plan.VideoCodec, "libsvtav1"),
		"-crf", strconv.Itoa(plan.CRF),
	}
	if plan.Preset != "" {
		args = append(args, "-preset", plan.Preset)
	}
	if plan.PixelFormat != "" {
		args = append(args, "-pix_fmt", plan.PixelFormat)
	}
	if plan.Threads > 0 {
		args = append(args, "-threads", strconv.Itoa(plan.Threads))
	}
	args = append(args, audioArgs(plan.AudioMode)...)
	args = append(args, subtitleArgs(plan.SubtitleMode)...)
	if plan.MetadataMode == domain.MetadataModeStrip {
		args = append(args, "-map_metadata", "-1")
	}
	if plan.AttachmentMode == domain.MetadataModeStrip {
		args = append(args, "-map", "-0:t?")
	}
	if plan.ChapterMode == domain.MetadataModeStrip {
		args = append(args, "-map_chapters", "-1")
	}
	args = append(args, plan.OutputPath)
	return args
}

type Block struct {
	Encoder Encoder
}

func (Block) Name() string {
	return "encode"
}

func (b Block) Run(ctx context.Context, job *pipeline.JobContext) error {
	plan, err := BuildPlan(job.Profile, job.InputPath, job.OutputPath, job.Resources, job.Search)
	if err != nil {
		return err
	}
	job.EncodePlan = &plan
	_, err = b.Encoder.Encode(ctx, plan)
	if err != nil {
		return fmt.Errorf("ffmpeg encode: %w", err)
	}
	return nil
}

func audioArgs(mode domain.StreamPolicyMode) []string {
	switch mode {
	case domain.StreamPolicyCleanup:
		return []string{"-c:a", "copy"}
	default:
		return []string{"-c:a", "copy"}
	}
}

func subtitleArgs(mode domain.StreamPolicyMode) []string {
	switch mode {
	case domain.StreamPolicyCleanup:
		return []string{"-c:s", "copy"}
	default:
		return []string{"-c:s", "copy"}
	}
}

func valueOr(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
