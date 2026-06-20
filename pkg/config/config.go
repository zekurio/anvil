package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	DefaultFlowName    = "av1-crf-search"
	DefaultProfileName = "default-av1"
)

// Config is the top-level Anvil daemon configuration.
type Config struct {
	Daemon    DaemonConfig    `toml:"daemon"`
	Flows     []FlowConfig    `toml:"flows"`
	Profiles  []ProfileConfig `toml:"profiles"`
	Libraries []LibraryConfig `toml:"libraries"`
}

// DaemonConfig contains process-wide runtime settings.
type DaemonConfig struct {
	TempDir     string `toml:"temp_dir"`
	WorkerCount int    `toml:"worker_count"`
	LogLevel    string `toml:"log_level"`
}

// FlowConfig names an orchestration flow. The steps are declarative for now.
type FlowConfig struct {
	Name  string   `toml:"name"`
	Steps []string `toml:"steps"`
}

// ProfileConfig groups encode settings that libraries can reference.
type ProfileConfig struct {
	Name  string      `toml:"name"`
	Video VideoConfig `toml:"video"`
}

// VideoConfig contains the initial video settings shape for AV1 search work.
type VideoConfig struct {
	Codec       string  `toml:"codec"`
	Preset      string  `toml:"preset"`
	PixelFormat string  `toml:"pixel_format"`
	CRFMin      int     `toml:"crf_min"`
	CRFMax      int     `toml:"crf_max"`
	TargetVMAF  float64 `toml:"target_vmaf"`
}

// LibraryConfig describes a user-defined media library.
type LibraryConfig struct {
	Name    string   `toml:"name"`
	Path    string   `toml:"path"`
	Flow    string   `toml:"flow"`
	Profile string   `toml:"profile"`
	Include []string `toml:"include"`
	Exclude []string `toml:"exclude"`
}

// Load reads a TOML configuration file, applies defaults, and validates it.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return Config{}, fmt.Errorf("load config %q: %w", path, err)
		}
	}

	applyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Default returns a valid minimal configuration.
func Default() Config {
	return Config{
		Daemon: DaemonConfig{
			TempDir:     filepath.Join(os.TempDir(), "anvil"),
			WorkerCount: max(runtime.NumCPU(), 1),
			LogLevel:    "info",
		},
		Flows: []FlowConfig{
			{
				Name:  DefaultFlowName,
				Steps: []string{"probe", "crf-search", "encode"},
			},
		},
		Profiles: []ProfileConfig{
			{
				Name: DefaultProfileName,
				Video: VideoConfig{
					Codec:       "libsvtav1",
					Preset:      "6",
					PixelFormat: "yuv420p10le",
					CRFMin:      18,
					CRFMax:      40,
					TargetVMAF:  95,
				},
			},
		},
	}
}

// Validate checks that the configuration is internally consistent.
func (c Config) Validate() error {
	var problems []string

	if strings.TrimSpace(c.Daemon.TempDir) == "" {
		problems = append(problems, "daemon.temp_dir is required")
	}
	if c.Daemon.WorkerCount < 1 {
		problems = append(problems, "daemon.worker_count must be at least 1")
	}

	flows := make(map[string]struct{}, len(c.Flows))
	for i, flow := range c.Flows {
		name := strings.TrimSpace(flow.Name)
		if name == "" {
			problems = append(problems, fmt.Sprintf("flows[%d].name is required", i))
			continue
		}
		if _, exists := flows[name]; exists {
			problems = append(problems, fmt.Sprintf("duplicate flow %q", name))
			continue
		}
		flows[name] = struct{}{}
	}

	profiles := make(map[string]struct{}, len(c.Profiles))
	for i, profile := range c.Profiles {
		name := strings.TrimSpace(profile.Name)
		if name == "" {
			problems = append(problems, fmt.Sprintf("profiles[%d].name is required", i))
			continue
		}
		if _, exists := profiles[name]; exists {
			problems = append(problems, fmt.Sprintf("duplicate profile %q", name))
			continue
		}
		profiles[name] = struct{}{}

		if profile.Video.CRFMin < 0 || profile.Video.CRFMax < 0 {
			problems = append(problems, fmt.Sprintf("profile %q CRF values must be non-negative", name))
		}
		if profile.Video.CRFMin > profile.Video.CRFMax {
			problems = append(problems, fmt.Sprintf("profile %q crf_min must be less than or equal to crf_max", name))
		}
		if profile.Video.TargetVMAF < 0 || profile.Video.TargetVMAF > 100 {
			problems = append(problems, fmt.Sprintf("profile %q target_vmaf must be between 0 and 100", name))
		}
	}

	libraries := make(map[string]struct{}, len(c.Libraries))
	for i, library := range c.Libraries {
		name := strings.TrimSpace(library.Name)
		if name == "" {
			problems = append(problems, fmt.Sprintf("libraries[%d].name is required", i))
			continue
		}
		if _, exists := libraries[name]; exists {
			problems = append(problems, fmt.Sprintf("duplicate library %q", name))
			continue
		}
		libraries[name] = struct{}{}

		if strings.TrimSpace(library.Path) == "" {
			problems = append(problems, fmt.Sprintf("library %q path is required", name))
		}
		if _, exists := flows[library.Flow]; !exists {
			problems = append(problems, fmt.Sprintf("library %q references unknown flow %q", name, library.Flow))
		}
		if _, exists := profiles[library.Profile]; !exists {
			problems = append(problems, fmt.Sprintf("library %q references unknown profile %q", name, library.Profile))
		}
	}

	if len(problems) > 0 {
		return errors.New("invalid config: " + strings.Join(problems, "; "))
	}

	return nil
}

func applyDefaults(c *Config) {
	defaults := Default()

	if strings.TrimSpace(c.Daemon.TempDir) == "" {
		c.Daemon.TempDir = defaults.Daemon.TempDir
	}
	if c.Daemon.WorkerCount == 0 {
		c.Daemon.WorkerCount = defaults.Daemon.WorkerCount
	}
	if strings.TrimSpace(c.Daemon.LogLevel) == "" {
		c.Daemon.LogLevel = defaults.Daemon.LogLevel
	}
	if len(c.Flows) == 0 {
		c.Flows = defaults.Flows
	}
	if len(c.Profiles) == 0 {
		c.Profiles = defaults.Profiles
	}

	for i := range c.Libraries {
		if strings.TrimSpace(c.Libraries[i].Flow) == "" {
			c.Libraries[i].Flow = DefaultFlowName
		}
		if strings.TrimSpace(c.Libraries[i].Profile) == "" {
			c.Libraries[i].Profile = DefaultProfileName
		}
	}
}
