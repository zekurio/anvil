package ffmpeg_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/audio"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/ffmpeg"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/probe"
	"github.com/zekurio/anvil/pkg/process"
	"github.com/zekurio/anvil/pkg/search"
	"github.com/zekurio/anvil/pkg/subtitle"
	"github.com/zekurio/anvil/pkg/validate"
)

// pgsSegment frames one Presentation Graphics segment as a .sup file holds it.
func pgsSegment(pts time.Duration, kind byte, payload ...byte) []byte {
	segment := []byte{'P', 'G'}
	segment = binary.BigEndian.AppendUint32(segment, uint32(pts.Seconds()*90000))
	segment = binary.BigEndian.AppendUint32(segment, 0)
	segment = append(segment, kind)
	segment = binary.BigEndian.AppendUint16(segment, uint16(len(payload)))
	return append(segment, payload...)
}

// pgsStream is a minimal image subtitle: one 16x8 box shown from 0.5s to 2s.
// ffmpeg cannot render text to a bitmap subtitle, so the fixture is built by
// hand.
func pgsStream(width, height uint16) []byte {
	size := binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(nil, width), height)
	window := []byte{1, 0, 0, 16, 0, 16, 0, 16, 0, 8} // one window: id 0 at 16,16, 16x8
	var pixels []byte
	for range 8 {
		pixels = append(pixels, 0x00, 0x90, 0x01, 0x00, 0x00) // 16 pixels of color 1, end of line
	}
	object := []byte{0, 0, 0, 0xC0, 0, 0, byte(len(pixels) + 4), 0, 16, 0, 8}
	show, hide := 500*time.Millisecond, 2*time.Second
	var stream []byte
	stream = append(stream, pgsSegment(show, 0x16, append(slices.Clone(size), 0x10, 0, 0, 0x80, 0, 0, 1, 0, 0, 0, 0, 0, 16, 0, 16)...)...)
	stream = append(stream, pgsSegment(show, 0x17, window...)...)
	stream = append(stream, pgsSegment(show, 0x14, 0, 0, 1, 235, 128, 128, 255)...)
	stream = append(stream, pgsSegment(show, 0x15, append(object, pixels...)...)...)
	stream = append(stream, pgsSegment(show, 0x80)...)
	stream = append(stream, pgsSegment(hide, 0x16, append(slices.Clone(size), 0x10, 0, 1, 0, 0, 0, 0)...)...)
	stream = append(stream, pgsSegment(hide, 0x17, window...)...)
	stream = append(stream, pgsSegment(hide, 0x80)...)
	return stream
}

// streamFixture writes a source with the audio and subtitle shapes the stream
// plans have to survive: a 5.1(side) track, a 7.1 track, a stereo commentary,
// a 44.1 kHz track, a text subtitle, and two image subtitles.
func streamFixture(ctx context.Context, t *testing.T, root string) string {
	t.Helper()
	text := filepath.Join(root, "text.srt")
	if err := os.WriteFile(text, []byte("1\n00:00:00,500 --> 00:00:02,000\nHello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(root, "image.sup")
	if err := os.WriteFile(image, pgsStream(192, 128), 0o600); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "source.mkv")
	spread := func(source, layout string, channels int, label string) string {
		pan := "pan=" + layout
		for channel := range channels {
			pan += fmt.Sprintf("|c%d=c0", channel)
		}
		return source + pan + label
	}
	_, err := (process.OSRunner{}).Run(ctx, process.Command{Name: "ffmpeg", Args: []string{
		"-hide_banner", "-nostdin", "-n",
		"-f", "lavfi", "-i", "testsrc2=size=192x128:rate=12:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=550:sample_rate=48000:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=770:sample_rate=44100:duration=3",
		// Without -fix_sub_duration the transcoded image subtitle never ends
		// and stretches the container duration to 49 days.
		"-i", text, "-fix_sub_duration", "-i", image,
		"-filter_complex", strings.Join([]string{
			spread("[1:a]", "5.1(side)", 6, "[surround]"),
			spread("[2:a]", "7.1", 8, "[wide]"),
			spread("[3:a]", "stereo", 2, "[stereo]"),
			spread("[4:a]", "5.1(side)", 6, "[lowrate]"),
		}, ";"),
		"-map", "0:v", "-map", "[surround]", "-map", "[wide]", "-map", "[stereo]", "-map", "[lowrate]",
		"-map", "5:0", "-map", "6:0", "-map", "6:0",
		"-c:v", "ffv1", "-threads", "1",
		"-c:a:0", "ac3", "-b:a:0", "448k", "-c:a:1", "flac", "-c:a:2", "aac", "-c:a:3", "dca", "-strict", "-2",
		"-c:s:0", "srt", "-c:s:1", "copy", "-c:s:2", "dvdsub",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:0", "title=Main", "-disposition:a:0", "default",
		"-metadata:s:a:1", "language=jpn", "-metadata:s:a:1", "title=Lossless", "-disposition:a:1", "0",
		"-metadata:s:a:2", "language=deu", "-metadata:s:a:2", "title=Kommentar", "-disposition:a:2", "comment",
		"-metadata:s:a:3", "language=fra", "-metadata:s:a:3", "title=Doublage", "-disposition:a:3", "forced",
		"-metadata:s:a:1", "BPS=1234567", "-metadata:s:a:1", "NUMBER_OF_BYTES=7654321",
		"-metadata:s:s:0", "language=eng", "-metadata:s:s:1", "language=deu", "-metadata:s:s:2", "language=eng",
		"-disposition:s:0", "default", "-disposition:s:1", "0", "-disposition:s:2", "0",
		input,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

// track is what ffprobe reports for one output stream.
type track struct {
	codec    string
	channels int
	layout   string
	language string
	title    string
	flags    string
}

func tracks(streams []domain.MediaStream, kind string) []track {
	var result []track
	for _, stream := range streams {
		if stream.Type != kind {
			continue
		}
		var flags []string
		for _, flag := range []string{"default", "forced", "comment"} {
			if stream.Disposition[flag] {
				flags = append(flags, flag)
			}
		}
		result = append(result, track{stream.Codec, stream.Channels, stream.ChannelLayout, stream.Language, stream.Title, strings.Join(flags, ",")})
	}
	return result
}

// Run with ANVIL_MEDIA_TEST=1 inside nix develop. Uses synthetic media only.
// Every case runs the exact argument list the plan produces through ffmpeg and
// reads the result back with ffprobe.
func TestStreamPlansWithFFmpeg(t *testing.T) {
	if os.Getenv("ANVIL_MEDIA_TEST") != "1" {
		t.Skip("set ANVIL_MEDIA_TEST=1 to run real FFmpeg encodes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root := t.TempDir()
	input := streamFixture(ctx, t, root)
	source, err := (probe.FFProbe{}).Probe(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("source audio %+v", tracks(source.Streams, "audio"))
	t.Logf("source subtitles %+v", tracks(source.Streams, "subtitle"))

	text := track{"subrip", 0, "", "eng", "", "default"}
	pgs := track{"hdmv_pgs_subtitle", 0, "", "deu", "", ""}
	vobsub := track{"dvd_subtitle", 0, "", "eng", "", ""}
	tests := []struct {
		name      string
		audio     domain.AudioProfile
		subtitles domain.SubtitleProfile
		titles    domain.TrackTitleMode
		wantAudio []track
		wantSubs  []track
	}{
		{
			name:  "opus from 5.1(side), 7.1, stereo, and 44.1 kHz",
			audio: domain.AudioProfile{Encode: &domain.AudioEncode{Codec: "opus", BitrateKbpsPerChannel: 64}},
			wantAudio: []track{
				{"opus", 6, "5.1", "eng", "Main", "default"},
				{"opus", 8, "7.1", "jpn", "Lossless", ""},
				{"opus", 2, "stereo", "deu", "Kommentar", "comment"},
				{"opus", 6, "5.1", "fra", "Doublage", "forced"},
			},
			wantSubs: []track{text, pgs, vobsub},
		},
		{
			name:  "eac3 downmixes 7.1 to 5.1 and the other tracks are copied",
			audio: domain.AudioProfile{Encode: &domain.AudioEncode{Codec: "eac3", BitrateKbpsPerChannel: 96, SourceCodecs: []string{"flac"}}},
			wantAudio: []track{
				{"ac3", 6, "5.1(side)", "eng", "Main", "default"},
				{"eac3", 6, "5.1(side)", "jpn", "Lossless", ""},
				{"aac", 2, "stereo", "deu", "Kommentar", "comment"},
				{"dts", 6, "5.1(side)", "fra", "Doublage", "forced"},
			},
			wantSubs: []track{text, pgs, vobsub},
		},
		{
			name:  "aac stereo downmix",
			audio: domain.AudioProfile{Downmix: &domain.AudioDownmix{MaxChannels: 2, Codec: "aac", BitrateKbpsPerChannel: 96}},
			wantAudio: []track{
				{"aac", 2, "stereo", "eng", "Main", "default"},
				{"aac", 2, "stereo", "jpn", "Lossless", ""},
				{"aac", 2, "stereo", "deu", "Kommentar", "comment"},
				{"aac", 2, "stereo", "fra", "Doublage", "forced"},
			},
			wantSubs: []track{text, pgs, vobsub},
		},
		{
			// Left to itself the aac encoder writes 5.1(side) as a program
			// config element, which ffprobe reads back as an unknown layout.
			name:  "aac writes a standard layout from 5.1(side)",
			audio: domain.AudioProfile{Encode: &domain.AudioEncode{Codec: "aac", BitrateKbpsPerChannel: 64, SourceCodecs: []string{"ac3", "dts"}}},
			wantAudio: []track{
				{"aac", 6, "5.1", "eng", "Main", "default"},
				{"flac", 8, "7.1", "jpn", "Lossless", ""},
				{"aac", 2, "stereo", "deu", "Kommentar", "comment"},
				{"aac", 6, "5.1", "fra", "Doublage", "forced"},
			},
			wantSubs: []track{text, pgs, vobsub},
		},
		{
			name:  "flac from lossy 5.1(side) tracks",
			audio: domain.AudioProfile{Encode: &domain.AudioEncode{Codec: "flac", BitrateKbpsPerChannel: 128, SourceCodecs: []string{"ac3", "dts"}}},
			wantAudio: []track{
				{"flac", 6, "5.1(side)", "eng", "Main", "default"},
				{"flac", 8, "7.1", "jpn", "Lossless", ""},
				{"aac", 2, "stereo", "deu", "Kommentar", "comment"},
				{"flac", 6, "5.1(side)", "fra", "Doublage", "forced"},
			},
			wantSubs: []track{text, pgs, vobsub},
		},
		{
			name: "ac3 downmix after a language filter moved the output indexes",
			audio: domain.AudioProfile{
				LanguagesToKeep: []string{"jpn", "fra"},
				Downmix:         &domain.AudioDownmix{MaxChannels: 6, Codec: "ac3", BitrateKbpsPerChannel: 128},
			},
			// The default track was dropped, so the Matroska muxer marks the
			// first remaining audio track default, as it does for a plain copy.
			wantAudio: []track{
				{"ac3", 6, "5.1(side)", "jpn", "Lossless", "default"},
				{"dts", 6, "5.1(side)", "fra", "Doublage", "forced"},
			},
			wantSubs: []track{text, pgs, vobsub},
		},
		{
			name: "standardized titles name the output codec and channels",
			audio: domain.AudioProfile{
				Encode:  &domain.AudioEncode{Codec: "eac3", SourceCodecs: []string{"flac", "dts"}},
				Downmix: &domain.AudioDownmix{MaxChannels: 2, Codec: "aac"},
			},
			titles: domain.TrackTitleModeStandardize,
			wantAudio: []track{
				{"aac", 2, "stereo", "eng", "English AAC Stereo", "default"},
				{"eac3", 2, "stereo", "jpn", "Japanese E-AC-3 Stereo", ""},
				{"aac", 2, "stereo", "deu", "German AAC Stereo", "comment"},
				{"eac3", 2, "stereo", "fra", "French E-AC-3 Stereo", "forced"},
			},
			wantSubs: []track{
				{"subrip", 0, "", "eng", "English Full SRT Subtitle", "default"},
				{"hdmv_pgs_subtitle", 0, "", "deu", "German Full PGS Subtitle", ""},
				{"dvd_subtitle", 0, "", "eng", "English Full dvd_subtitle Subtitle", ""},
			},
		},
		{
			name:      "remove all subtitles",
			subtitles: domain.SubtitleProfile{RemoveAll: true, LanguagesToKeep: []string{"eng"}, Fallback: domain.StreamFallbackKeepAll},
			wantAudio: tracks(source.Streams, "audio"),
		},
		{
			name:      "drop image subtitles",
			subtitles: domain.SubtitleProfile{DropBitmap: true},
			wantAudio: tracks(source.Streams, "audio"),
			wantSubs:  []track{text},
		},
		{
			name:      "drop image subtitles and fall back among the text tracks",
			subtitles: domain.SubtitleProfile{DropBitmap: true, LanguagesToKeep: []string{"deu"}, Fallback: domain.StreamFallbackKeepAll},
			wantAudio: tracks(source.Streams, "audio"),
			wantSubs:  []track{text},
		},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			titles := tt.titles
			if titles == "" {
				titles = domain.TrackTitleModePreserve
			}
			profile := domain.Profile{
				Container: "mkv",
				Video:     domain.VideoProfile{SkipEncode: true},
				Audio:     tt.audio,
				Subtitles: tt.subtitles,
				Metadata:  domain.MetadataPolicy{Mode: domain.MetadataModePreserve, TrackTitles: titles},
			}
			audioSelection, err := (audio.Selector{}).Select(&source, profile.Audio, domain.JobMetadata{})
			if err != nil {
				t.Fatal(err)
			}
			subtitleSelection, err := (subtitle.Selector{}).Select(&source, profile.Subtitles, domain.JobMetadata{})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := ffmpeg.BuildPlanFromRequest(ffmpeg.BuildPlanRequest{
				Profile: profile, InputPath: input, OutputPath: filepath.Join(root, fmt.Sprintf("case-%d.anvil-part", index)),
				Probe: &source, Audio: &audioSelection, Subtitles: &subtitleSelection,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, planned := range plan.AudioTracks {
				t.Logf("plan %s %+v", planned.Summary(), planned)
			}
			t.Logf("ffmpeg %q", ffmpeg.Args(plan))
			if _, err := (ffmpeg.Encoder{}).Encode(ctx, plan); err != nil {
				t.Fatal(err)
			}
			output, err := (probe.FFProbe{}).Probe(ctx, plan.OutputPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := tracks(output.Streams, "audio"); !reflect.DeepEqual(got, tt.wantAudio) {
				t.Errorf("audio = %+v\nwant    %+v", got, tt.wantAudio)
			}
			if got := tracks(output.Streams, "subtitle"); !reflect.DeepEqual(got, tt.wantSubs) {
				t.Errorf("subtitles = %+v\nwant        %+v", got, tt.wantSubs)
			}
			for position, stream := range streamsOf(output.Streams, "audio") {
				if position < len(plan.AudioTracks) && plan.AudioTracks[position].Action == domain.AudioTrackEncode && stream.Tags["BPS"] != "" {
					t.Errorf("re-encoded audio track %d kept the source statistics: %v", position, stream.Tags)
				}
			}
			result, err := (validate.Validator{}).Validate(ctx, validate.Request{
				SourceProbe: &source, SourcePath: input, OutputPath: plan.OutputPath, Profile: profile,
				EncodePlan: &plan, Audio: &audioSelection, Subtitles: &subtitleSelection,
			})
			if err != nil {
				t.Errorf("validation = %v: %q", err, result.Errors)
			}
		})
	}
}

func streamsOf(streams []domain.MediaStream, kind string) []domain.MediaStream {
	var result []domain.MediaStream
	for _, stream := range streams {
		if stream.Type == kind {
			result = append(result, stream)
		}
	}
	return result
}

type failingSearcher struct{}

func (failingSearcher) Search(context.Context, domain.EncodePlan, string) (domain.SearchResult, error) {
	return domain.SearchResult{}, errors.New("fixed CRF must not search")
}

// Run with ANVIL_MEDIA_TEST=1 inside nix develop. A fixed CRF goes from the
// profile through the crf-search step to a real libsvtav1 encode without any
// sampling, and a different CRF changes the result.
func TestFixedCRFEncodeWithFFmpeg(t *testing.T) {
	if os.Getenv("ANVIL_MEDIA_TEST") != "1" {
		t.Skip("set ANVIL_MEDIA_TEST=1 to run real FFmpeg encodes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root := t.TempDir()
	input := streamFixture(ctx, t, root)
	source, err := (probe.FFProbe{}).Probe(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	sizes := make(map[int]int64)
	for _, crf := range []int{20, 55} {
		job := &pipeline.JobContext{
			InputPath: input, OutputPath: filepath.Join(root, fmt.Sprintf("crf-%d.anvil-part", crf)), Probe: &source,
			Resources: domain.ResourceAllocation{Threads: 2},
			Profile: domain.Profile{
				Container: "mkv",
				Crop:      domain.CropPolicy{Disabled: true},
				// A target and savings floor no encode could meet: fixed mode
				// must not look at either.
				Video: domain.VideoProfile{
					Codec: "av1", Accelerator: "software", Preset: "12", BitDepth: 10,
					QualityMode: domain.VideoQualityFixed, CRF: crf, CRFMin: 30, CRFMax: 32,
					Metric: domain.QualityMetricVMAF, Target: 100, MinSavingsPercent: 99,
					FFmpegArgs: []string{"-svtav1-params", "lp=2"},
				},
			},
			// A crop left over from an earlier profile must not be applied.
			Metadata: domain.JobMetadata{CropFilter: "crop=160:96:16:16"},
		}
		block := search.Block{Searcher: failingSearcher{}}
		if err := block.Run(ctx, job); err != nil {
			t.Fatal(err)
		}
		report, _ := block.Artifact(job)
		t.Logf("crf-search: %s", report.Message)
		plan, err := ffmpeg.BuildPlanFromRequest(ffmpeg.BuildPlanRequest{
			Profile: job.Profile, InputPath: job.InputPath, OutputPath: job.OutputPath, Resources: job.Resources,
			Search: job.Search, Metadata: job.Metadata, Probe: job.Probe,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("ffmpeg %q", ffmpeg.Args(plan))
		if _, err := (ffmpeg.Encoder{}).Encode(ctx, plan); err != nil {
			t.Fatal(err)
		}
		output, err := (probe.FFProbe{}).Probe(ctx, plan.OutputPath)
		if err != nil {
			t.Fatal(err)
		}
		video, ok := domain.PrimaryVideoStream(output.Streams)
		if !ok || video.Codec != "av1" || video.PixelFormat != "yuv420p10le" || video.Width != 192 || video.Height != 128 {
			t.Fatalf("video = %+v", video)
		}
		info, err := os.Stat(plan.OutputPath)
		if err != nil {
			t.Fatal(err)
		}
		sizes[crf] = info.Size()
	}
	t.Logf("output bytes by CRF: %v", sizes)
	if sizes[20] <= sizes[55] {
		t.Fatalf("CRF 20 output (%d bytes) is not larger than CRF 55 (%d bytes): the fixed CRF was not applied", sizes[20], sizes[55])
	}
}
