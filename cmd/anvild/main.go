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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	charmlog "charm.land/log/v2"
	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/metadata"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/scheduler"
	"github.com/zekurio/anvil/pkg/staging"
	"github.com/zekurio/anvil/pkg/store"
	"github.com/zekurio/anvil/pkg/worker"
)

const (
	commandRun         = "run"
	commandCheckConfig = "check-config"
	commandScan        = "scan"
	commandPreflight   = "preflight"
	commandJobs        = "jobs"
	commandStats       = "stats"
	commandInspect     = "inspect"
	commandRetry       = "retry"
	commandRecover     = "recover"
	commandCleanup     = "cleanup-staging"
	commandBackup      = "backup"
	commandPruneJobs   = "prune-jobs"
	commandForce       = "force-occurrence"
	commandHelp        = "help"
)

var activeLogLevel slog.LevelVar

type options struct {
	command         string
	configPath      string
	daemonMode      bool
	checkConfig     bool
	shutdownPolicy  string
	shutdownTimeout string
	libraryName     string
	jobStates       []domain.JobState
	jobStateFilter  string
	jobLimit        int
	preflightLimit  int
	jsonOutput      bool
	retryFailed     bool
	jobIDs          []domain.JobID
	jobRefs         []string
	cleanupOlder    string
	cleanupDryRun   bool
	backupPath      string
	pruneApply      bool
	forcePath       string
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
	case commandScan:
		return runScanCommand(ctx, cfg, opts)
	case commandPreflight:
		return runPreflightCommand(ctx, cfg, opts)
	case commandJobs:
		return runJobsCommand(ctx, cfg, opts)
	case commandStats:
		return runStatsCommand(ctx, cfg, opts)
	case commandInspect:
		return runInspectCommand(ctx, cfg, opts)
	case commandRetry:
		return runRetryCommand(ctx, cfg, opts)
	case commandRecover:
		return runRecoverCommand(ctx, cfg, opts)
	case commandCleanup:
		return runCleanupStagingCommand(ctx, cfg, opts)
	case commandBackup:
		return runBackupCommand(ctx, cfg, opts)
	case commandPruneJobs:
		return runPruneJobsCommand(ctx, cfg, opts)
	case commandForce:
		return runForceOccurrenceCommand(ctx, cfg, opts)
	default:
		return fmt.Errorf("unknown command %q", opts.command)
	}
}

func parseOptions(args []string) (options, error) {
	opts := options{
		command:  commandRun,
		jobLimit: 20,
	}

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
	case commandScan:
		flags.StringVar(&opts.libraryName, "library", "", "scan one configured library")
	case commandPreflight:
		flags.StringVar(&opts.libraryName, "library", "", "preflight one configured library")
		flags.IntVar(&opts.preflightLimit, "limit", 0, "maximum candidates to show; 0 means no limit")
		flags.BoolVar(&opts.jsonOutput, "json", false, "write JSON output")
	case commandJobs:
		flags.StringVar(&opts.libraryName, "library", "", "filter by library name")
		flags.StringVar(&opts.jobStateFilter, "state", "", "comma-separated job states to show")
		flags.IntVar(&opts.jobLimit, "limit", opts.jobLimit, "maximum jobs to show; 0 means no limit")
		flags.BoolVar(&opts.jsonOutput, "json", false, "write JSON output")
	case commandStats:
		flags.StringVar(&opts.libraryName, "library", "", "filter by library name")
		flags.BoolVar(&opts.jsonOutput, "json", false, "write JSON output")
	case commandInspect:
		flags.BoolVar(&opts.jsonOutput, "json", false, "write JSON output")
	case commandRetry:
		flags.BoolVar(&opts.retryFailed, "failed", false, "retry all failed jobs")
		flags.StringVar(&opts.libraryName, "library", "", "limit --failed to one library")
	case commandRecover:
	case commandCleanup:
		flags.StringVar(&opts.cleanupOlder, "older-than", "", "remove Anvil staging dirs older than this duration; defaults to daemon.staging_cleanup_age")
		flags.BoolVar(&opts.cleanupDryRun, "dry-run", false, "show cleanup candidates without deleting them")
	case commandBackup:
	case commandPruneJobs:
		flags.StringVar(&opts.libraryName, "library", "", "limit pruning to one library")
		flags.StringVar(&opts.jobStateFilter, "state", "", "comma-separated terminal job states; defaults to complete,failed,skipped")
		flags.BoolVar(&opts.pruneApply, "apply", false, "delete matching jobs; without this flag the command is a dry run")
	case commandForce:
		flags.StringVar(&opts.libraryName, "library", "", "configured library containing the target")
	case commandHelp:
	default:
		return options{}, fmt.Errorf("unknown command %q", opts.command)
	}
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}

	switch opts.command {
	case commandPreflight:
		if opts.preflightLimit < 0 {
			return options{}, errors.New("preflight --limit must be non-negative")
		}
		if flags.NArg() > 0 {
			return options{}, fmt.Errorf("preflight does not accept arguments: %v", flags.Args())
		}
	case commandJobs:
		states, err := parseJobStates(opts.jobStateFilter)
		if err != nil {
			return options{}, err
		}
		opts.jobStates = states
		if flags.NArg() > 0 {
			return options{}, fmt.Errorf("jobs does not accept arguments: %v", flags.Args())
		}
	case commandStats:
		if flags.NArg() > 0 {
			return options{}, fmt.Errorf("stats does not accept arguments: %v", flags.Args())
		}
	case commandInspect:
		ids, err := parseJobIDs(flags.Args())
		if err != nil {
			return options{}, err
		}
		if len(flags.Args()) != 1 {
			return options{}, errors.New("inspect requires exactly one job reference")
		}
		opts.jobIDs = ids
		opts.jobRefs = append([]string(nil), flags.Args()...)
	case commandRetry:
		ids, err := parseJobIDs(flags.Args())
		if err != nil {
			return options{}, err
		}
		opts.jobIDs = ids
		opts.jobRefs = append([]string(nil), flags.Args()...)
		if !opts.retryFailed && len(opts.jobRefs) == 0 {
			return options{}, errors.New("retry requires job references or --failed")
		}
		if opts.libraryName != "" && !opts.retryFailed {
			return options{}, errors.New("--library can only be used with retry --failed")
		}
	case commandBackup:
		if flags.NArg() != 1 {
			return options{}, errors.New("backup requires exactly one destination path")
		}
		opts.backupPath = flags.Arg(0)
	case commandPruneJobs:
		states, err := parseJobStates(opts.jobStateFilter)
		if err != nil {
			return options{}, err
		}
		for _, state := range states {
			if !state.Terminal() {
				return options{}, fmt.Errorf("prune-jobs state %q is not terminal", state)
			}
		}
		opts.jobStates = states
		if flags.NArg() > 0 {
			return options{}, fmt.Errorf("prune-jobs does not accept arguments: %v", flags.Args())
		}
	case commandForce:
		if strings.TrimSpace(opts.libraryName) == "" {
			return options{}, errors.New("force-occurrence requires --library")
		}
		if flags.NArg() != 1 {
			return options{}, errors.New("force-occurrence requires exactly one relative path")
		}
		opts.forcePath = flags.Arg(0)
	default:
		if flags.NArg() > 0 {
			return options{}, fmt.Errorf("%s does not accept arguments: %v", opts.command, flags.Args())
		}
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

	state, err := openStore(serviceCtx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)

	recovered, err := state.RecoverStaleJobs(serviceCtx, cfg.Daemon.MaxAttempts, time.Now())
	if err != nil {
		return err
	}

	runConfiguredStagingCleanup(cfg)

	slog.Info("starting anvild", "mode", mode, "config", configPathLabel(opts.configPath), "temp_dir", cfg.Daemon.TempDir, "store", cfg.Daemon.StorePath, "control_socket", cfg.Daemon.ControlSocket, "workers", cfg.Daemon.WorkerCount, "threads", cfg.Daemon.TotalThreads, "filesystem_event_debounce", cfg.FilesystemEventDebounce(), "shutdown_policy", cfg.Daemon.ShutdownPolicy, "shutdown_timeout", cfg.Daemon.ShutdownTimeout, "log_level", cfg.Daemon.LogLevel, "recovered_jobs", recovered)
	logConfiguredWork(cfg)

	var wg sync.WaitGroup
	var plannerRef atomic.Pointer[scheduler.Scheduler]
	control, err := startControlAPI(serviceCtx, &wg, runtimeCfg.Get, state, func() int {
		planner := plannerRef.Load()
		if planner == nil {
			return 0
		}
		return planner.ActiveCount()
	}, startedAt)
	if err != nil {
		return err
	}
	defer func() {
		if err := control.cleanup(); err != nil {
			slog.Error("clean up control API", "error", err)
		}
	}()
	startReloadLoop(serviceCtx, &wg, opts, runtimeCfg, reloadSignals)
	runInitialScan(serviceCtx, runtimeCfg.Get(), state)
	startRecoveryLoop(serviceCtx, &wg, runtimeCfg.Get, state)
	planner := startSchedulerLoop(serviceCtx, workerCtx, &wg, runtimeCfg.Get, state)
	plannerRef.Store(planner)
	startScannerLoop(serviceCtx, &wg, runtimeCfg.Get, state, planner.ActiveCount)

	var request shutdownRequest
	select {
	case request = <-shutdown:
	case err := <-control.errors:
		request.err = fmt.Errorf("control API stopped: %w", err)
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

func runScanCommand(ctx context.Context, cfg config.Config, opts options) error {
	state, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)

	var result scanner.ScanResult
	if opts.libraryName != "" {
		library, ok := cfg.Libraries[opts.libraryName]
		if !ok {
			return fmt.Errorf("library %q not found", opts.libraryName)
		}
		library.Name = opts.libraryName
		result, err = (scanner.Scanner{Store: state}).ScanLibrary(ctx, library)
	} else {
		result, err = (scanner.Scanner{Store: state}).Scan(ctx, cfg)
	}
	if err != nil {
		return err
	}
	return writeScanResult(os.Stdout, result)
}

func runJobsCommand(ctx context.Context, cfg config.Config, opts options) error {
	state, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)

	jobs, err := state.ListJobs(ctx, store.JobListFilter{
		LibraryName: domain.LibraryName(opts.libraryName),
		States:      opts.jobStates,
		Limit:       opts.jobLimit,
	})
	if err != nil {
		return err
	}
	if opts.jsonOutput {
		return writeIndentedJSON(os.Stdout, jobs)
	}
	return writeJobs(os.Stdout, jobs)
}

func runStatsCommand(ctx context.Context, cfg config.Config, opts options) error {
	state, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)

	stats, err := state.ListLibraryStats(ctx, store.LibraryStatsFilter{
		LibraryName: domain.LibraryName(opts.libraryName),
	})
	if err != nil {
		return err
	}
	if opts.jsonOutput {
		return writeIndentedJSON(os.Stdout, stats)
	}
	return writeLibraryStats(os.Stdout, stats)
}

func runRetryCommand(ctx context.Context, cfg config.Config, opts options) error {
	state, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)

	now := time.Now().UTC()
	w := newOutputWriter(os.Stdout)
	if opts.retryFailed {
		count, err := state.RetryFailedJobs(ctx, domain.LibraryName(opts.libraryName), now)
		if err != nil {
			return err
		}
		w.printf("retried_failed_jobs=%d\n", count)
		if w.err != nil {
			return fmt.Errorf("write output: %w", w.err)
		}
	}
	for _, reference := range opts.jobRefs {
		resolved, err := state.ResolveJobReference(ctx, reference)
		if err != nil {
			return fmt.Errorf("resolve job %q: %w", reference, err)
		}
		job, err := state.RetryJob(ctx, resolved.ID, now)
		if err != nil {
			return err
		}
		w.printf("job=%s id=%d state=%s\n", job.Label(), job.ID, job.State)
		if w.err != nil {
			return fmt.Errorf("write output: %w", w.err)
		}
	}
	return nil
}

func runRecoverCommand(ctx context.Context, cfg config.Config, opts options) error {
	state, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)

	recovered, err := state.RecoverStaleJobs(ctx, cfg.Daemon.MaxAttempts, time.Now())
	if err != nil {
		return err
	}
	return writeOutput(os.Stdout, func(w *outputWriter) {
		w.printf("recovered_jobs=%d\n", recovered)
	})
}

func runCleanupStagingCommand(ctx context.Context, cfg config.Config, opts options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	age := cfg.StagingCleanupAge()
	if opts.cleanupOlder != "" {
		parsed, err := time.ParseDuration(opts.cleanupOlder)
		if err != nil {
			return fmt.Errorf("parse --older-than: %w", err)
		}
		age = parsed
	}
	if age < 0 {
		return errors.New("staging cleanup age must be non-negative")
	}
	result, err := staging.Manager{Root: stagingRoot(cfg)}.CleanupStale(age, time.Now().UTC(), opts.cleanupDryRun)
	if err != nil {
		return err
	}
	if err := writeCleanupResult(os.Stdout, os.Stderr, result, opts.cleanupDryRun); err != nil {
		return err
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("staging cleanup completed with %d errors", len(result.Errors))
	}
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

func runConfiguredStagingCleanup(cfg config.Config) {
	age := cfg.StagingCleanupAge()
	if age <= 0 {
		return
	}
	result, err := staging.Manager{Root: stagingRoot(cfg)}.CleanupStale(age, time.Now().UTC(), false)
	if err != nil {
		slog.Error("staging cleanup failed", "error", err)
		return
	}
	slog.Info("staging cleanup complete", "candidates", result.Candidates, "removed", result.Removed, "skipped", result.Skipped, "errors", len(result.Errors))
	for _, message := range result.Errors {
		slog.Error("staging cleanup error", "error", message)
	}
}

func writeScanResult(out io.Writer, result scanner.ScanResult) error {
	return writeOutput(out, func(w *outputWriter) {
		w.printf("libraries=%d sources=%d assets=%d enqueued_jobs=%d existing_jobs=%d skipped_ignored=%d skipped_unstable=%d",
			result.Libraries,
			result.Sources,
			result.Assets,
			result.EnqueuedJobs,
			result.ExistingJobs,
			result.SkippedIgnored,
			result.SkippedUnstable,
		)
		if !result.NextStableAt.IsZero() {
			w.printf(" next_stable_at=%s", result.NextStableAt.Format(time.RFC3339))
		}
		w.println()
	})
}

func writeCleanupResult(out io.Writer, errOut io.Writer, result staging.CleanupStaleResult, dryRun bool) error {
	if err := writeOutput(out, func(w *outputWriter) {
		w.printf("dry_run=%t candidates=%d removed=%d skipped=%d errors=%d\n",
			dryRun,
			result.Candidates,
			result.Removed,
			result.Skipped,
			len(result.Errors),
		)
	}); err != nil {
		return err
	}
	return writeOutput(errOut, func(w *outputWriter) {
		for _, message := range result.Errors {
			w.printf("cleanup_error=%q\n", message)
		}
	})
}

func stagingRoot(cfg config.Config) string {
	return filepath.Join(cfg.Daemon.TempDir, "staging")
}

func writeJobs(out io.Writer, jobs []store.JobSummary) error {
	return writeTable(out, func(w *outputWriter) {
		w.println("JOB\tID\tSTATE\tLIBRARY\tATTEMPTS\tUPDATED\tPATH\tERROR")
		for _, summary := range jobs {
			w.printf("%s\t%d\t%s\t%s\t%d\t%s\t%s\t%s\n",
				summary.Job.Label(),
				summary.Job.ID,
				summary.Job.State,
				summary.Job.LibraryName,
				summary.Job.AttemptCount,
				summary.Job.UpdatedAt.Format(time.RFC3339),
				jobPath(summary),
				summary.Job.LastError,
			)
		}
	})
}

func writeLibraryStats(out io.Writer, stats []store.LibraryStats) error {
	return writeTable(out, func(w *outputWriter) {
		w.println("LIBRARY\tJOBS\tBEFORE\tAFTER\tSAVED\tSAVED%")
		for _, stat := range stats {
			w.printf("%s\t%d\t%s\t%s\t%s\t%s\n",
				stat.LibraryName,
				stat.Jobs,
				formatBytes(stat.InputSizeBytes),
				formatBytes(stat.OutputSizeBytes),
				formatBytes(stat.SavedBytes),
				formatPercent(stat.SavedPercent),
			)
		}
	})
}

func jobPath(summary store.JobSummary) string {
	if summary.AssetPath == "" || summary.AssetPath == summary.SourcePath {
		return summary.SourcePath
	}
	source := strings.Trim(summary.SourcePath, "/")
	asset := strings.Trim(summary.AssetPath, "/")
	if source == "" {
		return asset
	}
	if asset == "" {
		return source
	}
	return source + "/" + asset
}

func formatBytes(value int64) string {
	sign := ""
	size := float64(value)
	if value < 0 {
		sign = "-"
		size = -size
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	unit := 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%s%d %s", sign, int64(size), units[unit])
	}
	return fmt.Sprintf("%s%.1f %s", sign, size, units[unit])
}

func formatPercent(value float64) string {
	return fmt.Sprintf("%.1f%%", value)
}

func parseJobStates(value string) ([]domain.JobState, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	states := make([]domain.JobState, 0, len(parts))
	for _, part := range parts {
		state := domain.JobState(strings.TrimSpace(part))
		if state == "" {
			continue
		}
		if !validJobState(state) {
			return nil, fmt.Errorf("unknown job state %q", state)
		}
		states = append(states, state)
	}
	return states, nil
}

func validJobState(state domain.JobState) bool {
	switch state {
	case domain.JobStatePending, domain.JobStateLeased, domain.JobStateRunning,
		domain.JobStateValidating, domain.JobStateReplacing, domain.JobStateComplete,
		domain.JobStateFailed, domain.JobStateRetrying, domain.JobStateSkipped:
		return true
	default:
		return false
	}
}

func parseJobIDs(args []string) ([]domain.JobID, error) {
	ids := make([]domain.JobID, 0, len(args))
	for _, arg := range args {
		value, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			if !validJobSlug(arg) {
				return nil, fmt.Errorf("invalid job reference %q", arg)
			}
			continue
		}
		if value <= 0 {
			return nil, fmt.Errorf("invalid job reference %q", arg)
		}
		ids = append(ids, domain.JobID(value))
	}
	return ids, nil
}

func validJobSlug(value string) bool {
	parts := strings.Split(value, "-")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < 'a' || r > 'z' {
				return false
			}
		}
	}
	return true
}

func isCommand(value string) bool {
	switch value {
	case commandRun, commandCheckConfig, commandScan, commandPreflight, commandJobs, commandStats, commandInspect, commandRetry, commandRecover, commandCleanup, commandBackup, commandPruneJobs, commandForce, commandHelp:
		return true
	default:
		return false
	}
}

func writeUsage(out io.Writer) error {
	return writeOutput(out, func(w *outputWriter) {
		w.println(`Usage:
  anvild [--config PATH] [--daemon] [--shutdown-policy drain|cancel] [--shutdown-timeout DURATION]
  anvild run [--config PATH] [--daemon] [--shutdown-policy drain|cancel] [--shutdown-timeout DURATION]
  anvild check-config [--config PATH]
  anvild scan [--config PATH] [--library NAME]
  anvild preflight [--config PATH] [--library NAME] [--limit N] [--json]
  anvild jobs [--config PATH] [--library NAME] [--state pending,failed] [--limit N] [--json]
  anvild stats [--config PATH] [--library NAME] [--json]
  anvild inspect [--config PATH] [--json] JOB_ID_OR_SLUG
  anvild retry [--config PATH] JOB_ID_OR_SLUG...
  anvild retry [--config PATH] --failed [--library NAME]
  anvild recover [--config PATH]
  anvild cleanup-staging [--config PATH] [--older-than DURATION] [--dry-run]
  anvild backup [--config PATH] DESTINATION
  anvild prune-jobs [--config PATH] [--library NAME] [--state complete,failed,skipped] [--apply]
  anvild force-occurrence [--config PATH] --library NAME RELATIVE_PATH

Legacy --check-config is still accepted.`)
	})
}

func configPathLabel(path string) string {
	if path == "" {
		return "<defaults>"
	}
	return path
}
