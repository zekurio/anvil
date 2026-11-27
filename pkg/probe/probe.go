package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
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
		Name:              binary,
		RequireFullStdout: true,
		Args:              []string{"-v", "error", "-print_format", "json", "-show_format", "-show_streams", path},
	})
	if err != nil {
		return domain.ProbeResult{}, fmt.Errorf("run ffprobe for %q: %w", path, err)
	}
	probed, err := parseFFProbe(path, result.Stdout)
	if err != nil {
		return domain.ProbeResult{}, fmt.Errorf("parse ffprobe output for %q: %w", path, err)
	}
	if err := probeVideoFrames(ctx, runner, binary, &probed); err != nil {
		return domain.ProbeResult{}, err
	}
	return probed, nil
}

// frameProbePackets bounds the frame probe. A few packets get past a leading
// frame that failed to decode or carries no side data, without decoding
// more than a fraction of a second.
const frameProbePackets = 8

// frameArgs decodes the first packets of one stream and prints only their
// frame side data.
func frameArgs(path string, streamIndex int) []string {
	return []string{"-v", "error", "-print_format", "json",
		"-select_streams", strconv.Itoa(streamIndex),
		"-read_intervals", "%+#" + strconv.Itoa(frameProbePackets),
		"-show_frames", "-show_entries", "frame=side_data_list", path}
}

// probeVideoFrames adds frame-level HDR signalling to the primary video
// stream. HDR10 static metadata often lives only in SEI messages, and
// remuxes made without Dolby Vision support (FFmpeg's MP4 muxer without
// -strict unofficial, for one) keep the RPU but drop the configuration
// record, so the container alone misses both.
func probeVideoFrames(ctx context.Context, runner process.Runner, binary string, probed *domain.ProbeResult) error {
	var stream *domain.MediaStream
	for i := range probed.Streams {
		if probed.Streams[i].Type == "video" && !probed.Streams[i].AttachedPic() {
			stream = &probed.Streams[i]
			break
		}
	}
	if stream == nil {
		return nil
	}
	result, err := runner.Run(ctx, process.Command{
		Name:              binary,
		RequireFullStdout: true,
		Args:              frameArgs(probed.Path, stream.Index),
	})
	if err != nil {
		return fmt.Errorf("run ffprobe frame probe for %q: %w", probed.Path, err)
	}
	var raw struct {
		Frames []struct {
			SideData []ffprobeSideData `json:"side_data_list"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(result.Stdout, &raw); err != nil {
		return fmt.Errorf("parse ffprobe frame probe for %q: %w", probed.Path, err)
	}
	for _, frame := range raw.Frames {
		applySideData(stream, frame.SideData)
	}
	return nil
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
	return nil
}

// ffprobeSideData covers both stream side data (the Dolby Vision
// configuration record) and frame side data (the parsed RPU header). Types
// are matched by FFmpeg's side data names.
type ffprobeSideData struct {
	Type              string `json:"side_data_type"`
	DVProfile         int    `json:"dv_profile"`
	DVLevel           int    `json:"dv_level"`
	ELPresent         int    `json:"el_present_flag"`
	BLCompatibilityID int    `json:"dv_bl_signal_compatibility_id"`
	VDRRPUProfile     int    `json:"vdr_rpu_profile"`
	BLFullRange       int    `json:"bl_video_full_range_flag"`
}

const (
	sideDataDolbyVisionRecord = "DOVI configuration record"
	sideDataDolbyVisionRPU    = "Dolby Vision Metadata"
	sideDataMasteringDisplay  = "Mastering display metadata"
	sideDataContentLight      = "Content light level metadata"
)

func applySideData(stream *domain.MediaStream, sideData []ffprobeSideData) {
	for _, data := range sideData {
		switch data.Type {
		case sideDataMasteringDisplay:
			stream.MasteringDisplay = true
		case sideDataContentLight:
			stream.ContentLightLevel = true
		case sideDataDolbyVisionRecord:
			dv := dolbyVision(stream)
			dv.ConfigurationRecord = true
			dv.Profile = data.DVProfile
			dv.Level = data.DVLevel
			dv.BLCompatibilityID = data.BLCompatibilityID
			dv.EnhancementLayer = data.ELPresent != 0
		case sideDataDolbyVisionRPU:
			dv := dolbyVision(stream)
			if !dv.RPU && !dv.ConfigurationRecord {
				guessDolbyVision(dv, *stream, data)
			}
			dv.RPU = true
		}
	}
}

func dolbyVision(stream *domain.MediaStream) *domain.DolbyVision {
	if stream.DolbyVision == nil {
		stream.DolbyVision = &domain.DolbyVision{}
	}
	return stream.DolbyVision
}

// guessDolbyVision fills in what a missing configuration record would have
// said, the way FFmpeg guesses it: an RPU with vdr_rpu_profile 0 and a
// full-range base layer is profile 5. Otherwise the base layer is whatever
// its color tags say, and unknown tags leave it marked incompatible.
func guessDolbyVision(dv *domain.DolbyVision, stream domain.MediaStream, rpu ffprobeSideData) {
	if rpu.VDRRPUProfile == 0 && rpu.BLFullRange != 0 {
		dv.Profile, dv.BLCompatibilityID = 5, 0
		return
	}
	switch stream.Codec {
	case "av1":
		dv.Profile = 10
	case "h264":
		dv.Profile = 9
	default:
		dv.Profile = 8
	}
	switch stream.ColorTransfer {
	case "smpte2084":
		dv.BLCompatibilityID = 1
	case "arib-std-b67":
		dv.BLCompatibilityID = 4
	case "bt709":
		dv.BLCompatibilityID = 2
	default:
		dv.BLCompatibilityID = 0
	}
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
		SideData       []ffprobeSideData `json:"side_data_list"`
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
		parsed := domain.MediaStream{
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
		}
		applySideData(&parsed, stream.SideData)
		probed.Streams = append(probed.Streams, parsed)
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

func copyTags(tags map[string]string) map[string]string {
	copied := make(map[string]string, len(tags))
	for key, value := range tags {
		copied[key] = value
	}
	return copied
}
