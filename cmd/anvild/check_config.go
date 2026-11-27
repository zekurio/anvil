package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/zekurio/anvil/internal/textout"
	"github.com/zekurio/anvil/pkg/config"
)

const redacted = "********"

func runCheckConfig(cfg config.Config, opts options) error {
	slog.Info("config ok", "config", configPathLabel(opts.configPath), "control_socket", cfg.Daemon.ControlSocket, "web_enabled", cfg.Web.Enabled, "web_listen", cfg.Web.Listen, "web_auth", cfg.Web.AuthRequired(), "log_level", cfg.Daemon.LogLevel, "legacy_sections", strings.Join(cfg.Legacy.Sections, ","))
	if !opts.showConfig {
		return nil
	}
	return writeEffectiveConfig(os.Stdout, cfg)
}

// writeEffectiveConfig prints what the daemon would run with: the [daemon]
// and [web] tables with defaults filled in. The legacy tables follow only
// when the file has them, under a note that they are read once and then
// ignored, so nobody takes this output for the live library settings.
func writeEffectiveConfig(out io.Writer, cfg config.Config) error {
	process := struct {
		Daemon config.DaemonConfig `toml:"daemon"`
		Web    config.WebConfig    `toml:"web"`
	}{Daemon: cfg.Daemon, Web: cfg.Web}
	if process.Web.Password != "" {
		process.Web.Password = redacted
	}
	if err := toml.NewEncoder(out).Encode(process); err != nil {
		return fmt.Errorf("encode effective config: %w", err)
	}
	if len(cfg.Legacy.Sections) == 0 {
		return nil
	}

	legacy := config.LegacyConfig{}
	if slices.Contains(cfg.Legacy.Sections, "arrs") {
		legacy.Arrs = make(map[string]config.ArrConfig, len(cfg.Legacy.Arrs))
		for name, arr := range cfg.Legacy.Arrs {
			if arr.APIKey != "" {
				arr.APIKey = redacted
			}
			legacy.Arrs[name] = arr
		}
	}
	if slices.Contains(cfg.Legacy.Sections, "profiles") {
		legacy.Profiles = cfg.Legacy.Profiles
	}
	if slices.Contains(cfg.Legacy.Sections, "libraries") {
		legacy.Libraries = cfg.Legacy.Libraries
	}
	if err := textout.Write(out, func(w *textout.Writer) {
		w.Println("")
		w.Println("# Import-only: the tables below are read once, by the first start of a daemon")
		w.Println("# whose store has never imported them, and become libraries, flows, and Arr")
		w.Println("# connections there. After that they are ignored; edit those in the web UI.")
	}); err != nil {
		return err
	}
	if err := toml.NewEncoder(out).Encode(legacy); err != nil {
		return fmt.Errorf("encode legacy config: %w", err)
	}
	return nil
}
