package main

import (
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestParseOptionsParsesBackupCommand(t *testing.T) {
	opts, err := parseOptions([]string{"backup", "--config", "anvil.toml", "backups/anvil.db"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandBackup {
		t.Fatalf("command = %q, want backup", opts.command)
	}
	if opts.configPath != "anvil.toml" {
		t.Fatalf("config path = %q, want anvil.toml", opts.configPath)
	}
	if opts.backupPath != "backups/anvil.db" {
		t.Fatalf("backup path = %q, want backups/anvil.db", opts.backupPath)
	}
}

func TestParseOptionsRejectsBackupWithoutExactlyOneDestination(t *testing.T) {
	for _, args := range [][]string{{"backup"}, {"backup", "one.db", "two.db"}} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("parseOptions(%v) error = nil, want destination error", args)
		}
	}
}

func TestParseOptionsParsesPruneJobsCommand(t *testing.T) {
	opts, err := parseOptions([]string{"prune-jobs", "--library", "movies", "--state", "complete,failed", "--apply"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandPruneJobs {
		t.Fatalf("command = %q, want prune-jobs", opts.command)
	}
	if opts.libraryName != "movies" {
		t.Fatalf("library = %q, want movies", opts.libraryName)
	}
	if !opts.pruneApply {
		t.Fatal("prune apply = false, want true")
	}
	want := []domain.JobState{domain.JobStateComplete, domain.JobStateFailed}
	if len(opts.jobStates) != len(want) {
		t.Fatalf("job states = %v, want %v", opts.jobStates, want)
	}
	for i := range want {
		if opts.jobStates[i] != want[i] {
			t.Fatalf("job state %d = %q, want %q", i, opts.jobStates[i], want[i])
		}
	}
}

func TestParseOptionsPruneJobsDefaultsToDryRun(t *testing.T) {
	opts, err := parseOptions([]string{"prune-jobs"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.pruneApply {
		t.Fatal("prune apply = true, want default dry run")
	}
}

func TestParseOptionsRejectsActivePruneState(t *testing.T) {
	if _, err := parseOptions([]string{"prune-jobs", "--state", "running"}); err == nil {
		t.Fatal("parseOptions() error = nil, want active-state refusal")
	}
}

func TestParseOptionsParsesForceOccurrenceCommand(t *testing.T) {
	opts, err := parseOptions([]string{"force-occurrence", "--config", "anvil.toml", "--library", "downloads", "Release/Episode.mkv"})
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if opts.command != commandForce || opts.libraryName != "downloads" || opts.forcePath != "Release/Episode.mkv" {
		t.Fatalf("force-occurrence options = %+v", opts)
	}
}

func TestParseOptionsRejectsIncompleteForceOccurrenceCommand(t *testing.T) {
	for _, args := range [][]string{
		{"force-occurrence", "Movie.mkv"},
		{"force-occurrence", "--library", "movies"},
		{"force-occurrence", "--library", "movies", "one.mkv", "two.mkv"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("parseOptions(%v) error = nil, want force-occurrence argument refusal", args)
		}
	}
}
