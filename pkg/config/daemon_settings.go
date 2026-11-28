package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Where the value of a DaemonKeys key comes from, in order of precedence. A
// key set in anvil.toml or by a command-line option of anvild is pinned: the
// web UI shows it but cannot change it, so a NixOS module or any other
// declarative setup stays the authority over what it sets.
const (
	SourceFlag    = "flag"
	SourceFile    = "file"
	SourceStore   = "store"
	SourceDefault = "default"
)

// DaemonOverrides holds the [daemon] values saved in the web UI. A nil field
// was not saved and leaves the key to anvil.toml or its default. The JSON
// names are the TOML keys, and the store keeps the value in this shape.
type DaemonOverrides struct {
	WorkerCount       *int      `json:"worker_count,omitempty"`
	TotalThreads      *int      `json:"total_threads,omitempty"`
	MaxThreadsPerJob  *int      `json:"max_threads_per_job,omitempty"`
	MaxAttempts       *int      `json:"max_attempts,omitempty"`
	ScanInterval      *Duration `json:"scan_interval,omitempty"`
	FSDebounce        *Duration `json:"filesystem_event_debounce,omitempty"`
	SchedulerInterval *Duration `json:"scheduler_interval,omitempty"`
	LeaseDuration     *Duration `json:"lease_duration,omitempty"`
	ShutdownPolicy    *string   `json:"shutdown_policy,omitempty"`
	ShutdownTimeout   *Duration `json:"shutdown_timeout,omitempty"`
	StagingCleanupAge *Duration `json:"staging_cleanup_age,omitempty"`
	LogLevel          *string   `json:"log_level,omitempty"`
}

// DaemonKey is one [daemon] key the web UI can change.
type DaemonKey struct {
	// Key is the TOML key, e.g. "worker_count".
	Key string
	// Choices lists the values an enumerated key accepts; nil otherwise.
	Choices []string
	// AtStart marks a key the daemon acts on only when it starts. A new
	// value is in the snapshot at once but does its work at the next start.
	AtStart bool

	// inFile reports whether anvil.toml sets the key.
	inFile func(rawDaemonConfig) bool
	// apply copies the saved value into d and reports whether there was one.
	apply func(DaemonOverrides, *DaemonConfig) bool
}

// daemonKeys lists the keys in schema order. Every other [daemon] key is
// file-only: media_roots confines what a signed-in browser may name, so the
// browser must never be able to widen it, and the paths cannot change while
// the daemon runs.
var daemonKeys = []DaemonKey{
	{Key: "worker_count", inFile: func(r rawDaemonConfig) bool { return r.WorkerCount != nil }, apply: func(o DaemonOverrides, d *DaemonConfig) bool { return assign(&d.WorkerCount, o.WorkerCount) }},
	{Key: "total_threads", inFile: func(r rawDaemonConfig) bool { return r.TotalThreads != nil }, apply: func(o DaemonOverrides, d *DaemonConfig) bool { return assign(&d.TotalThreads, o.TotalThreads) }},
	{Key: "max_threads_per_job", inFile: func(r rawDaemonConfig) bool { return r.MaxThreadsPerJob != nil }, apply: func(o DaemonOverrides, d *DaemonConfig) bool { return assign(&d.MaxThreadsPerJob, o.MaxThreadsPerJob) }},
	{Key: "max_attempts", inFile: func(r rawDaemonConfig) bool { return r.MaxAttempts != nil }, apply: func(o DaemonOverrides, d *DaemonConfig) bool { return assign(&d.MaxAttempts, o.MaxAttempts) }},
	{Key: "scan_interval", inFile: func(r rawDaemonConfig) bool { return r.ScanInterval != nil }, apply: func(o DaemonOverrides, d *DaemonConfig) bool { return assign(&d.ScanInterval, o.ScanInterval) }},
	{Key: "filesystem_event_debounce", inFile: func(r rawDaemonConfig) bool { return r.FSDebounce != nil }, apply: func(o DaemonOverrides, d *DaemonConfig) bool { return assign(&d.FSDebounce, o.FSDebounce) }},
	{Key: "scheduler_interval", inFile: func(r rawDaemonConfig) bool { return r.SchedulerInterval != nil }, apply: func(o DaemonOverrides, d *DaemonConfig) bool {
		return assign(&d.SchedulerInterval, o.SchedulerInterval)
	}},
	{Key: "lease_duration", inFile: func(r rawDaemonConfig) bool { return r.LeaseDuration != nil }, apply: func(o DaemonOverrides, d *DaemonConfig) bool { return assign(&d.LeaseDuration, o.LeaseDuration) }},
	{Key: "shutdown_policy", Choices: shutdownPolicies, inFile: func(r rawDaemonConfig) bool { return strings.TrimSpace(r.ShutdownPolicy) != "" }, apply: func(o DaemonOverrides, d *DaemonConfig) bool { return assign(&d.ShutdownPolicy, o.ShutdownPolicy) }},
	{Key: "shutdown_timeout", inFile: func(r rawDaemonConfig) bool { return r.ShutdownTimeout != nil }, apply: func(o DaemonOverrides, d *DaemonConfig) bool { return assign(&d.ShutdownTimeout, o.ShutdownTimeout) }},
	// The start-up sweep is the one use of the age that is not live; the
	// default of anvilctl staging cleanup follows the snapshot.
	{Key: "staging_cleanup_age", AtStart: true, inFile: func(r rawDaemonConfig) bool { return r.StagingCleanupAge != nil }, apply: func(o DaemonOverrides, d *DaemonConfig) bool {
		return assign(&d.StagingCleanupAge, o.StagingCleanupAge)
	}},
	{Key: "log_level", Choices: logLevels, inFile: func(r rawDaemonConfig) bool { return strings.TrimSpace(r.LogLevel) != "" }, apply: func(o DaemonOverrides, d *DaemonConfig) bool {
		if o.LogLevel == nil {
			return false
		}
		d.LogLevel = strings.TrimSpace(*o.LogLevel)
		if normalized, ok := NormalizeLogLevel(d.LogLevel); ok {
			d.LogLevel = normalized
		}
		return true
	}},
}

// DaemonKeys lists the [daemon] keys the web UI can change, in schema order.
func DaemonKeys() []DaemonKey {
	keys := slices.Clone(daemonKeys)
	for i := range keys {
		keys[i].Choices = slices.Clone(keys[i].Choices)
	}
	return keys
}

func assign[T any](target *T, value *T) bool {
	if value == nil {
		return false
	}
	*target = *value
	return true
}

// fileDaemonSources records the DaemonKeys keys anvil.toml sets.
func fileDaemonSources(raw rawDaemonConfig) map[string]string {
	sources := make(map[string]string, len(daemonKeys))
	for _, key := range daemonKeys {
		if key.inFile(raw) {
			sources[key.Key] = SourceFile
		}
	}
	return sources
}

// DefaultDaemon returns the [daemon] table of an empty anvil.toml.
func DefaultDaemon() DaemonConfig {
	return resolveDaemon(rawDaemonConfig{}, processStateHost())
}

// DaemonSource says where the value of a DaemonKeys key comes from: one of
// the Source constants.
func (c Config) DaemonSource(key string) string {
	if source, ok := c.DaemonSources[key]; ok {
		return source
	}
	return SourceDefault
}

// Keys lists the keys that hold a saved value, in schema order.
func (o DaemonOverrides) Keys() []string {
	var keys []string
	for _, key := range daemonKeys {
		var probe DaemonConfig
		if key.apply(o, &probe) {
			keys = append(keys, key.Key)
		}
	}
	return keys
}

// Problems reports every rule a saved value breaks, worded for the operator
// and attributed to its key. The rules are the ones Validate applies to the
// file, so the two can never disagree.
func (o DaemonOverrides) Problems() []FieldProblem {
	// The defaults break no rule, so every problem is one of a saved value.
	daemon := DefaultDaemon()
	for _, key := range daemonKeys {
		key.apply(o, &daemon)
	}
	var found []FieldProblem
	for _, problem := range daemon.tunableProblems() {
		found = append(found, FieldProblem{Field: problem.field, Message: problem.message})
	}
	return found
}

// WithDaemonOverrides returns c with the values saved in the web UI for every
// key neither anvil.toml nor a command-line option sets. A saved value for a
// pinned key stays in the store unused, and applies again once the file no
// longer sets the key.
func (c Config) WithDaemonOverrides(o DaemonOverrides) (Config, error) {
	sources := make(map[string]string, len(daemonKeys))
	maps.Copy(sources, c.DaemonSources)
	for _, key := range daemonKeys {
		switch sources[key.Key] {
		case SourceFile, SourceFlag:
			continue
		}
		if key.apply(o, &c.Daemon) {
			sources[key.Key] = SourceStore
		}
	}
	c.DaemonSources = sources
	var p problems
	for _, problem := range c.Daemon.tunableProblems() {
		p.addf("daemon.%s", problem.text)
	}
	if len(p) > 0 {
		return Config{}, errors.New("invalid daemon settings saved in the web UI (set the key in anvil.toml to override one):\n- " + strings.Join(p, "\n- "))
	}
	return c, nil
}

// daemonProblem is one rule a DaemonKeys value breaks, in two wordings:
// message is a sentence for the operator in the web UI, and text is
// check-config's, without the "daemon." prefix.
type daemonProblem struct {
	field   string
	message string
	text    string
}

func daemonProblemf(field, message, format string, args ...any) daemonProblem {
	return daemonProblem{field: field, message: message, text: fmt.Sprintf(format, args...)}
}

// tunableProblems checks the DaemonKeys keys.
func (d DaemonConfig) tunableProblems() []daemonProblem {
	var found []daemonProblem
	if d.WorkerCount < 1 {
		found = append(found, daemonProblemf("worker_count", "Enter a whole number of 1 or more.", "worker_count must be at least 1"))
	}
	if d.TotalThreads < 0 {
		found = append(found, daemonProblemf("total_threads", "Enter a whole number of 0 or more.", "total_threads must be non-negative"))
	}
	if d.MaxThreadsPerJob < 0 {
		found = append(found, daemonProblemf("max_threads_per_job", "Enter 0 to share total_threads across the workers, or a whole number above 0.", "max_threads_per_job must be non-negative"))
	}
	if d.MaxAttempts < 1 {
		found = append(found, daemonProblemf("max_attempts", "Enter a whole number of 1 or more.", "max_attempts must be at least 1"))
	}
	for _, duration := range []struct {
		key      string
		value    Duration
		positive bool
	}{
		{"scan_interval", d.ScanInterval, true},
		{"filesystem_event_debounce", d.FSDebounce, false},
		{"scheduler_interval", d.SchedulerInterval, true},
		{"lease_duration", d.LeaseDuration, true},
		{"shutdown_timeout", d.ShutdownTimeout, false},
		{"staging_cleanup_age", d.StagingCleanupAge, false},
	} {
		switch {
		case duration.positive && duration.value.Duration <= 0:
			found = append(found, daemonProblemf(duration.key, "Enter a duration above zero, such as \"30s\" or \"10m\".", "%s must be greater than zero", duration.key))
		case duration.value.Duration < 0:
			found = append(found, daemonProblemf(duration.key, "Enter a duration of zero or more, such as \"0s\" or \"10m\".", "%s must be non-negative", duration.key))
		}
	}
	if !slices.Contains(shutdownPolicies, d.ShutdownPolicy) {
		found = append(found, daemonProblemf("shutdown_policy", fmt.Sprintf("Choose drain or cancel, not %q.", d.ShutdownPolicy), "shutdown_policy %q is invalid", d.ShutdownPolicy))
	}
	if _, ok := NormalizeLogLevel(d.LogLevel); !ok {
		found = append(found, daemonProblemf("log_level", fmt.Sprintf("Choose debug, info, warn, or error, not %q.", d.LogLevel), "log_level %q is invalid (must be debug, info, warn, or error)", d.LogLevel))
	}
	return found
}
