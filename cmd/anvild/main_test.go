package main

import (
	"testing"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
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

func TestParseOptionsParsesJobsCommand(t *testing.T) {
	opts, err := parseOptions([]string{"--config", "anvil.toml", "jobs", "--library", "movies", "--state", "pending,failed", "--limit", "5", "--json"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandJobs {
		t.Fatalf("command = %q, want jobs", opts.command)
	}
	if opts.configPath != "anvil.toml" {
		t.Fatalf("config path = %q, want anvil.toml", opts.configPath)
	}
	if opts.libraryName != "movies" {
		t.Fatalf("library = %q, want movies", opts.libraryName)
	}
	if opts.jobLimit != 5 {
		t.Fatalf("limit = %d, want 5", opts.jobLimit)
	}
	if !opts.jsonOutput {
		t.Fatal("json output = false, want true")
	}
	want := []domain.JobState{domain.JobStatePending, domain.JobStateFailed}
	if len(opts.jobStates) != len(want) {
		t.Fatalf("states len = %d, want %d", len(opts.jobStates), len(want))
	}
	for i := range want {
		if opts.jobStates[i] != want[i] {
			t.Fatalf("state[%d] = %q, want %q", i, opts.jobStates[i], want[i])
		}
	}
}

func TestParseOptionsParsesRetryCommand(t *testing.T) {
	opts, err := parseOptions([]string{"retry", "12", "13"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandRetry {
		t.Fatalf("command = %q, want retry", opts.command)
	}
	if got, want := opts.jobIDs, []domain.JobID{12, 13}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("job ids = %v, want %v", got, want)
	}
}

func TestParseOptionsRejectsRetryWithoutTarget(t *testing.T) {
	if _, err := parseOptions([]string{"retry"}); err == nil {
		t.Fatal("parseOptions() error = nil, want retry target error")
	}
}

func TestParseOptionsParsesCleanupStagingCommand(t *testing.T) {
	opts, err := parseOptions([]string{"cleanup-staging", "--older-than", "24h", "--dry-run"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandCleanup {
		t.Fatalf("command = %q, want cleanup-staging", opts.command)
	}
	if opts.cleanupOlder != "24h" {
		t.Fatalf("cleanup older = %q, want 24h", opts.cleanupOlder)
	}
	if !opts.cleanupDryRun {
		t.Fatal("cleanup dry run = false, want true")
	}
}

func TestValidateReloadRejectsStoreAndTempDirChanges(t *testing.T) {
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
}

func TestValidateReloadAllowsRuntimeKnobs(t *testing.T) {
	current := config.Default()
	next := current
	next.Daemon.WorkerCount = current.Daemon.WorkerCount + 1
	next.Daemon.TotalThreads = current.Daemon.TotalThreads + 2
	next.Daemon.ScanInterval = "1m"
	if err := validateReload(current, next); err != nil {
		t.Fatalf("validateReload() error = %v", err)
	}
}
