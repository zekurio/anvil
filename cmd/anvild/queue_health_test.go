package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/store"
)

func TestQueueStalledSignature(t *testing.T) {
	stalled := scanner.ScanResult{Sources: 2, ExistingJobs: 2}
	tests := []struct {
		name          string
		result        scanner.ScanResult
		activeWorkers int
		viableWork    bool
		want          bool
	}{
		{name: "exact signature", result: stalled, want: true},
		{name: "no sources", result: scanner.ScanResult{ExistingJobs: 2}},
		{name: "no existing jobs", result: scanner.ScanResult{Sources: 2}},
		{name: "new job enqueued", result: scanner.ScanResult{Sources: 2, ExistingJobs: 2, EnqueuedJobs: 1}},
		{name: "worker active", result: stalled, activeWorkers: 1},
		{name: "viable queue work", result: stalled, viableWork: true},
		{name: "unstable source", result: scanner.ScanResult{Sources: 2, ExistingJobs: 2, SkippedUnstable: 1}},
		{name: "stability retry pending", result: scanner.ScanResult{Sources: 2, ExistingJobs: 2, NextStableAt: time.Now().Add(time.Minute)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := queueStalled(tt.result, tt.activeWorkers, tt.viableWork); got != tt.want {
				t.Fatalf("queueStalled() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestLogQueueHealthEmitsStableStructuredWarning(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	state := &fakeQueueHealthStore{}
	observeQueueHealth(context.Background(), state, "downloads", "interval", scanner.ScanResult{Sources: 3, ExistingJobs: 3}, 0)
	line := output.String()
	for _, want := range []string{
		`"level":"WARN"`,
		`"msg":"queue stall detected"`,
		`"library":"downloads"`,
		`"reason":"interval"`,
		`"event":"queue_stall_detected"`,
		`"metric":"anvil_queue_stalled"`,
		`"value":1`,
		`"active_workers":0`,
		`"viable_queue_work":false`,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("queue health log %q missing %q", line, want)
		}
	}
}

func TestLogQueueHealthSuppressesNormalProcessing(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	state := &fakeQueueHealthStore{}
	observeQueueHealth(context.Background(), state, "downloads", "interval", scanner.ScanResult{Sources: 3, ExistingJobs: 3}, 1)
	if output.Len() != 0 {
		t.Fatalf("queue health log = %q, want no warning", output.String())
	}
	if state.calls != 0 {
		t.Fatalf("queue health query calls = %d, want no query outside stall signature", state.calls)
	}
}

func TestObserveQueueHealthQueryErrorSuppressesStallWarning(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	state := &fakeQueueHealthStore{err: errors.New("database unavailable")}
	observeQueueHealth(context.Background(), state, "downloads", "interval", scanner.ScanResult{Sources: 3, ExistingJobs: 3}, 0)
	line := output.String()
	for _, want := range []string{`"level":"ERROR"`, `"msg":"queue health query failed"`, `database unavailable`} {
		if !strings.Contains(line, want) {
			t.Fatalf("queue health error log %q missing %q", line, want)
		}
	}
	if strings.Contains(line, "queue stall detected") {
		t.Fatalf("queue health error log %q contains false stall warning", line)
	}
}

func TestObserveQueueHealthSuppressesPendingWorkAndWarnsForFailedOccurrence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeForceTestFile(t, filepath.Join(root, "Movie.mkv"))
	library := forceTestLibrary(root)
	state, err := store.Open(ctx, filepath.Join(t.TempDir(), "anvil.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Fatalf("state.Close() error = %v", err)
		}
	})

	now := time.Now().UTC().Add(time.Second)
	if _, err := (scanner.Scanner{Store: state, Now: func() time.Time { return now }}).ScanLibrary(ctx, library); err != nil {
		t.Fatalf("ScanLibrary() error = %v", err)
	}
	result := scanner.ScanResult{Sources: 1, ExistingJobs: 1}

	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	observeQueueHealth(ctx, state, library.Name, "interval", result, 0)
	if output.Len() != 0 {
		t.Fatalf("pending queue health log = %q, want no warning", output.String())
	}

	leased, err := state.LeaseNextJob(ctx, "worker-1", now.Add(time.Minute), now)
	if err != nil || leased == nil {
		t.Fatalf("LeaseNextJob() = %v, %v", leased, err)
	}
	if _, err := state.TransitionJob(ctx, leased.ID, domain.JobStateRunning, now, ""); err != nil {
		t.Fatalf("TransitionJob(running) error = %v", err)
	}
	if _, err := state.TransitionJob(ctx, leased.ID, domain.JobStateFailed, now, "terminal failure"); err != nil {
		t.Fatalf("TransitionJob(failed) error = %v", err)
	}

	observeQueueHealth(ctx, state, library.Name, "interval", result, 0)
	if !strings.Contains(output.String(), `"msg":"queue stall detected"`) {
		t.Fatalf("failed occurrence queue health log = %q, want stall warning", output.String())
	}
}

type fakeQueueHealthStore struct {
	viable bool
	err    error
	calls  int
}

func (f *fakeQueueHealthStore) HasViableQueueWorkForLibrary(_ context.Context, _ domain.LibraryName) (bool, error) {
	f.calls++
	return f.viable, f.err
}

func forceTestLibrary(root string) config.LibraryConfig {
	return config.LibraryConfig{
		Name:    "movies",
		Kind:    "media",
		Path:    root,
		Flow:    config.DefaultFlowName,
		Profile: config.DefaultProfileName,
	}
}

func writeForceTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("media"), 0o600); err != nil {
		t.Fatalf("write queue health test file: %v", err)
	}
}
