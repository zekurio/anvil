package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Load reads a TOML configuration file, applies defaults, and validates it.
func Load(path string) (Config, error) {
	var raw rawConfig
	if path != "" {
		meta, err := toml.DecodeFile(path, &raw)
		if err != nil {
			return Config{}, fmt.Errorf("load config %q: %w", path, err)
		}
		if undecoded := meta.Undecoded(); len(undecoded) > 0 {
			if hint := videoMigrationHint(undecoded); hint != "" {
				return Config{}, fmt.Errorf("load config %q: unknown config keys: %s; %s", path, formatUndecodedKeys(undecoded), hint)
			}
			return Config{}, fmt.Errorf("load config %q: unknown config keys: %s", path, formatUndecodedKeys(undecoded))
		}
	}

	cfg, err := resolve(raw)
	if err != nil {
		return Config{}, fmt.Errorf("invalid config:\n- %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func videoMigrationHint(keys []toml.Key) string {
	for _, key := range keys {
		if len(key) >= 4 && key[0] == "profiles" && key[2] == "video" && key[len(key)-1] == "ab_av1_args" {
			return "ab_av1_args was removed with the native CRF search; remove the key, move encoder options to ffmpeg_args and sampling settings to samples/sample_duration"
		}
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
