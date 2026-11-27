package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/store"
)

// fakeSnapshotStore holds daemon settings the way the store does and no
// libraries.
type fakeSnapshotStore struct {
	mu        sync.Mutex
	overrides config.DaemonOverrides
}

func (s *fakeSnapshotStore) set(overrides config.DaemonOverrides) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overrides = overrides
}

func (s *fakeSnapshotStore) DaemonSettings(context.Context) (config.DaemonOverrides, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overrides, nil
}

func (s *fakeSnapshotStore) ListLibraries(context.Context) ([]store.Library, error) {
	return []store.Library{}, nil
}

func (s *fakeSnapshotStore) ListArrs(context.Context) ([]store.Arr, error) {
	return []store.Arr{}, nil
}

func loadFile(t *testing.T, data string, opts options) config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "anvil.toml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadRuntimeConfig(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A reload re-reads anvil.toml and merges the values saved in the web UI back
// in, so a SIGHUP never reverts an edit, and a key the file pins wins.
func TestReloadMergesTheStoredDaemonSettings(t *testing.T) {
	ctx := context.Background()
	workers, attempts, level := 8, 4, "debug"
	state := &fakeSnapshotStore{overrides: config.DaemonOverrides{WorkerCount: &workers, MaxAttempts: &attempts, LogLevel: &level}}
	var logLevel slog.LevelVar
	runtime := newRuntimeConfig(state, &logLevel)
	woken := runtime.Reconfigured()

	pinned := loadFile(t, "[daemon]\nworker_count = 2\n", options{})
	if err := runtime.Reload(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	cfg := runtime.Get()
	if cfg.Daemon.WorkerCount != 2 || cfg.DaemonSource("worker_count") != config.SourceFile {
		t.Errorf("worker_count = %d from %s, want the file's 2", cfg.Daemon.WorkerCount, cfg.DaemonSource("worker_count"))
	}
	if cfg.Daemon.MaxAttempts != 4 || cfg.DaemonSource("max_attempts") != config.SourceStore {
		t.Errorf("max_attempts = %d from %s, want the stored 4", cfg.Daemon.MaxAttempts, cfg.DaemonSource("max_attempts"))
	}
	if logLevel.Level() != slog.LevelDebug {
		t.Errorf("log level = %s, want the stored debug to apply live", logLevel.Level())
	}
	select {
	case <-woken:
	default:
		t.Error("the reload did not wake the loops")
	}

	// A web write saves to the store and refreshes from the file as loaded.
	attempts = 6
	state.set(config.DaemonOverrides{WorkerCount: &workers, MaxAttempts: &attempts})
	if err := runtime.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if cfg = runtime.Get(); cfg.Daemon.MaxAttempts != 6 || cfg.Daemon.WorkerCount != 2 {
		t.Errorf("after the web write: max_attempts = %d, worker_count = %d", cfg.Daemon.MaxAttempts, cfg.Daemon.WorkerCount)
	}
	if logLevel.Level() != slog.LevelInfo {
		t.Errorf("log level = %s, want info once the stored level is gone", logLevel.Level())
	}

	// The file stops pinning worker_count: the stored value takes over, and
	// the web edit survives the reload.
	if err := runtime.Reload(ctx, loadFile(t, "", options{})); err != nil {
		t.Fatal(err)
	}
	if cfg = runtime.Get(); cfg.Daemon.WorkerCount != 8 || cfg.Daemon.MaxAttempts != 6 {
		t.Errorf("after the reload: worker_count = %d, max_attempts = %d", cfg.Daemon.WorkerCount, cfg.Daemon.MaxAttempts)
	}

	// A stored value that breaks a rule rejects the reload and leaves the
	// snapshot, and the file a refresh merges from, as they were. One for a
	// key the file pins is never applied, so it breaks nothing.
	zero := 0
	state.set(config.DaemonOverrides{WorkerCount: &workers, MaxAttempts: &zero})
	if err := runtime.Reload(ctx, pinned); err == nil {
		t.Fatal("a reload with an invalid stored value succeeded")
	}
	if cfg = runtime.Get(); cfg.Daemon.WorkerCount != 8 || cfg.Daemon.MaxAttempts != 6 {
		t.Errorf("after the rejected reload: worker_count = %d, max_attempts = %d, want 8 and 6", cfg.Daemon.WorkerCount, cfg.Daemon.MaxAttempts)
	}
	state.set(config.DaemonOverrides{WorkerCount: &zero})
	if err := runtime.Reload(ctx, pinned); err != nil {
		t.Fatalf("an invalid stored value for a pinned key rejected the reload: %v", err)
	}
	state.set(config.DaemonOverrides{})
	if err := runtime.Reload(ctx, loadFile(t, "", options{})); err != nil {
		t.Fatal(err)
	}
	state.set(config.DaemonOverrides{MaxAttempts: &zero})
	if err := runtime.Reload(ctx, pinned); err == nil {
		t.Fatal("a reload with an invalid stored value succeeded")
	}
	state.set(config.DaemonOverrides{})
	if err := runtime.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if cfg = runtime.Get(); cfg.DaemonSource("worker_count") != config.SourceDefault {
		t.Errorf("the refresh merged over the rejected file: worker_count from %s", cfg.DaemonSource("worker_count"))
	}
}

// A command-line option pins its key like the file does.
func TestShutdownOptionsPinTheirKeys(t *testing.T) {
	cfg := loadFile(t, "", options{shutdownPolicy: "cancel", shutdownTimeout: "90s"})
	policy, timeout := "drain", config.Duration{}
	merged, err := cfg.WithDaemonOverrides(config.DaemonOverrides{ShutdownPolicy: &policy, ShutdownTimeout: &timeout})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Daemon.ShutdownPolicy != "cancel" || merged.DaemonSource("shutdown_policy") != config.SourceFlag {
		t.Errorf("shutdown_policy = %q from %s, want the option's cancel", merged.Daemon.ShutdownPolicy, merged.DaemonSource("shutdown_policy"))
	}
	if merged.ShutdownTimeout().String() != "1m30s" || merged.DaemonSource("shutdown_timeout") != config.SourceFlag {
		t.Errorf("shutdown_timeout = %s from %s, want the option's 90s", merged.ShutdownTimeout(), merged.DaemonSource("shutdown_timeout"))
	}
}
