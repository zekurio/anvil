package webapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/config"
)

func (s *Server) settings(http.ResponseWriter, *http.Request) (int, any, error) {
	return ok(s.settingsOf(s.Config()))
}

func (s *Server) settingsOf(cfg config.Config) Settings {
	keys := make(map[string]DaemonKey)
	for _, key := range config.DaemonKeys() {
		source := cfg.DaemonSource(key.Key)
		keys[key.Key] = DaemonKey{
			Source:   source,
			Editable: editableSource(source),
			AtStart:  key.AtStart,
			// Never null: the UI lists them.
			Choices: append([]string{}, key.Choices...),
		}
	}
	return Settings{
		Version:    s.Version,
		ConfigPath: s.ConfigPath,
		Daemon: DaemonSettings{
			MediaRoots:    append([]string{}, cfg.Daemon.MediaRoots...),
			TempDir:       cfg.Daemon.TempDir,
			StorePath:     cfg.Daemon.StorePath,
			ControlSocket: cfg.Daemon.ControlSocket,
			DaemonValues:  daemonValuesOf(cfg.Daemon),
			Defaults:      daemonValuesOf(config.DefaultDaemon()),
			Keys:          keys,
		},
		Web: WebSettings{Listen: cfg.Web.Listen, AuthRequired: s.auth.required()},
	}
}

// editableSource reports whether the web UI may change a key whose value
// comes from source. The file and the command line pin what they set.
func editableSource(source string) bool {
	return source != config.SourceFile && source != config.SourceFlag
}

func daemonValuesOf(daemon config.DaemonConfig) DaemonValues {
	return DaemonValues{
		WorkerCount:             daemon.WorkerCount,
		TotalThreads:            daemon.TotalThreads,
		MaxThreadsPerJob:        daemon.MaxThreadsPerJob,
		MaxAttempts:             daemon.MaxAttempts,
		ScanInterval:            durationText(daemon.ScanInterval.Duration),
		FilesystemEventDebounce: durationText(daemon.FSDebounce.Duration),
		SchedulerInterval:       durationText(daemon.SchedulerInterval.Duration),
		LeaseDuration:           durationText(daemon.LeaseDuration.Duration),
		ShutdownPolicy:          daemon.ShutdownPolicy,
		ShutdownTimeout:         durationText(daemon.ShutdownTimeout.Duration),
		StagingCleanupAge:       durationText(daemon.StagingCleanupAge.Duration),
		LogLevel:                daemon.LogLevel,
	}
}

// updateDaemonSettings replaces the daemon settings saved in the web UI and
// applies them the way a reload does: the store is written, the runtime
// snapshot is rebuilt from anvil.toml and the store, and the scanner and the
// scheduler are woken, all before the answer.
func (s *Server) updateDaemonSettings(_ http.ResponseWriter, r *http.Request) (int, any, error) {
	var write DaemonSettingsWrite
	if err := decodeBody(r, &write); err != nil {
		return fail(err)
	}
	overrides, err := daemonOverrides(write)
	if err != nil {
		return fail(err)
	}

	s.mutations.Lock()
	defer s.mutations.Unlock()
	cfg := s.Config()
	// media_roots is not among the keys at all: a signed-in browser must
	// never widen its own confinement. A key the file or an option sets is
	// theirs, so a declarative setup stays the authority over it.
	for _, key := range overrides.Keys() {
		switch cfg.DaemonSource(key) {
		case config.SourceFile:
			return fail(invalidf(key, "This key is set in anvil.toml, so only the file can change it. Remove it from the file to manage it here."))
		case config.SourceFlag:
			return fail(invalidf(key, "This key is set by an option on the anvild command line, so it cannot be changed here."))
		}
	}
	if err := fieldProblem(overrides.Problems()); err != nil {
		return fail(err)
	}
	if err := s.Store.SetDaemonSettings(r.Context(), overrides); err != nil {
		return fail(err)
	}
	if err := s.refresh(r.Context()); err != nil {
		return fail(err)
	}
	return ok(s.settingsOf(s.Config()))
}

// daemonOverrides converts a request body into the values the store keeps.
// An empty string is the same as null. It reports the first unreadable
// duration; every other rule is checked by config.DaemonOverrides.Problems.
func daemonOverrides(write DaemonSettingsWrite) (config.DaemonOverrides, error) {
	overrides := config.DaemonOverrides{
		WorkerCount:      write.WorkerCount,
		TotalThreads:     write.TotalThreads,
		MaxThreadsPerJob: write.MaxThreadsPerJob,
		MaxAttempts:      write.MaxAttempts,
		ShutdownPolicy:   trimmedText(write.ShutdownPolicy),
		LogLevel:         trimmedText(write.LogLevel),
	}
	for _, duration := range []struct {
		field  string
		text   *string
		target **config.Duration
	}{
		{"scan_interval", write.ScanInterval, &overrides.ScanInterval},
		{"filesystem_event_debounce", write.FilesystemEventDebounce, &overrides.FSDebounce},
		{"scheduler_interval", write.SchedulerInterval, &overrides.SchedulerInterval},
		{"lease_duration", write.LeaseDuration, &overrides.LeaseDuration},
		{"shutdown_timeout", write.ShutdownTimeout, &overrides.ShutdownTimeout},
		{"staging_cleanup_age", write.StagingCleanupAge, &overrides.StagingCleanupAge},
	} {
		text := trimmedText(duration.text)
		if text == nil {
			continue
		}
		value, err := time.ParseDuration(*text)
		if err != nil {
			return config.DaemonOverrides{}, invalidf(duration.field, "%q is not a duration. Write one such as \"30m\", \"90s\", or \"2h\".", *text)
		}
		*duration.target = &config.Duration{Duration: value}
	}
	return overrides, nil
}

// trimmedText trims a text field and turns an empty one into nil.
func trimmedText(text *string) *string {
	if text == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*text)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
