package ffmpeg

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func encodeTrack(index int, codec string, channels int, layout string, kbps int) domain.AudioTrackPlan {
	return domain.AudioTrackPlan{StreamIndex: index, Action: domain.AudioTrackEncode, Codec: codec, Channels: channels, Layout: layout, BitrateKbps: kbps}
}

func TestAudioArgs(t *testing.T) {
	tests := []struct {
		name string
		plan domain.EncodePlan
		want []string
	}{
		{
			name: "no selection copies",
			plan: domain.EncodePlan{},
			want: []string{"-c:a", "copy"},
		},
		{
			name: "selection without a track plan copies",
			plan: domain.EncodePlan{AudioSelectionApplied: true, AudioStreamIndexes: []int{1, 2}},
			want: []string{"-c:a", "copy"},
		},
		{
			name: "copied tracks need no per-stream options",
			plan: domain.EncodePlan{
				AudioSelectionApplied: true, AudioStreamIndexes: []int{1},
				AudioTracks: []domain.AudioTrackPlan{{StreamIndex: 1, Action: domain.AudioTrackCopy, Codec: "eac3", Channels: 6}},
			},
			want: []string{"-c:a", "copy"},
		},
		{
			name: "options address the output audio index, not the input stream",
			plan: domain.EncodePlan{
				AudioSelectionApplied: true, AudioStreamIndexes: []int{2, 5, 7},
				AudioTracks: []domain.AudioTrackPlan{
					{StreamIndex: 2, Action: domain.AudioTrackCopy, Codec: "eac3", Channels: 6},
					encodeTrack(5, "opus", 6, "5.1", 384),
					encodeTrack(7, "eac3", 6, "5.1(side)", 576),
				},
			},
			want: []string{
				"-c:a", "copy",
				"-c:a:1", "libopus", "-b:a:1", "384k", "-filter:a:1", "aformat=channel_layouts=5.1",
				"-c:a:2", "eac3", "-b:a:2", "576k", "-filter:a:2", "aformat=channel_layouts=5.1(side)",
			},
		},
		{
			name: "zero bitrate and a kept layout leave the encoder defaults",
			plan: domain.EncodePlan{
				AudioSelectionApplied: true, AudioStreamIndexes: []int{1, 2, 3},
				AudioTracks: []domain.AudioTrackPlan{
					encodeTrack(1, "flac", 8, "", 0),
					encodeTrack(2, "aac", 2, "stereo", 0),
					encodeTrack(3, "ac3", 6, "", 640),
				},
			},
			want: []string{
				"-c:a", "copy",
				"-c:a:0", "flac",
				"-c:a:1", "aac", "-filter:a:1", "aformat=channel_layouts=stereo",
				"-c:a:2", "ac3", "-b:a:2", "640k",
			},
		},
		{
			name: "a track plan for an unselected stream is ignored",
			plan: domain.EncodePlan{
				AudioSelectionApplied: true, AudioStreamIndexes: []int{2},
				AudioTracks: []domain.AudioTrackPlan{encodeTrack(1, "opus", 2, "", 128), encodeTrack(2, "opus", 2, "", 128)},
			},
			want: []string{"-c:a", "copy", "-c:a:0", "libopus", "-b:a:0", "128k"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := audioArgs(tt.plan); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("args = %q\nwant   %q", got, tt.want)
			}
		})
	}
}

func TestArgsClearStatisticsOfReencodedAudioOnly(t *testing.T) {
	plan := domain.EncodePlan{
		InputPath: "in.mkv", OutputPath: "out.anvil-part", VideoCopy: true,
		AudioSelectionApplied: true, AudioStreamIndexes: []int{1, 2},
		AudioTracks: []domain.AudioTrackPlan{
			{StreamIndex: 1, Action: domain.AudioTrackCopy},
			encodeTrack(2, "opus", 6, "5.1", 384),
		},
	}
	args := Args(plan)
	if !containsPair(args, "-metadata:s:a:1", "BPS=") || !containsPair(args, "-metadata:s:a:1", "NUMBER_OF_BYTES=") {
		t.Fatalf("re-encoded audio keeps source statistics: %q", args)
	}
	if slices.Contains(args, "-metadata:s:a:0") || slices.Contains(args, "-metadata:s:v") {
		t.Fatalf("copied streams lost their statistics: %q", args)
	}
}

func TestEncodeRejectsAudioPlanWithoutEncoder(t *testing.T) {
	for _, track := range []domain.AudioTrackPlan{encodeTrack(1, "mp3", 2, "", 128), encodeTrack(1, "opus", 0, "", 128)} {
		plan := domain.EncodePlan{InputPath: "in.mkv", OutputPath: "out.mkv", AudioSelectionApplied: true, AudioStreamIndexes: []int{1}, AudioTracks: []domain.AudioTrackPlan{track}}
		if _, err := (Encoder{Runner: destinationRunner(nil)}).Encode(t.Context(), plan); err == nil {
			t.Fatalf("accepted %+v", track)
		}
	}
}

func audioPlanRequest(titles domain.TrackTitleMode) BuildPlanRequest {
	return BuildPlanRequest{
		InputPath: "in.mkv", OutputPath: "out.anvil-part",
		Profile: domain.Profile{Container: "mkv", Metadata: domain.MetadataPolicy{TrackTitles: titles}},
		Probe: &domain.ProbeResult{Streams: []domain.MediaStream{
			{Index: 0, Type: "video", Codec: "hevc", Width: 1920, Height: 1080},
			{Index: 1, Type: "audio", Codec: "truehd", Channels: 8, ChannelLayout: "7.1", Language: "eng"},
			{Index: 2, Type: "audio", Codec: "ac3", Channels: 6, ChannelLayout: "5.1(side)", Language: "deu"},
		}},
		Audio: &domain.AudioSelection{
			StreamIndexes: []int{1, 2},
			Tracks: []domain.AudioTrackPlan{
				{StreamIndex: 1, Action: domain.AudioTrackEncode, SourceCodec: "truehd", SourceChannels: 8, Codec: "eac3", Channels: 6, Layout: "5.1(side)", BitrateKbps: 576},
				{StreamIndex: 2, Action: domain.AudioTrackCopy, SourceCodec: "ac3", SourceChannels: 6, Codec: "ac3", Channels: 6},
			},
		},
	}
}

func TestBuildPlanCarriesAudioTracksAndTitlesTheOutput(t *testing.T) {
	plan, err := BuildPlanFromRequest(audioPlanRequest(domain.TrackTitleModeStandardize))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.AudioTracks) != 2 || plan.AudioTracks[0].Codec != "eac3" {
		t.Fatalf("audio tracks = %+v", plan.AudioTracks)
	}
	args := Args(plan)
	if !containsPair(args, "-metadata:s:a:0", "title=English E-AC-3 5.1") {
		t.Fatalf("re-encoded track is not titled by its output: %q", args)
	}
	if !containsPair(args, "-metadata:s:a:1", "title=German AC-3 5.1") {
		t.Fatalf("copied track title changed: %q", args)
	}
	if !containsPair(args, "-c:a:0", "eac3") || !containsPair(args, "-filter:a:0", "aformat=channel_layouts=5.1(side)") {
		t.Fatalf("args = %q", args)
	}
}

func TestTrackTitleModesLeaveReencodedAudioToFFmpeg(t *testing.T) {
	preserve, err := BuildPlanFromRequest(audioPlanRequest(domain.TrackTitleModePreserve))
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(Args(preserve), " "); strings.Contains(joined, "title=") {
		t.Fatalf("preserve mode rewrote a title: %s", joined)
	}
	strip, err := BuildPlanFromRequest(audioPlanRequest(domain.TrackTitleModeStrip))
	if err != nil {
		t.Fatal(err)
	}
	if !containsPair(Args(strip), "-metadata:s", "title=") {
		t.Fatal("strip mode kept titles")
	}
}

func TestBuildPlanFixedQualityAndCropOptOut(t *testing.T) {
	request := BuildPlanRequest{
		InputPath: "in.mkv", OutputPath: "out.anvil-part",
		Profile: domain.Profile{Container: "mkv", Video: domain.VideoProfile{
			Codec: "av1", QualityMode: domain.VideoQualityFixed, CRF: 24, CRFMin: 18, CRFMax: 40,
		}},
		Probe:    &domain.ProbeResult{Streams: []domain.MediaStream{{Index: 0, Type: "video", Codec: "hevc", Width: 1920, Height: 1080}}},
		Metadata: domain.JobMetadata{CropFilter: "crop=1920:800:0:140"},
	}
	tests := []struct {
		name     string
		change   func(*BuildPlanRequest)
		wantCRF  int
		wantCrop string
		wantCopy bool
	}{
		{"fixed CRF without a search result", func(*BuildPlanRequest) {}, 24, "crop=1920:800:0:140", false},
		{"fixed CRF ignores a search result", func(r *BuildPlanRequest) { r.Search = &domain.SearchResult{CRF: 33} }, 24, "crop=1920:800:0:140", false},
		{"search mode takes the search result", func(r *BuildPlanRequest) {
			r.Profile.Video.QualityMode = domain.VideoQualitySearch
			r.Search = &domain.SearchResult{CRF: 33}
		}, 33, "crop=1920:800:0:140", false},
		{"zero mode means search", func(r *BuildPlanRequest) {
			r.Profile.Video.QualityMode = ""
			r.Search = &domain.SearchResult{CRF: 31}
		}, 31, "crop=1920:800:0:140", false},
		{"skip encode still copies in fixed mode", func(r *BuildPlanRequest) { r.Profile.Video.SkipEncode = true }, 0, "crop=1920:800:0:140", true},
		{"disabled crop drops a filter left in the metadata", func(r *BuildPlanRequest) { r.Profile.Crop.Disabled = true }, 24, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := request
			tt.change(&request)
			plan, err := BuildPlanFromRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			if plan.CRF != tt.wantCRF || plan.CropFilter != tt.wantCrop || plan.VideoCopy != tt.wantCopy || plan.QualityMode != request.Profile.Video.QualityMode {
				t.Fatalf("plan CRF %d crop %q copy %v mode %q", plan.CRF, plan.CropFilter, plan.VideoCopy, plan.QualityMode)
			}
			args := Args(plan)
			if !tt.wantCopy && !containsPair(args, "-crf", strconv.Itoa(tt.wantCRF)) {
				t.Fatalf("args = %q", args)
			}
			if tt.wantCrop == "" && slices.Contains(args, "-vf") {
				t.Fatalf("disabled crop reached the encoder: %q", args)
			}
		})
	}
}

func containsPair(args []string, option, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == option && args[i+1] == value {
			return true
		}
	}
	return false
}
