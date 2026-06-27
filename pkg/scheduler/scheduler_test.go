package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/resources"
)

func TestScheduleOnceHonorsLibraryConcurrency(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	store := &fakeScheduleStore{
		jobs: []domain.Job{
			{ID: 1, LibraryName: "movies", State: domain.JobStatePending},
			{ID: 2, LibraryName: "tv", State: domain.JobStatePending},
		},
	}
	worker := newBlockingWorker()
	defer worker.releaseAll()

	s := &Scheduler{
		Store:          store,
		Worker:         worker,
		ConfigProvider: scheduleConfig,
		Allocator:      resources.NewAllocator(8),
		WorkerCount:    2,
		LeaseDuration:  time.Minute,
		Now:            func() time.Time { return now },
	}

	started, err := s.ScheduleOnce(ctx)
	if err != nil {
		t.Fatalf("first ScheduleOnce() error = %v", err)
	}
	if !started {
		t.Fatal("first ScheduleOnce() started = false, want true")
	}
	worker.waitStarted(t)

	started, err = s.ScheduleOnce(ctx)
	if err != nil {
		t.Fatalf("second ScheduleOnce() error = %v", err)
	}
	if !started {
		t.Fatal("second ScheduleOnce() started = false, want true")
	}

	if len(store.allowed) != 2 {
		t.Fatalf("lease calls = %d, want 2", len(store.allowed))
	}
	if containsLibrary(store.allowed[1], "movies") {
		t.Fatalf("second allowed libraries = %v, want movies excluded", store.allowed[1])
	}
	if !containsLibrary(store.allowed[1], "tv") {
		t.Fatalf("second allowed libraries = %v, want tv included", store.allowed[1])
	}
}

func TestScheduleOnceStopsWhenWorkerPoolIsFull(t *testing.T) {
	ctx := context.Background()
	store := &fakeScheduleStore{
		jobs: []domain.Job{{ID: 1, LibraryName: "movies", State: domain.JobStatePending}},
	}
	worker := newBlockingWorker()
	defer worker.releaseAll()

	s := &Scheduler{
		Store:          store,
		Worker:         worker,
		ConfigProvider: scheduleConfig,
		Allocator:      resources.NewAllocator(4),
		WorkerCount:    1,
		LeaseDuration:  time.Minute,
	}

	if started, err := s.ScheduleOnce(ctx); err != nil || !started {
		t.Fatalf("first ScheduleOnce() = %v, %v, want true, nil", started, err)
	}
	worker.waitStarted(t)
	if started, err := s.ScheduleOnce(ctx); err != nil || started {
		t.Fatalf("second ScheduleOnce() = %v, %v, want false, nil", started, err)
	}
	if len(store.allowed) != 1 {
		t.Fatalf("lease calls = %d, want 1", len(store.allowed))
	}
}

func TestScheduleAvailableFillsOpenSlots(t *testing.T) {
	ctx := context.Background()
	store := &fakeScheduleStore{
		jobs: []domain.Job{
			{ID: 1, LibraryName: "movies", State: domain.JobStatePending},
			{ID: 2, LibraryName: "tv", State: domain.JobStatePending},
		},
	}
	worker := newBlockingWorker()
	defer worker.releaseAll()

	s := &Scheduler{
		Store:          store,
		Worker:         worker,
		ConfigProvider: scheduleConfig,
		Allocator:      resources.NewAllocator(4),
		WorkerCount:    2,
		LeaseDuration:  time.Minute,
	}

	started, err := s.ScheduleAvailable(ctx)
	if err != nil {
		t.Fatalf("ScheduleAvailable() error = %v", err)
	}
	if started != 2 {
		t.Fatalf("started = %d, want 2", started)
	}
	worker.waitStarted(t)
	worker.waitStarted(t)
	if len(store.allowed) != 2 {
		t.Fatalf("lease calls = %d, want 2", len(store.allowed))
	}
}

func scheduleConfig() config.Config {
	cfg := config.Default()
	cfg.Daemon.WorkerCount = 2
	cfg.Daemon.LeaseDuration = "1m"
	cfg.Libraries = map[string]config.LibraryConfig{
		"movies": {Kind: "media", Path: "/media/movies", Flow: config.DefaultFlowName, Profile: config.DefaultProfileName, ConcurrencyLimit: 1},
		"tv":     {Kind: "media", Path: "/media/tv", Flow: config.DefaultFlowName, Profile: config.DefaultProfileName, ConcurrencyLimit: 1},
	}
	return cfg
}

type fakeScheduleStore struct {
	jobs    []domain.Job
	allowed [][]domain.LibraryName
}

func (f *fakeScheduleStore) LeaseNextJobForLibraries(_ context.Context, workerID string, leaseDeadline time.Time, now time.Time, allowedLibraries []domain.LibraryName) (*domain.Job, error) {
	f.allowed = append(f.allowed, append([]domain.LibraryName(nil), allowedLibraries...))
	for i, job := range f.jobs {
		if !containsLibrary(allowedLibraries, job.LibraryName) {
			continue
		}
		f.jobs = append(f.jobs[:i], f.jobs[i+1:]...)
		job.LeaseOwner = workerID
		job.LeaseDeadline = &leaseDeadline
		job.HeartbeatAt = &now
		job.State = domain.JobStateLeased
		return &job, nil
	}
	return nil, nil
}

type blockingWorker struct {
	started chan struct{}
	release chan struct{}
}

func newBlockingWorker() *blockingWorker {
	return &blockingWorker{
		started: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
}

func (w *blockingWorker) Run(ctx context.Context, _ Assignment) error {
	w.started <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.release:
		return nil
	}
}

func (w *blockingWorker) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-w.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
}

func (w *blockingWorker) releaseAll() {
	close(w.release)
}

func containsLibrary(libraries []domain.LibraryName, want domain.LibraryName) bool {
	for _, library := range libraries {
		if library == want {
			return true
		}
	}
	return false
}
