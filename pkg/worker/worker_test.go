package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/process"
	"github.com/zekurio/anvil/pkg/scheduler"
	"github.com/zekurio/anvil/pkg/staging"
)

func TestRunnerResolvesLatestConfigAndCompletesJob(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	store := newFakeWorkerStore()
	store.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv"}
	store.asset = domain.MediaAsset{ID: 2, SourceID: 1, RelativePath: "Movie.mkv"}

	runner := Runner{
		Store:            store,
		ConfigProvider:   workerConfig,
		MetadataResolver: staticMetadataResolver{metadata: domain.JobMetadata{OriginalLanguage: "eng"}},
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(pipeline.BlockFunc{BlockName: "noop", Fn: func(_ context.Context, job *pipeline.JobContext) error {
				if job.InputPath == "" {
					t.Fatal("input path was empty")
				}
				if got, want := job.Metadata.OriginalLanguage, "eng"; got != want {
					t.Fatalf("original language = %q, want %q", got, want)
				}
				return nil
			}}),
		},
		Now: func() time.Time { return now },
	}

	err := runner.Run(ctx, scheduler.Assignment{
		Job:       domain.Job{ID: 99, SourceID: 1, AssetID: 2, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID:  "worker-1",
		Resources: domain.ResourceAllocation{WorkerID: "worker-1", Threads: 2},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if store.attempt.State != domain.AttemptStateSucceeded {
		t.Fatalf("attempt state = %q, want succeeded", store.attempt.State)
	}
	if got := store.transitions[len(store.transitions)-1]; got != domain.JobStateComplete {
		t.Fatalf("last transition = %q, want complete", got)
	}
	var library domain.Library
	if err := json.Unmarshal(store.resolvedLibrary, &library); err != nil {
		t.Fatalf("resolved library json: %v", err)
	}
	if library.Name != "movies" {
		t.Fatalf("resolved library name = %q, want movies", library.Name)
	}
}

func TestRunnerRecordsJobFileSizes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "output.mkv")

	store := newFakeWorkerStore()
	store.source = domain.MediaSource{
		ID:           1,
		LibraryName:  "movies",
		Kind:         domain.SourceKindFile,
		RelativePath: "Movie.mkv",
		Fingerprint:  domain.FileFingerprint{SizeBytes: 1000},
	}
	runner := Runner{
		Store:          store,
		ConfigProvider: workerConfig,
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(pipeline.BlockFunc{BlockName: "noop", Fn: func(_ context.Context, job *pipeline.JobContext) error {
				if err := os.WriteFile(outputPath, make([]byte, 650), 0o600); err != nil {
					t.Fatalf("write output: %v", err)
				}
				job.FinalPath = outputPath
				return nil
			}}),
		},
		Now: func() time.Time { return now },
	}

	err := runner.Run(ctx, scheduler.Assignment{
		Job:      domain.Job{ID: 99, SourceID: 1, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID: "worker-1",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if store.recordedInputSize != 1000 || store.recordedOutputSize != 650 {
		t.Fatalf("recorded sizes = %d/%d, want 1000/650", store.recordedInputSize, store.recordedOutputSize)
	}
}

func TestRunnerRequeuesFailedAttemptBeforeMaxAttempts(t *testing.T) {
	ctx := context.Background()
	store := newFakeWorkerStore()
	store.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv"}
	runner := Runner{
		Store:          store,
		ConfigProvider: workerConfig,
		MaxAttempts:    2,
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(pipeline.BlockFunc{BlockName: "noop", Fn: func(_ context.Context, _ *pipeline.JobContext) error {
				return errors.New("encode failed")
			}}),
		},
	}

	err := runner.Run(ctx, scheduler.Assignment{
		Job:      domain.Job{ID: 99, SourceID: 1, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID: "worker-1",
	})
	if err == nil {
		t.Fatal("Run() error = nil, want failure")
	}
	if got := store.transitions[len(store.transitions)-1]; got != domain.JobStatePending {
		t.Fatalf("last transition = %q, want pending", got)
	}
}

func TestRunnerCleansStagingAfterPipelineFailure(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	cfg := workerConfig()
	cfg.Daemon.TempDir = tempDir
	cfg.Flows["test-flow"] = config.FlowConfig{Steps: []string{"stage", "fail"}}

	store := newFakeWorkerStore()
	store.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv"}
	var stagedDir string
	runner := Runner{
		Store:          store,
		ConfigProvider: func() config.Config { return cfg },
		TempDir:        tempDir,
		MaxAttempts:    2,
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(
				staging.StageBlock{Manager: staging.Manager{Root: filepath.Join(tempDir, "staging")}},
				pipeline.BlockFunc{BlockName: "fail", Fn: func(_ context.Context, job *pipeline.JobContext) error {
					stagedDir = job.StagingDir
					if stagedDir == "" {
						t.Fatal("staging dir was empty")
					}
					if err := os.WriteFile(filepath.Join(stagedDir, "partial.mkv"), []byte("partial"), 0o640); err != nil {
						t.Fatalf("write partial output: %v", err)
					}
					return errors.New("encode failed")
				}},
			),
		},
	}

	err := runner.Run(ctx, scheduler.Assignment{
		Job:      domain.Job{ID: 99, SourceID: 1, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID: "worker-1",
	})
	if err == nil {
		t.Fatal("Run() error = nil, want failure")
	}
	if _, statErr := os.Stat(stagedDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("staging dir stat error = %v, want not exist", statErr)
	}
	if got := store.transitions[len(store.transitions)-1]; got != domain.JobStatePending {
		t.Fatalf("last transition = %q, want pending", got)
	}
}

func TestRunnerCapturesProcessOutputLogs(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	ffmpegPath := filepath.Join(tempDir, "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nprintf stdout\nprintf stderr >&2\n"), 0o750); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	cfg := workerConfig()
	cfg.Daemon.TempDir = tempDir
	cfg.Flows["test-flow"] = config.FlowConfig{Steps: []string{"run-ffmpeg"}}
	store := newFakeWorkerStore()
	store.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv"}
	runner := Runner{
		Store:          store,
		ConfigProvider: func() config.Config { return cfg },
		TempDir:        tempDir,
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(pipeline.BlockFunc{BlockName: "run-ffmpeg", Fn: func(ctx context.Context, _ *pipeline.JobContext) error {
				_, err := process.OSRunner{}.Run(ctx, process.Command{Name: ffmpegPath})
				return err
			}}),
		},
	}

	err := runner.Run(ctx, scheduler.Assignment{
		Job:      domain.Job{ID: 99, SourceID: 1, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID: "worker-1",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	logDir := filepath.Join(tempDir, "process-logs", "job-99-attempt-1")
	stdoutPath := filepath.Join(logDir, "run-ffmpeg-ffmpeg.stdout.log")
	stderrPath := filepath.Join(logDir, "run-ffmpeg-ffmpeg.stderr.log")
	if got, err := os.ReadFile(stdoutPath); err != nil || string(got) != "stdout" {
		t.Fatalf("stdout log = %q, err = %v", got, err)
	}
	if got, err := os.ReadFile(stderrPath); err != nil || string(got) != "stderr" {
		t.Fatalf("stderr log = %q, err = %v", got, err)
	}
	if !hasAttemptEvent(store.events, "process-output") {
		t.Fatalf("recorded events = %+v, want process-output artifact", store.events)
	}
}

func TestRunnerDoesNotFailJobWhenMetadataResolutionFails(t *testing.T) {
	ctx := context.Background()
	store := newFakeWorkerStore()
	store.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv"}
	runner := Runner{
		Store:            store,
		ConfigProvider:   workerConfig,
		MetadataResolver: staticMetadataResolver{err: errors.New("arr unavailable")},
		Pipeline: pipeline.Runner{
			Registry: pipeline.NewRegistry(pipeline.BlockFunc{BlockName: "noop", Fn: func(_ context.Context, job *pipeline.JobContext) error {
				if !job.Metadata.StreamCleanupDisabled {
					t.Fatal("StreamCleanupDisabled = false, want true")
				}
				if job.Metadata.StreamCleanupDisabledReason == "" {
					t.Fatal("StreamCleanupDisabledReason was empty")
				}
				return nil
			}}),
		},
	}

	err := runner.Run(ctx, scheduler.Assignment{
		Job:      domain.Job{ID: 99, SourceID: 1, LibraryName: "movies", State: domain.JobStateLeased},
		WorkerID: "worker-1",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if store.attempt.State != domain.AttemptStateSucceeded {
		t.Fatalf("attempt state = %q, want succeeded", store.attempt.State)
	}
}

func workerConfig() config.Config {
	cfg := config.Default()
	cfg.Daemon.LeaseDuration = "1m"
	cfg.Daemon.MaxAttempts = 2
	cfg.Flows = map[string]config.FlowConfig{"test-flow": {Steps: []string{"noop"}}}
	cfg.Libraries = map[string]config.LibraryConfig{"movies": {
		Kind:    "media",
		Path:    "/media/movies",
		Flow:    "test-flow",
		Profile: config.DefaultProfileName,
	}}
	return cfg
}

type staticMetadataResolver struct {
	metadata domain.JobMetadata
	err      error
}

func (s staticMetadataResolver) ResolveJobMetadata(context.Context, domain.Library, domain.MediaSource, domain.MediaAsset, string) (domain.JobMetadata, error) {
	return s.metadata, s.err
}

type fakeWorkerStore struct {
	source             domain.MediaSource
	asset              domain.MediaAsset
	attempt            domain.Attempt
	resolvedLibrary    []byte
	recordedInputSize  int64
	recordedOutputSize int64
	transitions        []domain.JobState
	events             []domain.AttemptEvent
}

func newFakeWorkerStore() *fakeWorkerStore {
	return &fakeWorkerStore{
		attempt: domain.Attempt{ID: 1, JobID: 99, Number: 1, WorkerID: "worker-1", State: domain.AttemptStateRunning},
	}
}

func (f *fakeWorkerStore) GetMediaSource(_ context.Context, id domain.MediaSourceID) (domain.MediaSource, error) {
	if f.source.ID == id {
		return f.source, nil
	}
	return domain.MediaSource{}, errors.New("source not found")
}

func (f *fakeWorkerStore) GetMediaAsset(_ context.Context, id domain.MediaAssetID) (domain.MediaAsset, error) {
	if f.asset.ID == id {
		return f.asset, nil
	}
	return domain.MediaAsset{}, errors.New("asset not found")
}

func (f *fakeWorkerStore) StartAttempt(_ context.Context, _ domain.JobID, _ string, resolvedLibrary []byte, _ []byte, _ []byte, _ time.Time) (domain.Attempt, error) {
	f.resolvedLibrary = resolvedLibrary
	return f.attempt, nil
}

func (f *fakeWorkerStore) FinishAttempt(_ context.Context, _ domain.AttemptID, state domain.AttemptState, message string, finishedAt time.Time) (domain.Attempt, error) {
	f.attempt.State = state
	f.attempt.Error = message
	f.attempt.FinishedAt = &finishedAt
	return f.attempt, nil
}

func (f *fakeWorkerStore) TransitionJob(_ context.Context, _ domain.JobID, to domain.JobState, _ time.Time, _ string) (domain.Job, error) {
	f.transitions = append(f.transitions, to)
	return domain.Job{State: to}, nil
}

func (f *fakeWorkerStore) RecordJobFileSizes(_ context.Context, _ domain.JobID, inputSizeBytes int64, outputSizeBytes int64, _ time.Time) (domain.Job, error) {
	f.recordedInputSize = inputSizeBytes
	f.recordedOutputSize = outputSizeBytes
	return domain.Job{InputSizeBytes: inputSizeBytes, OutputSizeBytes: outputSizeBytes}, nil
}

func (f *fakeWorkerStore) HeartbeatJob(_ context.Context, _ domain.JobID, _ string, _ time.Time, _ time.Time) (domain.Job, error) {
	return domain.Job{}, nil
}

func (f *fakeWorkerStore) RecordAttemptEvent(_ context.Context, event domain.AttemptEvent) (domain.AttemptEvent, error) {
	event.ID = domain.AttemptEventID(len(f.events) + 1)
	f.events = append(f.events, event)
	return event, nil
}

func hasAttemptEvent(events []domain.AttemptEvent, name string) bool {
	for _, event := range events {
		if event.Name == name {
			return true
		}
	}
	return false
}
