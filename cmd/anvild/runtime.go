package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/store"
)

// snapshotStore is what building the runtime snapshot reads.
type snapshotStore interface {
	ListLibraries(context.Context) ([]store.Library, error)
	ListArrs(context.Context) ([]store.Arr, error)
}

// withStoreSnapshot returns cfg with its libraries and Arr connections
// replaced by what the store holds. The process settings stay as they are.
func withStoreSnapshot(ctx context.Context, cfg config.Config, state snapshotStore) (config.Config, error) {
	libraries, err := state.ListLibraries(ctx)
	if err != nil {
		return config.Config{}, fmt.Errorf("load libraries: %w", err)
	}
	arrs, err := state.ListArrs(ctx)
	if err != nil {
		return config.Config{}, fmt.Errorf("load arr connections: %w", err)
	}
	cfg.Libraries = make(map[string]config.LibraryConfig, len(libraries))
	for _, library := range libraries {
		cfg.Libraries[library.Name] = library.LibraryConfig
	}
	cfg.Arrs = make(map[string]config.ArrConfig, len(arrs))
	for _, arr := range arrs {
		cfg.Arrs[arr.Name] = arr.ArrConfig
	}
	return cfg, nil
}

// runtimeConfig is the snapshot every loop of the daemon reads: the process
// settings of anvil.toml plus the libraries and Arr connections of the store.
// Each half has one writer path. SIGHUP replaces the process settings; a
// change made through the web API re-reads the store half. Both end in
// libraryChanged, which is how the scan monitor and the filesystem watcher
// learn of a new, changed, or deleted library without waiting for a tick.
type runtimeConfig struct {
	mu  sync.RWMutex
	cfg config.Config

	// refreshMu serializes store reads, so an older read can never overwrite
	// the result of a newer one.
	refreshMu sync.Mutex
	state     snapshotStore
	// reconfigured holds one coalescing wake-up channel per listener.
	reconfigured []chan struct{}
}

func newRuntimeConfig(cfg config.Config, state snapshotStore) *runtimeConfig {
	return &runtimeConfig{cfg: cfg, state: state}
}

func (r *runtimeConfig) Get() config.Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

// Reconfigured returns a channel that fires after the libraries changed.
// Every caller gets its own, so no listener can swallow another's wake-up.
// It must be called before the loops that use it start.
func (r *runtimeConfig) Reconfigured() <-chan struct{} {
	listener := make(chan struct{}, 1)
	r.reconfigured = append(r.reconfigured, listener)
	return listener
}

// Refresh re-reads the libraries and Arr connections from the store. The web
// API calls it after every change it persists and before it answers, so the
// next request, scan, and lease already see the change.
func (r *runtimeConfig) Refresh(ctx context.Context) error {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	next, err := withStoreSnapshot(ctx, config.Config{}, r.state)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.cfg.Libraries, r.cfg.Arrs = next.Libraries, next.Arrs
	r.mu.Unlock()
	r.libraryChanged()
	return nil
}

// Reload takes the process settings of a freshly loaded anvil.toml and
// re-reads the store half as well.
func (r *runtimeConfig) Reload(ctx context.Context, loaded config.Config) error {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	next, err := withStoreSnapshot(ctx, loaded, r.state)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.cfg = next
	r.mu.Unlock()
	r.libraryChanged()
	return nil
}

func (r *runtimeConfig) libraryChanged() {
	for _, listener := range r.reconfigured {
		select {
		case listener <- struct{}{}:
		default:
		}
	}
}

// importLegacyConfig moves the libraries, profiles, and Arr connections of
// anvil.toml into the store the first time a daemon with a store that can
// hold them starts. It runs after the ownership claims, like every other
// start-up side effect.
func importLegacyConfig(ctx context.Context, cfg config.Config, state *store.SQLiteStore) error {
	result, err := state.ImportLegacyConfig(ctx, cfg.Legacy, time.Now())
	if err != nil {
		return fmt.Errorf("import libraries from anvil.toml: %w", err)
	}
	if !result.Imported {
		return nil
	}
	for _, name := range result.Arrs {
		slog.Info("imported arr connection from anvil.toml", "arr", name)
	}
	for _, imported := range result.Flows {
		if imported.Created {
			slog.Info("imported profile from anvil.toml as a flow", "profile", imported.Profile, "flow", imported.Flow, "flow_id", imported.FlowID)
		} else {
			slog.Info("profile from anvil.toml matches an existing flow", "profile", imported.Profile, "flow", imported.Flow, "flow_id", imported.FlowID)
		}
	}
	for _, library := range result.Libraries {
		if library.PausedFor != "" {
			// One line per library, naming what to fix: left running, a
			// library on a flow that does not validate fails every file it
			// holds until the attempts run out.
			slog.Warn("imported library from anvil.toml is paused because its flow does not validate; fix the flow in the web UI, then resume the library",
				"library", library.Name, "flow", library.Flow, "issue", library.PausedFor)
			continue
		}
		slog.Info("imported library from anvil.toml", "library", library.Name, "flow", library.Flow)
	}
	for _, skipped := range result.Skipped {
		slog.Warn("anvil.toml entry not imported", "reason", skipped)
	}
	// A fresh installation has nothing to import; the store only notes that
	// the question is settled, and there is nothing to tell the operator.
	if len(result.Libraries)+len(result.Flows)+len(result.Arrs)+len(result.Skipped) == 0 {
		return nil
	}
	slog.Info("anvil.toml import finished; libraries, flows, and arr connections are now managed in the web UI",
		"libraries", len(result.Libraries), "flows", len(result.Flows), "arrs", len(result.Arrs))
	return nil
}

// legacySectionNouns says what each legacy table of anvil.toml held.
var legacySectionNouns = map[string]string{
	"arrs":      "arr connections",
	"libraries": "libraries",
	"profiles":  "profiles became flows, which",
}

// warnLegacySections says, on every start and reload, that the legacy tables
// still in anvil.toml have no effect. Editing them and wondering why nothing
// changes is the mistake this is here to prevent.
func warnLegacySections(cfg config.Config) {
	for _, section := range cfg.Legacy.Sections {
		slog.Warn(fmt.Sprintf("[%s] in anvil.toml is ignored; %s are managed in the web UI", section, legacySectionNouns[section]), "section", section)
	}
}
