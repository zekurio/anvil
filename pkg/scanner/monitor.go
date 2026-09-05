package scanner

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
)

const (
	DefaultFilesystemEventDebounce = 2 * time.Second
	DefaultConfigReconcileInterval = 30 * time.Second
)

type LibraryScanner interface {
	ScanLibrary(context.Context, config.LibraryConfig) (ScanResult, error)
}

type pathScanner interface {
	ScanPaths(context.Context, config.LibraryConfig, []string) (ScanResult, error)
}

type ConfigProvider func() config.Config

type ScanTrigger struct {
	LibraryName domain.LibraryName
	Reason      string
	Path        string
	Paths       []string
	Completed   bool
}

type EventSource interface {
	Run(context.Context, ConfigProvider, chan<- ScanTrigger) error
}

type Monitor struct {
	pending           map[domain.LibraryName]ScanTrigger
	fullDue           map[domain.LibraryName]time.Time
	Scanner           LibraryScanner
	ConfigProvider    ConfigProvider
	EventSource       EventSource
	Debounce          time.Duration
	ReconcileInterval time.Duration
	Now               func() time.Time
	OnScan            func(config.LibraryConfig, string, ScanResult, error)
	OnEventError      func(error)
}

func (m *Monitor) Run(ctx context.Context) error {
	if m.Scanner == nil {
		return errors.New("scanner monitor scanner is required")
	}
	if m.ConfigProvider == nil {
		return errors.New("scanner monitor config provider is required")
	}

	triggers := make(chan ScanTrigger, 256)
	eventSource := m.EventSource
	if eventSource == nil {
		eventSource = FilesystemEventSource{}
	}

	watcherCtx, stopWatcher := context.WithCancel(ctx)
	var watcherWG sync.WaitGroup
	watcherWG.Add(1)
	go func() {
		defer watcherWG.Done()
		err := eventSource.Run(watcherCtx, m.ConfigProvider, triggers)
		if err != nil && watcherCtx.Err() == nil {
			m.eventError(err)
		}
	}()
	defer func() {
		stopWatcher()
		watcherWG.Wait()
	}()

	m.pending = make(map[domain.LibraryName]ScanTrigger)
	m.fullDue = make(map[domain.LibraryName]time.Time)
	schedules := make(map[domain.LibraryName]time.Time)
	reasons := make(map[domain.LibraryName]string)
	cfg := m.reconcileSchedules(schedules, reasons)

	for {
		nextDue, hasDue := nextScheduledScan(schedules)
		var scanTimer *time.Timer
		var scanC <-chan time.Time
		if hasDue {
			scanTimer = time.NewTimer(durationUntil(m.now(), nextDue))
			scanC = scanTimer.C
		}
		reconcileTimer := time.NewTimer(m.reconcileInterval(cfg))

		select {
		case <-ctx.Done():
			stopTimer(scanTimer)
			stopTimer(reconcileTimer)
			return ctx.Err()
		case trigger := <-triggers:
			stopTimer(scanTimer)
			stopTimer(reconcileTimer)
			cfg = m.reconcileSchedules(schedules, reasons)
			m.scheduleTrigger(cfg, schedules, reasons, trigger)
		case <-reconcileTimer.C:
			stopTimer(scanTimer)
			cfg = m.reconcileSchedules(schedules, reasons)
		case <-scanC:
			stopTimer(reconcileTimer)
			cfg = m.reconcileSchedules(schedules, reasons)
			if err := m.scanDue(ctx, cfg, schedules, reasons); err != nil {
				return err
			}
		}
	}
}

func (m *Monitor) reconcileSchedules(schedules map[domain.LibraryName]time.Time, reasons map[domain.LibraryName]string) config.Config {
	cfg := m.ConfigProvider()
	now := m.now()
	seen := make(map[domain.LibraryName]struct{}, len(cfg.Libraries))

	for name := range cfg.Libraries {
		libraryName := domain.LibraryName(name)
		seen[libraryName] = struct{}{}
		next := now.Add(cfg.ScanIntervalForLibrary(libraryName))
		if m.fullDue != nil {
			if deadline, ok := m.fullDue[libraryName]; ok && deadline.Before(next) {
				next = deadline
			}
			m.fullDue[libraryName] = next
		}
		current, exists := schedules[libraryName]
		if !exists || current.IsZero() || current.After(next) {
			schedules[libraryName] = next
		}
	}
	for name := range schedules {
		if _, ok := seen[name]; !ok {
			delete(schedules, name)
			delete(reasons, name)
			delete(m.pending, name)
			delete(m.fullDue, name)
		}
	}

	return cfg
}

func (m *Monitor) scheduleTrigger(cfg config.Config, schedules map[domain.LibraryName]time.Time, reasons map[domain.LibraryName]string, trigger ScanTrigger) {
	if trigger.LibraryName == "" {
		return
	}
	library, ok := cfg.FindLibrary(trigger.LibraryName)
	if !ok {
		return
	}
	if m.pending != nil {
		if current, exists := m.pending[trigger.LibraryName]; exists {
			trigger = mergeScanTrigger(current, trigger)
		}
		m.pending[trigger.LibraryName] = trigger
	}
	due := m.triggerDue(cfg, library, trigger)
	current, exists := schedules[trigger.LibraryName]
	if !exists || current.After(due) {
		schedules[trigger.LibraryName] = due
	}
	reason := strings.TrimSpace(trigger.Reason)
	if reason == "" {
		reason = "filesystem"
	}
	// A weaker event can follow a completion event before the debounced scan.
	// Keep the stronger reason while retaining the earliest scheduled time. The
	// reason affects scheduling and reporting only: Scanner still consults the
	// completion tracker, so a stale completion cannot make a changed file stable.
	if reasons[trigger.LibraryName] == "transfer-complete" && !trigger.Completed {
		return
	}
	reasons[trigger.LibraryName] = reason
}

func (m *Monitor) scanDue(ctx context.Context, cfg config.Config, schedules map[domain.LibraryName]time.Time, reasons map[domain.LibraryName]string) error {
	now := m.now()
	names := make([]domain.LibraryName, 0, len(schedules))
	for name, due := range schedules {
		if !due.After(now) {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		return names[i] < names[j]
	})

	for _, name := range names {
		library, ok := cfg.FindLibrary(name)
		if !ok {
			delete(schedules, name)
			delete(reasons, name)
			continue
		}
		reason := reasons[name]
		if reason == "" {
			reason = "timer"
		}
		var result ScanResult
		var err error
		trigger, pending := m.pending[name]
		paths := triggerPaths(trigger)
		targeted, supportsPaths := m.Scanner.(pathScanner)
		full := !pending || len(paths) == 0 || !supportsPaths
		if deadline, ok := m.fullDue[name]; !ok || !deadline.After(now) {
			full = true
		}
		if full {
			result, err = m.Scanner.ScanLibrary(ctx, library)
		} else {
			result, err = targeted.ScanPaths(ctx, library, paths)
		}
		delete(m.pending, name)
		if m.OnScan != nil {
			m.OnScan(library, reason, result, err)
		}
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		nextDue := m.now().Add(cfg.ScanIntervalForLibrary(name))
		if full && m.fullDue != nil {
			m.fullDue[name] = nextDue
		}
		if deadline, ok := m.fullDue[name]; ok && deadline.Before(nextDue) {
			nextDue = deadline
		}
		nextReason := ""
		if err == nil {
			if retryDue, ok := m.stabilityRetryDue(cfg, library, result); ok && retryDue.Before(nextDue) {
				nextDue = retryDue
				nextReason = "stability"
				if !full {
					m.pending[name] = trigger
				}
				if filesystemReason(reason) {
					nextReason = "filesystem-stability"
				}
				slog.Info("scheduled stability rescan", "library", library.Name, "reason", nextReason, "due", nextDue, "skipped_unstable", result.SkippedUnstable)
			}
		}
		schedules[name] = nextDue
		if nextReason == "" {
			delete(reasons, name)
		} else {
			reasons[name] = nextReason
		}
	}

	return nil
}

func (m *Monitor) triggerDue(cfg config.Config, library config.LibraryConfig, trigger ScanTrigger) time.Time {
	// Discovery computes stability from all source files. Do not stat each
	// incoming event, which can repeat thousands of times during a write.
	return m.now().Add(m.debounce(cfg))
}

func (m *Monitor) stabilityRetryDue(cfg config.Config, library config.LibraryConfig, result ScanResult) (time.Time, bool) {
	if result.SkippedUnstable <= 0 || library.Kind != "download" {
		return time.Time{}, false
	}
	now := m.now()
	due := result.NextStableAt
	if due.IsZero() {
		stableFor := downloadStableFor(library)
		if stableFor <= 0 {
			return time.Time{}, false
		}
		due = now.Add(stableFor)
	}
	due = due.Add(m.debounce(cfg))
	if due.Before(now) {
		due = now
	}
	return due, true
}

func (m *Monitor) debounce(cfg config.Config) time.Duration {
	if m.Debounce > 0 {
		return m.Debounce
	}
	debounce := cfg.FilesystemEventDebounce()
	if debounce < 0 {
		return DefaultFilesystemEventDebounce
	}
	return debounce
}

func filesystemReason(reason string) bool {
	reason = strings.TrimSpace(reason)
	return reason == "filesystem" || strings.HasPrefix(reason, "filesystem-")
}

func downloadStableFor(library config.LibraryConfig) time.Duration {
	if library.Kind != "download" {
		return 0
	}
	stableFor := strings.TrimSpace(library.Download.StableFor)
	if stableFor == "" {
		stableFor = config.DefaultStableFor
	}
	duration, err := time.ParseDuration(stableFor)
	if err != nil {
		return 0
	}
	return duration
}

func (m *Monitor) reconcileInterval(cfg config.Config) time.Duration {
	if m.ReconcileInterval > 0 {
		return m.ReconcileInterval
	}
	interval := cfg.SchedulerInterval()
	if interval <= 0 {
		return DefaultConfigReconcileInterval
	}
	if interval > DefaultConfigReconcileInterval {
		return DefaultConfigReconcileInterval
	}
	return interval
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Monitor) eventError(err error) {
	if m.OnEventError != nil {
		m.OnEventError(err)
		return
	}
	slog.Error("filesystem event source stopped", "error", err)
}

type FilesystemEventSource struct {
	ReconcileInterval time.Duration
	Completion        *CompletionTracker
}

func nextScheduledScan(schedules map[domain.LibraryName]time.Time) (time.Time, bool) {
	var next time.Time
	for _, due := range schedules {
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	return next, !next.IsZero()
}

func durationUntil(now time.Time, due time.Time) time.Duration {
	delay := due.Sub(now)
	if delay < 0 {
		return 0
	}
	return delay
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

const scanPathLimit = 256

func triggerPaths(trigger ScanTrigger) []string {
	if len(trigger.Paths) > 0 {
		return trigger.Paths
	}
	if trigger.Path != "" {
		return []string{trigger.Path}
	}
	return nil
}

// Keep every changed path. A full scan bounds memory during event bursts.
func mergeScanTrigger(current, next ScanTrigger) ScanTrigger {
	left, right := triggerPaths(current), triggerPaths(next)
	if len(left) == 0 || len(right) == 0 {
		return ScanTrigger{LibraryName: current.LibraryName, Reason: "filesystem"}
	}
	paths := make(map[string]struct{}, len(left)+len(right))
	for _, path := range left {
		paths[path] = struct{}{}
	}
	for _, path := range right {
		paths[path] = struct{}{}
	}
	if len(paths) > scanPathLimit {
		return ScanTrigger{LibraryName: current.LibraryName, Reason: "filesystem"}
	}
	result := current
	if next.Completed {
		result = next
	}
	result.Paths = make([]string, 0, len(paths))
	for path := range paths {
		result.Paths = append(result.Paths, path)
	}
	sort.Strings(result.Paths)
	return result
}
