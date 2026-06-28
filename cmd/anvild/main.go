package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

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
	commandJobs        = "jobs"
	commandInspect     = "inspect"
	commandRetry       = "retry"
	commandRecover     = "recover"
	commandCleanup     = "cleanup-staging"
	commandHelp        = "help"
)

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
	jsonOutput      bool
	retryFailed     bool
	jobIDs          []domain.JobID
	cleanupOlder    string
	cleanupDryRun   bool
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
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	if opts.command == commandHelp {
		printUsage()
		return nil
	}

	cfg, err := loadRuntimeConfig(opts.configPath, opts)
	if err != nil {
		return err
	}

	switch opts.command {
	case commandRun:
		return runDaemon(cfg, opts)
	case commandCheckConfig:
		return runCheckConfig(cfg, opts)
	case commandScan:
		return runScanCommand(context.Background(), cfg, opts)
	case commandJobs:
		return runJobsCommand(context.Background(), cfg, opts)
	case commandInspect:
		return runInspectCommand(context.Background(), cfg, opts)
	case commandRetry:
		return runRetryCommand(context.Background(), cfg, opts)
	case commandRecover:
		return runRecoverCommand(context.Background(), cfg, opts)
	case commandCleanup:
		return runCleanupStagingCommand(context.Background(), cfg, opts)
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
	case commandJobs:
		flags.StringVar(&opts.libraryName, "library", "", "filter by library name")
		flags.StringVar(&opts.jobStateFilter, "state", "", "comma-separated job states to show")
		flags.IntVar(&opts.jobLimit, "limit", opts.jobLimit, "maximum jobs to show; 0 means no limit")
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
	case commandHelp:
	default:
		return options{}, fmt.Errorf("unknown command %q", opts.command)
	}
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}

	switch opts.command {
	case commandJobs:
		states, err := parseJobStates(opts.jobStateFilter)
		if err != nil {
			return options{}, err
		}
		opts.jobStates = states
		if flags.NArg() > 0 {
			return options{}, fmt.Errorf("jobs does not accept arguments: %v", flags.Args())
		}
	case commandInspect:
		ids, err := parseJobIDs(flags.Args())
		if err != nil {
			return options{}, err
		}
		if len(ids) != 1 {
			return options{}, errors.New("inspect requires exactly one job ID")
		}
		opts.jobIDs = ids
	case commandRetry:
		ids, err := parseJobIDs(flags.Args())
		if err != nil {
			return options{}, err
		}
		opts.jobIDs = ids
		if !opts.retryFailed && len(opts.jobIDs) == 0 {
			return options{}, errors.New("retry requires job IDs or --failed")
		}
		if opts.libraryName != "" && !opts.retryFailed {
			return options{}, errors.New("--library can only be used with retry --failed")
		}
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

func runDaemon(cfg config.Config, opts options) error {
	runtimeCfg := newRuntimeConfig(cfg)
	serviceCtx, stopServices := context.WithCancel(context.Background())
	defer stopServices()
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()

	shutdownSignals := make(chan os.Signal, 2)
	reloadSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, os.Interrupt, syscall.SIGTERM)
	signal.Notify(reloadSignals, syscall.SIGHUP)
	defer signal.Stop(shutdownSignals)
	defer signal.Stop(reloadSignals)
	firstSignal := make(chan os.Signal, 1)
	go func() {
		sig := <-shutdownSignals
		firstSignal <- sig
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

	slog.Info("starting anvild", "mode", mode, "config", configPathLabel(opts.configPath), "temp_dir", cfg.Daemon.TempDir, "store", cfg.Daemon.StorePath, "workers", cfg.Daemon.WorkerCount, "threads", cfg.Daemon.TotalThreads, "shutdown_policy", cfg.Daemon.ShutdownPolicy, "shutdown_timeout", cfg.Daemon.ShutdownTimeout, "recovered_jobs", recovered)
	logConfiguredWork(cfg)

	var wg sync.WaitGroup
	startReloadLoop(serviceCtx, &wg, opts, runtimeCfg, reloadSignals)
	runInitialScan(serviceCtx, runtimeCfg.Get(), state)
	startScannerLoop(serviceCtx, &wg, runtimeCfg.Get, state)
	startRecoveryLoop(serviceCtx, &wg, runtimeCfg.Get, state)
	planner := startSchedulerLoop(serviceCtx, workerCtx, &wg, runtimeCfg.Get, state)

	sig := <-firstSignal
	shutdownCfg := runtimeCfg.Get()
	slog.Info("stopping anvild", "signal", sig.String(), "policy", shutdownCfg.Daemon.ShutdownPolicy)
	if shutdownCfg.Daemon.ShutdownPolicy == "cancel" {
		stopWorkers()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
		planner.Wait()
	}()

	return waitForShutdown(done, shutdownSignals, shutdownCfg.ShutdownTimeout(), stopWorkers)
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
	slog.Info("config ok", "config", configPathLabel(opts.configPath), "libraries", len(cfg.Libraries), "flows", len(cfg.Flows), "profiles", len(cfg.Profiles))
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
	printScanResult(result)
	return nil
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
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(jobs)
	}
	printJobs(jobs)
	return nil
}

func runRetryCommand(ctx context.Context, cfg config.Config, opts options) error {
	state, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)

	now := time.Now().UTC()
	if opts.retryFailed {
		count, err := state.RetryFailedJobs(ctx, domain.LibraryName(opts.libraryName), now)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "retried_failed_jobs=%d\n", count)
	}
	for _, id := range opts.jobIDs {
		job, err := state.RetryJob(ctx, id, now)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "job_id=%d state=%s\n", job.ID, job.State)
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
	fmt.Fprintf(os.Stdout, "recovered_jobs=%d\n", recovered)
	return nil
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
	printCleanupResult(result, opts.cleanupDryRun)
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
		slog.Info("library configured", "name", name, "kind", library.Kind, "path", library.Path, "flow", library.Flow, "profile", library.Profile)
	}
	slog.Info("scanner, scheduler, worker, and built-in media pipeline are enabled")
}

func runInitialScan(ctx context.Context, cfg config.Config, state *store.SQLiteStore) {
	result, err := scanner.Scanner{Store: state}.Scan(ctx, cfg)
	if err != nil {
		slog.Error("initial scan failed", "error", err)
		return
	}
	slog.Info("initial scan complete", "libraries", result.Libraries, "sources", result.Sources, "assets", result.Assets, "enqueued_jobs", result.EnqueuedJobs, "existing_jobs", result.ExistingJobs, "skipped_ignored", result.SkippedIgnored, "skipped_unstable", result.SkippedUnstable)
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
				runtimeCfg.Set(next)
				slog.Info("config reloaded", "config", configPathLabel(opts.configPath), "libraries", len(next.Libraries), "flows", len(next.Flows), "profiles", len(next.Profiles), "workers", next.Daemon.WorkerCount, "threads", next.Daemon.TotalThreads)
			}
		}
	}()
}

func startScannerLoop(ctx context.Context, wg *sync.WaitGroup, cfgProvider func() config.Config, state *store.SQLiteStore) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			timer := time.NewTimer(cfgProvider().ScanInterval())
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				cfg := cfgProvider()
				result, err := scanner.Scanner{Store: state}.Scan(ctx, cfg)
				if err != nil {
					slog.Error("scan failed", "error", err)
					continue
				}
				slog.Info("scan complete", "libraries", result.Libraries, "sources", result.Sources, "assets", result.Assets, "enqueued_jobs", result.EnqueuedJobs, "existing_jobs", result.ExistingJobs, "skipped_ignored", result.SkippedIgnored, "skipped_unstable", result.SkippedUnstable)
			}
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
		Pipeline:         worker.DefaultPipeline(cfgProvider().Daemon.TempDir),
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

func printScanResult(result scanner.ScanResult) {
	fmt.Fprintf(os.Stdout, "libraries=%d sources=%d assets=%d enqueued_jobs=%d existing_jobs=%d skipped_ignored=%d skipped_unstable=%d\n",
		result.Libraries,
		result.Sources,
		result.Assets,
		result.EnqueuedJobs,
		result.ExistingJobs,
		result.SkippedIgnored,
		result.SkippedUnstable,
	)
}

func printCleanupResult(result staging.CleanupStaleResult, dryRun bool) {
	fmt.Fprintf(os.Stdout, "dry_run=%t candidates=%d removed=%d skipped=%d errors=%d\n",
		dryRun,
		result.Candidates,
		result.Removed,
		result.Skipped,
		len(result.Errors),
	)
	for _, message := range result.Errors {
		fmt.Fprintf(os.Stderr, "cleanup_error=%q\n", message)
	}
}

func stagingRoot(cfg config.Config) string {
	return filepath.Join(cfg.Daemon.TempDir, "staging")
}

func printJobs(jobs []store.JobSummary) {
	table := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tSTATE\tLIBRARY\tATTEMPTS\tUPDATED\tPATH\tERROR")
	for _, summary := range jobs {
		fmt.Fprintf(table, "%d\t%s\t%s\t%d\t%s\t%s\t%s\n",
			summary.Job.ID,
			summary.Job.State,
			summary.Job.LibraryName,
			summary.Job.AttemptCount,
			summary.Job.UpdatedAt.Format(time.RFC3339),
			jobPath(summary),
			summary.Job.LastError,
		)
	}
	_ = table.Flush()
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
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("invalid job id %q", arg)
		}
		ids = append(ids, domain.JobID(value))
	}
	return ids, nil
}

func isCommand(value string) bool {
	switch value {
	case commandRun, commandCheckConfig, commandScan, commandJobs, commandInspect, commandRetry, commandRecover, commandCleanup, commandHelp:
		return true
	default:
		return false
	}
}

func printUsage() {
	fmt.Fprintln(os.Stdout, `Usage:
  anvild [--config PATH] [--daemon] [--shutdown-policy drain|cancel] [--shutdown-timeout DURATION]
  anvild run [--config PATH] [--daemon] [--shutdown-policy drain|cancel] [--shutdown-timeout DURATION]
  anvild check-config [--config PATH]
  anvild scan [--config PATH] [--library NAME]
  anvild jobs [--config PATH] [--library NAME] [--state pending,failed] [--limit N] [--json]
  anvild inspect [--config PATH] [--json] JOB_ID
  anvild retry [--config PATH] JOB_ID...
  anvild retry [--config PATH] --failed [--library NAME]
  anvild recover [--config PATH]
  anvild cleanup-staging [--config PATH] [--older-than DURATION] [--dry-run]

Legacy --check-config is still accepted.`)
}

func configPathLabel(path string) string {
	if path == "" {
		return "<defaults>"
	}
	return path
}
