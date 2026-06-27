package probe

import (
	"context"
	"testing"

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
}

type fakeRunner struct {
	stdout []byte
}

func (f fakeRunner) Run(_ context.Context, command process.Command) (process.Result, error) {
	return process.Result{Command: command.ArgsWithName(), Stdout: f.stdout}, nil
}
