package ffmpeg

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
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

// BuildPlanRequest contains the inputs used to construct an ffmpeg encode plan.
// Search, Audio, Subtitles, and Probe are optional; nil means that pipeline
// stage has not produced a result for this job.
type BuildPlanRequest struct {
	Profile    domain.Profile
	InputPath  string
	OutputPath string
	Resources  domain.ResourceAllocation
	Search     *domain.SearchResult
	Audio      *domain.AudioSelection
	Subtitles  *domain.SubtitleSelection
	Metadata   domain.JobMetadata
	Probe      *domain.ProbeResult
}

func BuildPlan(profile domain.Profile, inputPath string, outputPath string, allocation domain.ResourceAllocation, search *domain.SearchResult, audio *domain.AudioSelection, metadata domain.JobMetadata) (domain.EncodePlan, error) {
	return BuildPlanWithProbe(profile, inputPath, outputPath, allocation, search, audio, metadata, nil)
}

func BuildPlanWithProbe(profile domain.Profile, inputPath string, outputPath string, allocation domain.ResourceAllocation, search *domain.SearchResult, audio *domain.AudioSelection, metadata domain.JobMetadata, probe *domain.ProbeResult) (domain.EncodePlan, error) {
	return BuildPlanFromRequest(BuildPlanRequest{
		Profile:    profile,
		InputPath:  inputPath,
		OutputPath: outputPath,
		Resources:  allocation,
		Search:     search,
		Audio:      audio,
		Metadata:   metadata,
		Probe:      probe,
	})
}

// BuildPlanFromRequest builds the ffmpeg encode plan for request.
func BuildPlanFromRequest(request BuildPlanRequest) (domain.EncodePlan, error) {
	if err := request.validate(); err != nil {
		return domain.EncodePlan{}, err
	}
	videoCopy, videoCopyReason := videoCopyState(request.Metadata, request.Search)
	video := domain.EffectiveVideoProfile(request.Profile, request.Metadata)
	inputVideo, inputVideoFound := primaryInputVideo(request.Probe)
	crf := selectedCRF(video, videoCopy, request.Search)
	plan := domain.EncodePlan{
		InputPath:          request.InputPath,
		OutputPath:         request.OutputPath,
		ProfileName:        request.Profile.Name,
		VideoCodec:         videocodec.ResolveEncoder(video.Codec, video.Accelerator),
		InputVideoCodec:    inputVideo.Codec,
		InputWidth:         inputVideo.Width,
		InputHeight:        inputVideo.Height,
		Accelerator:        videocodec.ResolveAccelerator(video.Accelerator),
		VideoSource:        domain.EffectiveVideoSource(request.Metadata),
		VideoCopy:          videoCopy,
		VideoCopyReason:    videoCopyReason,
		Preset:             video.Preset,
		BitDepth:           videocodec.NormalizeBitDepth(video.BitDepth),
		PixelFormat:        videocodec.SoftwarePixelFormat(video.BitDepth),
		CRF:                crf,
		CRFMin:             video.CRFMin,
		CRFMax:             video.CRFMax,
		TargetVMAF:         video.TargetVMAF,
		MinSavingsPercent:  video.MinSavingsPercent,
		ForceEncodeOnNoFit: video.ForceEncodeOnNoFit,
		Threads:            request.Resources.Threads,
		Container:          request.Profile.Container,
		CropFilter:         request.Metadata.CropFilter,
		MetadataMode:       request.Profile.Metadata.Mode,
		TrackTitleMode:     trackTitleModeOrDefault(request.Profile.Metadata.TrackTitles),
		AttachmentMode:     request.Profile.Attachments.Mode,
		ChapterMode:        request.Profile.Chapters.Mode,
		AnvilTags:          copyTags(request.Metadata.AnvilTags),
		FFmpegArgs:         append([]string(nil), video.FFmpegArgs...),
		ABAV1Args:          append([]string(nil), video.ABAV1Args...),
		HDR:                request.Metadata.HDR,
	}
	if inputVideoFound {
		plan.VideoSelectionApplied = true
		plan.VideoStreamIndex = inputVideo.Index
	}
	applyAudioSelection(&plan, request.Audio)
	applySubtitleSelection(&plan, request.Subtitles)
	plan.TrackTitles = standardizedTrackTitles(plan, request.Probe)
	return plan, nil
}

func (request BuildPlanRequest) validate() error {
	if request.InputPath == "" {
		return errors.New("input path is required")
	}
	if request.OutputPath == "" {
		return errors.New("output path is required")
	}
	return nil
}

func videoCopyState(metadata domain.JobMetadata, search *domain.SearchResult) (bool, string) {
	videoCopy := metadata.VideoAlreadyEncoded
	videoCopyReason := ""
	if videoCopy {
		videoCopyReason = "compatible Anvil video marker"
	}
	if search != nil && search.SkipVideoEncode {
		videoCopy = true
		videoCopyReason = search.VideoEncodeSkipReason
		if videoCopyReason == "" {
			videoCopyReason = "CRF search skipped video encode"
		}
	}
	return videoCopy, videoCopyReason
}

func selectedCRF(video domain.VideoProfile, videoCopy bool, search *domain.SearchResult) int {
	if videoCopy {
		return 0
	}
	if search != nil && search.CRF > 0 {
		return search.CRF
	}
	return video.CRFMin
}

func applyAudioSelection(plan *domain.EncodePlan, audio *domain.AudioSelection) {
	if audio != nil {
		plan.AudioSelectionApplied = true
		plan.AudioStreamIndexes = append([]int(nil), audio.StreamIndexes...)
	}
}

func applySubtitleSelection(plan *domain.EncodePlan, subtitles *domain.SubtitleSelection) {
	if subtitles != nil {
		plan.SubtitleSelectionApplied = true
		plan.SubtitleStreamIndexes = append([]int(nil), subtitles.StreamIndexes...)
	}
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
	args = append(args, subtitleArgs()...)
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
	plan, err := BuildPlanFromRequest(buildPlanRequest(job))
	if err != nil {
		return err
	}
	if dropped := attachedPicStreams(job.Probe); len(dropped) > 0 {
		slog.Info("dropping embedded cover art video streams",
			"input", plan.InputPath,
			"streams", dropped,
		)
	}
	job.EncodePlan = &plan
	_, err = b.Encoder.Encode(ctx, plan)
	if err != nil {
		return fmt.Errorf("ffmpeg encode: %w", err)
	}
	return nil
}

func attachedPicStreams(probe *domain.ProbeResult) []string {
	if probe == nil {
		return nil
	}
	var dropped []string
	for _, stream := range probe.Streams {
		if stream.AttachedPic() {
			dropped = append(dropped, fmt.Sprintf("0:%d(%s)", stream.Index, stream.Codec))
		}
	}
	return dropped
}

func buildPlanRequest(job *pipeline.JobContext) BuildPlanRequest {
	return BuildPlanRequest{
		Profile:    job.Profile,
		InputPath:  job.InputPath,
		OutputPath: job.OutputPath,
		Resources:  job.Resources,
		Search:     job.Search,
		Audio:      job.Audio,
		Subtitles:  job.Subtitles,
		Metadata:   job.Metadata,
		Probe:      job.Probe,
	}
}

type DolbyVisionBlock struct {
	Runner      process.Runner
	DoviTool    string
	MKVExtract  string
	MKVMerge    string
	MKVInfo     string
	MKVPropEdit string
	FFmpeg      string
}

func (DolbyVisionBlock) Name() string {
	return "dovi-fix"
}

func (b DolbyVisionBlock) Run(ctx context.Context, job *pipeline.JobContext) error {
	if !needsDolbyVisionFix(job) {
		return nil
	}
	if strings.TrimSpace(job.OutputPath) == "" {
		return errors.New("dolby vision fix output path is required")
	}
	if !strings.EqualFold(filepath.Ext(job.OutputPath), ".mkv") {
		return fmt.Errorf("dolby vision fix requires MKV output, got %q", filepath.Ext(job.OutputPath))
	}
	codec := job.EncodePlan.VideoCodec
	if strings.TrimSpace(codec) == "" {
		codec = job.Profile.Video.DolbyVision.Codec
	}
	if !hevcEncoder(codec) {
		return fmt.Errorf("dolby vision fix requires HEVC output, got encoder %q", codec)
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

	if err := b.extractDolbyVisionRPU(ctx, job, paths); err != nil {
		return err
	}
	if err := b.injectDolbyVisionRPU(ctx, job, paths); err != nil {
		return err
	}
	if err := b.muxDolbyVisionOutput(ctx, job.OutputPath, paths); err != nil {
		return err
	}
	if err := b.restoreVideoTags(ctx, job, paths); err != nil {
		return err
	}
	if err := replaceOutput(job.OutputPath, paths.fixedMKV); err != nil {
		return err
	}
	return nil
}

func (b DolbyVisionBlock) extractDolbyVisionRPU(ctx context.Context, job *pipeline.JobContext, paths doviPaths) error {
	originalForRPU, err := b.originalVideoForRPU(ctx, job.InputPath, paths)
	if err != nil {
		return err
	}
	extractArgs := doviArgs(
		job,
		"--crop", "--mode", "2",
		"extract-rpu",
		"-o", paths.originalRPU,
		originalForRPU,
	)
	return b.run(ctx, b.doviTool(), extractArgs...)
}

func (b DolbyVisionBlock) originalVideoForRPU(ctx context.Context, inputPath string, paths doviPaths) (string, error) {
	if strings.EqualFold(filepath.Ext(inputPath), ".mkv") {
		return inputPath, nil
	}
	err := b.run(
		ctx,
		b.ffmpeg(),
		"-v", "quiet", "-stats",
		"-i", inputPath,
		"-c:v", "copy",
		paths.originalHEVC,
	)
	if err != nil {
		return "", err
	}
	return paths.originalHEVC, nil
}

func (b DolbyVisionBlock) injectDolbyVisionRPU(ctx context.Context, job *pipeline.JobContext, paths doviPaths) error {
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
	return b.run(ctx, b.doviTool(), injectArgs...)
}

func (b DolbyVisionBlock) muxDolbyVisionOutput(ctx context.Context, outputPath string, paths doviPaths) error {
	fps, err := b.outputFPS(ctx, outputPath)
	if err != nil {
		return err
	}
	return b.run(ctx, b.mkvMerge(), doviMuxArgs(outputPath, paths, fps)...)
}

func (b DolbyVisionBlock) restoreVideoTags(ctx context.Context, job *pipeline.JobContext, paths doviPaths) error {
	if job == nil || job.EncodePlan == nil {
		return nil
	}
	tags := marker.OutputTags(*job.EncodePlan)
	if len(tags) == 0 {
		return nil
	}
	if err := writeMatroskaTrackTags(paths.videoTags, tags); err != nil {
		return err
	}
	args := []string{paths.fixedMKV}
	if title := doviVideoTrackTitle(*job.EncodePlan); title != "" {
		args = append(args, "--edit", "track:v1", "--set", "name="+title)
	}
	args = append(args, "--tags", "track:v1:"+paths.videoTags)
	return b.run(ctx, b.mkvPropEdit(), args...)
}

func doviVideoTrackTitle(plan domain.EncodePlan) string {
	for _, title := range plan.TrackTitles {
		if title.Type == "v" && title.Index == 0 {
			return strings.TrimSpace(title.Title)
		}
	}
	return ""
}

func doviMuxArgs(outputPath string, paths doviPaths, fps string) []string {
	args := []string{
		"-o", paths.fixedMKV,
		paths.fixedHEVC,
		"-D", outputPath,
		"--track-order", "1:0",
	}
	if fps != "" {
		args = append(
			[]string{
				"--default-duration", "0:" + fps + "fps",
				"--fix-bitstream-timing-information", "0",
			},
			args...,
		)
	}
	return args
}

type doviPaths struct {
	originalHEVC  string
	originalRPU   string
	convertedHEVC string
	fixedHEVC     string
	fixedMKV      string
	videoTags     string
}

func doviWorkPaths(dir string) doviPaths {
	return doviPaths{
		originalHEVC:  filepath.Join(dir, "dovi-original.hevc"),
		originalRPU:   filepath.Join(dir, "dovi-original.rpu"),
		convertedHEVC: filepath.Join(dir, "dovi-converted.hevc"),
		fixedHEVC:     filepath.Join(dir, "dovi-fixed.hevc"),
		fixedMKV:      filepath.Join(dir, "dovi-fixed.mkv"),
		videoTags:     filepath.Join(dir, "dovi-video-tags.xml"),
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

func (b DolbyVisionBlock) mkvPropEdit() string {
	return valueOr(b.MKVPropEdit, "mkvpropedit")
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
	_ = os.Remove(backupPath) //nolint:errcheck // stale backup removal is best-effort before replacement
	if err := os.Rename(outputPath, backupPath); err != nil {
		return fmt.Errorf("backup pre-Dolby Vision output: %w", err)
	}
	if err := os.Rename(fixedPath, outputPath); err != nil {
		_ = os.Rename(backupPath, outputPath) //nolint:errcheck // restore attempt is best-effort; install error is returned
		return fmt.Errorf("install Dolby Vision fixed output: %w", err)
	}
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove pre-Dolby Vision output backup: %w", err)
	}
	return nil
}

func cleanupDoviPaths(paths doviPaths) {
	for _, path := range []string{
		paths.originalHEVC,
		paths.originalRPU,
		paths.convertedHEVC,
		paths.fixedHEVC,
		paths.fixedMKV,
		paths.videoTags,
	} {
		_ = os.Remove(path) //nolint:errcheck // temporary Dolby Vision work files are best-effort cleanup
	}
}

type matroskaTags struct {
	XMLName xml.Name      `xml:"Tags"`
	Tags    []matroskaTag `xml:"Tag"`
}

type matroskaTag struct {
	Simple []matroskaSimpleTag `xml:"Simple"`
}

type matroskaSimpleTag struct {
	Name   string `xml:"Name"`
	String string `xml:"String"`
}

func writeMatroskaTrackTags(path string, tags map[string]string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("matroska tags path is required")
	}
	keys := make([]string, 0, len(tags))
	for key, value := range tags {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	document := matroskaTags{Tags: []matroskaTag{{Simple: make([]matroskaSimpleTag, 0, len(keys))}}}
	for _, key := range keys {
		document.Tags[0].Simple = append(document.Tags[0].Simple, matroskaSimpleTag{
			Name:   strings.TrimSpace(key),
			String: strings.TrimSpace(tags[key]),
		})
	}
	data, err := xml.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode matroska video tags: %w", err)
	}
	data = append([]byte(xml.Header), data...)
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o640); err != nil {
		return fmt.Errorf("write matroska video tags: %w", err)
	}
	return nil
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
	if qsvInputPipeline(plan) {
		format := videocodec.QSVVPPFormat(plan.BitDepth)
		if noCropFilter(plan) {
			return videocodec.QSVFormatFilter(format)
		}
		if filter, ok := videocodec.QSVCropFilter(plan.CropFilter, format); ok {
			return filter
		}
	}
	if noCropFilter(plan) {
		return ""
	}
	return plan.CropFilter
}

func noCropFilter(plan domain.EncodePlan) bool {
	return strings.TrimSpace(plan.CropFilter) == "" ||
		videocodec.NoOpCrop(plan.CropFilter, plan.InputWidth, plan.InputHeight)
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

func primaryInputVideo(probe *domain.ProbeResult) (domain.MediaStream, bool) {
	if probe == nil {
		return domain.MediaStream{}, false
	}
	return domain.PrimaryVideoStream(probe.Streams)
}

func mapArgs(plan domain.EncodePlan) []string {
	// Embedded cover art appears as an extra video stream (attached_pic) and
	// must never reach the video encoder. Map the resolved primary video
	// stream explicitly; without a probe, 0:V excludes attached pictures.
	var args []string
	if plan.VideoSelectionApplied {
		args = []string{"-map", "0:" + strconv.Itoa(plan.VideoStreamIndex)}
	} else {
		args = []string{"-map", "0:V?"}
	}
	if plan.AudioSelectionApplied {
		for _, streamIndex := range plan.AudioStreamIndexes {
			args = append(args, "-map", "0:"+strconv.Itoa(streamIndex))
		}
	} else {
		args = append(args, "-map", "0:a?")
	}
	if plan.SubtitleSelectionApplied {
		for _, streamIndex := range plan.SubtitleStreamIndexes {
			args = append(args, "-map", "0:"+strconv.Itoa(streamIndex))
		}
	} else {
		args = append(args, "-map", "0:s?")
	}
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
	if pixelFormat := finalPixelFormat(plan); pixelFormat != "" {
		args = append(args, "-pix_fmt", pixelFormat)
	}
	if plan.Threads > 0 {
		args = append(args, "-threads", strconv.Itoa(plan.Threads))
	}
	return args
}

func finalPixelFormat(plan domain.EncodePlan) string {
	if videocodec.EncoderAccelerator(plan.VideoCodec) == videocodec.AcceleratorQSV {
		return ""
	}
	if plan.PixelFormat != "" {
		return plan.PixelFormat
	}
	return videocodec.SoftwarePixelFormat(plan.BitDepth)
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

func subtitleArgs() []string {
	return []string{"-c:s", "copy"}
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
	if video, ok := domain.PrimaryVideoStream(probe.Streams); ok {
		titles = appendTrackTitle(titles, "v", 0, videoTrackTitle(plan, video))
	}
	for outputIndex, stream := range selectedAudioStreams(plan, probe.Streams) {
		titles = appendTrackTitle(titles, "a", outputIndex, audioTrackTitle(stream))
	}
	for outputIndex, stream := range selectedSubtitleStreams(plan, probe.Streams) {
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

func selectedSubtitleStreams(plan domain.EncodePlan, streams []domain.MediaStream) []domain.MediaStream {
	if !plan.SubtitleSelectionApplied {
		return streamsOfType(streams, "subtitle")
	}
	byIndex := make(map[int]domain.MediaStream)
	for _, stream := range streams {
		if stream.Type == "subtitle" {
			byIndex[stream.Index] = stream
		}
	}
	selected := make([]domain.MediaStream, 0, len(plan.SubtitleStreamIndexes))
	for _, index := range plan.SubtitleStreamIndexes {
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
	case "av1", "libsvtav1", "svt-av1", "svtav1", "libaom-av1", "librav1e", "rav1e",
		"av1-qsv", "av1-nvenc", "av1-amf", "av1-vaapi", "av1-videotoolbox":
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
	case "h264", "h-264", "avc", "libx264", "x264", "h264-qsv", "h264-nvenc",
		"h264-amf", "h264-vaapi", "h264-videotoolbox":
		return "H.264"
	case "h265", "h-265", "hevc", "libx265", "x265", "hevc-qsv", "hevc-nvenc",
		"hevc-amf", "hevc-vaapi", "hevc-videotoolbox":
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
