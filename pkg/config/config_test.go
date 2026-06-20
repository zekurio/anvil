package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeConfig(t, `
[[libraries]]
name = "movies"
path = "/srv/media/movies"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Daemon.TempDir == "" {
		t.Fatal("expected daemon temp_dir default")
	}
	if cfg.Daemon.WorkerCount < 1 {
		t.Fatalf("expected worker_count default, got %d", cfg.Daemon.WorkerCount)
	}
	if got := cfg.Libraries[0].Flow; got != DefaultFlowName {
		t.Fatalf("expected default flow %q, got %q", DefaultFlowName, got)
	}
	if got := cfg.Libraries[0].Profile; got != DefaultProfileName {
		t.Fatalf("expected default profile %q, got %q", DefaultProfileName, got)
	}
}

func TestLoadRejectsUnknownReferences(t *testing.T) {
	path := writeConfig(t, `
[[libraries]]
name = "movies"
path = "/srv/media/movies"
flow = "missing-flow"
profile = "missing-profile"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid references")
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "anvil.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return path
}
