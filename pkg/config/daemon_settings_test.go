package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoadRecordsTheDaemonKeysTheFileSets(t *testing.T) {
	cfg, err := load(t, `
[daemon]
worker_count = 2
total_threads = 0
shutdown_policy = "cancel"
temp_dir = "/var/lib/anvil"
`)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"worker_count":    SourceFile,
		"total_threads":   SourceFile, // an explicit zero is set too
		"shutdown_policy": SourceFile,
		"log_level":       SourceDefault,
		"scan_interval":   SourceDefault,
	} {
		if got := cfg.DaemonSource(key); got != want {
			t.Errorf("DaemonSource(%q) = %q, want %q", key, got, want)
		}
	}
	// Only the keys the web UI can change are recorded.
	if _, recorded := cfg.DaemonSources["temp_dir"]; recorded {
		t.Error("temp_dir is recorded, but it is file-only")
	}
	if got := Default().DaemonSource("worker_count"); got != SourceDefault {
		t.Errorf("Default().DaemonSource = %q, want %q", got, SourceDefault)
	}
}

func TestDaemonOverridesApplyWhereTheFileIsSilent(t *testing.T) {
	cfg, err := load(t, `
[daemon]
worker_count = 2
`)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DaemonSources["shutdown_timeout"] = SourceFlag
	cfg.Daemon.ShutdownTimeout = Duration{Duration: time.Minute}
	workers, attempts, level, timeout := 8, 5, "DEBUG", Duration{Duration: time.Hour}
	merged, err := cfg.WithDaemonOverrides(DaemonOverrides{WorkerCount: &workers, MaxAttempts: &attempts, LogLevel: &level, ShutdownTimeout: &timeout})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Daemon.WorkerCount != 2 || merged.DaemonSource("worker_count") != SourceFile {
		t.Errorf("worker_count = %d from %s, want the file's 2", merged.Daemon.WorkerCount, merged.DaemonSource("worker_count"))
	}
	if merged.Daemon.ShutdownTimeout.Duration != time.Minute || merged.DaemonSource("shutdown_timeout") != SourceFlag {
		t.Errorf("shutdown_timeout = %s from %s, want the option's 1m", merged.Daemon.ShutdownTimeout, merged.DaemonSource("shutdown_timeout"))
	}
	if merged.Daemon.MaxAttempts != 5 || merged.DaemonSource("max_attempts") != SourceStore {
		t.Errorf("max_attempts = %d from %s, want the stored 5", merged.Daemon.MaxAttempts, merged.DaemonSource("max_attempts"))
	}
	if merged.Daemon.LogLevel != "debug" {
		t.Errorf("log_level = %q, want it normalized to debug", merged.Daemon.LogLevel)
	}
	if merged.DaemonSource("scan_interval") != SourceDefault {
		t.Errorf("scan_interval source = %s, want default", merged.DaemonSource("scan_interval"))
	}
	// The loaded config is left as it was: a later merge starts from it again.
	if cfg.Daemon.MaxAttempts != DefaultMaxAttempts || cfg.DaemonSource("max_attempts") != SourceDefault {
		t.Errorf("the merge changed the loaded config: max_attempts = %d from %s", cfg.Daemon.MaxAttempts, cfg.DaemonSource("max_attempts"))
	}
}

func TestDaemonOverridesRefuseAnInvalidStoredValue(t *testing.T) {
	zero := 0
	_, err := Default().WithDaemonOverrides(DaemonOverrides{WorkerCount: &zero})
	if err == nil || !strings.Contains(err.Error(), "daemon.worker_count must be at least 1") {
		t.Fatalf("WithDaemonOverrides = %v, want the worker_count rule", err)
	}
}

func TestDaemonOverrideProblemsNameTheirKey(t *testing.T) {
	zero, negative, policy := 0, -1, "later"
	none := Duration{}
	problems := DaemonOverrides{WorkerCount: &zero, TotalThreads: &negative, ScanInterval: &none, ShutdownPolicy: &policy}.Problems()
	fields := make([]string, 0, len(problems))
	for _, problem := range problems {
		fields = append(fields, problem.Field)
		if !strings.HasSuffix(problem.Message, ".") || strings.ToUpper(problem.Message[:1]) != problem.Message[:1] {
			t.Errorf("%s: %q is not a sentence", problem.Field, problem.Message)
		}
	}
	if want := []string{"worker_count", "total_threads", "scan_interval", "shutdown_policy"}; !slices.Equal(fields, want) {
		t.Errorf("fields = %v, want %v", fields, want)
	}
	// A zero debounce and a zero timeout are valid: only the cadences must be positive.
	if problems := (DaemonOverrides{FSDebounce: &none, ShutdownTimeout: &none}).Problems(); len(problems) > 0 {
		t.Errorf("Problems = %v, want none", problems)
	}
	if problems := (DaemonOverrides{}).Problems(); len(problems) > 0 {
		t.Errorf("no overrides: Problems = %v, want none", problems)
	}
}

func TestDaemonOverridesKeysAndJSON(t *testing.T) {
	workers, level := 4, "warn"
	interval := Duration{Duration: 90 * time.Minute}
	overrides := DaemonOverrides{WorkerCount: &workers, ScanInterval: &interval, LogLevel: &level}
	if got, want := overrides.Keys(), []string{"worker_count", "scan_interval", "log_level"}; !slices.Equal(got, want) {
		t.Errorf("Keys = %v, want %v", got, want)
	}
	data, err := json.Marshal(overrides)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"worker_count":4,"scan_interval":"90m","log_level":"warn"}` {
		t.Errorf("JSON = %s", data)
	}
	var decoded DaemonOverrides
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if *decoded.WorkerCount != 4 || decoded.ScanInterval.Duration != 90*time.Minute || *decoded.LogLevel != "warn" || decoded.MaxAttempts != nil {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestDaemonKeysCoverEveryTunable(t *testing.T) {
	var keys []string
	for _, key := range DaemonKeys() {
		keys = append(keys, key.Key)
	}
	want := []string{"worker_count", "total_threads", "max_threads_per_job", "max_attempts", "scan_interval", "filesystem_event_debounce", "scheduler_interval", "lease_duration", "shutdown_policy", "shutdown_timeout", "staging_cleanup_age", "log_level"}
	if !slices.Equal(keys, want) {
		t.Errorf("DaemonKeys = %v, want %v", keys, want)
	}
	// Every key of the table reaches its own field: setting all of them
	// changes every one of the twelve values.
	one, policy, level := 1, "cancel", "error"
	minute := Duration{Duration: time.Minute}
	all := DaemonOverrides{
		WorkerCount: &one, TotalThreads: &one, MaxThreadsPerJob: &one, MaxAttempts: &one,
		ScanInterval: &minute, FSDebounce: &minute, SchedulerInterval: &minute, LeaseDuration: &minute,
		ShutdownPolicy: &policy, ShutdownTimeout: &minute, StagingCleanupAge: &minute, LogLevel: &level,
	}
	if got := all.Keys(); !slices.Equal(got, want) {
		t.Errorf("Keys of a full set = %v, want %v", got, want)
	}
}
