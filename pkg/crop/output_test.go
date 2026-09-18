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

func TestDetectorKeepsEvidenceFromGoodWindows(t *testing.T) {
	runner := &sampleRunner{
		outputs: []string{
			"[Parsed_cropdetect_0 @ 0x1] crop=1920:800:0:140",
			"[Parsed_cropdetect_0 @ 0x1] crop=1920:800:0:140",
			"[Parsed_cropdetect_0 @ 0x1] crop=1920:798:0:142",
		},
		failures: []error{nil, errors.New("decode failed"), nil},
	}
	result, err := (FFmpegDetector{Runner: runner, SeekOffsets: []time.Duration{0, time.Minute, 2 * time.Minute}}).Detect(context.Background(), "input.mkv")
	if err != nil {
		t.Fatal(err)
	}
	result = ApplySafetyPolicy(result, videoProbe(1920, 1080), domain.CropPolicy{})
	if result.Filter != "crop=1920:800:0:140" || result.SelectionReason != "" {
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
