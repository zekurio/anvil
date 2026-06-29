package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/marker"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
	videocodec "github.com/zekurio/anvil/pkg/video"
)

type Prober interface {
	Probe(ctx context.Context, path string) (domain.ProbeResult, error)
}

type FFProbe struct {
	Runner process.Runner
	Binary string
}

func (p FFProbe) Probe(ctx context.Context, path string) (domain.ProbeResult, error) {
	if path == "" {
		return domain.ProbeResult{}, errors.New("probe path is required")
	}
	runner := p.Runner
	if runner == nil {
		runner = process.OSRunner{}
	}
	binary := p.Binary
	if binary == "" {
		binary = "ffprobe"
	}
	result, err := runner.Run(ctx, process.Command{
		Name: binary,
		Args: []string{"-v", "error", "-print_format", "json", "-show_format", "-show_streams", path},
	})
	if err != nil {
		return domain.ProbeResult{}, err
	}
	probed, err := parseFFProbe(path, result.Stdout)
	if err != nil {
		return domain.ProbeResult{}, err
	}
	return probed, nil
}

type Block struct {
	Prober          Prober
	DolbyVisionTool DolbyVisionToolChecker
}

func (Block) Name() string {
	return "probe"
}

func (b Block) Run(ctx context.Context, job *pipeline.JobContext) error {
	prober := b.Prober
	if prober == nil {
		prober = FFProbe{}
	}
	result, err := prober.Probe(ctx, job.InputPath)
	if err != nil {
		return err
	}
	job.Probe = &result
	job.Metadata.HDR = hdrMetadata(result)
	if err := b.configureDolbyVision(ctx, job); err != nil {
		return err
	}
	video := domain.EffectiveVideoProfile(job.Profile, job.Metadata)
	match := marker.DetectVideo(result, job.Profile.Name, video.Codec, videocodec.SoftwarePixelFormat(video.BitDepth))
	if match.Compatible {
		job.Metadata.VideoAlreadyEncoded = true
		job.Metadata.AnvilTags = match.Tags
		if match.CropFilter != "" {
			job.Metadata.CropFilter = match.CropFilter
		}
	}
	return nil
}

type ffprobeJSON struct {
	Streams []struct {
		Index          int               `json:"index"`
		CodecType      string            `json:"codec_type"`
		CodecName      string            `json:"codec_name"`
		Width          int               `json:"width"`
		Height         int               `json:"height"`
		PixelFormat    string            `json:"pix_fmt"`
		BitRate        string            `json:"bit_rate"`
		Channels       int               `json:"channels"`
		ChannelLayout  string            `json:"channel_layout"`
		ColorRange     string            `json:"color_range"`
		ColorSpace     string            `json:"color_space"`
		ColorTransfer  string            `json:"color_transfer"`
		ColorPrimaries string            `json:"color_primaries"`
		Tags           map[string]string `json:"tags"`
		Disposition    map[string]int    `json:"disposition"`
		SideDataList   []struct {
			SideDataType              string `json:"side_data_type"`
			DVProfile                 int    `json:"dv_profile"`
			DVLevel                   int    `json:"dv_level"`
			RPUPresentFlag            int    `json:"rpu_present_flag"`
			ELPresentFlag             int    `json:"el_present_flag"`
			BLPresentFlag             int    `json:"bl_present_flag"`
			DVBLSignalCompatibilityID int    `json:"dv_bl_signal_compatibility_id"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		Size       string `json:"size"`
	} `json:"format"`
}

func parseFFProbe(path string, data []byte) (domain.ProbeResult, error) {
	var raw ffprobeJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return domain.ProbeResult{}, fmt.Errorf("parse ffprobe json: %w", err)
	}
	duration, _ := strconv.ParseFloat(raw.Format.Duration, 64)
	size, _ := strconv.ParseInt(raw.Format.Size, 10, 64)
	probed := domain.ProbeResult{
		Path:            path,
		FormatName:      raw.Format.FormatName,
		DurationSeconds: duration,
		SizeBytes:       size,
		Streams:         make([]domain.MediaStream, 0, len(raw.Streams)),
	}
	for _, stream := range raw.Streams {
		disposition := make(map[string]bool, len(stream.Disposition))
		for key, value := range stream.Disposition {
			disposition[key] = value != 0
		}
		bitRate, _ := strconv.ParseInt(stream.BitRate, 10, 64)
		probed.Streams = append(probed.Streams, domain.MediaStream{
			Index:          stream.Index,
			Type:           stream.CodecType,
			Codec:          stream.CodecName,
			Width:          stream.Width,
			Height:         stream.Height,
			PixelFormat:    stream.PixelFormat,
			BitRate:        bitRate,
			Channels:       stream.Channels,
			ChannelLayout:  stream.ChannelLayout,
			ColorRange:     stream.ColorRange,
			ColorSpace:     stream.ColorSpace,
			ColorTransfer:  stream.ColorTransfer,
			ColorPrimaries: stream.ColorPrimaries,
			DolbyVision:    dolbyVisionMetadata(stream.SideDataList),
			Language:       stream.Tags["language"],
			Title:          stream.Tags["title"],
			Tags:           copyTags(stream.Tags),
			Disposition:    disposition,
		})
	}
	return probed, nil
}

type DolbyVisionToolChecker interface {
	Available(ctx context.Context) (bool, string, error)
}

type DoviTool struct {
	Runner process.Runner
	Binary string
}

func (d DoviTool) Available(ctx context.Context) (bool, string, error) {
	runner := d.Runner
	if runner == nil {
		runner = process.OSRunner{}
	}
	binary := d.Binary
	if binary == "" {
		binary = "dovi_tool"
	}
	result, err := runner.Run(ctx, process.Command{Name: binary, Args: []string{"--version"}})
	output := strings.TrimSpace(strings.Join([]string{string(result.Stdout), string(result.Stderr)}, "\n"))
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return false, output, ctx.Err()
		}
		return false, output, nil
	}
	return true, output, nil
}

func (b Block) configureDolbyVision(ctx context.Context, job *pipeline.JobContext) error {
	if job == nil || job.Metadata.HDR.DolbyVision == nil {
		return nil
	}
	policy := job.Profile.Video.DolbyVision
	switch policy.Mode {
	case domain.DolbyVisionModeOff:
		job.Metadata.HDR.DolbyVisionReason = "source Dolby Vision detected, but Dolby Vision override mode is off"
		return nil
	case "", domain.DolbyVisionModeAuto, domain.DolbyVisionModeRequire:
	default:
		job.Metadata.HDR.DolbyVisionReason = "source Dolby Vision detected, but Dolby Vision override mode is invalid"
		return nil
	}
	if strings.TrimSpace(policy.Codec) == "" {
		job.Metadata.HDR.DolbyVisionReason = "source Dolby Vision detected, but video.dolby_vision.codec is not configured"
		if policy.Mode == domain.DolbyVisionModeRequire {
			return errors.New(job.Metadata.HDR.DolbyVisionReason)
		}
		return nil
	}
	checker := b.DolbyVisionTool
	if checker == nil {
		checker = DoviTool{}
	}
	available, details, err := checker.Available(ctx)
	if err != nil {
		return fmt.Errorf("check dovi_tool availability: %w", err)
	}
	job.Metadata.HDR.DolbyVisionToolAvailable = available
	if !available {
		job.Metadata.HDR.DolbyVisionReason = "source Dolby Vision detected, but dovi_tool is not available"
		if strings.TrimSpace(details) != "" {
			job.Metadata.HDR.DolbyVisionReason += ": " + strings.TrimSpace(details)
		}
		if policy.Mode == domain.DolbyVisionModeRequire {
			return errors.New(job.Metadata.HDR.DolbyVisionReason)
		}
		return nil
	}
	job.Metadata.HDR.DolbyVisionEncoderSelected = true
	job.Metadata.HDR.DolbyVisionReason = "source Dolby Vision detected and dovi_tool is available"
	return nil
}

func hdrMetadata(result domain.ProbeResult) domain.HDRMetadata {
	for _, stream := range result.Streams {
		if stream.Type != "video" {
			continue
		}
		metadata := domain.HDRMetadata{
			ColorRange:     stream.ColorRange,
			ColorSpace:     stream.ColorSpace,
			ColorTransfer:  stream.ColorTransfer,
			ColorPrimaries: stream.ColorPrimaries,
		}
		if stream.DolbyVision != nil {
			copied := *stream.DolbyVision
			metadata.DolbyVision = &copied
		}
		return metadata
	}
	return domain.HDRMetadata{}
}

func dolbyVisionMetadata(sideDataList []struct {
	SideDataType              string `json:"side_data_type"`
	DVProfile                 int    `json:"dv_profile"`
	DVLevel                   int    `json:"dv_level"`
	RPUPresentFlag            int    `json:"rpu_present_flag"`
	ELPresentFlag             int    `json:"el_present_flag"`
	BLPresentFlag             int    `json:"bl_present_flag"`
	DVBLSignalCompatibilityID int    `json:"dv_bl_signal_compatibility_id"`
}) *domain.DolbyVisionMetadata {
	for _, sideData := range sideDataList {
		if !strings.EqualFold(strings.TrimSpace(sideData.SideDataType), "DOVI configuration record") {
			continue
		}
		return &domain.DolbyVisionMetadata{
			Profile:                  sideData.DVProfile,
			Level:                    sideData.DVLevel,
			RPUPresent:               sideData.RPUPresentFlag != 0,
			ELPresent:                sideData.ELPresentFlag != 0,
			BLPresent:                sideData.BLPresentFlag != 0,
			BLSignalCompatibilityID:  sideData.DVBLSignalCompatibilityID,
			ConfigurationRecordFound: true,
		}
	}
	return nil
}

func copyTags(tags map[string]string) map[string]string {
	copied := make(map[string]string, len(tags))
	for key, value := range tags {
		copied[key] = value
	}
	return copied
}
