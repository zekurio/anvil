package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/resources"
)

type Store interface {
	LeaseNextJobForLibraries(ctx context.Context, workerID string, leaseDeadline time.Time, now time.Time, allowedLibraries []domain.LibraryName) (*domain.Job, error)
}

type Worker interface {
	Run(ctx context.Context, assignment Assignment) error
}

type ConfigProvider func() config.Config

type Assignment struct {
	Job       domain.Job
	WorkerID  string
	Resources domain.ResourceAllocation
}

type Scheduler struct {
	Store          Store
	Worker         Worker
	ConfigProvider ConfigProvider
	Allocator      resources.Allocator
	WorkerCount    int
	LeaseDuration  time.Duration
	Interval       time.Duration
	WorkerIDPrefix string
	Now            func() time.Time

	mu         sync.Mutex
	active     map[string]domain.LibraryName
	nextWorker atomic.Uint64
}

func (s *Scheduler) Run(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	interval := s.Interval
	if interval <= 0 {
		interval = configMustDuration(config.DefaultSchedulerTick)
	}

	_, err := s.ScheduleAvailable(ctx)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := s.ScheduleAvailable(ctx); err != nil {
				return err
			}
		}
	}
}

func (s *Scheduler) ScheduleAvailable(ctx context.Context) (int, error) {
	if err := s.validate(); err != nil {
		return 0, err
	}
	slots := s.workerCount() - s.activeCount()
	if slots <= 0 {
		return 0, nil
	}
	started := 0
	for range slots {
		ok, err := s.ScheduleOnce(ctx)
		if err != nil {
			return started, err
		}
		if !ok {
			return started, nil
		}
		started++
	}
	return started, nil
}

func (s *Scheduler) ScheduleOnce(ctx context.Context) (bool, error) {
	if err := s.validate(); err != nil {
		return false, err
	}

	cfg := s.ConfigProvider()
	allowed := s.eligibleLibraries(cfg)
	if len(allowed) == 0 {
		return false, nil
	}

	active := s.activeCount()
	if active >= s.workerCount() {
		return false, nil
	}

	workerID := s.newWorkerID()
	now := s.now()
	leaseDuration := s.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = cfg.LeaseDuration()
	}

	job, err := s.Store.LeaseNextJobForLibraries(ctx, workerID, now.Add(leaseDuration), now, allowed)
	if err != nil {
		return false, fmt.Errorf("lease next job: %w", err)
	}
	if job == nil {
		return false, nil
	}

	allocation := s.Allocator.Allocate(workerID, active+1)
	assignment := Assignment{
		Job:       *job,
		WorkerID:  workerID,
		Resources: allocation,
	}
	s.register(workerID, job.LibraryName)
	go s.runWorker(ctx, assignment)

	return true, nil
}

func (s *Scheduler) ActiveCount() int {
	return s.activeCount()
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
	if s.Allocator.TotalThreads == 0 {
		s.Allocator = resources.NewAllocator(0)
	}
	return nil
}

func (s *Scheduler) eligibleLibraries(cfg config.Config) []domain.LibraryName {
	activeByLibrary := s.activeByLibrary()
	allowed := make([]domain.LibraryName, 0, len(cfg.Libraries))
	for _, library := range cfg.Libraries {
		name := domain.LibraryName(library.Name)
		if library.ConcurrencyLimit > 0 && activeByLibrary[name] >= library.ConcurrencyLimit {
			continue
		}
		allowed = append(allowed, name)
	}
	return allowed
}

func (s *Scheduler) runWorker(ctx context.Context, assignment Assignment) {
	defer s.unregister(assignment.WorkerID)
	_ = s.Worker.Run(ctx, assignment)
}

func (s *Scheduler) register(workerID string, library domain.LibraryName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		s.active = make(map[string]domain.LibraryName)
	}
	s.active[workerID] = library
}

func (s *Scheduler) unregister(workerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, workerID)
}

func (s *Scheduler) activeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

func (s *Scheduler) activeByLibrary() map[domain.LibraryName]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := make(map[domain.LibraryName]int, len(s.active))
	for _, library := range s.active {
		counts[library]++
	}
	return counts
}

func (s *Scheduler) newWorkerID() string {
	prefix := s.WorkerIDPrefix
	if prefix == "" {
		prefix = "anvil-worker"
	}
	return fmt.Sprintf("%s-%d", prefix, s.nextWorker.Add(1))
}

func (s *Scheduler) workerCount() int {
	if s.WorkerCount > 0 {
		return s.WorkerCount
	}
	cfg := s.ConfigProvider()
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
