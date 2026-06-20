package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/zekurio/anvil/pkg/config"
)

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

	slog.Info("starting anvild", "mode", mode, "config", configPathLabel(opts.configPath), "temp_dir", cfg.Daemon.TempDir, "workers", cfg.Daemon.WorkerCount)
	logConfiguredWork(cfg)

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
		slog.Info("no libraries configured; scanning and scheduling are not implemented yet")
		return
	}

	for _, library := range cfg.Libraries {
		slog.Info("library configured", "name", library.Name, "path", library.Path, "flow", library.Flow, "profile", library.Profile)
	}
	slog.Info("scanning, scheduling, ab-av1, ffmpeg, SQLite, and replacement flows are not implemented yet")
}

func configPathLabel(path string) string {
	if path == "" {
		return "<defaults>"
	}
	return path
}
