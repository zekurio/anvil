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

func TestPausedLibrariesAreNotLeased(t *testing.T) {
	cfg := config.Default()
	cfg.Libraries = map[string]config.LibraryConfig{
		"paused": {Paused: true},
		"open":   {},
		"full":   {ConcurrencyLimit: 1},
	}
	allowed := eligibleLibrariesForCounts(cfg, map[domain.LibraryName]int{"full": 1})
	if len(allowed) != 1 || allowed[0] != "open" {
		t.Fatalf("eligible libraries = %v, want only the open one", allowed)
	}

	// With every library paused there is nothing to lease from. That must
	// not reach the store as an empty filter, which would mean "any library".
	cfg.Libraries = map[string]config.LibraryConfig{"media": {Paused: true}}
	queue := &queueStore{wake: make(chan struct{}, 1)}
	queue.add(1)
	worker := heldWorker{started: make(chan Assignment, 1), finish: make(chan struct{})}
	planner := &Scheduler{Store: queue, Worker: worker, ConfigProvider: func() config.Config { return cfg }}
	started, err := planner.ScheduleAvailable(context.Background())
	if err != nil || started != 0 {
		t.Fatalf("ScheduleAvailable = %d, %v; a paused library's job was started", started, err)
	}
	if len(queue.jobs) != 1 {
		t.Fatal("the pending job of a paused library was leased")
	}

	// Resuming the library lets the same job run.
	cfg.Libraries = map[string]config.LibraryConfig{"media": {}}
	started, err = planner.ScheduleAvailable(context.Background())
	if err != nil || started != 1 {
		t.Fatalf("ScheduleAvailable after resume = %d, %v", started, err)
	}
	receiveAssignment(t, worker.started)
	close(worker.finish)
	planner.Wait()
}

// windingDownWorker is canceled like any worker, but takes until release to
// return, the way an attempt stops ffmpeg and removes its part file.
type windingDownWorker struct {
	started  chan Assignment
	canceled chan struct{}
	release  chan struct{}
}

func (w windingDownWorker) Run(ctx context.Context, a Assignment) error {
	w.started <- a
	<-ctx.Done()
	close(w.canceled)
	<-w.release
	return ctx.Err()
}

// A canceled job's row is terminal at once, so only the scheduler can say
// that its worker is still at work. It has to say so until the worker has
// returned, and not a moment less.
func TestRunningJobsListsACanceledJobUntilItsWorkerReturns(t *testing.T) {
	cfg := config.Default()
	cfg.Daemon.WorkerCount = 1
	cfg.Libraries = map[string]config.LibraryConfig{"media": {Name: "media"}}
	state := &queueStore{wake: make(chan struct{}, 1)}
	worker := windingDownWorker{started: make(chan Assignment, 1), canceled: make(chan struct{}), release: make(chan struct{})}
	s := &Scheduler{Store: state, Worker: worker, ConfigProvider: func() config.Config { return cfg }, Interval: time.Hour}
	state.add(7)
	if started, err := s.ScheduleOnce(context.Background()); err != nil || !started {
		t.Fatalf("ScheduleOnce = %t, %v", started, err)
	}
	receiveAssignment(t, worker.started)
	if jobs := s.RunningJobs("media"); len(jobs) != 1 || jobs[0] != 7 {
		t.Fatalf("RunningJobs = %v, want the running job", jobs)
	}
	if jobs := s.RunningJobs("other"); len(jobs) != 0 {
		t.Fatalf("RunningJobs of another library = %v", jobs)
	}

	if !s.CancelJob(7) {
		t.Fatal("CancelJob signaled no worker")
	}
	<-worker.canceled
	if jobs := s.RunningJobs("media"); len(jobs) != 1 || jobs[0] != 7 {
		t.Fatalf("RunningJobs while the canceled worker winds down = %v, want it still listed", jobs)
	}
	close(worker.release)
	s.Wait()
	if jobs := s.RunningJobs("media"); len(jobs) != 0 {
		t.Fatalf("RunningJobs after the worker returned = %v", jobs)
	}
}
