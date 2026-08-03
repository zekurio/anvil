package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/zekurio/anvil/pkg/config"
)

func runCheckConfig(cfg config.Config, opts options) error {
	slog.Info("config ok", "config", configPathLabel(opts.configPath), "libraries", len(cfg.Libraries), "flows", len(cfg.Flows), "profiles", len(cfg.Profiles), "control_socket", cfg.Daemon.ControlSocket, "log_level", cfg.Daemon.LogLevel)
	if !opts.showConfig {
		return nil
	}
	if err := toml.NewEncoder(os.Stdout).Encode(redactedConfig(cfg)); err != nil {
		return fmt.Errorf("encode effective config: %w", err)
	}
	return nil
}

func redactedConfig(cfg config.Config) config.Config {
	redacted := cfg
	redacted.Arrs = make(map[string]config.ArrConfig, len(cfg.Arrs))
	for name, arr := range cfg.Arrs {
		if arr.APIKey != "" {
			arr.APIKey = "********"
		}
		redacted.Arrs[name] = arr
	}
	return redacted
}
