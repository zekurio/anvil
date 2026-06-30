package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/resources"
)

func TestScheduleAvailableHonorsLibraryConcurrency(t *testing.T) {
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
	if containsLibrary(store.allowed[1], "movies") {
		t.Fatalf("second allowed libraries = %v, want movies excluded", store.allowed[1])
	}
	if !containsLibrary(store.allowed[1], "tv") {
		t.Fatalf("second allowed libraries = %v, want tv included", store.allowed[1])
	}
}

func TestScheduleAvailableSplitsAvailableThreadsAndStallsWhenBudgetFull(t *testing.T) {
	ctx := context.Background()
	store := &fakeScheduleStore{
		jobs: []domain.Job{
			{ID: 1, LibraryName: "movies", State: domain.JobStatePending},
			{ID: 2, LibraryName: "movies", State: domain.JobStatePending},
		},
	}
	worker := newBlockingWorker()
	defer worker.releaseAll()

	s := &Scheduler{
		Store:          store,
		Worker:         worker,
		ConfigProvider: resourceScheduleConfig,
		Allocator:      resources.NewAllocator(8),
		WorkerCount:    4,
		LeaseDuration:  time.Minute,
	}

	started, err := s.ScheduleAvailable(ctx)
	if err != nil {
		t.Fatalf("ScheduleAvailable() error = %v", err)
	}
	if started != 2 {
		t.Fatalf("started = %d, want 2", started)
	}
	first := worker.waitAssignment(t)
	second := worker.waitAssignment(t)
	if first.Resources.Threads != 4 || second.Resources.Threads != 4 {
		t.Fatalf("threads = %d and %d, want 4 and 4", first.Resources.Threads, second.Resources.Threads)
	}

	store.jobs = append(store.jobs, domain.Job{ID: 3, LibraryName: "movies", State: domain.JobStatePending})
	leaseCalls := len(store.allowed)
	started, err = s.ScheduleAvailable(ctx)
	if err != nil {
		t.Fatalf("second ScheduleAvailable() error = %v", err)
	}
	if started != 0 {
		t.Fatalf("started = %d, want 0 while all threads are allocated", started)
	}
	if len(store.allowed) != leaseCalls {
		t.Fatalf("lease calls = %d, want %d while thread budget is full", len(store.allowed), leaseCalls)
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

func TestScheduleAvailableStopsLeasingWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &fakeScheduleStore{
		jobs: []domain.Job{
			{ID: 1, LibraryName: "movies", State: domain.JobStatePending},
			{ID: 2, LibraryName: "tv", State: domain.JobStatePending},
		},
		afterLease: cancel,
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
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScheduleAvailable() error = %v, want context canceled", err)
	}
	if started != 1 {
		t.Fatalf("started = %d, want 1 leased job dispatched before cancellation", started)
	}
	assignment := worker.waitAssignment(t)
	if assignment.Job.ID != 1 {
		t.Fatalf("worker job ID = %d, want 1", assignment.Job.ID)
	}
	if len(store.allowed) != 1 {
		t.Fatalf("lease calls = %d, want 1 after cancellation", len(store.allowed))
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := s.WaitContext(waitCtx); err != nil {
		t.Fatalf("WaitContext() error = %v", err)
	}
}

func TestWorkerContextCanOutliveSchedulerContext(t *testing.T) {
	schedulerCtx, stopScheduling := context.WithCancel(context.Background())
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()

	store := &fakeScheduleStore{
		jobs: []domain.Job{{ID: 1, LibraryName: "movies", State: domain.JobStatePending}},
	}
	worker := newBlockingWorker()
	defer worker.releaseAll()

	s := &Scheduler{
		Store:          store,
		Worker:         worker,
		ConfigProvider: scheduleConfig,
		WorkerContext:  workerCtx,
		Allocator:      resources.NewAllocator(4),
		WorkerCount:    1,
		LeaseDuration:  time.Minute,
	}

	if started, err := s.ScheduleOnce(schedulerCtx); err != nil || !started {
		t.Fatalf("ScheduleOnce() = %v, %v, want true, nil", started, err)
	}
	worker.waitStarted(t)
	stopScheduling()

	timeoutCtx, cancelTimeout := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancelTimeout()
	if err := s.WaitContext(timeoutCtx); err == nil {
		t.Fatal("WaitContext() error = nil, want timeout while worker context is still active")
	}

	stopWorker()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := s.WaitContext(waitCtx); err != nil {
		t.Fatalf("WaitContext() error = %v", err)
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

func resourceScheduleConfig() config.Config {
	cfg := config.Default()
	cfg.Daemon.WorkerCount = 4
	cfg.Daemon.TotalThreads = 8
	cfg.Daemon.LeaseDuration = "1m"
	cfg.Libraries = map[string]config.LibraryConfig{
		"movies": {Kind: "media", Path: "/media/movies", Flow: config.DefaultFlowName, Profile: config.DefaultProfileName},
	}
	return cfg
}

type fakeScheduleStore struct {
	jobs       []domain.Job
	allowed    [][]domain.LibraryName
	afterLease func()
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
		if f.afterLease != nil {
			f.afterLease()
		}
		return &job, nil
	}
	return nil, nil
}

type blockingWorker struct {
	started chan Assignment
	release chan struct{}
}

func newBlockingWorker() *blockingWorker {
	return &blockingWorker{
		started: make(chan Assignment, 8),
		release: make(chan struct{}),
	}
}

func (w *blockingWorker) Run(ctx context.Context, assignment Assignment) error {
	w.started <- assignment
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.release:
		return nil
	}
}

func (w *blockingWorker) waitStarted(t *testing.T) {
	t.Helper()
	_ = w.waitAssignment(t)
}

func (w *blockingWorker) waitAssignment(t *testing.T) Assignment {
	t.Helper()
	select {
	case assignment := <-w.started:
		return assignment
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	return Assignment{}
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
