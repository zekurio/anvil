package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	charmlog "charm.land/log/v2"
	"github.com/zekurio/anvil/internal/textout"
	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/controlapi"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/metadata"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/scheduler"
	"github.com/zekurio/anvil/pkg/staging"
	"github.com/zekurio/anvil/pkg/store"
	"github.com/zekurio/anvil/pkg/worker"
)

// anvild owns the daemon and the two things that are intrinsically local and
// read-only: validating a config file and planning against it. Everything that
// touches live state moved to anvilctl, which asks the running daemon, because
// a second process writing the daemon's database is a data-safety problem, not
// a convenience.
const (
	commandRun         = "run"
	commandCheckConfig = "check-config"
	commandPreflight   = "preflight"
	commandHelp        = "help"
)

// movedCommands maps every operator command anvild used to run directly against
// SQLite to its anvilctl replacement. They are still recognized so an operator
// who types the old form is told exactly what to run, instead of getting
// "unknown command" or, far worse, a second writer on a live database.
var movedCommands = map[string]string{
	"scan":             "anvilctl library scan [--library NAME]",
	"jobs":             "anvilctl job list [--library NAME] [--state STATE,...]",
	"stats":            "anvilctl library stats [--library NAME]",
	"inspect":          "anvilctl job show JOB_ID_OR_SLUG",
	"retry":            "anvilctl job retry JOB_ID_OR_SLUG... | anvilctl job retry --failed",
	"recover":          "anvilctl job recover",
	"cleanup-staging":  "anvilctl staging cleanup [--older-than DURATION] [--dry-run]",
	"backup":           "anvilctl store backup DESTINATION",
	"prune-jobs":       "anvilctl job prune [--library NAME] [--state STATE,...] [--apply]",
	"force-occurrence": "anvilctl occurrence force --library NAME RELATIVE_PATH",
}

var activeLogLevel slog.LevelVar

type options struct {
	command         string
	configPath      string
	daemonMode      bool
	checkConfig     bool
	shutdownPolicy  string
	shutdownTimeout string
	libraryName     string
	preflightLimit  int
	jsonOutput      bool
}

type runtimeConfig struct {
	mu  sync.RWMutex
	cfg config.Config
}

func newRuntimeConfig(cfg config.Config) *runtimeConfig {
	return &runtimeConfig{cfg: cfg}
}

func (r *runtimeConfig) Get() config.Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

func (r *runtimeConfig) Set(cfg config.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("anvild failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	return runWithContext(context.Background(), args)
}

func runWithContext(ctx context.Context, args []string) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	if opts.command == commandHelp {
		return writeUsage(os.Stdout)
	}

	cfg, err := loadRuntimeConfig(opts.configPath, opts)
	if err != nil {
		return err
	}
	if err := configureLogging(cfg.Daemon.LogLevel); err != nil {
		return err
	}

	return dispatchCommand(ctx, cfg, opts)
}

func dispatchCommand(ctx context.Context, cfg config.Config, opts options) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	switch opts.command {
	case commandRun:
		return runDaemon(ctx, cfg, opts)
	case commandCheckConfig:
		return runCheckConfig(cfg, opts)
	case commandPreflight:
		return runPreflightCommand(ctx, cfg, opts)
	default:
		return fmt.Errorf("unknown command %q", opts.command)
	}
}

// movedCommandError explains where a command went. It is an error rather than a
// silent forward so nothing keeps depending on anvild for live state.
func movedCommandError(command string, replacement string) error {
	return fmt.Errorf("%q moved to the control client, because the running daemon owns the database: use %q", command, replacement)
}

func parseOptions(args []string) (options, error) {
	opts := options{command: commandRun}

	globals := flag.NewFlagSet("anvild", flag.ContinueOnError)
	globals.SetOutput(os.Stderr)
	addGlobalFlags(globals, &opts)
	globals.BoolVar(&opts.checkConfig, "check-config", false, "load and validate config, then exit")
	if err := globals.Parse(args); err != nil {
		return options{}, err
	}
	remaining := globals.Args()
	if opts.checkConfig {
		if len(remaining) > 0 {
			return options{}, fmt.Errorf("--check-config does not accept arguments: %v", remaining)
		}
		opts.command = commandCheckConfig
		return opts, nil
	}
	if len(remaining) == 0 {
		return opts, nil
	}
	if replacement, moved := movedCommands[remaining[0]]; moved {
		return options{}, movedCommandError(remaining[0], replacement)
	}
	if !isCommand(remaining[0]) {
		return options{}, fmt.Errorf("unexpected arguments: %v", remaining)
	}
	opts.command = remaining[0]
	return parseCommandOptions(opts, remaining[1:])
}

func parseCommandOptions(opts options, args []string) (options, error) {
	flags := flag.NewFlagSet("anvild "+opts.command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	addConfigFlag(flags, &opts)

	switch opts.command {
	case commandRun:
		flags.BoolVar(&opts.daemonMode, "daemon", opts.daemonMode, "run in daemon mode")
		addShutdownFlags(flags, &opts)
	case commandCheckConfig:
	case commandPreflight:
		flags.StringVar(&opts.libraryName, "library", "", "preflight one configured library")
		flags.IntVar(&opts.preflightLimit, "limit", 0, "maximum candidates to show; 0 means no limit")
		flags.BoolVar(&opts.jsonOutput, "json", false, "write JSON output")
	case commandHelp:
	default:
		return options{}, fmt.Errorf("unknown command %q", opts.command)
	}
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}

	if opts.command == commandPreflight && opts.preflightLimit < 0 {
		return options{}, errors.New("preflight --limit must be non-negative")
	}
	if flags.NArg() > 0 {
		return options{}, fmt.Errorf("%s does not accept arguments: %v", opts.command, flags.Args())
	}
	return opts, nil
}

func addGlobalFlags(flags *flag.FlagSet, opts *options) {
	addConfigFlag(flags, opts)
	flags.BoolVar(&opts.daemonMode, "daemon", false, "run in daemon mode")
	addShutdownFlags(flags, opts)
}

func addConfigFlag(flags *flag.FlagSet, opts *options) {
	flags.StringVar(&opts.configPath, "config", opts.configPath, "path to TOML config file")
}

func addShutdownFlags(flags *flag.FlagSet, opts *options) {
	flags.StringVar(&opts.shutdownPolicy, "shutdown-policy", opts.shutdownPolicy, "override shutdown policy: drain or cancel")
	flags.StringVar(&opts.shutdownTimeout, "shutdown-timeout", opts.shutdownTimeout, "override maximum graceful shutdown wait; 0s waits indefinitely")
}

type shutdownRequest struct {
	signal os.Signal
	err    error
}

func runDaemon(ctx context.Context, cfg config.Config, opts options) error {
	startedAt := time.Now().UTC()
	runtimeCfg := newRuntimeConfig(cfg)
	serviceCtx, stopServices, workerCtx, stopWorkers := daemonContexts(ctx)
	defer stopServices()
	defer stopWorkers()

	shutdownSignals := make(chan os.Signal, 2)
	reloadSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, os.Interrupt, syscall.SIGTERM)
	signal.Notify(reloadSignals, syscall.SIGHUP)
	defer signal.Stop(shutdownSignals)
	defer signal.Stop(reloadSignals)
	shutdown := make(chan shutdownRequest, 1)
	stopSignalWatcher := make(chan struct{})
	defer close(stopSignalWatcher)
	go func() {
		var request shutdownRequest
		select {
		case sig := <-shutdownSignals:
			request.signal = sig
		case <-ctx.Done():
			request.err = ctx.Err()
		case <-stopSignalWatcher:
			return
		}
		shutdown <- request
		stopServices()
	}()

	mode := "foreground"
	if opts.daemonMode {
		mode = "daemon"
	}

	// Ownership is claimed before anything else, and the control socket is
	// claimed before any start-up side effect. A second daemon on the same
	// store used to recover stale jobs and sweep staging directories out from
	// under the running one, and only then discover the live socket and exit.
	ownership, err := acquireDaemonOwnership(cfg.Daemon.StorePath)
	if err != nil {
		return err
	}
	defer func() {
		if err := ownership.release(); err != nil {
			slog.Error("release daemon ownership", "error", err)
		}
	}()
	listener, releaseSocket, err := controlapi.ListenUnix(cfg.Daemon.ControlSocket)
	if err != nil {
		return err
	}
	defer func() {
		if err := releaseSocket(); err != nil {
			slog.Error("clean up control socket", "error", err)
		}
	}()

	state, err := openStore(serviceCtx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)

	recovered, err := state.RecoverStaleJobs(serviceCtx, cfg.Daemon.MaxAttempts, time.Now())
	if err != nil {
		return err
	}

	runConfiguredStagingCleanup(serviceCtx, cfg, state)

	slog.Info("starting anvild", "mode", mode, "config", configPathLabel(opts.configPath), "temp_dir", cfg.Daemon.TempDir, "store", cfg.Daemon.StorePath, "control_socket", cfg.Daemon.ControlSocket, "workers", cfg.Daemon.WorkerCount, "threads", cfg.Daemon.TotalThreads, "filesystem_event_debounce", cfg.FilesystemEventDebounce(), "shutdown_policy", cfg.Daemon.ShutdownPolicy, "shutdown_timeout", cfg.Daemon.ShutdownTimeout, "log_level", cfg.Daemon.LogLevel, "recovered_jobs", recovered)
	logConfiguredWork(cfg)

	var wg sync.WaitGroup
	var plannerRef atomic.Pointer[scheduler.Scheduler]
	control := startControlService(serviceCtx, &wg, listener, controlServiceDeps{
		store:     state,
		config:    runtimeCfg.Get,
		startedAt: startedAt,
		activeWorkers: func() int {
			planner := plannerRef.Load()
			if planner == nil {
				return 0
			}
			return planner.ActiveCount()
		},
		cancelJob: func(jobID domain.JobID) bool {
			planner := plannerRef.Load()
			if planner == nil {
				return false
			}
			return planner.CancelJob(jobID)
		},
	})
	startReloadLoop(serviceCtx, &wg, opts, runtimeCfg, reloadSignals)
	runInitialScan(serviceCtx, runtimeCfg.Get(), state)
	startRecoveryLoop(serviceCtx, &wg, runtimeCfg.Get, state)
	planner := startSchedulerLoop(serviceCtx, workerCtx, &wg, runtimeCfg.Get, state)
	plannerRef.Store(planner)
	startScannerLoop(serviceCtx, &wg, runtimeCfg.Get, state, planner.ActiveCount)

	var request shutdownRequest
	select {
	case request = <-shutdown:
	case err, ok := <-control:
		if !ok || err == nil {
			// The control service only stops cleanly because the service
			// context was canceled, and whoever canceled it has already queued
			// the real shutdown reason. Reporting the stop itself would replace
			// that reason with a nil error.
			request = <-shutdown
			break
		}
		request.err = fmt.Errorf("control service stopped: %w", err)
		stopServices()
	}
	shutdownCfg := runtimeCfg.Get()
	if request.err != nil {
		slog.Info("stopping anvild", "reason", request.err, "policy", shutdownCfg.Daemon.ShutdownPolicy)
	} else {
		slog.Info("stopping anvild", "signal", request.signal.String(), "policy", shutdownCfg.Daemon.ShutdownPolicy)
	}
	if shutdownCfg.Daemon.ShutdownPolicy == "cancel" {
		stopWorkers()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
		planner.Wait()
	}()

	if err := waitForShutdown(done, shutdownSignals, shutdownCfg.ShutdownTimeout(), stopWorkers); err != nil {
		return err
	}
	return nil
}

func daemonContexts(ctx context.Context) (context.Context, context.CancelFunc, context.Context, context.CancelFunc) {
	serviceCtx, stopServices := context.WithCancel(ctx)
	workerCtx, stopWorkers := context.WithCancel(context.WithoutCancel(ctx))
	return serviceCtx, stopServices, workerCtx, stopWorkers
}

func waitForShutdown(done <-chan struct{}, signals <-chan os.Signal, timeout time.Duration, stopWorkers context.CancelFunc) error {
	var timeoutC <-chan time.Time
	var timer *time.Timer
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timeoutC = timer.C
		defer timer.Stop()
	}

	workersCanceled := false
	for {
		select {
		case <-done:
			slog.Info("anvild stopped")
			return nil
		case sig := <-signals:
			if !workersCanceled {
				slog.Warn("forcing active workers to cancel after additional shutdown signal", "signal", sig.String())
				stopWorkers()
				workersCanceled = true
			}
		case <-timeoutC:
			if !workersCanceled {
				slog.Warn("shutdown timeout elapsed; canceling active workers")
				stopWorkers()
				workersCanceled = true
			}
			timeoutC = nil
		}
	}
}

func runCheckConfig(cfg config.Config, opts options) error {
	slog.Info("config ok", "config", configPathLabel(opts.configPath), "libraries", len(cfg.Libraries), "flows", len(cfg.Flows), "profiles", len(cfg.Profiles), "control_socket", cfg.Daemon.ControlSocket, "log_level", cfg.Daemon.LogLevel)
	return nil
}

func openStore(ctx context.Context, cfg config.Config) (*store.SQLiteStore, error) {
	return store.Open(ctx, cfg.Daemon.StorePath)
}

func closeStore(state *store.SQLiteStore) {
	if err := state.Close(); err != nil {
		slog.Error("close store", "error", err)
	}
}

func logConfiguredWork(cfg config.Config) {
	if len(cfg.Libraries) == 0 {
		slog.Info("no libraries configured; scanner and scheduler will stay idle")
		return
	}

	for name, library := range cfg.Libraries {
		slog.Info("library configured", "name", name, "kind", library.Kind, "path", library.Path, "flow", library.Flow, "profile", library.Profile, "scan_interval", cfg.ScanIntervalForLibrary(domain.LibraryName(name)))
	}
	slog.Info("scanner, scheduler, worker, and built-in media pipeline are enabled")
}

func runInitialScan(ctx context.Context, cfg config.Config, state *store.SQLiteStore) {
	result, err := scanner.Scanner{Store: state}.Scan(ctx, cfg)
	if err != nil {
		slog.Error("initial scan failed", "error", err)
		return
	}
	logScanComplete("initial scan complete", "", "", result)
}

func logScanComplete(message string, library string, reason string, result scanner.ScanResult) {
	args := []any{
		"libraries", result.Libraries,
		"sources", result.Sources,
		"assets", result.Assets,
		"enqueued_jobs", result.EnqueuedJobs,
		"existing_jobs", result.ExistingJobs,
		"skipped_ignored", result.SkippedIgnored,
		"skipped_unstable", result.SkippedUnstable,
	}
	if library != "" {
		args = append([]any{"library", library}, args...)
	}
	if reason != "" {
		args = append([]any{"reason", reason}, args...)
	}
	if !result.NextStableAt.IsZero() {
		args = append(args, "next_stable_at", result.NextStableAt)
	}
	slog.Info(message, args...)
}

func startReloadLoop(ctx context.Context, wg *sync.WaitGroup, opts options, runtimeCfg *runtimeConfig, reloadSignals <-chan os.Signal) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-reloadSignals:
				current := runtimeCfg.Get()
				next, err := loadRuntimeConfig(opts.configPath, opts)
				if err != nil {
					slog.Error("config reload failed", "error", err)
					continue
				}
				if err := validateReload(current, next); err != nil {
					slog.Error("config reload rejected", "error", err)
					continue
				}
				if err := configureLogging(next.Daemon.LogLevel); err != nil {
					slog.Error("config reload rejected", "error", err)
					continue
				}
				runtimeCfg.Set(next)
				slog.Info("config reloaded", "config", configPathLabel(opts.configPath), "libraries", len(next.Libraries), "flows", len(next.Flows), "profiles", len(next.Profiles), "workers", next.Daemon.WorkerCount, "threads", next.Daemon.TotalThreads, "filesystem_event_debounce", next.FilesystemEventDebounce(), "log_level", next.Daemon.LogLevel)
			}
		}
	}()
}

func configureLogging(logLevel string) error {
	if _, err := applyLogLevel(&activeLogLevel, logLevel); err != nil {
		return err
	}
	slog.SetDefault(slog.New(newCharmLogger(os.Stderr, activeLogLevel.Level())))
	return nil
}

func newCharmLogger(out io.Writer, level slog.Level) *charmlog.Logger {
	return charmlog.NewWithOptions(out, charmlog.Options{
		Level:           charmLogLevel(level),
		ReportTimestamp: true,
		TimeFormat:      "15:04:05",
		Formatter:       charmlog.TextFormatter,
	})
}

func charmLogLevel(level slog.Level) charmlog.Level {
	switch {
	case level < slog.LevelInfo:
		return charmlog.DebugLevel
	case level >= slog.LevelError:
		return charmlog.ErrorLevel
	case level >= slog.LevelWarn:
		return charmlog.WarnLevel
	default:
		return charmlog.InfoLevel
	}
}

func applyLogLevel(levelVar *slog.LevelVar, logLevel string) (string, error) {
	level, normalized, err := parseLogLevel(logLevel)
	if err != nil {
		return "", err
	}
	levelVar.Set(level)
	return normalized, nil
}

func parseLogLevel(logLevel string) (slog.Level, string, error) {
	normalized, ok := config.NormalizeLogLevel(logLevel)
	if !ok {
		return 0, "", fmt.Errorf("daemon.log_level %q is invalid (must be debug, info, warn, or error)", logLevel)
	}

	switch normalized {
	case "debug":
		return slog.LevelDebug, normalized, nil
	case "info":
		return slog.LevelInfo, normalized, nil
	case "warn":
		return slog.LevelWarn, normalized, nil
	case "error":
		return slog.LevelError, normalized, nil
	default:
		return 0, "", fmt.Errorf("daemon.log_level %q is invalid (must be debug, info, warn, or error)", logLevel)
	}
}

func startScannerLoop(ctx context.Context, wg *sync.WaitGroup, cfgProvider func() config.Config, state *store.SQLiteStore, activeWorkers func() int) {
	monitor := &scanner.Monitor{
		Scanner:        scanner.Scanner{Store: state},
		ConfigProvider: cfgProvider,
		OnScan: func(library config.LibraryConfig, reason string, result scanner.ScanResult, err error) {
			if err != nil {
				slog.Error("scan failed", "library", library.Name, "reason", reason, "error", err)
				return
			}
			logScanComplete("scan complete", library.Name, reason, result)
			observeQueueHealth(ctx, state, library.Name, reason, result, activeWorkers())
		},
		OnEventError: func(err error) {
			if !errors.Is(err, context.Canceled) {
				slog.Error("filesystem scanner stopped", "error", err)
			}
		},
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := monitor.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("scanner monitor stopped", "error", err)
		}
	}()
}

func startRecoveryLoop(ctx context.Context, wg *sync.WaitGroup, cfgProvider func() config.Config, state *store.SQLiteStore) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			cfg := cfgProvider()
			timer := time.NewTimer(cfg.SchedulerInterval())
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				cfg = cfgProvider()
				recovered, err := state.RecoverStaleJobs(ctx, cfg.Daemon.MaxAttempts, time.Now())
				if err != nil {
					slog.Error("stale job recovery failed", "error", err)
					continue
				}
				if recovered > 0 {
					slog.Info("stale jobs recovered", "count", recovered)
				}
			}
		}
	}()
}

func startSchedulerLoop(ctx context.Context, workerCtx context.Context, wg *sync.WaitGroup, cfgProvider func() config.Config, state *store.SQLiteStore) *scheduler.Scheduler {
	runner := worker.Runner{
		Store:            state,
		ConfigProvider:   cfgProvider,
		MetadataResolver: metadata.Resolver{},
		Pipeline:         worker.DefaultPipeline(cfgProvider().Daemon.TempDir, state),
		TempDir:          cfgProvider().Daemon.TempDir,
	}
	planner := &scheduler.Scheduler{
		Store:          state,
		Worker:         runner,
		ConfigProvider: cfgProvider,
		WorkerContext:  workerCtx,
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := planner.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("scheduler stopped", "error", err)
		}
	}()
	return planner
}

func loadRuntimeConfig(path string, opts options) (config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, err
	}
	if opts.shutdownPolicy != "" {
		cfg.Daemon.ShutdownPolicy = opts.shutdownPolicy
	}
	if opts.shutdownTimeout != "" {
		cfg.Daemon.ShutdownTimeout = opts.shutdownTimeout
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func validateReload(current config.Config, next config.Config) error {
	if current.Daemon.StorePath != next.Daemon.StorePath {
		return fmt.Errorf("daemon.store_path changes require restart: %q -> %q", current.Daemon.StorePath, next.Daemon.StorePath)
	}
	if current.Daemon.TempDir != next.Daemon.TempDir {
		return fmt.Errorf("daemon.temp_dir changes require restart: %q -> %q", current.Daemon.TempDir, next.Daemon.TempDir)
	}
	if current.Daemon.ControlSocket != next.Daemon.ControlSocket {
		return fmt.Errorf("daemon.control_socket changes require restart: %q -> %q", current.Daemon.ControlSocket, next.Daemon.ControlSocket)
	}
	return nil
}

// runConfiguredStagingCleanup sweeps leftovers from a previous run at start-up.
// It goes through the same protected sweep the control command uses, so the two
// can never disagree about which staging directories belong to work that is
// still alive: a crash can leave both the directory and the job that owns it
// behind, and recovery needs the staged artifact the publish journal names.
func runConfiguredStagingCleanup(ctx context.Context, cfg config.Config, state *store.SQLiteStore) {
	age := cfg.StagingCleanupAge()
	if age <= 0 {
		return
	}
	result, err := controlapi.SweepStaging(ctx, state, staging.Root(cfg.Daemon.TempDir), controlapi.StagingSweep{
		OlderThan: age,
		Now:       time.Now().UTC(),
	})
	if err != nil {
		slog.Error("staging cleanup skipped", "error", err)
		return
	}
	slog.Info("staging cleanup complete", "candidates", result.Candidates, "removed", result.Removed, "skipped", result.Skipped, "protected", result.Protected, "errors", len(result.Errors))
	for _, message := range result.Errors {
		slog.Error("staging cleanup error", "error", message)
	}
}

func isCommand(value string) bool {
	switch value {
	case commandRun, commandCheckConfig, commandPreflight, commandHelp:
		return true
	default:
		return false
	}
}

func writeUsage(out io.Writer) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Println(`Usage:
  anvild [--config PATH] [--daemon] [--shutdown-policy drain|cancel] [--shutdown-timeout DURATION]
  anvild run [--config PATH] [--daemon] [--shutdown-policy drain|cancel] [--shutdown-timeout DURATION]
  anvild check-config [--config PATH]
  anvild preflight [--config PATH] [--library NAME] [--limit N] [--json]

anvild is the service binary: it owns the config it is running, the SQLite
store, staging, and publication. check-config and preflight stay here because
both are local, read-only, and useful before a daemon exists.

Every live operation is asked of the running daemon with anvilctl:

  anvilctl status
  anvilctl job list|show|cancel|retry|prune|recover
  anvilctl library scan|stats
  anvilctl occurrence force --library NAME RELATIVE_PATH
  anvilctl staging cleanup
  anvilctl store backup DESTINATION

Legacy --check-config is still accepted.`)
	})
}

func configPathLabel(path string) string {
	if path == "" {
		return "<defaults>"
	}
	return path
}
