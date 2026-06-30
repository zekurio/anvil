package probe

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
)

func TestFFProbeParsesJSON(t *testing.T) {
	runner := fakeRunner{stdout: []byte(`{
		"streams": [
			{
				"index":0,
				"codec_type":"video",
				"codec_name":"hevc",
				"width":1920,
				"height":800,
				"pix_fmt":"yuv420p10le",
				"bit_rate":"4200000",
				"tags":{"language":"eng","title":"Main"},
				"disposition":{"default":1}
			},
			{
				"index":1,
				"codec_type":"audio",
				"codec_name":"aac",
				"bit_rate":"128000",
				"channels":2,
				"channel_layout":"stereo",
				"tags":{"language":"jpn"},
				"disposition":{"default":0}
			}
		],
		"format": {"format_name":"matroska,webm","duration":"123.456","size":"98765"}
	}`)}
	result, err := FFProbe{Runner: runner}.Probe(context.Background(), "/media/input.mkv")
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if result.DurationSeconds != 123.456 {
		t.Fatalf("duration = %f, want 123.456", result.DurationSeconds)
	}
	if len(result.Streams) != 2 {
		t.Fatalf("streams = %d, want 2", len(result.Streams))
	}
	if result.Streams[0].Codec != "hevc" || !result.Streams[0].Disposition["default"] {
		t.Fatalf("first stream = %+v, want hevc default", result.Streams[0])
	}
	if got, want := result.Streams[0].PixelFormat, "yuv420p10le"; got != want {
		t.Fatalf("first stream pixel format = %q, want %q", got, want)
	}
	if got, want := result.Streams[0].Width, 1920; got != want {
		t.Fatalf("first stream width = %d, want %d", got, want)
	}
	if got, want := result.Streams[0].Height, 800; got != want {
		t.Fatalf("first stream height = %d, want %d", got, want)
	}
	if got, want := result.Streams[0].BitRate, int64(4200000); got != want {
		t.Fatalf("first stream bit rate = %d, want %d", got, want)
	}
	if got, want := result.Streams[1].Channels, 2; got != want {
		t.Fatalf("second stream channels = %d, want %d", got, want)
	}
	if got, want := result.Streams[1].ChannelLayout, "stereo"; got != want {
		t.Fatalf("second stream channel layout = %q, want %q", got, want)
	}
	if got, want := result.Streams[0].Tags["title"], "Main"; got != want {
		t.Fatalf("first stream title tag = %q, want %q", got, want)
	}
}

func TestFFProbeWrapsRunnerErrorWithPath(t *testing.T) {
	runnerErr := errors.New("exit status 1")
	_, err := FFProbe{Runner: fakeRunner{err: runnerErr}}.Probe(context.Background(), "/media/input.mkv")
	if err == nil {
		t.Fatal("Probe() error = nil, want ffprobe runner error")
	}
	for _, want := range []string{"run ffprobe", "/media/input.mkv", "exit status 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Probe() error = %q, want %q", err, want)
		}
	}
	if !errors.Is(err, runnerErr) {
		t.Fatalf("Probe() error does not wrap runner error: %v", err)
	}
}

func TestFFProbeParsesHDRAndDolbyVisionMetadata(t *testing.T) {
	runner := fakeRunner{stdout: []byte(`{
		"streams": [
			{
				"index":0,
				"codec_type":"video",
				"codec_name":"hevc",
				"pix_fmt":"yuv420p10le",
				"color_range":"tv",
				"color_space":"bt2020nc",
				"color_transfer":"smpte2084",
				"color_primaries":"bt2020",
				"side_data_list":[{
					"side_data_type":"DOVI configuration record",
					"dv_profile":8,
					"dv_level":6,
					"rpu_present_flag":1,
					"el_present_flag":0,
					"bl_present_flag":1,
					"dv_bl_signal_compatibility_id":1
				}]
			}
		],
		"format": {"format_name":"matroska,webm","duration":"123.456","size":"98765"}
	}`)}
	result, err := FFProbe{Runner: runner}.Probe(context.Background(), "/media/input.mkv")
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	video := result.Streams[0]
	if got, want := video.ColorTransfer, "smpte2084"; got != want {
		t.Fatalf("ColorTransfer = %q, want %q", got, want)
	}
	if video.DolbyVision == nil {
		t.Fatal("DolbyVision = nil, want metadata")
	}
	if got, want := video.DolbyVision.Profile, 8; got != want {
		t.Fatalf("DolbyVision.Profile = %d, want %d", got, want)
	}
	if !video.DolbyVision.RPUPresent || !video.DolbyVision.BLPresent {
		t.Fatalf("DolbyVision flags = %+v, want RPU and BL present", video.DolbyVision)
	}
}

func TestBlockMarksCompatibleAnvilEncodedVideo(t *testing.T) {
	runner := fakeRunner{stdout: []byte(`{
		"streams": [
			{"index":0,"codec_type":"video","codec_name":"av1","tags":{
				"anvil.encoded":"true",
				"anvil.profile":"default-av1",
				"anvil.video.codec":"libsvtav1",
				"anvil.video.pixel_format":"yuv420p10le",
				"anvil.crop":"crop=1920:800:0:140"
			},"disposition":{}}
		],
		"format": {"format_name":"matroska,webm","duration":"123.456","size":"98765"}
	}`)}
	job := &pipeline.JobContext{
		InputPath: "/media/input.mkv",
		Profile: domain.Profile{
			Name: domain.ProfileName("default-av1"),
			Video: domain.VideoProfile{
				Codec:       "av1",
				Accelerator: "software",
				BitDepth:    10,
			},
		},
	}
	if err := (Block{Prober: FFProbe{Runner: runner}}).Run(context.Background(), job); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !job.Metadata.VideoAlreadyEncoded {
		t.Fatal("VideoAlreadyEncoded = false, want true")
	}
	if got, want := job.Metadata.CropFilter, "crop=1920:800:0:140"; got != want {
		t.Fatalf("crop filter = %q, want %q", got, want)
	}
}

func TestBlockSelectsDolbyVisionEncoderWhenDoviToolAvailable(t *testing.T) {
	runner := fakeRunner{stdout: []byte(`{
		"streams": [
			{
				"index":0,
				"codec_type":"video",
				"codec_name":"hevc",
				"pix_fmt":"yuv420p10le",
				"color_transfer":"smpte2084",
				"side_data_list":[{
					"side_data_type":"DOVI configuration record",
					"dv_profile":8,
					"rpu_present_flag":1,
					"bl_present_flag":1
				}]
			}
		],
		"format": {"format_name":"matroska,webm","duration":"123.456","size":"98765"}
	}`)}
	job := &pipeline.JobContext{
		InputPath: "/media/input.mkv",
		Profile: domain.Profile{
			Name: domain.ProfileName("default-av1"),
			Video: domain.VideoProfile{
				Codec:       "av1",
				Accelerator: "software",
				BitDepth:    10,
				DolbyVision: domain.DolbyVisionProfile{
					Mode:        domain.DolbyVisionModeAuto,
					Codec:       "hevc",
					Accelerator: "qsv",
					BitDepth:    10,
				},
			},
		},
	}
	block := Block{
		Prober:          FFProbe{Runner: runner},
		DolbyVisionTool: fakeDolbyVisionTool{available: true},
	}
	if err := block.Run(context.Background(), job); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !job.Metadata.HDR.DolbyVisionEncoderSelected {
		t.Fatal("DolbyVisionEncoderSelected = false, want true")
	}
	video := domain.EffectiveVideoProfile(job.Profile, job.Metadata)
	if got, want := video.Codec, "hevc"; got != want {
		t.Fatalf("effective codec = %q, want %q", got, want)
	}
	if got, want := video.Accelerator, "qsv"; got != want {
		t.Fatalf("effective accelerator = %q, want %q", got, want)
	}
}

func TestBlockRequireDolbyVisionFailsWhenDoviToolUnavailable(t *testing.T) {
	runner := fakeRunner{stdout: []byte(`{
		"streams": [
			{
				"index":0,
				"codec_type":"video",
				"codec_name":"hevc",
				"side_data_list":[{
					"side_data_type":"DOVI configuration record",
					"dv_profile":8
				}]
			}
		],
		"format": {"format_name":"matroska,webm","duration":"123.456","size":"98765"}
	}`)}
	job := &pipeline.JobContext{
		InputPath: "/media/input.mkv",
		Profile: domain.Profile{
			Video: domain.VideoProfile{
				Codec:       "av1",
				Accelerator: "software",
				DolbyVision: domain.DolbyVisionProfile{
					Mode:  domain.DolbyVisionModeRequire,
					Codec: "hevc",
				},
			},
		},
	}
	block := Block{
		Prober:          FFProbe{Runner: runner},
		DolbyVisionTool: fakeDolbyVisionTool{available: false},
	}
	err := block.Run(context.Background(), job)
	if err == nil {
		t.Fatal("Run() error = nil, want dovi_tool availability failure")
	}
}

type fakeRunner struct {
	stdout []byte
	err    error
}

func (f fakeRunner) Run(_ context.Context, command process.Command) (process.Result, error) {
	return process.Result{Command: command.ArgsWithName(), Stdout: f.stdout}, f.err
}

type fakeDolbyVisionTool struct {
	available bool
	details   string
	err       error
}

func (f fakeDolbyVisionTool) Available(context.Context) (bool, string, error) {
	return f.available, f.details, f.err
}
