package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/process"
)

func TestProcessLogRecorderWritesFFmpegOutputAndArtifact(t *testing.T) {
	root := t.TempDir()
	store := newFakeWorkerStore()
	recorder := processLogRecorder{
		root:      root,
		jobID:     99,
		attemptID: 7,
		attempt:   7,
		events:    store,
		now: func() time.Time {
			return time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
		},
	}
	ctx := process.WithStep(context.Background(), "encode")
	result := process.Result{
		Command:  []string{"ffmpeg", "-i", "in.mkv", "out.mkv"},
		Stdout:   []byte("out"),
		Stderr:   []byte("err"),
		ExitCode: 7,
		Duration: 1500 * time.Millisecond,
	}

	if err := recorder.LogProcess(ctx, process.Command{Name: "ffmpeg"}, result, errors.New("boom")); err != nil {
		t.Fatalf("LogProcess() error = %v", err)
	}

	stdoutPath := filepath.Join(root, "job-99", "attempt-7", "encode-ffmpeg.stdout.log")
	stderrPath := filepath.Join(root, "job-99", "attempt-7", "encode-ffmpeg.stderr.log")
	if got, err := os.ReadFile(stdoutPath); err != nil || string(got) != "out" {
		t.Fatalf("stdout log = %q, err = %v", got, err)
	}
	if got, err := os.ReadFile(stderrPath); err != nil || string(got) != "err" {
		t.Fatalf("stderr log = %q, err = %v", got, err)
	}
	if len(store.events) != 1 {
		t.Fatalf("recorded events = %d, want 1", len(store.events))
	}
	event := store.events[0]
	if event.Type != domain.AttemptEventArtifact || event.Name != "process-output" {
		t.Fatalf("event = %s/%s, want artifact/process-output", event.Type, event.Name)
	}
	var artifact processLogArtifact
	if err := json.Unmarshal(event.Payload, &artifact); err != nil {
		t.Fatalf("decode artifact payload: %v", err)
	}
	if artifact.StdoutPath != stdoutPath || artifact.StderrPath != stderrPath {
		t.Fatalf("artifact paths = %q/%q, want %q/%q", artifact.StdoutPath, artifact.StderrPath, stdoutPath, stderrPath)
	}
	if artifact.ExitCode != 7 || artifact.DurationMillis != 1500 || artifact.Error == "" {
		t.Fatalf("artifact = %+v, want exit/duration/error", artifact)
	}
}

func TestProcessLogRecorderKeepsRepeatedCommandLogsUnique(t *testing.T) {
	root := t.TempDir()
	store := newFakeWorkerStore()
	recorder := processLogRecorder{
		root:      root,
		jobID:     99,
		attemptID: 7,
		attempt:   7,
		events:    store,
	}
	ctx := process.WithStep(context.Background(), "encode")

	if err := recorder.LogProcess(ctx, process.Command{Name: "ffmpeg"}, process.Result{
		Command: []string{"ffmpeg", "-i", "first.mkv", "out.mkv"},
		Stdout:  []byte("first"),
	}, nil); err != nil {
		t.Fatalf("first LogProcess() error = %v", err)
	}
	if err := recorder.LogProcess(ctx, process.Command{Name: "ffmpeg"}, process.Result{
		Command: []string{"ffmpeg", "-i", "second.mkv", "out.mkv"},
		Stdout:  []byte("second"),
	}, nil); err != nil {
		t.Fatalf("second LogProcess() error = %v", err)
	}

	firstPath := filepath.Join(root, "job-99", "attempt-7", "encode-ffmpeg.stdout.log")
	secondPath := filepath.Join(root, "job-99", "attempt-7", "encode-ffmpeg-2.stdout.log")
	if got, err := os.ReadFile(firstPath); err != nil || string(got) != "first" {
		t.Fatalf("first stdout log = %q, err = %v", got, err)
	}
	if got, err := os.ReadFile(secondPath); err != nil || string(got) != "second" {
		t.Fatalf("second stdout log = %q, err = %v", got, err)
	}
	if len(store.events) != 2 {
		t.Fatalf("recorded events = %d, want 2", len(store.events))
	}
	var firstArtifact, secondArtifact processLogArtifact
	if err := json.Unmarshal(store.events[0].Payload, &firstArtifact); err != nil {
		t.Fatalf("decode first artifact payload: %v", err)
	}
	if err := json.Unmarshal(store.events[1].Payload, &secondArtifact); err != nil {
		t.Fatalf("decode second artifact payload: %v", err)
	}
	if firstArtifact.StdoutPath != firstPath || secondArtifact.StdoutPath != secondPath {
		t.Fatalf("artifact stdout paths = %q/%q, want %q/%q", firstArtifact.StdoutPath, secondArtifact.StdoutPath, firstPath, secondPath)
	}
}

func TestProcessLogRecorderIgnoresFFProbeOutput(t *testing.T) {
	root := t.TempDir()
	store := newFakeWorkerStore()
	recorder := processLogRecorder{
		root:      root,
		jobID:     99,
		attemptID: 7,
		attempt:   7,
		events:    store,
	}
	result := process.Result{
		Command: []string{"ffprobe", "-of", "json"},
		Stdout:  []byte(`{"streams":[]}`),
	}

	if err := recorder.LogProcess(context.Background(), process.Command{Name: "ffprobe"}, result, nil); err != nil {
		t.Fatalf("LogProcess() error = %v", err)
	}
	if len(store.events) != 0 {
		t.Fatalf("recorded events = %d, want 0", len(store.events))
	}
	if _, err := os.Stat(filepath.Join(root, "job-99", "attempt-7")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("log dir stat error = %v, want not exist", err)
	}
}

func TestProcessLogRecorderCapturesABAV1Output(t *testing.T) {
	root := t.TempDir()
	store := newFakeWorkerStore()
	recorder := processLogRecorder{
		root:      root,
		jobID:     99,
		attemptID: 7,
		attempt:   7,
		events:    store,
	}
	ctx := process.WithStep(context.Background(), "crf-search")
	result := process.Result{
		Command: []string{"ab-av1", "crf-search"},
		Stdout:  []byte("crf 28 vmaf 95.5"),
	}

	if err := recorder.LogProcess(ctx, process.Command{Name: "ab-av1"}, result, nil); err != nil {
		t.Fatalf("LogProcess() error = %v", err)
	}
	stdoutPath := filepath.Join(root, "job-99", "attempt-7", "crf-search-ab-av1.stdout.log")
	if got, err := os.ReadFile(stdoutPath); err != nil || string(got) != "crf 28 vmaf 95.5" {
		t.Fatalf("stdout log = %q, err = %v", got, err)
	}
	if !hasAttemptEvent(store.events, "process-output") {
		t.Fatalf("recorded events = %+v, want process-output artifact", store.events)
	}
}
