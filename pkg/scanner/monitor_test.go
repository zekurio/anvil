package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
)

func TestMonitorScansLibrariesOnIndependentTimers(t *testing.T) {
	cfg := monitorTestConfig()
	cfg.Libraries["fast"] = config.LibraryConfig{
		Kind:         "media",
		Path:         t.TempDir(),
		Flow:         config.DefaultFlowName,
		Profile:      config.DefaultProfileName,
		ScanInterval: "5ms",
	}
	cfg.Libraries["slow"] = config.LibraryConfig{
		Kind:         "media",
		Path:         t.TempDir(),
		Flow:         config.DefaultFlowName,
		Profile:      config.DefaultProfileName,
		ScanInterval: "25ms",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	scanner := newFakeMonitorScanner(func(counts map[domain.LibraryName]int) bool {
		return counts["fast"] >= 2 && counts["slow"] >= 1
	}, cancel, done)

	errC := make(chan error, 1)
	go func() {
		errC <- (&Monitor{
			Scanner:           scanner,
			ConfigProvider:    func() config.Config { return cfg },
			EventSource:       noopEventSource{},
			ReconcileInterval: 5 * time.Millisecond,
		}).Run(ctx)
	}()

	waitMonitorDone(t, done, errC)
	counts := scanner.Counts()
	if counts["fast"] < 2 {
		t.Fatalf("fast scan count = %d, want at least 2", counts["fast"])
	}
	if counts["slow"] < 1 {
		t.Fatalf("slow scan count = %d, want at least 1", counts["slow"])
	}
}

func TestMonitorScansLibraryFromFilesystemTrigger(t *testing.T) {
	cfg := monitorTestConfig()
	cfg.Libraries["movies"] = config.LibraryConfig{
		Kind:    "media",
		Path:    t.TempDir(),
		Flow:    config.DefaultFlowName,
		Profile: config.DefaultProfileName,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	scanner := newFakeMonitorScanner(func(counts map[domain.LibraryName]int) bool {
		return counts["movies"] == 1
	}, cancel, done)

	var mu sync.Mutex
	var reasons []string
	errC := make(chan error, 1)
	go func() {
		errC <- (&Monitor{
			Scanner:        scanner,
			ConfigProvider: func() config.Config { return cfg },
			EventSource: scriptedEventSource{Triggers: []ScanTrigger{
				{LibraryName: "movies", Reason: "filesystem", Path: filepath.Join(cfg.Libraries["movies"].Path, "Movie.mkv")},
				{LibraryName: "movies", Reason: "filesystem", Path: filepath.Join(cfg.Libraries["movies"].Path, "Movie.mkv")},
			}},
			Debounce:          20 * time.Millisecond,
			ReconcileInterval: 5 * time.Millisecond,
			OnScan: func(_ config.LibraryConfig, reason string, _ ScanResult, _ error) {
				mu.Lock()
				defer mu.Unlock()
				reasons = append(reasons, reason)
			},
		}).Run(ctx)
	}()

	waitMonitorDone(t, done, errC)
	counts := scanner.Counts()
	if counts["movies"] != 1 {
		t.Fatalf("movies scan count = %d, want 1 coalesced scan", counts["movies"])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reasons) != 1 || reasons[0] != "filesystem" {
		t.Fatalf("scan reasons = %v, want [filesystem]", reasons)
	}
}

func TestMonitorDelaysDownloadFilesystemTriggerUntilPathStable(t *testing.T) {
	cfg := monitorTestConfig()
	root := t.TempDir()
	path := filepath.Join(root, "Movie.mkv")
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	if err := os.WriteFile(path, []byte("movie"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	library := config.LibraryConfig{
		Kind:    "download",
		Path:    root,
		Flow:    config.DefaultFlowName,
		Profile: config.DefaultProfileName,
		Download: config.DownloadLibraryConfig{
			HandoffPath: "/imports/movies",
			StableFor:   "5m",
		},
	}
	cfg.Libraries["downloads"] = library

	monitor := Monitor{
		Debounce: 2 * time.Second,
		Now: func() time.Time {
			return now
		},
	}
	got := monitor.triggerDue(cfg, library, ScanTrigger{
		LibraryName: "downloads",
		Reason:      "filesystem",
		Path:        path,
	})
	want := now.Add(5*time.Minute + 2*time.Second)
	if !got.Equal(want) {
		t.Fatalf("trigger due = %s, want %s", got, want)
	}
}

func TestMonitorRetriesFilesystemScanAfterSkippedUnstable(t *testing.T) {
	cfg := monitorTestConfig()
	cfg.Libraries["downloads"] = config.LibraryConfig{
		Kind:    "download",
		Path:    t.TempDir(),
		Flow:    config.DefaultFlowName,
		Profile: config.DefaultProfileName,
		Download: config.DownloadLibraryConfig{
			HandoffPath: "/imports/movies",
			StableFor:   "20ms",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	scanner := newFakeMonitorScanner(func(counts map[domain.LibraryName]int) bool {
		return counts["downloads"] >= 2
	}, cancel, done)
	scanner.result = func(_ domain.LibraryName, count int) ScanResult {
		if count == 1 {
			return ScanResult{
				Libraries:       1,
				SkippedUnstable: 1,
				NextStableAt:    time.Now().Add(20 * time.Millisecond).UTC(),
			}
		}
		return ScanResult{Libraries: 1}
	}

	var mu sync.Mutex
	var reasons []string
	errC := make(chan error, 1)
	go func() {
		errC <- (&Monitor{
			Scanner:        scanner,
			ConfigProvider: func() config.Config { return cfg },
			EventSource: scriptedEventSource{Triggers: []ScanTrigger{
				{LibraryName: "downloads", Reason: "filesystem", Path: filepath.Join(cfg.Libraries["downloads"].Path, "Movie.mkv")},
			}},
			Debounce:          time.Millisecond,
			ReconcileInterval: time.Millisecond,
			OnScan: func(_ config.LibraryConfig, reason string, _ ScanResult, _ error) {
				mu.Lock()
				defer mu.Unlock()
				reasons = append(reasons, reason)
			},
		}).Run(ctx)
	}()

	waitMonitorDone(t, done, errC)
	counts := scanner.Counts()
	if counts["downloads"] != 2 {
		t.Fatalf("downloads scan count = %d, want 2", counts["downloads"])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reasons) != 2 || reasons[0] != "filesystem" || reasons[1] != "filesystem-stability" {
		t.Fatalf("scan reasons = %v, want [filesystem filesystem-stability]", reasons)
	}
}

func TestLibrariesForPathMatchesNestedRoots(t *testing.T) {
	root := t.TempDir()
	movies := filepath.Join(root, "movies")
	tv := filepath.Join(root, "tv")
	roots := map[domain.LibraryName]string{
		"movies": movies,
		"tv":     tv,
	}

	got := librariesForPath(roots, filepath.Join(movies, "Nested", "Movie.mkv"))
	if len(got) != 1 || got[0] != "movies" {
		t.Fatalf("librariesForPath() = %v, want [movies]", got)
	}
	if got := librariesForPath(roots, filepath.Join(root, "other", "Movie.mkv")); len(got) != 0 {
		t.Fatalf("librariesForPath() outside root = %v, want none", got)
	}
}

func monitorTestConfig() config.Config {
	cfg := config.Default()
	cfg.Daemon.ScanInterval = "1h"
	cfg.Daemon.SchedulerInterval = "5ms"
	cfg.Libraries = make(map[string]config.LibraryConfig)
	return cfg
}

type fakeMonitorScanner struct {
	mu     sync.Mutex
	counts map[domain.LibraryName]int
	done   chan struct{}
	cancel context.CancelFunc
	target func(map[domain.LibraryName]int) bool
	result func(domain.LibraryName, int) ScanResult
	closed bool
}

func newFakeMonitorScanner(target func(map[domain.LibraryName]int) bool, cancel context.CancelFunc, done chan struct{}) *fakeMonitorScanner {
	return &fakeMonitorScanner{
		counts: make(map[domain.LibraryName]int),
		done:   done,
		cancel: cancel,
		target: target,
	}
}

func (f *fakeMonitorScanner) ScanLibrary(_ context.Context, library config.LibraryConfig) (ScanResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := domain.LibraryName(library.Name)
	f.counts[name]++
	count := f.counts[name]
	result := ScanResult{Libraries: 1}
	if f.result != nil {
		result = f.result(name, count)
	}
	if !f.closed && f.target != nil && f.target(f.counts) {
		f.closed = true
		close(f.done)
		f.cancel()
	}
	return result, nil
}

func (f *fakeMonitorScanner) Counts() map[domain.LibraryName]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	counts := make(map[domain.LibraryName]int, len(f.counts))
	for name, count := range f.counts {
		counts[name] = count
	}
	return counts
}

type noopEventSource struct{}

func (noopEventSource) Run(ctx context.Context, _ ConfigProvider, _ chan<- ScanTrigger) error {
	<-ctx.Done()
	return ctx.Err()
}

type scriptedEventSource struct {
	Triggers []ScanTrigger
}

func (s scriptedEventSource) Run(ctx context.Context, _ ConfigProvider, triggers chan<- ScanTrigger) error {
	for _, trigger := range s.Triggers {
		select {
		case triggers <- trigger:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func waitMonitorDone(t *testing.T, done <-chan struct{}, errC <-chan error) {
	t.Helper()
	select {
	case <-done:
	case err := <-errC:
		t.Fatalf("Monitor.Run() returned before target scans: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for monitor scans")
	}

	select {
	case err := <-errC:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Monitor.Run() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for monitor shutdown")
	}
}
