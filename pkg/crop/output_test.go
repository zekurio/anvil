package crop

import (
	"context"
	"errors"
	"reflect"
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

func TestDetectorRejectsCropAfterFailedWindow(t *testing.T) {
	runner := &sampleRunner{
		outputs: []string{
			"[Parsed_cropdetect_0 @ 0x1] crop=1920:800:0:140",
			"[Parsed_cropdetect_0 @ 0x1] crop=1920:1080:0:0",
			"[Parsed_cropdetect_0 @ 0x1] crop=1920:798:0:142",
		},
		failures: []error{nil, errors.New("decode failed"), nil},
	}
	result, err := (FFmpegDetector{Runner: runner, SeekOffsets: []time.Duration{0, time.Minute, 2 * time.Minute}}).Detect(context.Background(), "input.mkv")
	if err != nil {
		t.Fatal(err)
	}
	result = ApplySafetyPolicy(result, videoProbe(1920, 1080), domain.CropPolicy{})
	if result.Filter != "" || result.SelectionReason != "crop sample failed" || result.RejectionReason != "crop sample failed" {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Samples) != 3 || result.Samples[0].Observations != 1 || result.Samples[1].Offset != time.Minute || result.Samples[1].Error != "decode failed" {
		t.Fatalf("samples = %#v", result.Samples)
	}
	block := Block{}
	report, ok := block.Artifact(&pipeline.JobContext{Crop: &result})
	payload, typed := report.Payload.(cropSelectionPayload)
	if !ok || !typed || len(payload.Samples) != 3 || payload.SelectionReason != result.SelectionReason {
		t.Fatalf("artifact = %#v", report)
	}
}

func TestJobDetectorSpreadsWindowsAcrossProbeDuration(t *testing.T) {
	job := &pipeline.JobContext{
		Profile: domain.Profile{},
		Probe: &domain.ProbeResult{
			DurationSeconds: (45 * time.Minute).Seconds(),
			Streams:         []domain.MediaStream{{Index: 0, Type: "video", Width: 1920, Height: 1080}},
		},
	}
	detector := jobDetector(job)
	if len(detector.SeekOffsets) != 15 || detector.SeekOffsets[0] != 90*time.Second {
		t.Fatalf("offsets = %v", detector.SeekOffsets)
	}
	if detector.FrameCount != 300 || detector.VideoStreamIndex != 0 || !detector.MapVideoStream {
		t.Fatalf("detector = %#v", detector)
	}
}

type fixedDetector struct{ result domain.CropResult }

func (d fixedDetector) Detect(context.Context, string) (domain.CropResult, error) {
	return d.result, nil
}

func TestBlockAppliesSafetyPolicyToDetectorResult(t *testing.T) {
	job := &pipeline.JobContext{Probe: videoProbe(1920, 1080)}
	block := Block{Detector: fixedDetector{result: domain.CropResult{CandidateFilter: "crop=1920:960:0:60"}}}
	if err := block.Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if job.Crop == nil || job.Crop.Filter != "crop=1920:960:0:60" || job.Metadata.CropFilter != "crop=1920:960:0:60" {
		t.Fatalf("crop = %#v", job.Crop)
	}
}

type countingDetector struct{ calls int }

func (d *countingDetector) Detect(context.Context, string) (domain.CropResult, error) {
	d.calls++
	return domain.CropResult{CandidateFilter: "crop=1920:800:0:140"}, nil
}

func TestBlockRunsNothingWhenCropIsDisabled(t *testing.T) {
	detector := &countingDetector{}
	job := &pipeline.JobContext{
		Probe:    videoProbe(1920, 1080),
		Profile:  domain.Profile{Crop: domain.CropPolicy{Disabled: true}},
		Metadata: domain.JobMetadata{CropFilter: "crop=1920:800:0:140"},
	}
	block := Block{Detector: detector}
	if err := block.Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if detector.calls != 0 {
		t.Fatalf("detector ran %d times", detector.calls)
	}
	want := domain.CropResult{Disabled: true, SourceWidth: 1920, SourceHeight: 1080, OutputWidth: 1920, OutputHeight: 1080, RetainedAreaPercent: 100}
	if !reflect.DeepEqual(*job.Crop, want) || job.Metadata.CropFilter != "" {
		t.Fatalf("crop = %#v, metadata filter = %q", job.Crop, job.Metadata.CropFilter)
	}
	report, ok := block.Artifact(job)
	if !ok || report.Message != "crop detection disabled by profile; using source dimensions" {
		t.Fatalf("report = %+v, %v", report, ok)
	}
}

func TestApplySafetyPolicyDiscardsCachedCropOnceDisabled(t *testing.T) {
	cached := domain.CropResult{
		CandidateFilter: "crop=1920:800:0:140",
		Filter:          "crop=1920:800:0:140",
		Samples:         []domain.CropSample{{Filter: "crop=1920:800:0:140"}, {Filter: "crop=1920:800:0:140"}},
	}
	if enabled := ApplySafetyPolicy(cached, videoProbe(1920, 1080), domain.CropPolicy{}); enabled.Filter != "crop=1920:800:0:140" || enabled.Disabled {
		t.Fatalf("enabled policy = %#v", enabled)
	}
	disabled := ApplySafetyPolicy(cached, videoProbe(1920, 1080), domain.CropPolicy{Disabled: true})
	if !disabled.Disabled || disabled.Filter != "" || disabled.CandidateFilter != "" || len(disabled.Samples) != 0 || disabled.OutputHeight != 1080 {
		t.Fatalf("disabled policy kept the cached crop: %#v", disabled)
	}
}

func TestArtifactMessages(t *testing.T) {
	tests := []struct {
		name string
		crop domain.CropResult
		want string
	}{
		{
			"applied crop names how many windows agree",
			domain.CropResult{
				CandidateFilter:     "crop=1920:960:0:60",
				Filter:              "crop=1920:960:0:60",
				RetainedAreaPercent: 88.89,
				Samples: []domain.CropSample{
					{Filter: "crop=1920:960:0:60"},
					{Filter: "crop=1920:952:0:60"},
					{Filter: "crop=384:400:450:438"},
					{},
					{Filter: "crop=1920:960:0:60", Error: "decode failed"},
				},
			},
			"selected crop=1920:960:0:60 (88.89% retained area; 2 of 5 windows agree)",
		},
		{
			"rejected candidate",
			domain.CropResult{
				CandidateFilter:     "crop=1920:960:0:0",
				RejectionReason:     "uneven borders: left 0, right 0, top 0, bottom 120",
				SelectionReason:     "uneven borders: left 0, right 0, top 0, bottom 120",
				RetainedAreaPercent: 88.89,
			},
			"rejected crop=1920:960:0:0; using no crop: uneven borders: left 0, right 0, top 0, bottom 120",
		},
		{
			"no candidate to report",
			domain.CropResult{SelectionReason: "no crop sample contains picture evidence", RejectionReason: "no crop sample contains picture evidence"},
			"using no crop: no crop sample contains picture evidence",
		},
		{
			"full frame no-op",
			domain.CropResult{CandidateFilter: "crop=1920:1072:0:4", NoOp: true},
			"selected crop=1920:1072:0:4 removes at most small edge strips; using source dimensions",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report, ok := (Block{}).Artifact(&pipeline.JobContext{Crop: &tt.crop})
			if !ok {
				t.Fatal("expected an artifact report")
			}
			if report.Message != tt.want {
				t.Fatalf("message = %q, want %q", report.Message, tt.want)
			}
		})
	}
}
