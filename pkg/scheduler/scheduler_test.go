package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
)

type queueStore struct {
	mu   sync.Mutex
	jobs []domain.Job
	wake chan struct{}
}

func (s *queueStore) WorkAvailable() <-chan struct{} { return s.wake }
func (s *queueStore) add(id domain.JobID) {
	s.mu.Lock()
	s.jobs = append(s.jobs, domain.Job{ID: id, LibraryName: "media"})
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *queueStore) LeaseNextJobForLibraries(_ context.Context, _ string, _, _ time.Time, _ []domain.LibraryName) (*domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.jobs) == 0 {
		return nil, nil
	}
	job := s.jobs[0]
	s.jobs = s.jobs[1:]
	return &job, nil
}
func (*queueStore) ReleaseLeasedJob(_ context.Context, _ domain.JobID, _ string, _ time.Time) (domain.Job, error) {
	return domain.Job{}, nil
}

type heldWorker struct {
	started chan Assignment
	finish  chan struct{}
}

func (w heldWorker) Run(ctx context.Context, a Assignment) error {
	w.started <- a
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.finish:
		return nil
	}
}

func receiveAssignment(t *testing.T, ch <-chan Assignment) Assignment {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not dispatch on wakeup")
		return Assignment{}
	}
}

func TestStaggeredJobsLeaveThreadsForLaterArrival(t *testing.T) {
	for _, cap := range []int{0, 3} {
		t.Run(string(rune('0'+cap)), func(t *testing.T) {
			cfg := config.Default()
			cfg.Daemon.TotalThreads = 8
			cfg.Daemon.WorkerCount = 2
			cfg.Daemon.MaxThreadsPerJob = cap
			cfg.Libraries = map[string]config.LibraryConfig{"media": {Name: "media"}}
			state := &queueStore{wake: make(chan struct{}, 1)}
			worker := heldWorker{started: make(chan Assignment, 2), finish: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			s := &Scheduler{Store: state, Worker: worker, ConfigProvider: func() config.Config { return cfg }, Interval: time.Hour}
			done := make(chan error, 1)
			go func() { done <- s.Run(ctx) }()
			t.Cleanup(func() { cancel(); <-done; s.Wait() })
			state.add(1)
			first := receiveAssignment(t, worker.started)
			want := 4
			if cap > 0 {
				want = cap
			}
			if first.Resources.Threads != want {
				t.Fatalf("first allocation = %d, want %d", first.Resources.Threads, want)
			}
			state.add(2)
			second := receiveAssignment(t, worker.started)
			if second.Resources.Threads != want {
				t.Fatalf("second allocation = %d, want %d", second.Resources.Threads, want)
			}
		})
	}
}

func TestWorkerCompletionWakesScheduler(t *testing.T) {
	cfg := config.Default()
	cfg.Daemon.WorkerCount = 1
	cfg.Daemon.TotalThreads = 4
	cfg.Libraries = map[string]config.LibraryConfig{"media": {Name: "media"}}
	state := &queueStore{wake: make(chan struct{}, 1)}
	state.add(1)
	state.add(2)
	worker := heldWorker{started: make(chan Assignment, 2), finish: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Scheduler{Store: state, Worker: worker, ConfigProvider: func() config.Config { return cfg }, Interval: time.Hour}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done; s.Wait() })
	receiveAssignment(t, worker.started)
	worker.finish <- struct{}{}
	if next := receiveAssignment(t, worker.started); next.Job.ID != 2 {
		t.Fatalf("next job = %d", next.Job.ID)
	}
}
