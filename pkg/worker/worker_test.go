package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/scheduler"
)

func TestRunnerResolvesLatestConfigAndCompletesJob(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	store := newFakeWorkerStore()
	store.source = domain.MediaSource{ID: 1, LibraryName: "movies", Kind: domain.SourceKindFile, RelativePath: "Movie.mkv"}
	store.asset = domain.MediaAsset{ID: 2, SourceID: 1, RelativePath: "Movie.mkv"}

	runner := Runner{
		Store:          store,
		ConfigProvider: workerConfig,
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

func workerConfig() config.Config {
	cfg := config.Default()
	cfg.Daemon.LeaseDuration = "1m"
	cfg.Daemon.MaxAttempts = 2
	cfg.Flows = []config.FlowConfig{{Name: "test-flow", Steps: []string{"noop"}}}
	cfg.Libraries = []config.LibraryConfig{{
		Name:             "movies",
		Kind:             "media",
		Path:             "/media/movies",
		OriginalLanguage: "eng",
		Flow:             "test-flow",
		Profile:          config.DefaultProfileName,
	}}
	return cfg
}

type fakeWorkerStore struct {
	source          domain.MediaSource
	asset           domain.MediaAsset
	attempt         domain.Attempt
	resolvedLibrary []byte
	transitions     []domain.JobState
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

func (f *fakeWorkerStore) HeartbeatJob(_ context.Context, _ domain.JobID, _ string, _ time.Time, _ time.Time) (domain.Job, error) {
	return domain.Job{}, nil
}

func (f *fakeWorkerStore) RecordAttemptEvent(_ context.Context, event domain.AttemptEvent) (domain.AttemptEvent, error) {
	return event, nil
}
