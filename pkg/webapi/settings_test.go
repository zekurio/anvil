package webapi

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
)

func TestSettingsDescribeEveryEditableDaemonKey(t *testing.T) {
	f := newFixture(t, "", nil)
	f.configure(func(cfg *config.Config) {
		cfg.Daemon.MaxAttempts = 5
		cfg.DaemonSources = map[string]string{"max_attempts": config.SourceFile, "shutdown_policy": config.SourceFlag}
	})
	settings := want[Settings](t, f.get("/api/v1/settings"), http.StatusOK)
	daemon := settings.Daemon

	var keys []string
	for _, key := range config.DaemonKeys() {
		keys = append(keys, key.Key)
	}
	if len(daemon.Keys) != len(keys) {
		t.Fatalf("keys = %v, want one for each of %v", daemon.Keys, keys)
	}
	for _, key := range keys {
		described, found := daemon.Keys[key]
		if !found {
			t.Fatalf("key %s is not described", key)
		}
		if described.Choices == nil {
			t.Errorf("%s: choices is null", key)
		}
	}
	if got := daemon.Keys["max_attempts"]; got.Source != SourceFile || got.Editable {
		t.Errorf("max_attempts = %+v, want pinned by the file", got)
	}
	if got := daemon.Keys["shutdown_policy"]; got.Source != SourceFlag || got.Editable || !slices.Equal(got.Choices, []string{"drain", "cancel"}) {
		t.Errorf("shutdown_policy = %+v, want pinned by an option, with its choices", got)
	}
	if got := daemon.Keys["worker_count"]; got.Source != SourceDefault || !got.Editable || got.AtStart || len(got.Choices) != 0 {
		t.Errorf("worker_count = %+v, want an editable default", got)
	}
	if !daemon.Keys["staging_cleanup_age"].AtStart {
		t.Error("staging_cleanup_age does not say it applies at the next start")
	}
	if daemon.MaxAttempts != 5 || daemon.Defaults.MaxAttempts != config.DefaultMaxAttempts || daemon.Defaults.ScanInterval != "30m" || daemon.FilesystemEventDebounce != "2s" {
		t.Errorf("values = %+v", daemon.DaemonValues)
	}
}

func TestDaemonSettingsWriteAppliesToTheSnapshot(t *testing.T) {
	f := newFixture(t, "", nil)
	write := DaemonSettingsWrite{WorkerCount: pointer(3), ScanInterval: pointer(" 45m "), LogLevel: pointer("DEBUG"), StagingCleanupAge: pointer("")}
	settings := want[Settings](t, f.send(http.MethodPut, "/api/v1/settings/daemon", write), http.StatusOK)
	if settings.Daemon.WorkerCount != 3 || settings.Daemon.ScanInterval != "45m" || settings.Daemon.LogLevel != "debug" {
		t.Fatalf("answer = %+v", settings.Daemon.DaemonValues)
	}
	if got := settings.Daemon.Keys["worker_count"]; got.Source != SourceStore || !got.Editable {
		t.Errorf("worker_count = %+v, want saved in the store and still editable", got)
	}
	// An empty string is the default, like null.
	if got := settings.Daemon.Keys["staging_cleanup_age"]; got.Source != SourceDefault {
		t.Errorf("staging_cleanup_age = %+v, want its default", got)
	}

	// The snapshot every loop reads has the change before the answer.
	cfg := f.config()
	if cfg.Daemon.WorkerCount != 3 || cfg.ScanInterval() != 45*time.Minute || cfg.Daemon.LogLevel != "debug" {
		t.Errorf("snapshot daemon = %+v", cfg.Daemon)
	}
	if cfg.Daemon.MaxAttempts != config.DefaultMaxAttempts {
		t.Errorf("max_attempts = %d, want its default", cfg.Daemon.MaxAttempts)
	}
	saved, err := f.state.DaemonSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.Keys(); !slices.Equal(got, []string{"worker_count", "scan_interval", "log_level"}) {
		t.Errorf("saved keys = %v", got)
	}

	// A key left out goes back to its default; nothing saved clears the store.
	settings = want[Settings](t, f.send(http.MethodPut, "/api/v1/settings/daemon", DaemonSettingsWrite{}), http.StatusOK)
	if settings.Daemon.WorkerCount != settings.Daemon.Defaults.WorkerCount || settings.Daemon.Keys["worker_count"].Source != SourceDefault {
		t.Errorf("after clearing: worker_count = %d from %s", settings.Daemon.WorkerCount, settings.Daemon.Keys["worker_count"].Source)
	}
	if f.config().ScanInterval() != config.DefaultScanInterval {
		t.Errorf("after clearing: scan interval = %s", f.config().ScanInterval())
	}
	if saved, err = f.state.DaemonSettings(context.Background()); err != nil || len(saved.Keys()) != 0 {
		t.Errorf("after clearing: saved = %v, %v", saved.Keys(), err)
	}
}

func TestDaemonSettingsWriteIsValidated(t *testing.T) {
	f := newFixture(t, "", nil)
	for _, test := range []struct {
		name  string
		write DaemonSettingsWrite
		field string
	}{
		{"no workers", DaemonSettingsWrite{WorkerCount: pointer(0)}, "worker_count"},
		{"negative threads", DaemonSettingsWrite{TotalThreads: pointer(-1)}, "total_threads"},
		{"unreadable duration", DaemonSettingsWrite{LeaseDuration: pointer("soon")}, "lease_duration"},
		{"zero cadence", DaemonSettingsWrite{SchedulerInterval: pointer("0s")}, "scheduler_interval"},
		{"negative wait", DaemonSettingsWrite{ShutdownTimeout: pointer("-1m")}, "shutdown_timeout"},
		{"unknown policy", DaemonSettingsWrite{ShutdownPolicy: pointer("later")}, "shutdown_policy"},
		{"unknown level", DaemonSettingsWrite{MaxAttempts: pointer(2), LogLevel: pointer("loud")}, "log_level"},
	} {
		t.Run(test.name, func(t *testing.T) {
			problem := wantError(t, f.send(http.MethodPut, "/api/v1/settings/daemon", test.write), http.StatusBadRequest, CodeInvalid)
			if problem.Field != test.field {
				t.Errorf("field = %q, want %q (%s)", problem.Field, test.field, problem.Message)
			}
		})
	}
	// A refused write changes nothing, also not the keys of it that were valid.
	if saved, err := f.state.DaemonSettings(context.Background()); err != nil || len(saved.Keys()) != 0 {
		t.Errorf("saved = %v, %v; want nothing", saved.Keys(), err)
	}
	if f.config().Daemon.MaxAttempts != config.DefaultMaxAttempts {
		t.Errorf("max_attempts = %d", f.config().Daemon.MaxAttempts)
	}
	// Only the keys the web UI may change are accepted at all.
	wantError(t, f.send(http.MethodPut, "/api/v1/settings/daemon", `{"media_roots":["/"]}`), http.StatusBadRequest, CodeInvalid)
}

func TestDaemonSettingsWriteRefusesPinnedKeys(t *testing.T) {
	f := newFixture(t, "", nil)
	f.configure(func(cfg *config.Config) {
		cfg.Daemon.WorkerCount = 2
		cfg.Daemon.ShutdownTimeout = config.Duration{Duration: time.Minute}
		cfg.DaemonSources = map[string]string{"worker_count": config.SourceFile, "shutdown_timeout": config.SourceFlag}
	})
	for key, write := range map[string]DaemonSettingsWrite{
		"worker_count":     {WorkerCount: pointer(8)},
		"shutdown_timeout": {ShutdownTimeout: pointer("5m")},
	} {
		problem := wantError(t, f.send(http.MethodPut, "/api/v1/settings/daemon", write), http.StatusBadRequest, CodeInvalid)
		if problem.Field != key {
			t.Errorf("%s: field = %q (%s)", key, problem.Field, problem.Message)
		}
	}
	if cfg := f.config(); cfg.Daemon.WorkerCount != 2 || cfg.ShutdownTimeout() != time.Minute {
		t.Errorf("pinned values changed: %+v", cfg.Daemon)
	}
	// The editable keys next to them still save.
	settings := want[Settings](t, f.send(http.MethodPut, "/api/v1/settings/daemon", DaemonSettingsWrite{MaxAttempts: pointer(4)}), http.StatusOK)
	if settings.Daemon.MaxAttempts != 4 || settings.Daemon.WorkerCount != 2 {
		t.Errorf("answer = %+v", settings.Daemon.DaemonValues)
	}
}

func TestDaemonSettingsWriteIsGuarded(t *testing.T) {
	f := newFixture(t, "", nil)
	write := DaemonSettingsWrite{WorkerCount: pointer(1)}
	wantError(t, f.do(call{method: http.MethodPut, path: "/api/v1/settings/daemon", body: write, header: map[string]string{"Origin": "http://evil.example"}}), http.StatusForbidden, CodeInvalid)
	wantError(t, f.do(call{method: http.MethodPut, path: "/api/v1/settings/daemon", body: `{"worker_count":1}`, plain: true}), http.StatusUnsupportedMediaType, CodeInvalid)
	if f.config().Daemon.WorkerCount == 1 && config.DefaultDaemon().WorkerCount != 1 {
		t.Error("a refused request changed worker_count")
	}
	if saved, err := f.state.DaemonSettings(context.Background()); err != nil || len(saved.Keys()) != 0 {
		t.Errorf("saved = %v, %v; want nothing", saved.Keys(), err)
	}

	signedOut := newFixture(t, "open sesame", nil)
	wantError(t, signedOut.send(http.MethodPut, "/api/v1/settings/daemon", write), http.StatusUnauthorized, CodeUnauthenticated)
}
