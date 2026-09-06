package crop

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"

	"github.com/zekurio/anvil/pkg/process"
)

type outputRunner struct {
	calls   int
	failure error
}

func (r *outputRunner) Run(_ context.Context, command process.Command) (process.Result, error) {
	r.calls++
	result := process.Result{Command: command.ArgsWithName(), Stderr: []byte("crop=1920:800:0:140")}
	if r.calls == 2 {
		return result, r.failure
	}
	return result, nil
}

func TestCropDoesNotHideOutputFailureAfterGoodSample(t *testing.T) {
	for _, failure := range []error{process.ErrOutputCapture, process.ErrOutputLog, context.Canceled} {
		runner := &outputRunner{failure: failure}
		_, err := (FFmpegDetector{Runner: runner}).Detect(context.Background(), "input.mkv")
		if !errors.Is(err, failure) || runner.calls != 2 {
			t.Fatalf("error = %v, calls = %d", err, runner.calls)
		}
	}
}

type sampleRunner struct {
	outputs  []string
	failures []error
	calls    int
}

func (r *sampleRunner) Run(_ context.Context, command process.Command) (process.Result, error) {
	i := r.calls
	r.calls++
	return process.Result{Command: command.ArgsWithName(), Stderr: []byte(r.outputs[i])}, r.failures[i]
}

func TestDetectorRecordsSamplesAndRejectsPartialFailure(t *testing.T) {
	runner := &sampleRunner{
		outputs:  []string{"[Parsed_cropdetect_0 @ 0x1] crop=1920:800:0:140", "[Parsed_cropdetect_0 @ 0x1] crop=1920:800:0:140"},
		failures: []error{nil, errors.New("decode failed")},
	}
	result, err := (FFmpegDetector{Runner: runner, SeekOffsets: []time.Duration{0, time.Minute}}).Detect(context.Background(), "input.mkv")
	if err != nil {
		t.Fatal(err)
	}
	result = ApplySafetyPolicy(result, videoProbe(1920, 1080), domain.CropPolicy{})
	if result.Filter != "" || result.RejectionReason != "crop sample failed" {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Samples) != 2 || result.Samples[0].Observations != 1 || result.Samples[1].Offset != time.Minute || result.Samples[1].Error != "decode failed" {
		t.Fatalf("samples = %#v", result.Samples)
	}
	block := Block{}
	report, ok := block.Artifact(&pipeline.JobContext{Crop: &result})
	payload, typed := report.Payload.(cropSelectionPayload)
	if !ok || !typed || len(payload.Samples) != 2 || payload.SelectionReason != result.SelectionReason {
		t.Fatalf("artifact = %#v", report)
	}
}
