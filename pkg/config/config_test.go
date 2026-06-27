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
	if cfg.Daemon.StorePath == "" {
		t.Fatal("expected daemon store_path default")
	}
	if cfg.Daemon.WorkerCount < 1 {
		t.Fatalf("expected worker_count default, got %d", cfg.Daemon.WorkerCount)
	}
	if got := cfg.Profiles[0].Audio.Fallback; got != DefaultStreamFallback {
		t.Fatalf("expected default audio fallback %q, got %q", DefaultStreamFallback, got)
	}
	if got := cfg.Libraries[0].Flow; got != DefaultFlowName {
		t.Fatalf("expected default flow %q, got %q", DefaultFlowName, got)
	}
	if got := cfg.Libraries[0].Profile; got != DefaultProfileName {
		t.Fatalf("expected default profile %q, got %q", DefaultProfileName, got)
	}
	if got := cfg.Libraries[0].Kind; got != DefaultLibraryKind {
		t.Fatalf("expected default library kind %q, got %q", DefaultLibraryKind, got)
	}
	if !containsString(cfg.Flows[0].Steps, "stage") {
		t.Fatalf("default flow steps = %v, want stage before encode output is needed", cfg.Flows[0].Steps)
	}
	if !containsString(cfg.Flows[0].Steps, "crop-detect") {
		t.Fatalf("default flow steps = %v, want crop detection before CRF search", cfg.Flows[0].Steps)
	}
	if !containsString(cfg.Flows[0].Steps, "audio-cleanup") {
		t.Fatalf("default flow steps = %v, want audio cleanup before encode", cfg.Flows[0].Steps)
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

func TestLoadRejectsNonPositiveDaemonDurations(t *testing.T) {
	path := writeConfig(t, `
[daemon]
scan_interval = "0s"
scheduler_interval = "-1s"
lease_duration = "0s"

[[libraries]]
name = "movies"
path = "/srv/media/movies"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid daemon durations")
	}
}

func TestLoadDownloadLibrary(t *testing.T) {
	path := writeConfig(t, `
[[flows]]
name = "download-av1-handoff"
steps = ["probe", "crf-search", "encode", "handoff"]

[[libraries]]
name = "usenet-tv"
kind = "download"
path = "/downloads/complete/tv"
flow = "download-av1-handoff"
download.handoff_path = "/imports/tv"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	library := cfg.Libraries[0]
	if got := library.Download.StableFor; got != DefaultStableFor {
		t.Fatalf("expected default stable_for %q, got %q", DefaultStableFor, got)
	}
	if got := library.Download.HandoffMode; got != DefaultHandoffMode {
		t.Fatalf("expected default handoff_mode %q, got %q", DefaultHandoffMode, got)
	}
	if library.Download.CleanupSourceMedia {
		t.Fatal("cleanup_source_media default = true, want explicit opt-in")
	}
}

func TestLoadMetadataProvider(t *testing.T) {
	path := writeConfig(t, `
[[libraries]]
name = "movies"
path = "/srv/media/movies"

[libraries.metadata]
provider = "radarr"
base_url = "http://radarr:7878"
api_key = "secret"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	metadata := cfg.Libraries[0].Metadata
	if got, want := metadata.Provider, "radarr"; got != want {
		t.Fatalf("metadata provider = %q, want %q", got, want)
	}
	if got, want := metadata.BaseURL, "http://radarr:7878"; got != want {
		t.Fatalf("metadata base URL = %q, want %q", got, want)
	}
	if got, want := metadata.APIKey, "secret"; got != want {
		t.Fatalf("metadata API key = %q, want %q", got, want)
	}
}

func TestLoadRejectsMetadataProviderWithoutConnectionDetails(t *testing.T) {
	path := writeConfig(t, `
[[libraries]]
name = "movies"
path = "/srv/media/movies"

[libraries.metadata]
provider = "sonarr"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want missing metadata connection details")
	}
}

func TestLoadRejectsDownloadLibraryWithoutHandoffPath(t *testing.T) {
	path := writeConfig(t, `
[[libraries]]
name = "usenet-tv"
kind = "download"
path = "/downloads/complete/tv"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want missing handoff_path")
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
