package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/store"
)

const defaultMaxJobAttempts = 3

type options struct {
	configPath  string
	daemonMode  bool
	checkConfig bool
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

	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return err
	}

	if opts.checkConfig {
		slog.Info("config ok", "config", configPathLabel(opts.configPath), "libraries", len(cfg.Libraries), "flows", len(cfg.Flows), "profiles", len(cfg.Profiles))
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mode := "foreground"
	if opts.daemonMode {
		mode = "daemon"
	}

	state, err := store.Open(ctx, cfg.Daemon.StorePath)
	if err != nil {
		return err
	}
	defer func() {
		if err := state.Close(); err != nil {
			slog.Error("close store", "error", err)
		}
	}()

	recovered, err := state.RecoverStaleJobs(ctx, defaultMaxJobAttempts, time.Now())
	if err != nil {
		return err
	}

	slog.Info("starting anvild", "mode", mode, "config", configPathLabel(opts.configPath), "temp_dir", cfg.Daemon.TempDir, "store", cfg.Daemon.StorePath, "workers", cfg.Daemon.WorkerCount, "recovered_jobs", recovered)
	logConfiguredWork(cfg)
	runInitialScan(ctx, cfg, state)

	<-ctx.Done()

	slog.Info("stopping anvild", "reason", ctx.Err())
	return nil
}

func parseOptions(args []string) (options, error) {
	var opts options
	flags := flag.NewFlagSet("anvild", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&opts.configPath, "config", "", "path to TOML config file")
	flags.BoolVar(&opts.daemonMode, "daemon", false, "run in daemon mode")
	flags.BoolVar(&opts.checkConfig, "check-config", false, "load and validate config, then exit")

	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}

	return opts, nil
}

func logConfiguredWork(cfg config.Config) {
	if len(cfg.Libraries) == 0 {
		slog.Info("no libraries configured; scanner and scheduler will stay idle")
		return
	}

	for _, library := range cfg.Libraries {
		slog.Info("library configured", "name", library.Name, "kind", library.Kind, "path", library.Path, "flow", library.Flow, "profile", library.Profile)
	}
	slog.Info("scanning, scheduling, ab-av1, ffmpeg, and replacement flows are not implemented yet")
}

func runInitialScan(ctx context.Context, cfg config.Config, state *store.SQLiteStore) {
	result, err := scanner.Scanner{Store: state}.Scan(ctx, cfg)
	if err != nil {
		slog.Error("initial scan failed", "error", err)
		return
	}
	slog.Info("initial scan complete", "libraries", result.Libraries, "sources", result.Sources, "assets", result.Assets, "enqueued_jobs", result.EnqueuedJobs, "existing_jobs", result.ExistingJobs, "skipped_ignored", result.SkippedIgnored, "skipped_unstable", result.SkippedUnstable)
}

func configPathLabel(path string) string {
	if path == "" {
		return "<defaults>"
	}
	return path
}
