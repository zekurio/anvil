package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeConfig(t, `
[libraries.movies]
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
	if got := cfg.Profiles[DefaultProfileName].Audio.Fallback; got != DefaultStreamFallback {
		t.Fatalf("expected default audio fallback %q, got %q", DefaultStreamFallback, got)
	}
	if got := cfg.Profiles[DefaultProfileName].Video.MinSavingsPercent; got != DefaultMinSavingsPct {
		t.Fatalf("expected default min_savings_percent %d, got %v", DefaultMinSavingsPct, got)
	}
	if got := cfg.Profiles[DefaultProfileName].Video.Codec; got != "av1" {
		t.Fatalf("expected default video codec av1, got %q", got)
	}
	if got := cfg.Profiles[DefaultProfileName].Video.Accelerator; got != "software" {
		t.Fatalf("expected default video accelerator software, got %q", got)
	}
	if got := cfg.Profiles[DefaultProfileName].Video.BitDepth; got != 10 {
		t.Fatalf("expected default video bit_depth 10, got %d", got)
	}
	if got := cfg.Profiles[DefaultProfileName].Metadata.TrackTitles; got != DefaultTrackTitleMode {
		t.Fatalf("expected default metadata track_titles %q, got %q", DefaultTrackTitleMode, got)
	}
	if got := cfg.Daemon.ShutdownPolicy; got != DefaultShutdownPolicy {
		t.Fatalf("expected default shutdown policy %q, got %q", DefaultShutdownPolicy, got)
	}
	if got := cfg.Daemon.ShutdownTimeout; got != DefaultShutdownTimeout {
		t.Fatalf("expected default shutdown timeout %q, got %q", DefaultShutdownTimeout, got)
	}
	if got := cfg.Daemon.StagingCleanupAge; got != DefaultStagingCleanup {
		t.Fatalf("expected default staging cleanup age %q, got %q", DefaultStagingCleanup, got)
	}
	if got := cfg.Daemon.LogLevel; got != DefaultLogLevel {
		t.Fatalf("expected default log level %q, got %q", DefaultLogLevel, got)
	}
	if got := cfg.Daemon.FSDebounce; got != DefaultFSDebounce {
		t.Fatalf("expected default filesystem event debounce %q, got %q", DefaultFSDebounce, got)
	}
	if got := cfg.Libraries["movies"].Flow; got != DefaultFlowName {
		t.Fatalf("expected default flow %q, got %q", DefaultFlowName, got)
	}
	if got := cfg.Libraries["movies"].Profile; got != DefaultProfileName {
		t.Fatalf("expected default profile %q, got %q", DefaultProfileName, got)
	}
	if got := cfg.Libraries["movies"].Kind; got != DefaultLibraryKind {
		t.Fatalf("expected default library kind %q, got %q", DefaultLibraryKind, got)
	}
	flow := cfg.Flows[DefaultFlowName]
	if !containsString(flow.Steps, "stage") {
		t.Fatalf("default flow steps = %v, want stage before encode output is needed", flow.Steps)
	}
	if !containsString(flow.Steps, "crop-detect") {
		t.Fatalf("default flow steps = %v, want crop detection before CRF search", flow.Steps)
	}
	if !containsString(flow.Steps, "audio-cleanup") {
		t.Fatalf("default flow steps = %v, want audio cleanup before encode", flow.Steps)
	}
}

func TestLoadAcceptsDaemonLogLevels(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "debug", value: "debug", want: "debug"},
		{name: "info", value: "info", want: "info"},
		{name: "warn", value: "warn", want: "warn"},
		{name: "error", value: "error", want: "error"},
		{name: "trim and lower", value: " DEBUG ", want: "debug"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, `
[daemon]
log_level = "`+tt.value+`"

[libraries.movies]
path = "/srv/media/movies"
`)

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Daemon.LogLevel != tt.want {
				t.Fatalf("log level = %q, want %q", cfg.Daemon.LogLevel, tt.want)
			}
		})
	}
}

func TestLoadRejectsInvalidDaemonLogLevel(t *testing.T) {
	path := writeConfig(t, `
[daemon]
log_level = "verbose"

[libraries.movies]
path = "/srv/media/movies"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid log level")
	}
	if !strings.Contains(err.Error(), "daemon.log_level") {
		t.Fatalf("Load() error = %q, want daemon.log_level", err.Error())
	}
}

func TestLoadRejectsConcreteFFmpegEncoderAsVideoCodec(t *testing.T) {
	path := writeConfig(t, `
[profiles.default-av1.video]
codec = "av1_qsv"

[libraries.movies]
path = "/srv/media/movies"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid semantic video codec")
	}
	if !strings.Contains(err.Error(), "video.codec") {
		t.Fatalf("Load() error = %q, want video.codec", err.Error())
	}
}

func TestLoadRejectsInvalidVideoBitDepth(t *testing.T) {
	path := writeConfig(t, `
[profiles.default-av1.video]
bit_depth = 12

[libraries.movies]
path = "/srv/media/movies"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid bit depth")
	}
	if !strings.Contains(err.Error(), "video.bit_depth") {
		t.Fatalf("Load() error = %q, want video.bit_depth", err.Error())
	}
}

func TestLoadRejectsLegacyPixelFormatConfig(t *testing.T) {
	path := writeConfig(t, `
[profiles.default-av1.video]
pixel_format = "yuv420p10le"

[libraries.movies]
path = "/srv/media/movies"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want legacy pixel_format rejection")
	}
	if !strings.Contains(err.Error(), "unknown config keys") || !strings.Contains(err.Error(), "pixel_format") {
		t.Fatalf("Load() error = %q, want unknown pixel_format key", err.Error())
	}
}

func TestLoadDaemonFilesystemEventDebounce(t *testing.T) {
	path := writeConfig(t, `
	[daemon]
	filesystem_event_debounce = "750ms"

	[libraries.movies]
	path = "/srv/media/movies"
	`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.FilesystemEventDebounce().String(); got != "750ms" {
		t.Fatalf("filesystem event debounce = %q, want 750ms", got)
	}
}

func TestLoadRejectsInvalidDaemonFilesystemEventDebounce(t *testing.T) {
	path := writeConfig(t, `
	[daemon]
	filesystem_event_debounce = "-1s"

	[libraries.movies]
	path = "/srv/media/movies"
	`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid filesystem event debounce")
	}
	if !strings.Contains(err.Error(), "daemon.filesystem_event_debounce") {
		t.Fatalf("Load() error = %q, want daemon.filesystem_event_debounce", err.Error())
	}
}

func TestLoadLibraryScanIntervalOverride(t *testing.T) {
	path := writeConfig(t, `
[daemon]
scan_interval = "30m"

[libraries.movies]
path = "/srv/media/movies"
scan_interval = "5m"

[libraries.tv]
path = "/srv/media/tv"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.ScanIntervalForLibrary("movies").String(); got != "5m0s" {
		t.Fatalf("movies scan interval = %q, want 5m0s", got)
	}
	if got := cfg.ScanIntervalForLibrary("tv").String(); got != "30m0s" {
		t.Fatalf("tv scan interval = %q, want 30m0s", got)
	}
}

func TestLoadRejectsInvalidLibraryScanInterval(t *testing.T) {
	path := writeConfig(t, `
[libraries.movies]
path = "/srv/media/movies"
scan_interval = "0s"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid library scan interval")
	}
	if !strings.Contains(err.Error(), `library "movies" scan_interval`) {
		t.Fatalf("Load() error = %q, want library scan_interval", err.Error())
	}
}

func TestLoadLibraryIgnoreRegex(t *testing.T) {
	path := writeConfig(t, `
[libraries.movies]
path = "/srv/media/movies"
ignore_regex = ['(^|/)_UNPACK[^/]*(/|$)', '.*\.partial$']
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.Libraries["movies"].IgnoreRegex; !sameStrings(got, []string{`(^|/)_UNPACK[^/]*(/|$)`, `.*\.partial$`}) {
		t.Fatalf("ignore_regex = %v, want configured regexes", got)
	}
}

func TestLoadRejectsInvalidLibraryIgnoreRegex(t *testing.T) {
	path := writeConfig(t, `
[libraries.movies]
path = "/srv/media/movies"
ignore_regex = ["["]
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid ignore_regex")
	}
	if !strings.Contains(err.Error(), `library "movies" ignore_regex[0]`) {
		t.Fatalf("Load() error = %q, want library ignore_regex", err.Error())
	}
}

func TestLoadRejectsUnknownReferences(t *testing.T) {
	path := writeConfig(t, `
[libraries.movies]
path = "/srv/media/movies"
flow = "missing-flow"
profile = "missing-profile"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid references")
	}
}

func TestLoadMapsMinimumSavingsPolicyToDomainProfile(t *testing.T) {
	path := writeConfig(t, `
[profiles.default-av1.video]
min_savings_percent = 25
force_encode_on_no_fit = true

[libraries.movies]
path = "/srv/media/movies"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	_, _, profile, err := cfg.ResolveForLibrary("movies")
	if err != nil {
		t.Fatalf("ResolveForLibrary() error = %v", err)
	}
	if got, want := profile.Video.MinSavingsPercent, 25.0; got != want {
		t.Fatalf("MinSavingsPercent = %v, want %v", got, want)
	}
	if !profile.Video.ForceEncodeOnNoFit {
		t.Fatal("ForceEncodeOnNoFit = false, want true")
	}
}

func TestLoadMapsCustomVideoAndDolbyVisionOptionsToDomainProfile(t *testing.T) {
	path := writeConfig(t, `
[profiles.default-av1.video]
codec = "av1"
accelerator = "qsv"
bit_depth = 10
ffmpeg_args = ["-svtav1-params", "film-grain=8"]
ab_av1_args = ["--enc", "lookahead=120"]

[profiles.default-av1.video.dolby_vision]
mode = "auto"
codec = "hevc"
accelerator = "qsv"
preset = "medium"
bit_depth = 10
ffmpeg_args = ["-global_quality", "24"]
ab_av1_args = ["--enc", "low_power=1"]
remove_hdr10plus = true

[libraries.movies]
path = "/srv/media/movies"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	_, _, profile, err := cfg.ResolveForLibrary("movies")
	if err != nil {
		t.Fatalf("ResolveForLibrary() error = %v", err)
	}
	if got, want := profile.Video.FFmpegArgs, []string{"-svtav1-params", "film-grain=8"}; !sameStrings(got, want) {
		t.Fatalf("FFmpegArgs = %v, want %v", got, want)
	}
	if got, want := profile.Video.ABAV1Args, []string{"--enc", "lookahead=120"}; !sameStrings(got, want) {
		t.Fatalf("ABAV1Args = %v, want %v", got, want)
	}
	if got, want := profile.Video.Codec, "av1"; got != want {
		t.Fatalf("Video.Codec = %q, want %q", got, want)
	}
	if got, want := profile.Video.Accelerator, "qsv"; got != want {
		t.Fatalf("Video.Accelerator = %q, want %q", got, want)
	}
	if got, want := profile.Video.BitDepth, 10; got != want {
		t.Fatalf("Video.BitDepth = %d, want %d", got, want)
	}
	if got, want := profile.Video.DolbyVision.Codec, "hevc"; got != want {
		t.Fatalf("DolbyVision.Codec = %q, want %q", got, want)
	}
	if got, want := profile.Video.DolbyVision.Accelerator, "qsv"; got != want {
		t.Fatalf("DolbyVision.Accelerator = %q, want %q", got, want)
	}
	if got, want := profile.Video.DolbyVision.BitDepth, 10; got != want {
		t.Fatalf("DolbyVision.BitDepth = %d, want %d", got, want)
	}
	if got, want := profile.Video.DolbyVision.FFmpegArgs, []string{"-global_quality", "24"}; !sameStrings(got, want) {
		t.Fatalf("DolbyVision.FFmpegArgs = %v, want %v", got, want)
	}
	if got, want := profile.Video.DolbyVision.ABAV1Args, []string{"--enc", "low_power=1"}; !sameStrings(got, want) {
		t.Fatalf("DolbyVision.ABAV1Args = %v, want %v", got, want)
	}
	if !profile.Video.DolbyVision.RemoveHDR10Plus {
		t.Fatal("DolbyVision.RemoveHDR10Plus = false, want true")
	}
}

func TestLoadMapsMetadataTrackTitlePolicyToDomainProfile(t *testing.T) {
	path := writeConfig(t, `
	[profiles.default-av1.metadata]
	track_titles = "standardize"

	[libraries.movies]
	path = "/srv/media/movies"
	`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	_, _, profile, err := cfg.ResolveForLibrary("movies")
	if err != nil {
		t.Fatalf("ResolveForLibrary() error = %v", err)
	}
	if got, want := profile.Metadata.TrackTitles, domain.TrackTitleModeStandardize; got != want {
		t.Fatalf("Metadata.TrackTitles = %q, want %q", got, want)
	}
}

func TestLoadRejectsInvalidMetadataTrackTitlePolicy(t *testing.T) {
	path := writeConfig(t, `
	[profiles.default-av1.metadata]
	track_titles = "advertise"

	[libraries.movies]
	path = "/srv/media/movies"
	`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid metadata track_titles")
	}
	if !strings.Contains(err.Error(), "metadata.track_titles") {
		t.Fatalf("Load() error = %q, want metadata.track_titles", err.Error())
	}
}

func TestLoadRejectsInvalidDolbyVisionPolicy(t *testing.T) {
	path := writeConfig(t, `
[profiles.default-av1.video.dolby_vision]
mode = "require"

[libraries.movies]
path = "/srv/media/movies"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid Dolby Vision policy")
	}
	if !strings.Contains(err.Error(), "video.dolby_vision.codec") {
		t.Fatalf("Load() error = %q, want Dolby Vision codec", err.Error())
	}
}

func TestLoadRejectsNonMKVContainer(t *testing.T) {
	path := writeConfig(t, `
[profiles.default-av1]
container = "mp4"

[libraries.movies]
path = "/srv/media/movies"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid container")
	}
	if !strings.Contains(err.Error(), "outputs MKV only") {
		t.Fatalf("Load() error = %q, want MKV-only message", err.Error())
	}
}

func TestLoadRejectsInvalidMinimumSavingsPolicy(t *testing.T) {
	path := writeConfig(t, `
[profiles.default-av1.video]
min_savings_percent = 120

[libraries.movies]
path = "/srv/media/movies"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid min_savings_percent")
	}
}

func TestLoadRejectsNonPositiveDaemonDurations(t *testing.T) {
	path := writeConfig(t, `
[daemon]
scan_interval = "0s"
scheduler_interval = "-1s"
lease_duration = "0s"

[libraries.movies]
path = "/srv/media/movies"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid daemon durations")
	}
}

func TestLoadRejectsInvalidShutdownPolicy(t *testing.T) {
	path := writeConfig(t, `
[daemon]
shutdown_policy = "hibernate"
shutdown_timeout = "-1s"
staging_cleanup_age = "-1s"

[libraries.movies]
path = "/srv/media/movies"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid shutdown settings")
	}
}

func TestLoadProfileValidationConfig(t *testing.T) {
	path := writeConfig(t, `
[profiles.remux]
[profiles.remux.validation]
duration_tolerance_seconds = 1.5

[libraries.movies]
path = "/srv/media/movies"
profile = "remux"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.Profiles["remux"].Validation.DurationToleranceSeconds, 1.5; got != want {
		t.Fatalf("config validation duration tolerance = %f, want %f", got, want)
	}
	_, _, profile, err := cfg.ResolveForLibrary(domain.LibraryName("movies"))
	if err != nil {
		t.Fatalf("ResolveForLibrary() error = %v", err)
	}
	if got, want := profile.Validation.DurationToleranceSeconds, 1.5; got != want {
		t.Fatalf("domain validation duration tolerance = %f, want %f", got, want)
	}
}

func TestLoadRejectsInvalidValidationPolicy(t *testing.T) {
	path := writeConfig(t, `
[profiles.bad.validation]
duration_tolerance_seconds = -1

[libraries.movies]
path = "/srv/media/movies"
profile = "bad"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want invalid validation policy")
	}
}

func TestLoadDownloadLibrary(t *testing.T) {
	path := writeConfig(t, `
[flows.download-av1-handoff]
steps = ["probe", "crf-search", "encode", "handoff"]

[libraries.usenet-tv]
kind = "download"
path = "/downloads/complete/tv"
flow = "download-av1-handoff"

[libraries.usenet-tv.download]
handoff_path = "/imports/tv"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	library := cfg.Libraries["usenet-tv"]
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

func TestLoadArrReference(t *testing.T) {
	path := writeConfig(t, `
[arrs.main-radarr]
type = "radarr"
base_url = "http://radarr:7878"
api_key_file = "/run/secrets/radarr-api-key"

[libraries.movies]
path = "/srv/media/movies"
arr = "main-radarr"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	arr := cfg.Arrs["main-radarr"]
	if got, want := arr.Type, "radarr"; got != want {
		t.Fatalf("arr type = %q, want %q", got, want)
	}
	if got, want := arr.BaseURL, "http://radarr:7878"; got != want {
		t.Fatalf("arr base URL = %q, want %q", got, want)
	}
	if got, want := arr.APIKeyFile, "/run/secrets/radarr-api-key"; got != want {
		t.Fatalf("arr API key file = %q, want %q", got, want)
	}
	if got, want := cfg.Libraries["movies"].Arr, "main-radarr"; got != want {
		t.Fatalf("library arr = %q, want %q", got, want)
	}
}

func TestLoadRejectsArrWithoutConnectionDetails(t *testing.T) {
	path := writeConfig(t, `
[arrs.sonarr]
type = "sonarr"

[libraries.tv]
path = "/srv/media/tv"
arr = "sonarr"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want missing arr connection details")
	}
}

func TestLoadRejectsLibraryWithUnknownArr(t *testing.T) {
	path := writeConfig(t, `
[libraries.movies]
path = "/srv/media/movies"
arr = "missing"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want unknown arr reference")
	}
}

func TestLoadRejectsDownloadLibraryWithoutHandoffPath(t *testing.T) {
	path := writeConfig(t, `
[libraries.usenet-tv]
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

func sameStrings(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
