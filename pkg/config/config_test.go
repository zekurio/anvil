package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func load(t *testing.T, data string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "anvil.toml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLoadPreservesExplicitZeros(t *testing.T) {
	cfg, err := load(t, `
[daemon]
total_threads = 0

[profiles.default-av1.video]
crf_min = 0
target = 0
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Daemon.TotalThreads != 0 {
		t.Fatalf("TotalThreads = %d, want explicit 0", cfg.Daemon.TotalThreads)
	}
	video := cfg.Profiles[DefaultProfileName].Video
	if video.CRFMin != 0 {
		t.Fatalf("CRFMin = %d, want explicit 0", video.CRFMin)
	}
	if video.Target != 0 {
		t.Fatalf("Target = %v, want explicit 0", video.Target)
	}
}

func TestLoadRejectsZeroLibraryScanInterval(t *testing.T) {
	_, err := load(t, `
[libraries.movies]
path = "/srv/movies"
scan_interval = "0s"
`)
	if err == nil {
		t.Fatal("Load succeeded with scan_interval = 0s")
	}
}

func TestLoadFillsDefaultsWhenOmitted(t *testing.T) {
	cfg, err := load(t, "")
	if err != nil {
		t.Fatal(err)
	}
	video := cfg.Profiles[DefaultProfileName].Video
	if video.CRFMin != DefaultCRFMin || video.CRFMax != DefaultCRFMax {
		t.Fatalf("CRF range = %d..%d", video.CRFMin, video.CRFMax)
	}
	if video.Target != DefaultTargetVMAF {
		t.Fatalf("Target = %v", video.Target)
	}
	if video.Preset != "6" {
		t.Fatalf("Preset = %q", video.Preset)
	}
	if cfg.Daemon.ScanInterval.Duration != DefaultScanInterval {
		t.Fatalf("ScanInterval = %v", cfg.Daemon.ScanInterval)
	}
}

func TestLoadKeepsDefaultWithPartialCustomProfile(t *testing.T) {
	cfg, err := load(t, `
[profiles.fast.video]
preset = "4"
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Profiles[DefaultProfileName]; !ok {
		t.Fatal("default profile is missing")
	}
	fast := cfg.Profiles["fast"].Video
	if fast.Preset != "4" || fast.CRFMin != DefaultCRFMin || fast.CRFMax != DefaultCRFMax {
		t.Fatalf("fast video = %#v", fast)
	}
}

func TestLoadFormatsValidationProblems(t *testing.T) {
	_, err := load(t, `
[daemon]
worker_count = 0
max_attempts = 0
`)
	if err == nil {
		t.Fatal("Load succeeded with invalid daemon values")
	}
	want := "invalid config:\n- daemon.worker_count must be at least 1\n- daemon.max_attempts must be at least 1"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestLoadRejectsBadDurationAtDecode(t *testing.T) {
	_, err := load(t, `
[daemon]
scan_interval = "soon"
`)
	if err == nil {
		t.Fatal("Load succeeded with invalid duration")
	}
	if !strings.Contains(err.Error(), "scan_interval") {
		t.Fatalf("error does not name the key: %v", err)
	}
}

func TestLoadDerivesPathsFromTempDir(t *testing.T) {
	cfg, err := load(t, `
[daemon]
temp_dir = "/srv/anvil"
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Daemon.StorePath != "/srv/anvil/anvil.db" {
		t.Fatalf("StorePath = %q", cfg.Daemon.StorePath)
	}
	if cfg.Daemon.ControlSocket != "/srv/anvil/anvild.sock" {
		t.Fatalf("ControlSocket = %q", cfg.Daemon.ControlSocket)
	}
}

func TestLoadKeepsExplicitStorePathWithTempDir(t *testing.T) {
	cfg, err := load(t, `
[daemon]
temp_dir = "/srv/anvil"
store_path = "/var/db/anvil.db"
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Daemon.StorePath != "/var/db/anvil.db" {
		t.Fatalf("StorePath = %q", cfg.Daemon.StorePath)
	}
	if cfg.Daemon.ControlSocket != "/srv/anvil/anvild.sock" {
		t.Fatalf("ControlSocket = %q", cfg.Daemon.ControlSocket)
	}
}

func TestLoadRejectsExplicitZeroFrameCount(t *testing.T) {
	_, err := load(t, `
[profiles.default-av1.crop]
frame_count = 0
`)
	if err == nil {
		t.Fatal("Load succeeded with frame_count = 0")
	}
}

func TestLoadRequiresTargetForXPSNR(t *testing.T) {
	_, err := load(t, `
[profiles.default-av1.video]
metric = "xpsnr"
`)
	if err == nil {
		t.Fatal("Load succeeded with xpsnr and no target")
	}
}

func TestLoadLibraryDurations(t *testing.T) {
	cfg, err := load(t, `
[libraries.downloads]
kind = "download"
path = "/srv/downloads"
scan_interval = "5m"

[libraries.downloads.download]
handoff_path = "/srv/imports"
stable_for = "90s"
`)
	if err != nil {
		t.Fatal(err)
	}
	library := cfg.Libraries["downloads"]
	if library.ScanInterval.Duration != 5*time.Minute {
		t.Fatalf("ScanInterval = %v", library.ScanInterval)
	}
	if library.Download.StableFor.Duration != 90*time.Second {
		t.Fatalf("StableFor = %v", library.Download.StableFor)
	}
	if got := cfg.ScanIntervalForLibrary("downloads"); got != 5*time.Minute {
		t.Fatalf("ScanIntervalForLibrary = %v", got)
	}
}

func TestLoadDownloadLibraryStableForDefault(t *testing.T) {
	cfg, err := load(t, `
[libraries.downloads]
kind = "download"
path = "/srv/downloads"

[libraries.downloads.download]
handoff_path = "/srv/imports"
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Libraries["downloads"].Download.StableFor.Duration; got != DefaultStableFor {
		t.Fatalf("StableFor = %v", got)
	}
}

func TestReferenceConfigLoads(t *testing.T) {
	if _, err := Load(filepath.Join("..", "..", "examples", "anvil-reference.toml")); err != nil {
		t.Fatal(err)
	}
}
