package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/language"
	"github.com/zekurio/anvil/pkg/marker"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
	videocodec "github.com/zekurio/anvil/pkg/video"
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

func BuildPlan(profile domain.Profile, inputPath string, outputPath string, allocation domain.ResourceAllocation, search *domain.SearchResult, audio *domain.AudioSelection, metadata domain.JobMetadata) (domain.EncodePlan, error) {
	return BuildPlanWithProbe(profile, inputPath, outputPath, allocation, search, audio, metadata, nil)
}

func BuildPlanWithProbe(profile domain.Profile, inputPath string, outputPath string, allocation domain.ResourceAllocation, search *domain.SearchResult, audio *domain.AudioSelection, metadata domain.JobMetadata, probe *domain.ProbeResult) (domain.EncodePlan, error) {
	if inputPath == "" {
		return domain.EncodePlan{}, errors.New("input path is required")
	}
	if outputPath == "" {
		return domain.EncodePlan{}, errors.New("output path is required")
	}
	videoCopy := metadata.VideoAlreadyEncoded
	videoCopyReason := ""
	if metadata.VideoAlreadyEncoded {
		videoCopyReason = "compatible Anvil video marker"
	}
	if search != nil && search.SkipVideoEncode {
		videoCopy = true
		videoCopyReason = search.VideoEncodeSkipReason
		if videoCopyReason == "" {
			videoCopyReason = "CRF search skipped video encode"
		}
	}
	video := domain.EffectiveVideoProfile(profile, metadata)
	inputVideoCodec, inputWidth, inputHeight := inputVideo(probe)
	crf := 0
	if !videoCopy {
		crf = video.CRFMin
		if search != nil && search.CRF > 0 {
			crf = search.CRF
		}
	}
	plan := domain.EncodePlan{
		InputPath:          inputPath,
		OutputPath:         outputPath,
		ProfileName:        profile.Name,
		VideoCodec:         videocodec.ResolveEncoder(video.Codec, video.Accelerator),
		InputVideoCodec:    inputVideoCodec,
		InputWidth:         inputWidth,
		InputHeight:        inputHeight,
		Accelerator:        videocodec.ResolveAccelerator(video.Accelerator),
		VideoSource:        domain.EffectiveVideoSource(metadata),
		VideoCopy:          videoCopy,
		VideoCopyReason:    videoCopyReason,
		Preset:             video.Preset,
		PixelFormat:        video.PixelFormat,
		CRF:                crf,
		CRFMin:             video.CRFMin,
		CRFMax:             video.CRFMax,
		TargetVMAF:         video.TargetVMAF,
		MinSavingsPercent:  video.MinSavingsPercent,
		ForceEncodeOnNoFit: video.ForceEncodeOnNoFit,
		Threads:            allocation.Threads,
		Container:          profile.Container,
		CropFilter:         metadata.CropFilter,
		SubtitleMode:       profile.Subtitles.Mode,
		MetadataMode:       profile.Metadata.Mode,
		TrackTitleMode:     trackTitleModeOrDefault(profile.Metadata.TrackTitles),
		AttachmentMode:     profile.Attachments.Mode,
		ChapterMode:        profile.Chapters.Mode,
		AnvilTags:          copyTags(metadata.AnvilTags),
		FFmpegArgs:         append([]string(nil), video.FFmpegArgs...),
		ABAV1Args:          append([]string(nil), video.ABAV1Args...),
		HDR:                metadata.HDR,
	}
	if audio != nil {
		plan.AudioSelectionApplied = true
		plan.AudioStreamIndexes = append([]int(nil), audio.StreamIndexes...)
	}
	plan.TrackTitles = standardizedTrackTitles(plan, probe)
	return plan, nil
}

func Args(plan domain.EncodePlan) []string {
	args := []string{
		"-hide_banner",
		"-y",
	}
	args = append(args, inputArgs(plan)...)
	args = append(args, "-i", plan.InputPath)
	args = append(args, mapArgs(plan)...)
	if filter := videoFilter(plan); filter != "" && !plan.VideoCopy {
		args = append(args, "-vf", filter)
	}
	args = append(args, videoArgs(plan)...)
	if !plan.VideoCopy && len(plan.FFmpegArgs) > 0 {
		args = append(args, plan.FFmpegArgs...)
	}
	args = append(args, audioArgs()...)
	args = append(args, subtitleArgs(plan.SubtitleMode)...)
	if plan.MetadataMode == domain.MetadataModeStrip {
		args = append(args, "-map_metadata", "-1")
	}
	args = append(args, trackTitleArgs(plan)...)
	args = append(args, anvilMetadataArgs(plan)...)
	if plan.AttachmentMode == domain.MetadataModeStrip {
		args = append(args, "-dn")
	} else {
		args = append(args, "-c:t", "copy")
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
	plan, err := BuildPlanWithProbe(job.Profile, job.InputPath, job.OutputPath, job.Resources, job.Search, job.Audio, job.Metadata, job.Probe)
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

type DolbyVisionBlock struct {
	Runner     process.Runner
	DoviTool   string
	MKVExtract string
	MKVMerge   string
	MKVInfo    string
	FFmpeg     string
}

func (DolbyVisionBlock) Name() string {
	return "dovi-fix"
}

func (b DolbyVisionBlock) Run(ctx context.Context, job *pipeline.JobContext) error {
	if !needsDolbyVisionFix(job) {
		return nil
	}
	if strings.TrimSpace(job.OutputPath) == "" {
		return errors.New("Dolby Vision fix output path is required")
	}
	if !strings.EqualFold(filepath.Ext(job.OutputPath), ".mkv") {
		return fmt.Errorf("Dolby Vision fix requires MKV output, got %q", filepath.Ext(job.OutputPath))
	}
	codec := job.EncodePlan.VideoCodec
	if strings.TrimSpace(codec) == "" {
		codec = job.Profile.Video.DolbyVision.Codec
	}
	if !hevcEncoder(codec) {
		return fmt.Errorf("Dolby Vision fix requires HEVC output, got encoder %q", codec)
	}

	dir := strings.TrimSpace(job.StagingDir)
	if dir == "" {
		dir = filepath.Dir(job.OutputPath)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create Dolby Vision staging dir: %w", err)
	}

	paths := doviWorkPaths(dir)
	defer cleanupDoviPaths(paths)

	originalForRPU := job.InputPath
	if !strings.EqualFold(filepath.Ext(job.InputPath), ".mkv") {
		if err := b.run(ctx, b.ffmpeg(), "-v", "quiet", "-stats", "-i", job.InputPath, "-c:v", "copy", paths.originalHEVC); err != nil {
			return err
		}
		originalForRPU = paths.originalHEVC
	}

	extractArgs := doviArgs(job, "--crop", "--mode", "2", "extract-rpu", "-o", paths.originalRPU, originalForRPU)
	if err := b.run(ctx, b.doviTool(), extractArgs...); err != nil {
		return err
	}
	if err := b.run(ctx, b.mkvExtract(), "tracks", job.OutputPath, "0:"+paths.convertedHEVC); err != nil {
		return err
	}
	injectArgs := doviArgs(job,
		"--crop", "--mode", "2",
		"inject-rpu",
		"--rpu-in", paths.originalRPU,
		"--input", paths.convertedHEVC,
		"--output", paths.fixedHEVC,
	)
	if err := b.run(ctx, b.doviTool(), injectArgs...); err != nil {
		return err
	}

	fps, err := b.outputFPS(ctx, job.OutputPath)
	if err != nil {
		return err
	}
	muxArgs := []string{"-o", paths.fixedMKV, paths.fixedHEVC, "-D", job.OutputPath, "--track-order", "1:0"}
	if fps != "" {
		muxArgs = append([]string{"--default-duration", "0:" + fps + "fps", "--fix-bitstream-timing-information", "0"}, muxArgs...)
	}
	if err := b.run(ctx, b.mkvMerge(), muxArgs...); err != nil {
		return err
	}
	if err := replaceOutput(job.OutputPath, paths.fixedMKV); err != nil {
		return err
	}
	return nil
}

type doviPaths struct {
	originalHEVC  string
	originalRPU   string
	convertedHEVC string
	fixedHEVC     string
	fixedMKV      string
}

func doviWorkPaths(dir string) doviPaths {
	return doviPaths{
		originalHEVC:  filepath.Join(dir, "dovi-original.hevc"),
		originalRPU:   filepath.Join(dir, "dovi-original.rpu"),
		convertedHEVC: filepath.Join(dir, "dovi-converted.hevc"),
		fixedHEVC:     filepath.Join(dir, "dovi-fixed.hevc"),
		fixedMKV:      filepath.Join(dir, "dovi-fixed.mkv"),
	}
}

func needsDolbyVisionFix(job *pipeline.JobContext) bool {
	return job != nil &&
		job.EncodePlan != nil &&
		!job.EncodePlan.VideoCopy &&
		job.Metadata.HDR.DolbyVision != nil &&
		job.Metadata.HDR.DolbyVisionEncoderSelected &&
		job.Profile.Video.DolbyVision.Mode != domain.DolbyVisionModeOff
}

func doviArgs(job *pipeline.JobContext, args ...string) []string {
	if job.Profile.Video.DolbyVision.RemoveHDR10Plus {
		return append([]string{"--drop-hdr10plus"}, args...)
	}
	return append([]string(nil), args...)
}

func (b DolbyVisionBlock) outputFPS(ctx context.Context, outputPath string) (string, error) {
	result, err := b.runner().Run(ctx, process.Command{
		Name: b.mkvInfo(),
		Args: []string{"--ui-language", "en_US", outputPath},
	})
	if err != nil {
		return "", fmt.Errorf("mkvinfo Dolby Vision output: %w", err)
	}
	matches := fpsPattern.FindStringSubmatch(string(result.Stdout))
	if len(matches) < 2 {
		return "", nil
	}
	return matches[1], nil
}

var fpsPattern = regexp.MustCompile(`(?i)([.0-9]+)\s+frames/fields`)

func (b DolbyVisionBlock) run(ctx context.Context, name string, args ...string) error {
	if _, err := b.runner().Run(ctx, process.Command{Name: name, Args: args}); err != nil {
		return fmt.Errorf("%s Dolby Vision fix: %w", filepath.Base(name), err)
	}
	return nil
}

func (b DolbyVisionBlock) runner() process.Runner {
	if b.Runner != nil {
		return b.Runner
	}
	return process.OSRunner{}
}

func (b DolbyVisionBlock) doviTool() string {
	return valueOr(b.DoviTool, "dovi_tool")
}

func (b DolbyVisionBlock) mkvExtract() string {
	return valueOr(b.MKVExtract, "mkvextract")
}

func (b DolbyVisionBlock) mkvMerge() string {
	return valueOr(b.MKVMerge, "mkvmerge")
}

func (b DolbyVisionBlock) mkvInfo() string {
	return valueOr(b.MKVInfo, "mkvinfo")
}

func (b DolbyVisionBlock) ffmpeg() string {
	return valueOr(b.FFmpeg, "ffmpeg")
}

func hevcEncoder(codec string) bool {
	codec = strings.ToLower(strings.TrimSpace(codec))
	codec = strings.ReplaceAll(codec, "_", "-")
	switch codec {
	case "hevc", "h265", "h.265", "libx265", "x265", "hevc-qsv", "hevc-nvenc", "hevc-amf", "hevc-videotoolbox":
		return true
	default:
		return false
	}
}

func replaceOutput(outputPath string, fixedPath string) error {
	backupPath := outputPath + ".pre-dovi"
	_ = os.Remove(backupPath)
	if err := os.Rename(outputPath, backupPath); err != nil {
		return fmt.Errorf("backup pre-Dolby Vision output: %w", err)
	}
	if err := os.Rename(fixedPath, outputPath); err != nil {
		_ = os.Rename(backupPath, outputPath)
		return fmt.Errorf("install Dolby Vision fixed output: %w", err)
	}
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove pre-Dolby Vision output backup: %w", err)
	}
	return nil
}

func cleanupDoviPaths(paths doviPaths) {
	for _, path := range []string{paths.originalHEVC, paths.originalRPU, paths.convertedHEVC, paths.fixedHEVC, paths.fixedMKV} {
		_ = os.Remove(path)
	}
}

func inputArgs(plan domain.EncodePlan) []string {
	if !qsvInputPipeline(plan) {
		return nil
	}
	decoder, ok := videocodec.QSVDecoder(plan.InputVideoCodec)
	if !ok {
		return nil
	}
	return []string{
		"-hwaccel", "qsv",
		"-hwaccel_output_format", "qsv",
		"-c:v", decoder,
	}
}

func videoFilter(plan domain.EncodePlan) string {
	if strings.TrimSpace(plan.CropFilter) == "" || videocodec.NoOpCrop(plan.CropFilter, plan.InputWidth, plan.InputHeight) {
		return ""
	}
	if qsvInputPipeline(plan) {
		if filter, ok := videocodec.QSVCropFilter(plan.CropFilter); ok {
			return filter
		}
	}
	return plan.CropFilter
}

func qsvInputPipeline(plan domain.EncodePlan) bool {
	if plan.VideoCopy {
		return false
	}
	if plan.Accelerator != videocodec.AcceleratorQSV {
		return false
	}
	if videocodec.EncoderAccelerator(plan.VideoCodec) != videocodec.AcceleratorQSV {
		return false
	}
	_, ok := videocodec.QSVDecoder(plan.InputVideoCodec)
	return ok
}

func inputVideo(probe *domain.ProbeResult) (string, int, int) {
	if probe == nil {
		return "", 0, 0
	}
	for _, stream := range probe.Streams {
		if stream.Type == "video" {
			return stream.Codec, stream.Width, stream.Height
		}
	}
	return "", 0, 0
}

func mapArgs(plan domain.EncodePlan) []string {
	args := []string{"-map", "0:v?"}
	if plan.AudioSelectionApplied {
		for _, streamIndex := range plan.AudioStreamIndexes {
			args = append(args, "-map", "0:"+strconv.Itoa(streamIndex))
		}
	} else {
		args = append(args, "-map", "0:a?")
	}
	args = append(args, "-map", "0:s?")
	if plan.AttachmentMode != domain.MetadataModeStrip {
		args = append(args, "-map", "0:t?")
	}
	return args
}

func videoArgs(plan domain.EncodePlan) []string {
	if plan.VideoCopy {
		return []string{"-c:v", "copy"}
	}
	args := []string{
		"-c:v", valueOr(plan.VideoCodec, "libsvtav1"),
	}
	args = append(args, qualityArgs(plan)...)
	if plan.Preset != "" {
		args = append(args, "-preset", plan.Preset)
	}
	if plan.PixelFormat != "" {
		args = append(args, "-pix_fmt", plan.PixelFormat)
	}
	if plan.Threads > 0 {
		args = append(args, "-threads", strconv.Itoa(plan.Threads))
	}
	return args
}

func qualityArgs(plan domain.EncodePlan) []string {
	if plan.CRF <= 0 {
		return nil
	}
	value := strconv.Itoa(plan.CRF)
	switch videocodec.EncoderAccelerator(plan.VideoCodec) {
	case videocodec.AcceleratorQSV, videocodec.AcceleratorVAAPI:
		return []string{"-global_quality", value}
	case videocodec.AcceleratorAMF:
		return []string{"-rc", "cqp", "-qp_i", value, "-qp_p", value, "-qp_b", value}
	default:
		return []string{"-crf", value}
	}
}

func audioArgs() []string {
	return []string{"-c:a", "copy"}
}

func subtitleArgs(mode domain.StreamPolicyMode) []string {
	switch mode {
	case domain.StreamPolicyCleanup:
		return []string{"-c:s", "copy"}
	default:
		return []string{"-c:s", "copy"}
	}
}

func trackTitleArgs(plan domain.EncodePlan) []string {
	switch trackTitleModeOrDefault(plan.TrackTitleMode) {
	case domain.TrackTitleModeStrip:
		return []string{"-metadata:s", "title="}
	case domain.TrackTitleModeStandardize:
		if len(plan.TrackTitles) > 0 {
			args := []string{"-metadata:s", "title="}
			for _, title := range plan.TrackTitles {
				if title.Type == "" || title.Title == "" {
					continue
				}
				args = append(args, "-metadata:s:"+title.Type+":"+strconv.Itoa(title.Index), "title="+title.Title)
			}
			if len(args) > 0 {
				return args
			}
		}
		return []string{
			"-metadata:s", "title=",
			"-metadata:s:v", "title=Video",
			"-metadata:s:a", "title=Audio",
			"-metadata:s:s", "title=Subtitle",
		}
	default:
		return nil
	}
}

func trackTitleModeOrDefault(mode domain.TrackTitleMode) domain.TrackTitleMode {
	if mode == "" {
		return domain.TrackTitleModeStrip
	}
	return mode
}

func standardizedTrackTitles(plan domain.EncodePlan, probe *domain.ProbeResult) []domain.TrackTitle {
	if trackTitleModeOrDefault(plan.TrackTitleMode) != domain.TrackTitleModeStandardize || probe == nil {
		return nil
	}
	var titles []domain.TrackTitle
	for outputIndex, stream := range streamsOfType(probe.Streams, "video") {
		titles = appendTrackTitle(titles, "v", outputIndex, videoTrackTitle(plan, stream))
	}
	for outputIndex, stream := range selectedAudioStreams(plan, probe.Streams) {
		titles = appendTrackTitle(titles, "a", outputIndex, audioTrackTitle(stream))
	}
	for outputIndex, stream := range streamsOfType(probe.Streams, "subtitle") {
		titles = appendTrackTitle(titles, "s", outputIndex, subtitleTrackTitle(stream))
	}
	return titles
}

func appendTrackTitle(titles []domain.TrackTitle, typ string, index int, title string) []domain.TrackTitle {
	title = strings.TrimSpace(title)
	if title == "" {
		return titles
	}
	return append(titles, domain.TrackTitle{Type: typ, Index: index, Title: title})
}

func streamsOfType(streams []domain.MediaStream, typ string) []domain.MediaStream {
	var selected []domain.MediaStream
	for _, stream := range streams {
		if stream.Type == typ {
			selected = append(selected, stream)
		}
	}
	return selected
}

func selectedAudioStreams(plan domain.EncodePlan, streams []domain.MediaStream) []domain.MediaStream {
	if !plan.AudioSelectionApplied {
		return streamsOfType(streams, "audio")
	}
	byIndex := make(map[int]domain.MediaStream)
	for _, stream := range streams {
		if stream.Type == "audio" {
			byIndex[stream.Index] = stream
		}
	}
	selected := make([]domain.MediaStream, 0, len(plan.AudioStreamIndexes))
	for _, index := range plan.AudioStreamIndexes {
		if stream, ok := byIndex[index]; ok {
			selected = append(selected, stream)
		}
	}
	return selected
}

func videoTrackTitle(plan domain.EncodePlan, stream domain.MediaStream) string {
	codec := plan.VideoCodec
	if plan.VideoCopy || strings.TrimSpace(codec) == "" {
		codec = stream.Codec
	}
	return joinTitleParts(
		resolutionLabel(stream),
		dynamicRangeLabel(stream),
		codecLabel(codec),
	)
}

func audioTrackTitle(stream domain.MediaStream) string {
	return joinTitleParts(
		languageLabel(stream.Language),
		codecLabel(stream.Codec),
		channelLabel(stream),
		bitRateLabel(stream.BitRate),
	)
}

func subtitleTrackTitle(stream domain.MediaStream) string {
	parts := []string{
		languageLabel(stream.Language),
		subtitleScopeLabel(stream),
	}
	if stream.Disposition["hearing_impaired"] || stream.Disposition["captions"] || stream.Disposition["descriptions"] {
		parts = append(parts, "SDH")
	}
	if stream.Disposition["comment"] || stream.Disposition["commentary"] {
		parts = append(parts, "Commentary")
	}
	parts = append(parts, codecLabel(stream.Codec), "Subtitle")
	return joinTitleParts(parts...)
}

func resolutionLabel(stream domain.MediaStream) string {
	height := stream.Height
	if stream.Width > 0 {
		inferredHeight := (stream.Width*9 + 8) / 16
		if inferredHeight > height {
			height = inferredHeight
		}
	}
	return standardResolutionLabel(height)
}

func standardResolutionLabel(height int) string {
	if height <= 0 {
		return ""
	}
	standards := []int{4320, 2160, 1080, 720, 576, 480, 360}
	best := standards[0]
	bestDelta := absInt(height - best)
	for _, standard := range standards[1:] {
		delta := absInt(height - standard)
		if delta < bestDelta {
			best = standard
			bestDelta = delta
		}
	}
	return strconv.Itoa(best) + "p"
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func dynamicRangeLabel(stream domain.MediaStream) string {
	if stream.DolbyVision != nil {
		return "Dolby Vision"
	}
	transfer := strings.ToLower(strings.TrimSpace(stream.ColorTransfer))
	switch transfer {
	case "":
		return ""
	case "arib-std-b67":
		return "HLG"
	case "smpte2084", "smpte2084-10":
		return "HDR10"
	default:
		return "SDR"
	}
}

func channelLabel(stream domain.MediaStream) string {
	switch stream.Channels {
	case 1:
		return "Mono"
	case 2:
		return "Stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	default:
		if stream.Channels > 0 {
			return strconv.Itoa(stream.Channels) + "ch"
		}
	}
	layout := strings.TrimSpace(stream.ChannelLayout)
	if layout == "" {
		return ""
	}
	return strings.ReplaceAll(layout, "(side)", "")
}

func bitRateLabel(bitRate int64) string {
	if bitRate <= 0 {
		return ""
	}
	if bitRate >= 1_000_000 {
		value := float64(bitRate) / 1_000_000
		if bitRate%1_000_000 == 0 {
			return strconv.FormatInt(bitRate/1_000_000, 10) + " Mb/s"
		}
		return strconv.FormatFloat(value, 'f', 1, 64) + " Mb/s"
	}
	return strconv.FormatInt((bitRate+500)/1000, 10) + " kb/s"
}

func subtitleScopeLabel(stream domain.MediaStream) string {
	if stream.Disposition["forced"] {
		return "Forced"
	}
	return "Full"
}

func languageLabel(value string) string {
	normalized := language.Normalize(value)
	if normalized == "" {
		return ""
	}
	switch normalized {
	case "deu":
		return "German"
	case "eng":
		return "English"
	case "fra":
		return "French"
	case "ita":
		return "Italian"
	case "jpn":
		return "Japanese"
	case "kor":
		return "Korean"
	case "spa":
		return "Spanish"
	case "zho":
		return "Chinese"
	default:
		return normalized
	}
}

func codecLabel(codec string) string {
	original := strings.TrimSpace(codec)
	normalized := strings.ToLower(original)
	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch normalized {
	case "av1", "libsvtav1", "svt-av1", "svtav1", "libaom-av1", "librav1e", "rav1e", "av1-qsv", "av1-nvenc", "av1-amf", "av1-vaapi", "av1-videotoolbox":
		return "AV1"
	case "aac":
		return "AAC"
	case "ac3", "ac-3":
		return "AC-3"
	case "eac3", "e-ac3", "e-ac-3":
		return "E-AC-3"
	case "ass", "ssa":
		return "ASS"
	case "dts":
		return "DTS"
	case "flac":
		return "FLAC"
	case "h264", "h-264", "avc", "libx264", "x264", "h264-qsv", "h264-nvenc", "h264-amf", "h264-vaapi", "h264-videotoolbox":
		return "H.264"
	case "h265", "h-265", "hevc", "libx265", "x265", "hevc-qsv", "hevc-nvenc", "hevc-amf", "hevc-vaapi", "hevc-videotoolbox":
		return "HEVC"
	case "hdmv-pgs-subtitle", "pgs":
		return "PGS"
	case "opus", "libopus":
		return "Opus"
	case "subrip", "srt":
		return "SRT"
	case "truehd":
		return "TrueHD"
	case "webvtt":
		return "VTT"
	default:
		return original
	}
}

func joinTitleParts(parts ...string) string {
	joined := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			joined = append(joined, part)
		}
	}
	return strings.Join(joined, " ")
}

func valueOr(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func anvilMetadataArgs(plan domain.EncodePlan) []string {
	tags := marker.OutputTags(plan)
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		args = append(args, "-metadata:s:v:0", key+"="+tags[key])
	}
	return args
}

func copyTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	copied := make(map[string]string, len(tags))
	for key, value := range tags {
		copied[key] = value
	}
	return copied
}
