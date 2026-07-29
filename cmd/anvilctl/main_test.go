package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/controlapi"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/store"
)

// testDaemon runs the real control service over a real Unix socket. The client
// is only worth testing against the daemon it is supposed to drive: a fake
// server would happily accept a command the daemon rejects.
type testDaemon struct {
	socketPath string
	state      *store.SQLiteStore
	job        domain.Job
	library    config.LibraryConfig
}

func startTestDaemon(t *testing.T) *testDaemon {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	state, err := store.Open(ctx, filepath.Join(root, "anvil.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Errorf("state.Close() error = %v", err)
		}
	})

	library := config.LibraryConfig{
		Name: "movies", Kind: string(domain.LibraryKindMedia),
		Path: filepath.Join(root, "movies"), Flow: config.DefaultFlowName,
		Profile: config.DefaultProfileName,
	}
	if err := os.MkdirAll(library.Path, 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(library.Path, "Movie.mkv"), []byte("media"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg := config.Default()
	cfg.Daemon.TempDir = filepath.Join(root, "tmp")
	cfg.Libraries = map[string]config.LibraryConfig{"movies": library}

	now := time.Now().UTC()
	forced, err := state.ForceOccurrence(ctx, store.ForceOccurrenceInput{
		LibraryName: "movies", SourceKind: domain.SourceKindFile,
		SourceRelativePath: "Movie.mkv", AssetRelativePath: "Movie.mkv",
		AssetRole:         domain.MediaAssetRolePrimaryVideo,
		SourceFingerprint: domain.FileFingerprint{SizeBytes: 5, ModTime: now},
		AssetFingerprint:  domain.FileFingerprint{SizeBytes: 5, ModTime: now},
		Now:               now,
	})
	if err != nil {
		t.Fatalf("ForceOccurrence() error = %v", err)
	}

	socketPath := testSocketPath(t)
	listener, cleanup, err := controlapi.ListenUnix(socketPath)
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	serveCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- controlapi.Server{Service: controlapi.Service{
			Store:         state,
			Scanner:       scanner.Scanner{Store: state},
			Config:        func() config.Config { return cfg },
			ActiveWorkers: func() int { return 0 },
			StartedAt:     now,
			DaemonVersion: "test-daemon",
		}}.Serve(serveCtx, listener)
	}()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("Serve() error = %v", err)
		}
		if err := cleanup(); err != nil {
			t.Errorf("cleanup() error = %v", err)
		}
	})
	return &testDaemon{socketPath: socketPath, state: state, job: forced.Job, library: library}
}

// testSocketPath keeps the socket path under the platform's sockaddr_un limit,
// which t.TempDir() paths routinely exceed on macOS.
func testSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "anvil")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove socket dir: %v", err)
		}
	})
	return filepath.Join(dir, "c.sock")
}

func (d *testDaemon) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), append([]string{"--socket", d.socketPath}, args...), &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestCommandTreeDrivesTheDaemon(t *testing.T) {
	daemon := startTestDaemon(t)

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "status", args: []string{"status"}, want: []string{"DAEMON", "ready", "WORKERS", "QUEUE", "pending=1"}},
		{name: "job list", args: []string{"job", "list"}, want: []string{"JOB", daemon.job.Label(), "pending"}},
		{name: "job show", args: []string{"job", "show", daemon.job.Label()}, want: []string{"Job " + daemon.job.Label(), "Attempts: none"}},
		{name: "library stats", args: []string{"library", "stats"}, want: []string{"LIBRARY", "SAVED%"}},
		{name: "job recover", args: []string{"job", "recover"}, want: []string{"recovered_jobs=0"}},
		{name: "job prune", args: []string{"job", "prune"}, want: []string{"dry_run=true", "deleted_jobs=0"}},
		{name: "staging cleanup", args: []string{"staging", "cleanup", "--older-than", "24h"}, want: []string{"dry_run=false", "removed=0"}},
		{name: "library scan", args: []string{"library", "scan", "movies"}, want: []string{"libraries=1"}},
		{name: "version", args: []string{"version"}, want: []string{"CLIENT", "DAEMON", "test-daemon", "PROTOCOL"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, err := daemon.run(t, tt.args...)
			if err != nil {
				t.Fatalf("run(%v) error = %v, stderr = %s", tt.args, err, stderr)
			}
			for _, want := range tt.want {
				if !strings.Contains(stdout, want) {
					t.Fatalf("run(%v) output missing %q:\n%s", tt.args, want, stdout)
				}
			}
		})
	}
}

// TestLegacyCommandNamesStillWork keeps the muscle memory of the old anvild
// subcommands working, so a migration does not also mean relearning names.
func TestLegacyCommandNamesStillWork(t *testing.T) {
	daemon := startTestDaemon(t)
	for _, args := range [][]string{
		{"jobs"},
		{"inspect", daemon.job.Label()},
		{"stats"},
		{"recover"},
		{"prune-jobs"},
		{"scan", "--library", "movies"},
		{"cleanup-staging", "--dry-run"},
	} {
		t.Run(args[0], func(t *testing.T) {
			if _, stderr, err := daemon.run(t, args...); err != nil {
				t.Fatalf("run(%v) error = %v, stderr = %s", args, err, stderr)
			}
		})
	}
}

func TestJobRetryAndCancelUseTheDaemon(t *testing.T) {
	daemon := startTestDaemon(t)

	// A bare cancel is refused before it reaches the daemon.
	stdout, _, err := daemon.run(t, "job", "cancel")
	if err == nil {
		t.Fatalf("run(job cancel) error = nil, want a rejected bare cancel: %s", stdout)
	}
	if exitCode(err) != exitUsage {
		t.Fatalf("bare cancel exit code = %d, want %d", exitCode(err), exitUsage)
	}

	stdout, stderr, err := daemon.run(t, "job", "cancel", "--library", "movies", daemon.job.Label())
	if err != nil {
		t.Fatalf("run(job cancel) error = %v, stderr = %s", err, stderr)
	}
	if !strings.Contains(stdout, "canceled 1 of 1 matching jobs") {
		t.Fatalf("cancel output = %s", stdout)
	}

	// A canceled job is retryable, which is the documented recovery path.
	stdout, stderr, err = daemon.run(t, "job", "retry", daemon.job.Label())
	if err != nil {
		t.Fatalf("run(job retry) error = %v, stderr = %s", err, stderr)
	}
	if !strings.Contains(stdout, "retried_jobs=1") {
		t.Fatalf("retry output = %s", stdout)
	}
}

func TestJobShowAcceptsIDsAndSlugsAndReportsMissingJobs(t *testing.T) {
	daemon := startTestDaemon(t)

	if _, _, err := daemon.run(t, "job", "show", "1"); err != nil {
		t.Fatalf("run(job show 1) error = %v", err)
	}
	if _, _, err := daemon.run(t, "job", "show", daemon.job.Label()); err != nil {
		t.Fatalf("run(job show slug) error = %v", err)
	}
	_, _, err := daemon.run(t, "job", "show", "no-such-job")
	if err == nil {
		t.Fatal("run(job show missing) error = nil, want not found")
	}
	if got := exitCode(err); got != exitNotFound {
		t.Fatalf("exit code = %d, want %d", got, exitNotFound)
	}
}

func TestOccurrenceForceRequiresALibraryAndRefusesActiveWork(t *testing.T) {
	daemon := startTestDaemon(t)

	if _, _, err := daemon.run(t, "occurrence", "force", "Movie.mkv"); err == nil {
		t.Fatal("run(occurrence force) without --library error = nil, want usage error")
	}

	// The fixture already has a pending job for this path, and forcing a new
	// occurrence on top of live work is exactly the mistake the refusal exists
	// for. It is an operator-fixable state, so it must not read as an internal
	// daemon failure.
	_, _, err := daemon.run(t, "occurrence", "force", "--library", "movies", "Movie.mkv")
	if err == nil {
		t.Fatal("run(occurrence force) over active work error = nil, want a refusal")
	}
	if got := exitCode(err); got != exitUsage {
		t.Fatalf("exit code = %d, want %d", got, exitUsage)
	}

	// A second file with no job is forced normally.
	if err := os.WriteFile(filepath.Join(daemon.library.Path, "Другой.mkv"), []byte("media"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	stdout, stderr, err := daemon.run(t, "occurrence", "force", "--library", "movies", "Другой.mkv")
	if err != nil {
		t.Fatalf("run(occurrence force) error = %v, stderr = %s", err, stderr)
	}
	if !strings.Contains(stdout, "library=movies path=Другой.mkv") {
		t.Fatalf("force output = %s", stdout)
	}
}

func TestStoreBackupResolvesRelativeDestinations(t *testing.T) {
	daemon := startTestDaemon(t)
	destination := filepath.Join(t.TempDir(), "backup.db")

	stdout, stderr, err := daemon.run(t, "store", "backup", destination)
	if err != nil {
		t.Fatalf("run(store backup) error = %v, stderr = %s", err, stderr)
	}
	if !strings.Contains(stdout, "integrity=ok") {
		t.Fatalf("backup output = %s", stdout)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("backup file stat error = %v", err)
	}
}

// TestGlobalJSONFlagAppliesToEveryCommand keeps --json usable before the
// command name, which is where operators reach for it.
func TestGlobalJSONFlagAppliesToEveryCommand(t *testing.T) {
	daemon := startTestDaemon(t)
	var out, errOut bytes.Buffer
	if err := run(context.Background(), []string{"--socket", daemon.socketPath, "--json", "status"}, &out, &errOut); err != nil {
		t.Fatalf("run(--json status) error = %v, stderr = %s", err, errOut.String())
	}
	if !strings.Contains(out.String(), `"api_version"`) {
		t.Fatalf("json status output = %s", out.String())
	}
}

func TestUnreachableDaemonExitsWithTheUnavailableCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "status"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run(status) error = nil, want an unreachable daemon")
	}
	if got := exitCode(err); got != exitUnavailable {
		t.Fatalf("exit code = %d, want %d", got, exitUnavailable)
	}
	var controlErr *controlapi.Error
	if !errors.As(err, &controlErr) || controlErr.Code != controlapi.CodeUnavailable {
		t.Fatalf("error = %v, want a structured unavailable error", err)
	}
}

func TestRunHelpDoesNotRequireDaemon(t *testing.T) {
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"help"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("run(help) error = %v", err)
	}
	for _, want := range []string{"anvilctl", "job list", "store backup", "Exit status"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestUnknownCommandsAreUsageErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"--socket", "/run/anvil/anvild.sock", "explode"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run(explode) error = nil, want an unknown command error")
	}
	if got := exitCode(err); got != exitUsage {
		t.Fatalf("exit code = %d, want %d", got, exitUsage)
	}
}

// TestFlagsMayFollowPositionalArguments covers what an operator actually types.
// Stdlib flag parsing stops at the first non-flag argument, which would make
// "job show 42 --json" report the flag as a stray argument.
func TestFlagsMayFollowPositionalArguments(t *testing.T) {
	daemon := startTestDaemon(t)
	destination := filepath.Join(t.TempDir(), "backup.db")

	tests := [][]string{
		{"job", "show", daemon.job.Label(), "--json"},
		{"library", "scan", "movies", "--json"},
		{"store", "backup", destination, "--json"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, err := daemon.run(t, args...)
			if err != nil {
				t.Fatalf("run(%v) error = %v, stderr = %s", args, err, stderr)
			}
			if !strings.Contains(stdout, `"api_version"`) {
				t.Fatalf("run(%v) did not produce JSON:\n%s", args, stdout)
			}
		})
	}
}
