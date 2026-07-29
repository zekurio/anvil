package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/config"
)

func TestParseOptionsKeepsLegacyRunFlags(t *testing.T) {
	opts, err := parseOptions([]string{"--config", "anvil.toml", "--daemon", "--shutdown-policy", "cancel", "--shutdown-timeout", "10s"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandRun {
		t.Fatalf("command = %q, want run", opts.command)
	}
	if opts.configPath != "anvil.toml" {
		t.Fatalf("config path = %q, want anvil.toml", opts.configPath)
	}
	if !opts.daemonMode {
		t.Fatal("daemon mode = false, want true")
	}
	if opts.shutdownPolicy != "cancel" {
		t.Fatalf("shutdown policy = %q, want cancel", opts.shutdownPolicy)
	}
	if opts.shutdownTimeout != "10s" {
		t.Fatalf("shutdown timeout = %q, want 10s", opts.shutdownTimeout)
	}
}

func TestParseOptionsSupportsLegacyCheckConfig(t *testing.T) {
	opts, err := parseOptions([]string{"--config", "anvil.toml", "--check-config"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandCheckConfig {
		t.Fatalf("command = %q, want check-config", opts.command)
	}
}

func TestDaemonWorkerContextOutlivesParentContext(t *testing.T) {
	parentCtx, stopParent := context.WithCancel(context.Background())
	serviceCtx, stopServices, workerCtx, stopWorkers := daemonContexts(parentCtx)
	defer stopServices()
	defer stopWorkers()

	stopParent()
	if err := serviceCtx.Err(); err != context.Canceled {
		t.Fatalf("service context error = %v, want context canceled", err)
	}
	if err := workerCtx.Err(); err != nil {
		t.Fatalf("worker context error = %v, want nil before explicit worker stop", err)
	}

	stopWorkers()
	if err := workerCtx.Err(); err != context.Canceled {
		t.Fatalf("worker context error after stop = %v, want context canceled", err)
	}
}

// TestParseOptionsReportsMovedOperatorCommands pins the migration: the old
// direct-SQLite subcommands must fail with the anvilctl replacement rather than
// quietly running a second writer against a live daemon's database.
func TestParseOptionsReportsMovedOperatorCommands(t *testing.T) {
	for _, args := range [][]string{
		{"jobs", "--library", "movies"},
		{"scan"},
		{"stats"},
		{"inspect", "42"},
		{"retry", "--failed"},
		{"recover"},
		{"cleanup-staging", "--dry-run"},
		{"backup", "/tmp/anvil.db"},
		{"prune-jobs", "--apply"},
		{"force-occurrence", "--library", "movies", "Movie.mkv"},
	} {
		t.Run(args[0], func(t *testing.T) {
			_, err := parseOptions(args)
			if err == nil {
				t.Fatalf("parseOptions(%v) error = nil, want a migration error", args)
			}
			if !strings.Contains(err.Error(), "anvilctl") {
				t.Fatalf("parseOptions(%v) error = %v, want the anvilctl replacement", args, err)
			}
		})
	}
}

func TestCharmLoggerFormatsHumanReadableLine(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(newCharmLogger(&buf, slog.LevelInfo))
	logger.Info("ffmpeg progress", "job", "pretty-pink-panther", "attempt", 2, "speed", "1.4x")
	line := buf.String()
	for _, want := range []string{"INFO", "ffmpeg progress", "job=pretty-pink-panther", "attempt=2", "speed=1.4x"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line %q missing %q", line, want)
		}
	}
}

func TestParseOptionsParsesPreflightCommand(t *testing.T) {
	opts, err := parseOptions([]string{"preflight", "--config", "anvil.toml", "--library", "movies", "--limit", "10", "--json"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandPreflight {
		t.Fatalf("command = %q, want preflight", opts.command)
	}
	if opts.configPath != "anvil.toml" {
		t.Fatalf("config path = %q, want anvil.toml", opts.configPath)
	}
	if opts.libraryName != "movies" {
		t.Fatalf("library = %q, want movies", opts.libraryName)
	}
	if opts.preflightLimit != 10 {
		t.Fatalf("preflight limit = %d, want 10", opts.preflightLimit)
	}
	if !opts.jsonOutput {
		t.Fatal("json output = false, want true")
	}
}

func TestParseOptionsRejectsNegativePreflightLimit(t *testing.T) {
	if _, err := parseOptions([]string{"preflight", "--limit", "-1"}); err == nil {
		t.Fatal("parseOptions() error = nil, want negative limit rejection")
	}
}

func TestValidateReloadRejectsStoreTempDirAndControlSocketChanges(t *testing.T) {
	current := config.Default()
	next := current
	next.Daemon.StorePath = "/other/anvil.db"
	if err := validateReload(current, next); err == nil {
		t.Fatal("validateReload() error = nil, want store path rejection")
	}

	next = current
	next.Daemon.TempDir = "/other/tmp"
	if err := validateReload(current, next); err == nil {
		t.Fatal("validateReload() error = nil, want temp dir rejection")
	}

	next = current
	next.Daemon.ControlSocket = "/other/anvild.sock"
	if err := validateReload(current, next); err == nil {
		t.Fatal("validateReload() error = nil, want control socket rejection")
	}
}

func TestValidateReloadAllowsRuntimeKnobs(t *testing.T) {
	current := config.Default()
	next := current
	next.Daemon.WorkerCount = current.Daemon.WorkerCount + 1
	next.Daemon.TotalThreads = current.Daemon.TotalThreads + 2
	next.Daemon.ScanInterval = "1m"
	next.Daemon.LogLevel = "debug"
	if err := validateReload(current, next); err != nil {
		t.Fatalf("validateReload() error = %v", err)
	}
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantLevel slog.Level
		wantLabel string
		wantErr   bool
	}{
		{name: "debug", value: "debug", wantLevel: slog.LevelDebug, wantLabel: "debug"},
		{name: "info", value: "info", wantLevel: slog.LevelInfo, wantLabel: "info"},
		{name: "warn", value: "warn", wantLevel: slog.LevelWarn, wantLabel: "warn"},
		{name: "error", value: "error", wantLevel: slog.LevelError, wantLabel: "error"},
		{name: "trim and lower", value: " WARN ", wantLevel: slog.LevelWarn, wantLabel: "warn"},
		{name: "invalid", value: "trace", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, label, err := parseLogLevel(tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatal("parseLogLevel() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLogLevel() error = %v", err)
			}
			if level != tt.wantLevel {
				t.Fatalf("level = %v, want %v", level, tt.wantLevel)
			}
			if label != tt.wantLabel {
				t.Fatalf("label = %q, want %q", label, tt.wantLabel)
			}
		})
	}
}

func TestApplyLogLevelUpdatesLevelVar(t *testing.T) {
	var levelVar slog.LevelVar
	label, err := applyLogLevel(&levelVar, " DEBUG ")
	if err != nil {
		t.Fatalf("applyLogLevel() error = %v", err)
	}
	if label != "debug" {
		t.Fatalf("label = %q, want debug", label)
	}
	if got := levelVar.Level(); got != slog.LevelDebug {
		t.Fatalf("level = %v, want %v", got, slog.LevelDebug)
	}
}
