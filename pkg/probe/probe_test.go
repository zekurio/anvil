package probe

import (
	"context"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
)

func TestFFProbeParsesJSON(t *testing.T) {
	runner := fakeRunner{stdout: []byte(`{
		"streams": [
			{"index":0,"codec_type":"video","codec_name":"hevc","tags":{"language":"eng","title":"Main"},"disposition":{"default":1}},
			{"index":1,"codec_type":"audio","codec_name":"aac","tags":{"language":"jpn"},"disposition":{"default":0}}
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
	if got, want := result.Streams[0].Tags["title"], "Main"; got != want {
		t.Fatalf("first stream title tag = %q, want %q", got, want)
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
				Codec:       "libsvtav1",
				PixelFormat: "yuv420p10le",
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

type fakeRunner struct {
	stdout []byte
}

func (f fakeRunner) Run(_ context.Context, command process.Command) (process.Result, error) {
	return process.Result{Command: command.ArgsWithName(), Stdout: f.stdout}, nil
}
