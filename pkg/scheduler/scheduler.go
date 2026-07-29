package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/resources"
)

type Store interface {
	LeaseNextJobForLibraries(ctx context.Context, workerID string, leaseDeadline time.Time, now time.Time, allowedLibraries []domain.LibraryName) (*domain.Job, error)
	ReleaseLeasedJob(ctx context.Context, jobID domain.JobID, workerID string, now time.Time) (domain.Job, error)
}

type Worker interface {
	Run(ctx context.Context, assignment Assignment) error
}

type ConfigProvider func() config.Config

// ErrJobCanceled is the cancellation cause attached to a dispatched worker's
// context when an operator cancels that job. Workers use it to tell an
// operator cancel apart from daemon shutdown.
var ErrJobCanceled = errors.New("job canceled by operator")

type Assignment struct {
	Job       domain.Job
	WorkerID  string
	Resources domain.ResourceAllocation
}

type Scheduler struct {
	Store          Store
	Worker         Worker
	ConfigProvider ConfigProvider
	WorkerContext  context.Context
	Allocator      resources.Allocator
	WorkerCount    int
	LeaseDuration  time.Duration
	Interval       time.Duration
	WorkerIDPrefix string
	Now            func() time.Time

	mu         sync.Mutex
	active     map[string]activeAssignment
	workerWG   sync.WaitGroup
	nextWorker atomic.Uint64
}

type activeAssignment struct {
	jobID   domain.JobID
	library domain.LibraryName
	threads int
	cancel  context.CancelCauseFunc
}

type activeSnapshot struct {
	count     int
	threads   int
	byLibrary map[domain.LibraryName]int
}

type leasedAssignment struct {
	job           domain.Job
	workerID      string
	leaseDeadline time.Time
}

func (s *Scheduler) Run(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}

	for {
		if _, err := s.ScheduleAvailable(ctx); err != nil {
			return err
		}
		timer := time.NewTimer(s.interval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Scheduler) ScheduleAvailable(ctx context.Context) (int, error) {
	return s.scheduleAvailable(ctx, 0)
}

func (s *Scheduler) scheduleAvailable(ctx context.Context, limit int) (int, error) {
	if err := s.validate(); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	cfg := s.ConfigProvider()
	active := s.activeSnapshot()
	workerCount := s.workerCountForConfig(cfg)
	slots := workerCount - active.count
	if slots <= 0 {
		slog.Debug("scheduler has no available worker slots", "active_workers", active.count, "worker_count", workerCount)
		return 0, nil
	}
	allocator := s.allocator(cfg)
	availableThreads := allocator.AvailableThreads(active.threads)
	if availableThreads <= 0 {
		slog.Debug("scheduler has no available worker threads", "active_workers", active.count, "allocated_threads", active.threads, "total_threads", allocator.TotalThreads)
		return 0, nil
	}

	maxJobs := slots
	if availableThreads < maxJobs {
		maxJobs = availableThreads
	}
	if limit > 0 && limit < maxJobs {
		maxJobs = limit
	}
	if maxJobs <= 0 {
		return 0, nil
	}

	leased, err := s.leaseAvailable(ctx, cfg, active.byLibrary, maxJobs)
	if ctxErr := ctx.Err(); ctxErr != nil {
		releaseErr := s.releaseLeased(context.WithoutCancel(ctx), leased)
		return 0, errors.Join(err, ctxErr, releaseErr)
	}
	if workerErr := s.workerContextError(ctx); workerErr != nil {
		releaseErr := s.releaseLeased(context.WithoutCancel(ctx), leased)
		return 0, errors.Join(err, workerErr, releaseErr)
	}

	started, dispatchErr := s.dispatchLeased(ctx, leased, allocator, availableThreads, active.count)
	if err != nil || dispatchErr != nil {
		return started, errors.Join(err, dispatchErr)
	}
	return started, nil
}

func (s *Scheduler) ScheduleOnce(ctx context.Context) (bool, error) {
	started, err := s.scheduleAvailable(ctx, 1)
	if err != nil {
		return false, err
	}
	return started > 0, nil
}

func (s *Scheduler) ActiveCount() int {
	return s.activeCount()
}

// CancelJob signals every worker currently running jobID and reports whether
// any worker was signaled. It is safe to call for unknown or already finished
// jobs, which makes operator cancellation idempotent.
func (s *Scheduler) CancelJob(jobID domain.JobID) bool {
	s.mu.Lock()
	cancels := make([]context.CancelCauseFunc, 0, 1)
	for _, assignment := range s.active {
		if assignment.jobID == jobID && assignment.cancel != nil {
			cancels = append(cancels, assignment.cancel)
		}
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel(ErrJobCanceled)
	}
	if len(cancels) > 0 {
		slog.Info("worker cancellation requested", "job", int64(jobID), "workers", len(cancels))
	}
	return len(cancels) > 0
}

func (s *Scheduler) Wait() {
	s.workerWG.Wait()
}

func (s *Scheduler) WaitContext(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Wait()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (s *Scheduler) validate() error {
	if s.Store == nil {
		return errors.New("scheduler store is required")
	}
	if s.Worker == nil {
		return errors.New("scheduler worker is required")
	}
	if s.ConfigProvider == nil {
		return errors.New("scheduler config provider is required")
	}
	return nil
}

func (s *Scheduler) interval() time.Duration {
	if s.Interval > 0 {
		return s.Interval
	}
	if s.ConfigProvider != nil {
		return s.ConfigProvider().SchedulerInterval()
	}
	return configMustDuration(config.DefaultSchedulerTick)
}

func (s *Scheduler) allocator(cfg config.Config) resources.Allocator {
	if s.Allocator.TotalThreads > 0 {
		return s.Allocator
	}
	return resources.NewAllocator(cfg.Daemon.TotalThreads)
}

func eligibleLibrariesForCounts(cfg config.Config, activeByLibrary map[domain.LibraryName]int) []domain.LibraryName {
	allowed := make([]domain.LibraryName, 0, len(cfg.Libraries))
	for libraryName, library := range cfg.Libraries {
		name := domain.LibraryName(libraryName)
		if library.ConcurrencyLimit > 0 && activeByLibrary[name] >= library.ConcurrencyLimit {
			continue
		}
		allowed = append(allowed, name)
	}
	return allowed
}

func (s *Scheduler) leaseAvailable(ctx context.Context, cfg config.Config, activeByLibrary map[domain.LibraryName]int, maxJobs int) ([]leasedAssignment, error) {
	counts := make(map[domain.LibraryName]int, len(activeByLibrary))
	for library, count := range activeByLibrary {
		counts[library] = count
	}
	leaseDuration := s.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = cfg.LeaseDuration()
	}

	leased := make([]leasedAssignment, 0, maxJobs)
	for len(leased) < maxJobs {
		if err := ctx.Err(); err != nil {
			return leased, err
		}

		allowed := eligibleLibrariesForCounts(cfg, counts)
		if len(allowed) == 0 {
			if len(leased) == 0 {
				slog.Debug("scheduler has no eligible libraries")
			}
			return leased, nil
		}

		workerID := s.newWorkerID()
		now := s.now()
		leaseDeadline := now.Add(leaseDuration)
		job, err := s.Store.LeaseNextJobForLibraries(ctx, workerID, leaseDeadline, now, allowed)
		if err != nil {
			return leased, fmt.Errorf("lease next job: %w", err)
		}
		if job == nil {
			slog.Debug("scheduler found no pending jobs", "allowed_libraries", allowed)
			return leased, nil
		}

		leased = append(leased, leasedAssignment{
			job:           *job,
			workerID:      workerID,
			leaseDeadline: leaseDeadline,
		})
		counts[job.LibraryName]++
	}
	return leased, nil
}

func (s *Scheduler) dispatchLeased(ctx context.Context, leased []leasedAssignment, allocator resources.Allocator, availableThreads int, activeBefore int) (int, error) {
	if len(leased) == 0 {
		return 0, nil
	}
	workerCtx := ctx
	if s.WorkerContext != nil {
		workerCtx = s.WorkerContext
	}
	started := 0
	for i, leasedAssignment := range leased {
		if err := ctx.Err(); err != nil {
			releaseErr := s.releaseLeased(context.WithoutCancel(ctx), leased[i:])
			return started, errors.Join(err, releaseErr)
		}
		allocation := allocator.AllocateFrom(leasedAssignment.workerID, availableThreads, len(leased))
		assignment := Assignment{
			Job:       leasedAssignment.job,
			WorkerID:  leasedAssignment.workerID,
			Resources: allocation,
		}
		slog.Info("worker scheduled", "worker", assignment.WorkerID, "job", assignment.Job.Label(), "library", string(assignment.Job.LibraryName), "threads", allocation.Threads, "active_workers", activeBefore+i+1, "lease_deadline", leasedAssignment.leaseDeadline)
		jobCtx, cancel := context.WithCancelCause(workerCtx)
		s.register(assignment, cancel)
		s.workerWG.Add(1)
		go s.runWorker(jobCtx, cancel, assignment)
		started++
	}
	return started, nil
}

func (s *Scheduler) workerContextError(ctx context.Context) error {
	workerCtx := ctx
	if s.WorkerContext != nil {
		workerCtx = s.WorkerContext
	}
	return workerCtx.Err()
}

func (s *Scheduler) releaseLeased(ctx context.Context, leased []leasedAssignment) error {
	var errs []error
	for _, assignment := range leased {
		if _, err := s.Store.ReleaseLeasedJob(ctx, assignment.job.ID, assignment.workerID, s.now()); err != nil {
			errs = append(errs, fmt.Errorf("release leased job %d for worker %q: %w", assignment.job.ID, assignment.workerID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Scheduler) runWorker(ctx context.Context, cancel context.CancelCauseFunc, assignment Assignment) {
	started := time.Now()
	defer s.workerWG.Done()
	// Releasing the per-job context after unregistering keeps the cancel func
	// reachable for the whole run and prevents it from leaking afterwards.
	defer cancel(context.Canceled)
	defer s.unregister(assignment.WorkerID)
	slog.Info("worker started", "worker", assignment.WorkerID, "job", assignment.Job.Label(), "library", string(assignment.Job.LibraryName), "threads", assignment.Resources.Threads)
	if err := s.Worker.Run(ctx, assignment); err != nil {
		slog.Warn("worker exited with error", "worker", assignment.WorkerID, "job", assignment.Job.Label(), "library", string(assignment.Job.LibraryName), "duration", time.Since(started), "error", err)
		return
	}
	slog.Info("worker finished", "worker", assignment.WorkerID, "job", assignment.Job.Label(), "library", string(assignment.Job.LibraryName), "duration", time.Since(started))
}

func (s *Scheduler) register(assignment Assignment, cancel context.CancelCauseFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		s.active = make(map[string]activeAssignment)
	}
	s.active[assignment.WorkerID] = activeAssignment{
		jobID:   assignment.Job.ID,
		library: assignment.Job.LibraryName,
		threads: assignment.Resources.Threads,
		cancel:  cancel,
	}
}

func (s *Scheduler) unregister(workerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, workerID)
}

func (s *Scheduler) activeCount() int {
	return s.activeSnapshot().count
}

func (s *Scheduler) activeSnapshot() activeSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := activeSnapshot{
		count:     len(s.active),
		byLibrary: make(map[domain.LibraryName]int, len(s.active)),
	}
	for _, assignment := range s.active {
		snapshot.byLibrary[assignment.library]++
		snapshot.threads += assignment.threads
	}
	return snapshot
}

func (s *Scheduler) newWorkerID() string {
	prefix := s.WorkerIDPrefix
	if prefix == "" {
		prefix = "anvil-worker"
	}
	return fmt.Sprintf("%s-%d", prefix, s.nextWorker.Add(1))
}

func (s *Scheduler) workerCountForConfig(cfg config.Config) int {
	if s.WorkerCount > 0 {
		return s.WorkerCount
	}
	if cfg.Daemon.WorkerCount > 0 {
		return cfg.Daemon.WorkerCount
	}
	return 1
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func configMustDuration(value string) time.Duration {
	duration, err := time.ParseDuration(value)
	if err != nil {
		panic(err)
	}
	return duration
}
