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
	video := cfg.Legacy.Profiles[DefaultProfileName].Video
	if video.CRFMin != 0 {
		t.Fatalf("CRFMin = %d, want explicit 0", video.CRFMin)
	}
	if video.Target != 0 {
		t.Fatalf("Target = %v, want explicit 0", video.Target)
	}
}

func TestLoadTrimsWebBasePath(t *testing.T) {
	for raw, want := range map[string]string{"": "", "/": "", " /anvil/ ": "/anvil", "/a/b": "/a/b"} {
		cfg, err := load(t, "[web]\nbase_path = \""+raw+"\"\n")
		if err != nil {
			t.Fatalf("base_path %q: %v", raw, err)
		}
		if cfg.Web.BasePath != want {
			t.Errorf("base_path %q resolved to %q, want %q", raw, cfg.Web.BasePath, want)
		}
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
	video := cfg.Legacy.Profiles[DefaultProfileName].Video
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

func TestMediaRootsMustBeAbsoluteCleanAndUnique(t *testing.T) {
	_, err := load(t, "[daemon]\nmedia_roots = [\"relative\", \"/srv/media/../media\", \"/srv/media\", \"/srv/media\"]\n")
	if err == nil {
		t.Fatal("Load succeeded with invalid media_roots")
	}
	for _, problem := range []string{"must be absolute", "must be cleaned", "duplicate path"} {
		if !strings.Contains(err.Error(), problem) {
			t.Errorf("error %q does not contain %q", err, problem)
		}
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
	if _, ok := cfg.Legacy.Profiles[DefaultProfileName]; !ok {
		t.Fatal("default profile is missing")
	}
	fast := cfg.Legacy.Profiles["fast"].Video
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
	if cfg.Daemon.ControlSocket != "/srv/anvil/anvild.sock" {
		t.Fatalf("ControlSocket = %q", cfg.Daemon.ControlSocket)
	}
	// temp_dir is scratch, so the store default never follows it there.
	if cfg.Daemon.StorePath == "" || strings.HasPrefix(cfg.Daemon.StorePath, "/srv/anvil/") {
		t.Fatalf("StorePath = %q", cfg.Daemon.StorePath)
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
	library := cfg.Legacy.Libraries["downloads"]
	if library.ScanInterval.Duration != 5*time.Minute {
		t.Fatalf("ScanInterval = %v", library.ScanInterval)
	}
	if library.Download.StableFor.Duration != 90*time.Second {
		t.Fatalf("StableFor = %v", library.Download.StableFor)
	}
	// The runtime snapshot is empty until the daemon fills it from the store;
	// a legacy library converts into it with its settings intact.
	if len(cfg.Libraries) != 0 {
		t.Fatalf("Load filled the runtime libraries: %+v", cfg.Libraries)
	}
	cfg.Libraries["downloads"] = library.Runtime()
	if got := cfg.ScanIntervalForLibrary("downloads"); got != 5*time.Minute {
		t.Fatalf("ScanIntervalForLibrary = %v", got)
	}
	if got := cfg.Legacy.Sections; len(got) != 1 || got[0] != "libraries" {
		t.Fatalf("Sections = %v", got)
	}
}

func TestLibraryProblemsAndPathConflicts(t *testing.T) {
	valid := LibraryConfig{Name: "movies", Kind: "media", Path: "/media/movies", FlowID: 1}.WithDefaults()
	if problems := valid.Problems(); len(problems) != 0 {
		t.Fatalf("valid library has problems: %+v", problems)
	}
	if valid.Media.ReplacementMode != DefaultReplacementMode || valid.RuleFallback != RuleAllow {
		t.Fatalf("defaults = %+v", valid)
	}

	broken := LibraryConfig{
		Name: "inbox", Kind: "download", Path: "relative/inbox", ConcurrencyLimit: -1,
		Rules:    []Rule{{Action: RuleDeny, Field: RuleFieldSize, Op: RuleOpLess, Value: "small"}},
		Download: DownloadLibraryConfig{HandoffPath: "also/relative", HandoffMode: "teleport", IgnorableGlobs: []string{"[a-"}},
	}.WithDefaults()
	fields := make(map[string]string)
	for _, problem := range broken.Problems() {
		fields[problem.Field] = problem.Message
	}
	for _, field := range []string{"path", "flow_id", "concurrency_limit", "rules.0.value", "download.handoff_path", "download.handoff_mode", "download.ignorable_globs.0"} {
		if fields[field] == "" {
			t.Errorf("no problem reported for %s: %+v", field, fields)
		}
	}
	if len(fields) != 7 {
		t.Errorf("unexpected problems: %+v", fields)
	}

	others := map[string]LibraryConfig{
		"movies": valid,
		"inbox":  {Name: "inbox", Kind: "download", Path: "/downloads", Download: DownloadLibraryConfig{HandoffPath: "/import/movies"}},
	}
	conflicts := func(candidate LibraryConfig) string {
		var fields []string
		for _, problem := range LibraryPathConflicts("/var/lib/anvil/tmp", others, candidate) {
			fields = append(fields, problem.Field)
		}
		return strings.Join(fields, ",")
	}
	tests := []struct {
		name      string
		candidate LibraryConfig
		want      string
	}{
		{"disjoint", LibraryConfig{Name: "shows", Kind: "media", Path: "/media/shows"}, ""},
		{"sibling with a shared prefix", LibraryConfig{Name: "m2", Kind: "media", Path: "/media/movies2"}, ""},
		{"same root", LibraryConfig{Name: "dup", Kind: "media", Path: "/media/movies/"}, "path"},
		{"below another root", LibraryConfig{Name: "sub", Kind: "media", Path: "/media/movies/4k"}, "path"},
		{"above another root", LibraryConfig{Name: "all", Kind: "media", Path: "/media"}, "path"},
		{"on another library's handoff directory", LibraryConfig{Name: "imp", Kind: "media", Path: "/import"}, "path"},
		{"handoff into a library", LibraryConfig{Name: "dl", Kind: "download", Path: "/dl", Download: DownloadLibraryConfig{HandoffPath: "/media/movies/new"}}, "download.handoff_path"},
		{"handoff inside its own root", LibraryConfig{Name: "dl", Kind: "download", Path: "/dl", Download: DownloadLibraryConfig{HandoffPath: "/dl/done"}}, "download.handoff_path"},
		{"itself is not a conflict", valid, ""},
		{"same as temp_dir", LibraryConfig{Name: "tmp", Kind: "media", Path: "/var/lib/anvil/tmp/"}, "path"},
		{"inside temp_dir", LibraryConfig{Name: "tmp", Kind: "media", Path: "/var/lib/anvil/tmp/samples"}, "path"},
		{"around temp_dir", LibraryConfig{Name: "var", Kind: "media", Path: "/var/lib"}, "path"},
		{"beside temp_dir with a shared prefix", LibraryConfig{Name: "tmp2", Kind: "media", Path: "/var/lib/anvil/tmp2"}, ""},
		{"handoff inside temp_dir", LibraryConfig{Name: "dl", Kind: "download", Path: "/dl", Download: DownloadLibraryConfig{HandoffPath: "/var/lib/anvil/tmp/done"}}, "download.handoff_path"},
		{"handoff around temp_dir", LibraryConfig{Name: "dl", Kind: "download", Path: "/dl", Download: DownloadLibraryConfig{HandoffPath: "/var/lib/anvil"}}, "download.handoff_path"},
	}
	for _, test := range tests {
		if got := conflicts(test.candidate); got != test.want {
			t.Errorf("%s: conflicts on %q, want %q", test.name, got, test.want)
		}
	}
	// Without a temp_dir there is nothing to compare against.
	if problems := LibraryPathConflicts("", nil, LibraryConfig{Name: "tmp", Kind: "media", Path: "/var/lib/anvil/tmp"}); len(problems) != 0 {
		t.Errorf("conflicts without a temp_dir: %+v", problems)
	}
}

func TestArrProblems(t *testing.T) {
	if problems := (ArrConfig{Type: "radarr", BaseURL: "http://localhost:7878", APIKeyFile: "/run/secrets/radarr"}).Problems(); len(problems) != 0 {
		t.Fatalf("valid arr has problems: %+v", problems)
	}
	problems := ArrConfig{Type: "plex", BaseURL: "localhost:7878", APIKeyFile: "key.txt"}.Problems()
	var fields []string
	for _, problem := range problems {
		fields = append(fields, problem.Field)
	}
	if got := strings.Join(fields, ","); got != "type,base_url,api_key_file" {
		t.Fatalf("problem fields = %q", got)
	}
	if problems := (ArrConfig{Type: "sonarr", BaseURL: "https://sonarr.example"}).Problems(); len(problems) != 1 || problems[0].Field != "api_key" {
		t.Fatalf("missing key: %+v", problems)
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
	if got := cfg.Legacy.Libraries["downloads"].Download.StableFor.Duration; got != DefaultStableFor {
		t.Fatalf("StableFor = %v", got)
	}
}

func TestReferenceConfigLoads(t *testing.T) {
	if _, err := Load(filepath.Join("..", "..", "examples", "anvil-reference.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestNativeSearchConfiguration(t *testing.T) {
	cfg, err := load(t, `[profiles.default-av1.video]
samples = 4
sample_duration = "10s"
ffmpeg_args = ["-svtav1-params", "tune=0"]
`)
	if err != nil {
		t.Fatal(err)
	}
	video := cfg.Legacy.Profiles[DefaultProfileName].ToDomain().Video
	if video.Samples != 4 || video.SampleDuration != 10*time.Second {
		t.Fatalf("video = %+v", video)
	}
	for _, field := range []string{`sample_duration = "0s"`, `ab_av1_args = []`, `ffmpeg_args = ["-crf", "20"]`} {
		if _, err := load(t, "[profiles.default-av1.video]\n"+field); err == nil {
			t.Fatalf("accepted %s", field)
		}
	}
	if _, err := load(t, "[profiles.default-av1.video.overrides.hevc]\nab_av1_args = []"); err == nil || !strings.Contains(err.Error(), "was removed") {
		t.Fatalf("migration error = %v", err)
	}
}

// An explicit store_path under /tmp or inside temp_dir is the only copy of the
// libraries and flows, so the daemon warns. The defaults never get there.
func TestStoreOnScratch(t *testing.T) {
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	for _, test := range []struct {
		name   string
		daemon DaemonConfig
		want   bool
	}{
		{name: "default as root", daemon: resolveDaemon(rawDaemonConfig{}, stateHost{euid: 0}), want: false},
		{name: "default as a user", daemon: resolveDaemon(rawDaemonConfig{}, stateHost{euid: 1000, home: fixedHome("/home/operator")}), want: false},
		{name: "file URI under the system temp", daemon: DaemonConfig{TempDir: "/var/cache/anvil", StorePath: "file:" + filepath.Join(scratch, "anvil.db") + "?_pragma=busy_timeout(5000)"}, want: true},
		{name: "in memory", daemon: DaemonConfig{TempDir: "/var/cache/anvil", StorePath: ":memory:"}, want: false},
		{name: "inside temp_dir", daemon: DaemonConfig{TempDir: "/var/cache/anvil", StorePath: "/var/cache/anvil/state/anvil.db"}, want: true},
		{name: "under the system temp", daemon: DaemonConfig{TempDir: "/var/cache/anvil", StorePath: filepath.Join(scratch, "anvil.db")}, want: true},
		{name: "persistent", daemon: DaemonConfig{TempDir: "/var/cache/anvil", StorePath: "/var/lib/anvil/anvil.db"}, want: false},
		{name: "sibling with a shared prefix", daemon: DaemonConfig{TempDir: "/var/cache/anvil", StorePath: "/var/cache/anvil-state/anvil.db"}, want: false},
	} {
		if got := test.daemon.StoreOnScratch(); got != test.want {
			t.Errorf("%s: StoreOnScratch() = %v for %q, want %v", test.name, got, test.daemon.StorePath, test.want)
		}
	}
}
