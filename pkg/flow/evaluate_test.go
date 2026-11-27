package flow

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestEvaluateConditions(t *testing.T) {
	hevc := videoStream("hevc", 1920, 800)
	cover := domain.MediaStream{Type: "video", Codec: "mjpeg", Width: 600, Height: 600, Disposition: map[string]bool{"attached_pic": true}}
	hdr10 := videoStream("hevc", 3840, 2160)
	hdr10.ColorTransfer = "smpte2084"
	hlg := videoStream("hevc", 3840, 2160)
	hlg.ColorTransfer = "arib-std-b67"
	dolbyVisionOnHDR10 := hdr10
	dolbyVisionOnHDR10.DolbyVision = &domain.DolbyVision{Profile: 8, BLCompatibilityID: 1}
	dolbyVisionAlone := videoStream("hevc", 3840, 2160)
	dolbyVisionAlone.DolbyVision = &domain.DolbyVision{Profile: 5}
	sdr := videoStream("h264", 1920, 1080)
	sdr.ColorTransfer = "bt709"

	english := audioStream("ac3", "eng", 6)
	japanese := audioStream("truehd", "jpn", 8)
	untagged := audioStream("aac", "und", 2)

	noDuration := probeOf(hevc)
	noDuration.Probe.DurationSeconds = 0
	noSize := probeOf(hevc)
	noSize.Probe.SizeBytes = 0
	japaneseOriginal := probeOf(hevc, english, japanese)
	japaneseOriginal.Metadata.OriginalLanguage = "ja"

	tests := []struct {
		name     string
		nodeType string
		options  map[string]any
		facts    Facts
		want     bool
		reason   string
	}{
		{"video codec listed", typeCheckVideoCodec, map[string]any{"codecs": []string{"h264", "hevc"}}, probeOf(hevc), true, "video codec is hevc"},
		{"video codec not listed", typeCheckVideoCodec, map[string]any{"codecs": []string{"hevc"}}, probeOf(sdr), false, "video codec is h264"},
		{"video codec default is av1", typeCheckVideoCodec, nil, probeOf(videoStream("av1", 1920, 1080)), true, "video codec is av1"},
		{"video codec family of an ffprobe name", typeCheckVideoCodec, map[string]any{"codecs": []string{"mpeg2"}}, probeOf(videoStream("mpeg2video", 720, 576)), true, "video codec is mpeg2"},
		{"video codec outside the catalog", typeCheckVideoCodec, map[string]any{"codecs": []string{"mpeg4"}}, probeOf(videoStream("msmpeg4v3", 640, 480)), false, "video codec is msmpeg4v3"},
		{"video codec without a video stream", typeCheckVideoCodec, nil, probeOf(english), false, "no video stream"},
		{"video codec ignores cover art", typeCheckVideoCodec, map[string]any{"codecs": []string{"h264"}}, probeOf(cover, english), false, "no video stream"},
		{"video codec with an empty name", typeCheckVideoCodec, nil, probeOf(videoStream("", 1920, 1080)), false, "video codec is unknown"},

		{"resolution reached by width", typeCheckResolution, map[string]any{"op": "at_least", "resolution": "1080p"}, probeOf(hevc), true, "video is 1920x800, 1080p"},
		{"resolution reached by height", typeCheckResolution, map[string]any{"op": "at_least", "resolution": "1080p"}, probeOf(videoStream("h264", 1440, 1080)), true, "video is 1440x1080, 1080p"},
		{"resolution not reached", typeCheckResolution, map[string]any{"op": "at_least", "resolution": "1080p"}, probeOf(videoStream("h264", 1280, 720)), false, "video is 1280x720, 720p"},
		{"resolution default is at least 1080p", typeCheckResolution, nil, probeOf(hdr10), true, "video is 3840x2160, 2160p"},
		{"resolution at most, equal", typeCheckResolution, map[string]any{"op": "at_most", "resolution": "1080p"}, probeOf(sdr), true, "video is 1920x1080, 1080p"},
		{"resolution at most, above", typeCheckResolution, map[string]any{"op": "at_most", "resolution": "1080p"}, probeOf(hdr10), false, "video is 3840x2160, 2160p"},
		{"resolution at least 2160p", typeCheckResolution, map[string]any{"op": "at_least", "resolution": "2160p"}, probeOf(videoStream("hevc", 3840, 1600)), true, "video is 3840x1600, 2160p"},
		{"resolution below the smallest class", typeCheckResolution, map[string]any{"op": "at_least", "resolution": "480p"}, probeOf(videoStream("mpeg4", 640, 360)), false, "video is 640x360, below 480p"},
		{"resolution at most the smallest class", typeCheckResolution, map[string]any{"op": "at_most", "resolution": "480p"}, probeOf(videoStream("mpeg4", 640, 360)), true, "video is 640x360, below 480p"},
		{"resolution without a video stream", typeCheckResolution, nil, probeOf(english), false, "no video stream"},
		{"resolution without a frame size", typeCheckResolution, map[string]any{"op": "at_most", "resolution": "2160p"}, probeOf(videoStream("hevc", 0, 0)), false, "video size is unknown"},

		{"hdr10", typeCheckHDR, nil, probeOf(hdr10), true, "video is hdr10"},
		{"hlg when only hdr10 is wanted", typeCheckHDR, map[string]any{"kinds": []string{"hdr10"}}, probeOf(hlg), false, "video is hlg"},
		{"hlg", typeCheckHDR, map[string]any{"kinds": []string{"hlg"}}, probeOf(hlg), true, "video is hlg"},
		{"dolby vision counts as its base layer", typeCheckHDR, map[string]any{"kinds": []string{"hdr10"}}, probeOf(dolbyVisionOnHDR10), true, "video is dolby_vision, hdr10"},
		{"dolby vision without a tagged base layer", typeCheckHDR, map[string]any{"kinds": []string{"dolby_vision"}}, probeOf(dolbyVisionAlone), true, "video is dolby_vision"},
		{"dolby vision is not hlg", typeCheckHDR, map[string]any{"kinds": []string{"hlg"}}, probeOf(dolbyVisionOnHDR10), false, "video is dolby_vision, hdr10"},
		{"sdr", typeCheckHDR, nil, probeOf(sdr), false, "video is sdr"},
		{"hdr without a video stream", typeCheckHDR, nil, probeOf(english), false, "no video stream"},

		{"bitrate above", typeCheckBitrate, map[string]any{"op": "above", "kbps": 8000}, probeOf(hevc), true, "overall bitrate is 9544 kb/s"},
		{"bitrate not above", typeCheckBitrate, map[string]any{"op": "above", "kbps": 10000}, probeOf(hevc), false, "overall bitrate is 9544 kb/s"},
		{"bitrate below", typeCheckBitrate, map[string]any{"op": "below", "kbps": 10000}, probeOf(hevc), true, "overall bitrate is 9544 kb/s"},
		{"bitrate not below", typeCheckBitrate, map[string]any{"op": "below", "kbps": 8000}, probeOf(hevc), false, "overall bitrate is 9544 kb/s"},
		{"bitrate with unknown duration", typeCheckBitrate, map[string]any{"op": "below", "kbps": 8000}, noDuration, false, "duration is unknown"},
		{"bitrate with unknown size", typeCheckBitrate, nil, noSize, false, "file size is unknown"},

		{"file size above", typeCheckFileSize, map[string]any{"op": "above", "megabytes": 1024}, probeOf(hevc), true, "file is 8192 MiB"},
		{"file size not above", typeCheckFileSize, map[string]any{"op": "above", "megabytes": 8192}, probeOf(hevc), false, "file is 8192 MiB"},
		{"file size below", typeCheckFileSize, map[string]any{"op": "below", "megabytes": 9000}, probeOf(hevc), true, "file is 8192 MiB"},
		{"file size not below", typeCheckFileSize, map[string]any{"op": "below", "megabytes": 700}, probeOf(hevc), false, "file is 8192 MiB"},
		{"file size unknown", typeCheckFileSize, map[string]any{"op": "below", "megabytes": 700}, noSize, false, "file size is unknown"},

		{"audio language present", typeCheckAudioLanguage, map[string]any{"languages": []string{"jpn"}}, probeOf(hevc, english, japanese), true, "audio languages are eng, jpn"},
		{"audio language absent", typeCheckAudioLanguage, map[string]any{"languages": []string{"deu", "fra"}}, probeOf(hevc, english, japanese), false, "audio languages are eng, jpn"},
		{"audio language default is eng", typeCheckAudioLanguage, nil, probeOf(hevc, english), true, "audio language is eng"},
		{"audio language tags are normalized", typeCheckAudioLanguage, map[string]any{"languages": []string{" EN "}}, probeOf(hevc, english), true, "audio language is eng"},
		{"audio language orig resolves", typeCheckAudioLanguage, map[string]any{"languages": []string{"orig"}}, japaneseOriginal, true, "audio languages are eng, jpn"},
		{"audio language orig without metadata", typeCheckAudioLanguage, map[string]any{"languages": []string{"orig"}}, probeOf(hevc, english), false, "audio language is eng; original language is unknown"},
		{"audio language of an untagged track", typeCheckAudioLanguage, map[string]any{"languages": []string{"eng", "und"}}, probeOf(hevc, untagged), false, "audio language is und"},
		{"audio language without audio", typeCheckAudioLanguage, nil, probeOf(hevc), false, "no audio tracks"},

		{"audio codec present", typeCheckAudioCodec, nil, probeOf(hevc, english, japanese), true, "audio codecs are ac3, truehd"},
		{"audio codec absent", typeCheckAudioCodec, map[string]any{"codecs": []string{"dts", "flac"}}, probeOf(hevc, english, japanese), false, "audio codecs are ac3, truehd"},
		{"audio codec names are normalized", typeCheckAudioCodec, map[string]any{"codecs": []string{" AC3"}}, probeOf(hevc, english), true, "audio codec is ac3"},
		{"audio codec without audio", typeCheckAudioCodec, nil, probeOf(hevc), false, "no audio tracks"},

		{"audio channels above stereo", typeCheckAudioChannels, nil, probeOf(hevc, untagged, english), true, "largest audio track has 6 channels"},
		{"audio channels not above 5.1", typeCheckAudioChannels, map[string]any{"more_than": "6"}, probeOf(hevc, untagged, english), false, "largest audio track has 6 channels"},
		{"audio channels above 5.1", typeCheckAudioChannels, map[string]any{"more_than": "6"}, probeOf(hevc, english, japanese), true, "largest audio track has 8 channels"},
		{"audio channels stereo only", typeCheckAudioChannels, map[string]any{"more_than": "2"}, probeOf(hevc, untagged), false, "largest audio track has 2 channels"},
		{"audio channels without audio", typeCheckAudioChannels, nil, probeOf(hevc), false, "no audio tracks"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := viaJSON(t, branching(newNode("check", test.nodeType, test.options)))
			resolution := mustEvaluate(t, g, test.facts)

			wantOutcome, wantEnd := OutcomeSkip, "skip"
			if test.want {
				wantOutcome, wantEnd = OutcomeProcess, "publish"
			}
			if resolution.Outcome != wantOutcome {
				t.Errorf("Outcome = %q, want %q", resolution.Outcome, wantOutcome)
			}
			if want := []string{"in", "check", wantEnd}; !slices.Equal(resolution.Path, want) {
				t.Errorf("Path = %v, want %v", resolution.Path, want)
			}
			label := nodeDefsByID[test.nodeType].Label
			want := []string{label + " → " + map[bool]string{true: "true", false: "false"}[test.want] + " (" + test.reason + ")"}
			if !slices.Equal(resolution.Decisions, want) {
				t.Errorf("Decisions = %q, want %q", resolution.Decisions, want)
			}
		})
	}
}

func TestEvaluateUsesTheNodeLabelInDecisions(t *testing.T) {
	check := Node{ID: "check", Type: typeCheckVideoCodec, Label: "Video is hevc?", Options: map[string]any{"codecs": []string{"hevc"}}}
	resolution := mustEvaluate(t, branching(check), probeOf(videoStream("hevc", 1920, 1080)))
	if want := []string{"Video is hevc? → true (video codec is hevc)"}; !slices.Equal(resolution.Decisions, want) {
		t.Errorf("Decisions = %q, want %q", resolution.Decisions, want)
	}
}

func TestEvaluateInputToPublishIsAPlainRemux(t *testing.T) {
	g := chain(newNode("in", typeInput, nil), newNode("out", typeOutputPublish, nil))
	resolution := mustEvaluate(t, g, probeOf(videoStream("hevc", 1920, 1080)))

	if resolution.Outcome != OutcomeProcess {
		t.Errorf("Outcome = %q", resolution.Outcome)
	}
	if want := []string{"in", "out"}; !slices.Equal(resolution.Path, want) {
		t.Errorf("Path = %v, want %v", resolution.Path, want)
	}
	if resolution.Decisions == nil || len(resolution.Decisions) != 0 {
		t.Errorf("Decisions = %#v, want an empty, non-nil list", resolution.Decisions)
	}

	profile := resolution.Profile
	if profile.Name != "flow" || profile.Container != "mkv" {
		t.Errorf("Name, Container = %q, %q", profile.Name, profile.Container)
	}
	if !profile.Video.SkipEncode || profile.Video.Overrides != nil {
		t.Errorf("Video = %+v, want a copy without overrides", profile.Video)
	}
	if !profile.Crop.Disabled {
		t.Error("Crop is enabled")
	}
	wantAudio := domain.AudioProfile{Fallback: domain.StreamFallbackKeepAll}
	if !reflect.DeepEqual(profile.Audio, wantAudio) {
		t.Errorf("Audio = %+v, want %+v", profile.Audio, wantAudio)
	}
	wantSubtitles := domain.SubtitleProfile{Fallback: domain.StreamFallbackKeepAll}
	if !reflect.DeepEqual(profile.Subtitles, wantSubtitles) {
		t.Errorf("Subtitles = %+v, want %+v", profile.Subtitles, wantSubtitles)
	}
	wantMetadata := domain.MetadataPolicy{Mode: domain.MetadataModePreserve, TrackTitles: domain.TrackTitleModePreserve}
	if profile.Metadata != wantMetadata {
		t.Errorf("Metadata = %+v, want %+v", profile.Metadata, wantMetadata)
	}
	if profile.Attachments.Mode != domain.MetadataModePreserve || profile.Chapters.Mode != domain.MetadataModePreserve {
		t.Errorf("Attachments, Chapters = %q, %q", profile.Attachments.Mode, profile.Chapters.Mode)
	}
	if profile.Validation.DurationToleranceSeconds != 0 {
		t.Errorf("DurationToleranceSeconds = %v, want the config default 0", profile.Validation.DurationToleranceSeconds)
	}

	wantPlan := []string{
		"Video: copy",
		"Audio: keep every track; copy",
		"Subtitles: keep every track",
		"Container: mkv; keep metadata, track titles, attachments, and chapters",
	}
	if !slices.Equal(resolution.Plan, wantPlan) {
		t.Errorf("Plan =\n%s\nwant\n%s", strings.Join(resolution.Plan, "\n"), strings.Join(wantPlan, "\n"))
	}
}

func TestEvaluateSkip(t *testing.T) {
	g := chain(newNode("in", typeInput, nil), newNode("encode", typeVideoEncode, nil), newNode("skip", typeOutputSkip, nil))
	resolution := mustEvaluate(t, g, probeOf(videoStream("av1", 1920, 1080)))
	if resolution.Outcome != OutcomeSkip {
		t.Errorf("Outcome = %q, want skip", resolution.Outcome)
	}
	if want := []string{"in", "encode", "skip"}; !slices.Equal(resolution.Path, want) {
		t.Errorf("Path = %v, want %v", resolution.Path, want)
	}
	if want := []string{"File: skip, leave it untouched"}; !slices.Equal(resolution.Plan, want) {
		t.Errorf("Plan = %q, want %q", resolution.Plan, want)
	}
}

func TestEvaluateRejectsAnInvalidGraph(t *testing.T) {
	tests := map[string]Graph{
		"empty":       {},
		"dangling":    chain(newNode("in", typeInput, nil), newNode("encode", typeVideoEncode, nil)),
		"bad option":  chain(newNode("in", typeInput, nil), newNode("encode", typeVideoEncode, map[string]any{"codec": "vp9"}), newNode("out", typeOutputPublish, nil)),
		"two errors":  chain(newNode("in", typeInput, nil), newNode("encode", typeVideoEncode, map[string]any{"codec": "vp9", "preset": 6})),
		"unknown":     chain(newNode("in", typeInput, nil), newNode("x", "nope", nil), newNode("out", typeOutputPublish, nil)),
		"cycle":       {Nodes: []Node{newNode("in", typeInput, nil), newNode("a", typeVideoCopy, nil)}, Edges: []Edge{{From: "in", To: "a"}, {From: "a", To: "a"}}},
		"unknown key": chain(newNode("in", typeInput, map[string]any{"x": 1}), newNode("out", typeOutputPublish, nil)),
	}
	for name, g := range tests {
		t.Run(name, func(t *testing.T) {
			resolution, err := Evaluate(g, Facts{})
			if err == nil {
				t.Fatalf("Evaluate() = %+v, want an error", resolution)
			}
			if !strings.HasPrefix(err.Error(), "flow is not valid: ") || strings.HasSuffix(err.Error(), ".") {
				t.Errorf("error = %q", err)
			}
			if _, err := Canonical(g); err == nil {
				t.Error("Canonical() succeeded")
			}
		})
	}
	// Warnings do not block evaluation.
	g := chain(newNode("in", typeInput, nil), newNode("out", typeOutputPublish, nil))
	g.Nodes = append(g.Nodes, newNode("spare", typeVideoCopy, nil))
	if _, err := Evaluate(g, Facts{}); err != nil {
		t.Errorf("Evaluate() with only a warning: %v", err)
	}
}

func TestEvaluateLastNodeOfAPartWins(t *testing.T) {
	g := chain(
		newNode("in", typeInput, nil),
		newNode("first", typeVideoEncode, map[string]any{"preset": "4", "codec": "hevc", "ffmpeg_args": []string{"-g", "240"}}),
		newNode("copy", typeVideoCopy, nil),
		newNode("last", typeVideoEncode, map[string]any{"preset": "8"}),
		newNode("audio-first", typeAudioKeep, map[string]any{"languages": []string{"eng"}, "keep_commentary": true}),
		newNode("audio-last", typeAudioKeep, map[string]any{"languages": []string{"jpn"}}),
		newNode("meta-first", typeContainerMetadata, map[string]any{"metadata": "strip", "chapters": "strip"}),
		newNode("meta-last", typeContainerMetadata, map[string]any{"track_titles": "standardize"}),
		newNode("out", typeOutputPublish, nil),
	)
	profile := mustEvaluate(t, viaJSON(t, g), probeOf()).Profile

	// The last encode replaces the whole video part: nothing of the first
	// one, such as its codec or arguments, leaks through.
	if profile.Video.SkipEncode || profile.Video.Preset != "8" || profile.Video.Codec != "av1" || profile.Video.FFmpegArgs != nil {
		t.Errorf("Video = %+v", profile.Video)
	}
	if !slices.Equal(profile.Audio.LanguagesToKeep, []string{"jpn"}) || profile.Audio.KeepCommentary {
		t.Errorf("Audio = %+v", profile.Audio)
	}
	if profile.Metadata.Mode != domain.MetadataModePreserve || profile.Metadata.TrackTitles != domain.TrackTitleModeStandardize || profile.Chapters.Mode != domain.MetadataModePreserve {
		t.Errorf("Metadata = %+v, Chapters = %+v", profile.Metadata, profile.Chapters)
	}

	// A copy after an encode wins the same way.
	g = chain(newNode("in", typeInput, nil), newNode("encode", typeVideoEncode, map[string]any{"preset": "4"}), newNode("copy", typeVideoCopy, nil), newNode("out", typeOutputPublish, nil))
	resolution := mustEvaluate(t, g, probeOf())
	if !resolution.Profile.Video.SkipEncode || resolution.Plan[0] != "Video: copy" {
		t.Errorf("Video = %+v, Plan = %q", resolution.Profile.Video, resolution.Plan[0])
	}
	if !reflect.DeepEqual(resolution.Profile.Video, baseProfile().Video) {
		t.Errorf("a copy leaves encode settings behind: %+v", resolution.Profile.Video)
	}
}

func TestEvaluateBranchesRejoin(t *testing.T) {
	g := Graph{
		Nodes: []Node{
			newNode("in", typeInput, nil),
			{ID: "check", Type: typeCheckVideoCodec, Label: "Already AV1?"},
			newNode("copy", typeVideoCopy, nil),
			newNode("encode", typeVideoEncode, nil),
			newNode("audio", typeAudioKeep, map[string]any{"languages": []string{"orig", "eng"}}),
			newNode("out", typeOutputPublish, nil),
		},
		Edges: []Edge{
			{From: "audio", To: "out"},
			{From: "encode", To: "audio"},
			{From: "copy", To: "audio"},
			{From: "check", Port: 1, To: "encode"},
			{From: "check", Port: 0, To: "copy"},
			{From: "in", To: "check"},
		},
	}
	g = viaJSON(t, g)

	copied := mustEvaluate(t, g, probeOf(videoStream("av1", 1920, 1080)))
	if want := []string{"in", "check", "copy", "audio", "out"}; !slices.Equal(copied.Path, want) {
		t.Errorf("Path = %v, want %v", copied.Path, want)
	}
	if !copied.Profile.Video.SkipEncode {
		t.Error("AV1 source is not copied")
	}

	encoded := mustEvaluate(t, g, probeOf(videoStream("h264", 1920, 1080)))
	if want := []string{"in", "check", "encode", "audio", "out"}; !slices.Equal(encoded.Path, want) {
		t.Errorf("Path = %v, want %v", encoded.Path, want)
	}
	if encoded.Profile.Video.SkipEncode {
		t.Error("H.264 source is copied")
	}
	if want := []string{"Already AV1? → false (video codec is h264)"}; !slices.Equal(encoded.Decisions, want) {
		t.Errorf("Decisions = %q, want %q", encoded.Decisions, want)
	}
	for _, resolution := range []Resolution{copied, encoded} {
		if !slices.Equal(resolution.Profile.Audio.LanguagesToKeep, []string{"orig", "eng"}) {
			t.Errorf("the shared audio node did not apply: %+v", resolution.Profile.Audio)
		}
	}
}

func TestEvaluateActionNodesSetTheirPartOfTheProfile(t *testing.T) {
	g := chain(
		newNode("in", typeInput, nil),
		newNode("encode", typeVideoEncode, map[string]any{
			"codec": "hevc", "accelerator": "qsv", "preset": "veryslow", "bit_depth": "8",
			"metric": "xpsnr", "target": 42.5, "crf_min": 20, "crf_max": 30, "min_savings_percent": 35,
			"on_no_fit": "encode", "samples": 4, "sample_duration": "30s", "ffmpeg_args": []string{"-look_ahead", "1"},
		}),
		newNode("crop", typeVideoCrop, map[string]any{
			"min_retained_area_percent": 60.5, "samples": 8, "frame_count": 120, "seek_offsets": []string{"5m", "1h10m"},
			"limit": 32, "round": 2, "reset_count": 10, "required_alignment": 4, "min_width": 320, "min_height": 240,
		}),
		newNode("audio", typeAudioKeep, map[string]any{"languages": []string{"orig", "eng"}, "keep_commentary": true, "fallback": "keep_first", "unknown_as_original": true}),
		newNode("audio-encode", typeAudioEncode, map[string]any{"codec": "opus", "bitrate_kbps_per_channel": 64, "source_codecs": []string{" TrueHD ", "dts"}}),
		newNode("downmix", typeAudioDownmix, map[string]any{"max_channels": "2", "codec": "aac", "bitrate_kbps_per_channel": 80}),
		newNode("subtitles", typeSubtitlesKeep, map[string]any{"languages": []string{"eng"}, "keep_forced": true, "keep_sdh": true, "keep_commentary": true, "fallback": "fail_job", "unknown_as_original": true}),
		newNode("images", typeSubtitlesDropImage, nil),
		newNode("metadata", typeContainerMetadata, map[string]any{"metadata": "strip", "track_titles": "standardize", "attachments": "strip", "chapters": "strip"}),
		newNode("out", typeOutputPublish, nil),
	)
	resolution := mustEvaluate(t, viaJSON(t, g), probeOf())

	want := baseProfile()
	want.Video = domain.VideoProfile{
		Codec: "hevc", Accelerator: "qsv", Preset: "veryslow", BitDepth: 8,
		CRFMin: 20, CRFMax: 30, Samples: 4, SampleDuration: 30 * time.Second,
		Metric: domain.QualityMetricXPSNR, Target: 42.5, MinSavingsPercent: 35,
		ForceEncodeOnNoFit: true, FFmpegArgs: []string{"-look_ahead", "1"},
	}
	want.Crop = domain.CropPolicy{
		SeekOffsets: []time.Duration{5 * time.Minute, 70 * time.Minute}, Samples: 8, FrameCount: 120,
		Limit: 32, Round: 2, ResetCount: 10, MinRetainedAreaPercent: 60.5, MinWidth: 320, MinHeight: 240, RequiredAlignment: 4,
	}
	want.Audio = domain.AudioProfile{
		LanguagesToKeep: []string{"orig", "eng"}, KeepCommentary: true, Fallback: domain.StreamFallbackKeepFirst, UnknownAsOriginal: true,
		Encode:  &domain.AudioEncode{Codec: "opus", BitrateKbpsPerChannel: 64, SourceCodecs: []string{"truehd", "dts"}},
		Downmix: &domain.AudioDownmix{MaxChannels: 2, Codec: "aac", BitrateKbpsPerChannel: 80},
	}
	want.Subtitles = domain.SubtitleProfile{
		LanguagesToKeep: []string{"eng"}, KeepForced: true, KeepSDH: true, KeepCommentary: true,
		Fallback: domain.StreamFallbackFailJob, UnknownAsOriginal: true, DropBitmap: true,
	}
	want.Metadata = domain.MetadataPolicy{Mode: domain.MetadataModeStrip, TrackTitles: domain.TrackTitleModeStandardize}
	want.Attachments.Mode = domain.MetadataModeStrip
	want.Chapters.Mode = domain.MetadataModeStrip
	if !reflect.DeepEqual(resolution.Profile, want) {
		t.Errorf("Profile =\n%+v\nwant\n%+v", resolution.Profile, want)
	}

	wantPlan := []string{
		"Video: encode to hevc with hevc_qsv, preset veryslow, 8-bit, XPSNR 42.5 target (CRF 20–30); encode anyway when no CRF fits; crop black bars",
		"Audio: keep orig, eng; encode truehd, dts to opus at 64 kb/s per channel; downmix above stereo to aac at 80 kb/s per channel",
		"Subtitles: keep eng, including forced, SDH, commentary; drop image-based",
		"Container: mkv; strip metadata; standardize track titles; strip attachments; strip chapters",
	}
	if !slices.Equal(resolution.Plan, wantPlan) {
		t.Errorf("Plan =\n%s\nwant\n%s", strings.Join(resolution.Plan, "\n"), strings.Join(wantPlan, "\n"))
	}
}

func TestEvaluateFixedCRFAndRemainingNodes(t *testing.T) {
	g := chain(
		newNode("in", typeInput, nil),
		// The search options are hidden in fixed mode and must not take effect.
		newNode("encode", typeVideoEncode, map[string]any{"quality_mode": "fixed", "crf": 22, "codec": "hevc", "accelerator": "qsv", "preset": "", "crf_min": 1, "target": 50}),
		newNode("audio-encode", typeAudioEncode, map[string]any{"codec": "flac", "bitrate_kbps_per_channel": 200, "source_codecs": []string{}}),
		newNode("downmix", typeAudioDownmix, nil),
		newNode("subtitles", typeSubtitlesKeep, map[string]any{"languages": []string{"eng"}}),
		newNode("remove", typeSubtitlesRemoveAll, nil),
		newNode("out", typeOutputPublish, nil),
	)
	resolution := mustEvaluate(t, viaJSON(t, g), probeOf())
	video := resolution.Profile.Video
	if video.QualityMode != domain.VideoQualityFixed || video.CRF != 22 || video.SkipEncode {
		t.Errorf("Video = %+v", video)
	}
	if video.CRFMin != 18 || video.Target != 95 {
		t.Errorf("hidden search options took effect: %+v", video)
	}
	audio := resolution.Profile.Audio
	if want := (domain.AudioEncode{Codec: "flac"}); !reflect.DeepEqual(*audio.Encode, want) {
		t.Errorf("Audio.Encode = %+v, want %+v", *audio.Encode, want)
	}
	if want := (domain.AudioDownmix{MaxChannels: 6, Codec: "eac3", BitrateKbpsPerChannel: 96}); *audio.Downmix != want {
		t.Errorf("Audio.Downmix = %+v, want %+v", *audio.Downmix, want)
	}
	if !resolution.Profile.Subtitles.RemoveAll {
		t.Error("RemoveAll is not set")
	}
	wantPlan := []string{
		"Video: encode to hevc with hevc_qsv, 10-bit, CRF 22",
		"Audio: keep every track; encode every track to flac; downmix above 5.1 to eac3 at 96 kb/s per channel",
		"Subtitles: remove all",
		"Container: mkv; keep metadata, track titles, attachments, and chapters",
	}
	if !slices.Equal(resolution.Plan, wantPlan) {
		t.Errorf("Plan =\n%s\nwant\n%s", strings.Join(resolution.Plan, "\n"), strings.Join(wantPlan, "\n"))
	}
}

func TestEvaluatePlanReadsLikeTheDryRun(t *testing.T) {
	g := chain(
		newNode("in", typeInput, nil),
		newNode("encode", typeVideoEncode, nil),
		newNode("audio", typeAudioKeep, map[string]any{"languages": []string{"orig", "eng"}}),
		newNode("audio-encode", typeAudioEncode, nil),
		newNode("subtitles", typeSubtitlesKeep, map[string]any{"languages": []string{"eng"}}),
		newNode("images", typeSubtitlesDropImage, nil),
		newNode("metadata", typeContainerMetadata, map[string]any{"track_titles": "standardize"}),
		newNode("out", typeOutputPublish, nil),
	)
	want := []string{
		"Video: encode to av1 with libsvtav1, preset 6, 10-bit, VMAF 95 target (CRF 18–40)",
		"Audio: keep orig, eng; encode truehd, dts to eac3 at 96 kb/s per channel",
		"Subtitles: keep eng; drop image-based",
		"Container: mkv; standardize track titles",
	}
	if got := mustEvaluate(t, g, probeOf()).Plan; !slices.Equal(got, want) {
		t.Errorf("Plan =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestEvaluateCropNeedsAnEncode(t *testing.T) {
	crop := newNode("crop", typeVideoCrop, map[string]any{"limit": 32})
	tests := []struct {
		name      string
		nodes     []Node
		wantCrop  bool
		wantVideo string
	}{
		{
			name:      "crop with an encode",
			nodes:     []Node{crop, newNode("encode", typeVideoEncode, nil)},
			wantCrop:  true,
			wantVideo: "Video: encode to av1 with libsvtav1, preset 6, 10-bit, VMAF 95 target (CRF 18–40); crop black bars",
		},
		{
			name:      "crop with an explicit copy",
			nodes:     []Node{crop, newNode("copy", typeVideoCopy, nil)},
			wantVideo: "Video: copy; black bars are not cropped, because cropping needs a re-encode",
		},
		{
			name:      "crop with a copy that wins over an encode",
			nodes:     []Node{newNode("encode", typeVideoEncode, nil), crop, newNode("copy", typeVideoCopy, nil)},
			wantVideo: "Video: copy; black bars are not cropped, because cropping needs a re-encode",
		},
		{
			name:      "crop without any video node",
			nodes:     []Node{crop},
			wantVideo: "Video: copy; black bars are not cropped, because cropping needs a re-encode",
		},
		{
			name:      "an encode without a crop node",
			nodes:     []Node{newNode("encode", typeVideoEncode, nil)},
			wantVideo: "Video: encode to av1 with libsvtav1, preset 6, 10-bit, VMAF 95 target (CRF 18–40)",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nodes := append([]Node{newNode("in", typeInput, nil)}, test.nodes...)
			resolution := mustEvaluate(t, chain(append(nodes, newNode("out", typeOutputPublish, nil))...), probeOf())
			if resolution.Profile.Crop.Disabled == test.wantCrop {
				t.Errorf("Crop.Disabled = %t", resolution.Profile.Crop.Disabled)
			}
			if test.wantCrop && resolution.Profile.Crop.Limit != 32 {
				t.Errorf("Crop = %+v, want the node's options", resolution.Profile.Crop)
			}
			if resolution.Plan[0] != test.wantVideo {
				t.Errorf("Plan[0] = %q, want %q", resolution.Plan[0], test.wantVideo)
			}
		})
	}
}

func TestSummaries(t *testing.T) {
	tests := []struct {
		nodeType string
		options  map[string]any
		want     string
	}{
		{typeInput, nil, "every queued file"},
		{typeCheckVideoCodec, map[string]any{"codecs": []string{"mpeg2", "h264"}}, "video.codec in h264, mpeg2"},
		{typeCheckVideoCodec, map[string]any{"codecs": []string{}}, "video.codec in nothing"},
		{typeCheckResolution, map[string]any{"resolution": "2160p"}, "at least 2160p"},
		{typeCheckResolution, map[string]any{"op": "at_most", "resolution": "720p"}, "at most 720p"},
		{typeCheckHDR, nil, "video.hdr in hdr10, hlg, dolby_vision"},
		{typeCheckBitrate, nil, "above 8000 kb/s"},
		{typeCheckFileSize, map[string]any{"op": "below", "megabytes": 700}, "below 700 MiB"},
		{typeCheckAudioLanguage, map[string]any{"languages": []string{"orig", "ENG"}}, "audio.language in orig, eng"},
		{typeCheckAudioCodec, nil, "audio.codec in truehd, dts"},
		{typeCheckAudioChannels, map[string]any{"more_than": "6"}, "audio.channels above 6"},
		{typeVideoEncode, nil, "libsvtav1 · vmaf 95 · preset 6"},
		{typeVideoEncode, map[string]any{"codec": "hevc", "accelerator": "qsv", "quality_mode": "fixed", "crf": 22, "preset": ""}, "hevc_qsv · crf 22"},
		{typeVideoEncode, map[string]any{"codec": "h264", "metric": "xpsnr", "target": 42.5, "preset": "slow"}, "libx264 · xpsnr 42.5 · preset slow"},
		{typeVideoCopy, nil, "keep the source video stream"},
		{typeVideoCrop, nil, "keep at least 70% · limit 64"},
		{typeAudioKeep, map[string]any{"languages": []string{"orig", "eng"}}, "orig, eng · keep_all fallback"},
		{typeAudioKeep, nil, "every language"},
		{typeAudioEncode, nil, "truehd, dts → eac3 · 96 kb/s per channel"},
		{typeAudioEncode, map[string]any{"codec": "flac", "source_codecs": []string{}}, "every track → flac"},
		{typeAudioEncode, map[string]any{"codec": "opus", "bitrate_kbps_per_channel": 0}, "truehd, dts → opus · default bitrate"},
		{typeAudioDownmix, nil, "above 5.1 → 5.1 · eac3"},
		{typeAudioDownmix, map[string]any{"max_channels": "2", "codec": "aac"}, "above stereo → stereo · aac"},
		{typeSubtitlesKeep, map[string]any{"languages": []string{"eng"}, "fallback": "keep_first"}, "eng · keep_first fallback"},
		{typeSubtitlesDropImage, nil, "hdmv_pgs_subtitle, dvd_subtitle, dvb_subtitle"},
		{typeSubtitlesRemoveAll, nil, "every subtitle track"},
		{typeContainerMetadata, nil, "keep metadata · strip titles"},
		{typeContainerMetadata, map[string]any{"metadata": "strip", "track_titles": "standardize", "attachments": "strip", "chapters": "strip"}, "strip metadata · standardize titles · no attachments · no chapters"},
		{typeOutputPublish, nil, "replace or hand off, as the library says"},
		{typeOutputSkip, nil, "leave the file untouched"},
	}
	covered := map[string]bool{}
	for _, test := range tests {
		covered[test.nodeType] = true
		g := viaJSON(t, Graph{Nodes: []Node{newNode("n", test.nodeType, test.options)}})
		if got := Summaries(g)["n"]; got != test.want {
			t.Errorf("%s %v summary = %q, want %q", test.nodeType, test.options, got, test.want)
		}
	}
	for _, nodeType := range Catalog() {
		if !covered[nodeType.ID] {
			t.Errorf("no summary case for %s", nodeType.ID)
		}
	}
}

func TestSummariesCoverEveryNodeOfAnyGraph(t *testing.T) {
	g := Graph{Nodes: []Node{
		newNode("unknown", "video.upscale", nil),
		newNode("broken", typeVideoEncode, map[string]any{"preset": 4, "target": "high", "nope": true}),
		newNode("lonely", typeOutputSkip, nil),
	}}
	want := map[string]string{
		"unknown": "unknown node type",
		// Options that fail validation read as their defaults.
		"broken": "libsvtav1 · vmaf 95 · preset 6",
		"lonely": "leave the file untouched",
	}
	if got := Summaries(g); !reflect.DeepEqual(got, want) {
		t.Errorf("Summaries() = %v, want %v", got, want)
	}
	if got := Summaries(Graph{}); got == nil || len(got) != 0 {
		t.Errorf("Summaries(empty) = %#v", got)
	}
}

func TestDescribe(t *testing.T) {
	hdr := videoStream("hevc", 3840, 1600)
	hdr.ColorTransfer = "smpte2084"
	dolbyVision := hdr
	dolbyVision.DolbyVision = &domain.DolbyVision{Profile: 8, BLCompatibilityID: 1}
	hlg := videoStream("hevc", 1920, 1080)
	hlg.ColorTransfer = "arib-std-b67"
	noDuration := probeOf(hdr).Probe
	noDuration.DurationSeconds = 0

	tests := []struct {
		name  string
		probe domain.ProbeResult
		want  Description
	}{
		{"hdr10", probeOf(hdr).Probe, Description{Resolution: "2160p", HDR: "hdr10", BitrateKbps: 9544}},
		{"dolby vision wins over its base layer", probeOf(dolbyVision).Probe, Description{Resolution: "2160p", HDR: "dolby_vision", BitrateKbps: 9544}},
		{"hlg", probeOf(hlg).Probe, Description{Resolution: "1080p", HDR: "hlg", BitrateKbps: 9544}},
		{"sdr below 480p", probeOf(videoStream("mpeg4", 640, 360)).Probe, Description{Resolution: "360p", BitrateKbps: 9544}},
		{"unknown duration", noDuration, Description{Resolution: "2160p", HDR: "hdr10"}},
		{"audio only", probeOf(audioStream("flac", "eng", 2)).Probe, Description{BitrateKbps: 9544}},
		{"empty", domain.ProbeResult{}, Description{}},
	}
	for _, test := range tests {
		if got := Describe(test.probe); got != test.want {
			t.Errorf("%s: Describe() = %+v, want %+v", test.name, got, test.want)
		}
	}
}
