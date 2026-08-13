package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Load reads a TOML configuration file, applies defaults, and validates it.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		meta, err := toml.DecodeFile(path, &cfg)
		if err != nil {
			return Config{}, fmt.Errorf("load config %q: %w", path, err)
		}
		if undecoded := meta.Undecoded(); len(undecoded) > 0 {
			if hint := qualityTargetMigrationHint(undecoded); hint != "" {
				return Config{}, fmt.Errorf("load config %q: unknown config keys: %s; %s", path, formatUndecodedKeys(undecoded), hint)
			}
			return Config{}, fmt.Errorf("load config %q: unknown config keys: %s", path, formatUndecodedKeys(undecoded))
		}
		// Load starts from Default(), which already derived store_path and
		// control_socket from the default temp_dir. A file that sets only
		// temp_dir would otherwise keep those stale derivations, silently
		// ignoring the documented "defaults to temp_dir/..." cascade.
		if meta.IsDefined("daemon", "temp_dir") {
			if !meta.IsDefined("daemon", "store_path") {
				cfg.Daemon.StorePath = filepath.Join(cfg.Daemon.TempDir, "anvil.db")
			}
			if !meta.IsDefined("daemon", "control_socket") {
				cfg.Daemon.ControlSocket = filepath.Join(cfg.Daemon.TempDir, "anvild.sock")
			}
		}
	}

	var overrideKeyProblems []string
	for _, name := range sortedKeys(cfg.Profiles) {
		profile := cfg.Profiles[name]
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		overrideKeyProblems = append(overrideKeyProblems, videoOverrideKeyProblems(name, profile.Video.Overrides)...)
	}
	if len(overrideKeyProblems) > 0 {
		return Config{}, errors.New("invalid config: " + strings.Join(overrideKeyProblems, "; "))
	}

	applyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func qualityTargetMigrationHint(keys []toml.Key) string {
	for _, key := range keys {
		if len(key) >= 4 && key[0] == "profiles" && key[2] == "video" && key[len(key)-1] == "target_vmaf" {
			return "target_vmaf was replaced by metric and target; use metric = \"vmaf\" and target = 95"
		}
	}
	return ""
}

func formatUndecodedKeys(keys []toml.Key) string {
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key.String())
	}
	sort.Strings(values)
	return strings.Join(values, ", ")
}
