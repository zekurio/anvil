package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/ffmpeg"
	"github.com/zekurio/anvil/pkg/store"
)

// snapshotStore is what building the runtime snapshot reads.
type snapshotStore interface {
	DaemonSettings(context.Context) (config.DaemonOverrides, error)
	ListLibraries(context.Context) ([]store.Library, error)
	ListArrs(context.Context) ([]store.Arr, error)
}

// withStoredDaemonSettings returns loaded with the [daemon] values saved in
// the web UI merged in, for every key anvil.toml and the command line leave
// alone.
func withStoredDaemonSettings(ctx context.Context, loaded config.Config, state snapshotStore) (config.Config, error) {
	overrides, err := state.DaemonSettings(ctx)
	if err != nil {
		return config.Config{}, fmt.Errorf("load daemon settings: %w", err)
	}
	return loaded.WithDaemonOverrides(overrides)
}

// withStoreSnapshot returns loaded, the process settings of anvil.toml and
// the command line, with everything the store adds: the daemon settings saved
// in the web UI, the libraries, and the Arr connections.
func withStoreSnapshot(ctx context.Context, loaded config.Config, state snapshotStore) (config.Config, error) {
	cfg, err := withStoredDaemonSettings(ctx, loaded, state)
	if err != nil {
		return config.Config{}, err
	}
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
// settings of anvil.toml, the daemon settings saved in the web UI where the
// file leaves a key alone, and the libraries and Arr connections of the
// store. Every rebuild merges the same way, from the file as last loaded:
// SIGHUP loads the file again first, and a change made through the web API
// only re-reads the store. Both end in changed, which is how the scan
// monitor, the filesystem watcher, and the scheduler learn of the change
// without waiting for a tick.
type runtimeConfig struct {
	mu       sync.RWMutex
	cfg      config.Config
	encoders ffmpeg.EncoderCapabilities

	// Probe bookkeeping is serialized by refreshMu.
	probeEncoders func(context.Context, string) ffmpeg.EncoderCapabilities
	cancelProbe   context.CancelFunc
	probeWorkers  sync.WaitGroup

	// refreshMu serializes rebuilds, so an older read can never overwrite
	// the result of a newer one. It also guards loaded.
	refreshMu sync.Mutex
	// loaded is anvil.toml with the command-line options as last loaded,
	// before anything of the store is merged in.
	loaded config.Config
	state  snapshotStore
	// logLevel follows daemon.log_level of every snapshot; nil leaves the
	// log level alone.
	logLevel *slog.LevelVar
	// reconfigured holds one coalescing wake-up channel per listener.
	reconfigured []chan struct{}
}

func newRuntimeConfig(state snapshotStore, logLevel *slog.LevelVar) *runtimeConfig {
	return &runtimeConfig{state: state, logLevel: logLevel}
}

func (r *runtimeConfig) Get() config.Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

// Reconfigured returns a channel that fires after the snapshot changed.
// Every caller gets its own, so no listener can swallow another's wake-up.
// It must be called before the loops that use it start.
func (r *runtimeConfig) Reconfigured() <-chan struct{} {
	listener := make(chan struct{}, 1)
	r.reconfigured = append(r.reconfigured, listener)
	return listener
}

// Refresh re-reads the store half of the snapshot: the daemon settings, the
// libraries, and the Arr connections. The web API calls it after every
// change it persists and before it answers, so the next request, scan, and
// lease already see the change.
func (r *runtimeConfig) Refresh(ctx context.Context) error {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	return r.rebuild(ctx, r.loaded)
}

// Reload takes the process settings of a freshly loaded anvil.toml and
// re-reads the store half as well. On an error the snapshot and the file it
// merges from stay as they were.
func (r *runtimeConfig) Reload(ctx context.Context, loaded config.Config) error {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if err := r.rebuild(ctx, loaded); err != nil {
		return err
	}
	r.loaded = loaded
	return nil
}

func (r *runtimeConfig) rebuild(ctx context.Context, loaded config.Config) error {
	next, err := withStoreSnapshot(ctx, loaded, r.state)
	if err != nil {
		return err
	}
	level, _, err := parseLogLevel(next.Daemon.LogLevel)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.cfg = next
	if r.probeEncoders != nil && (r.encoders.State == "" || r.encoders.RenderDevice != next.Daemon.RenderDevice) {
		r.startEncoderProbe(ctx, next.Daemon.RenderDevice)
	}
	r.mu.Unlock()
	if r.logLevel != nil {
		r.logLevel.Set(level)
	}
	r.changed()
	return nil
}

func (r *runtimeConfig) Encoders() ffmpeg.EncoderCapabilities {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.encoders
}

// startEncoderProbe runs under mu so the new render device and probing state
// become visible together.
func (r *runtimeConfig) startEncoderProbe(ctx context.Context, renderDevice string) {
	if r.cancelProbe != nil {
		r.cancelProbe()
	}
	probeCtx, cancel := context.WithCancel(ctx)
	r.cancelProbe = cancel
	r.encoders = ffmpeg.EncoderCapabilities{
		State: ffmpeg.EncoderProbeProbing, RenderDevice: renderDevice, Encoders: []ffmpeg.EncoderCapability{},
	}
	r.probeWorkers.Go(func() {
		report := r.probeEncoders(probeCtx, renderDevice)
		r.mu.Lock()
		defer r.mu.Unlock()
		if probeCtx.Err() == nil {
			r.encoders = report
		}
	})
}

func (r *runtimeConfig) changed() {
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
	result, err := state.ImportLegacyConfig(ctx, cfg.Legacy, cfg.Daemon.TempDir, time.Now())
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
		if library.PathConflict != "" {
			// The TOML never compared directories. A library whose folders
			// overlap another's or temp_dir must not run as it is.
			slog.Warn("imported library from anvil.toml is paused because of where its folders are; move them in the web UI, then resume the library",
				"library", library.Name, "issue", library.PathConflict)
			continue
		}
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
