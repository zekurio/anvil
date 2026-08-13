package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

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
		return domain.ProbeResult{}, fmt.Errorf("run ffprobe for %q: %w", path, err)
	}
	probed, err := parseFFProbe(path, result.Stdout)
	if err != nil {
		return domain.ProbeResult{}, fmt.Errorf("parse ffprobe output for %q: %w", path, err)
	}
	return probed, nil
}

type Block struct {
	Prober Prober
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
	sourceCodec := ""
	if stream, ok := domain.PrimaryVideoStream(result.Streams); ok {
		sourceCodec = stream.Codec
	}
	video := domain.EffectiveVideoProfile(job.Profile, job.Metadata, sourceCodec)
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
	duration := parseFFProbeFloat(raw.Format.Duration)
	size := parseFFProbeInt(raw.Format.Size)
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
		bitRate := parseFFProbeInt(stream.BitRate)
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
			Language:       stream.Tags["language"],
			Title:          stream.Tags["title"],
			Tags:           copyTags(stream.Tags),
			Disposition:    disposition,
		})
	}
	return probed, nil
}

func parseFFProbeFloat(value string) float64 {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func parseFFProbeInt(value string) int64 {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func hdrMetadata(result domain.ProbeResult) domain.HDRMetadata {
	stream, ok := domain.PrimaryVideoStream(result.Streams)
	if !ok {
		return domain.HDRMetadata{}
	}
	return domain.HDRMetadata{
		ColorRange:     stream.ColorRange,
		ColorSpace:     stream.ColorSpace,
		ColorTransfer:  stream.ColorTransfer,
		ColorPrimaries: stream.ColorPrimaries,
	}
}

func copyTags(tags map[string]string) map[string]string {
	copied := make(map[string]string, len(tags))
	for key, value := range tags {
		copied[key] = value
	}
	return copied
}
