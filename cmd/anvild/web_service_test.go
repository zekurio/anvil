package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/config"
)

// A start that cannot have its web UI must fail before the store is opened:
// opening creates or migrates it, and a misconfigured start should leave no
// trace there.
func TestDaemonSettlesTheWebUIBeforeItOpensTheStore(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := taken.Close(); err != nil {
			t.Error(err)
		}
	}()
	empty := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		web  config.WebConfig
		want string
	}{
		{"address in use", config.WebConfig{Enabled: true, Listen: taken.Addr().String()}, "the web UI cannot listen on " + taken.Addr().String()},
		{"empty password file", config.WebConfig{Enabled: true, Listen: "127.0.0.1:0", PasswordFile: empty}, "is empty"},
		{"unreadable password file", config.WebConfig{Enabled: true, Listen: "127.0.0.1:0", PasswordFile: empty + ".missing"}, "read web.password_file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// A short directory: a Unix socket path has a length limit.
			dir, err := os.MkdirTemp("", "anvil")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.RemoveAll(dir); err != nil {
					t.Error(err)
				}
			}()
			cfg := config.Config{Web: test.web}
			cfg.Daemon.StorePath = filepath.Join(dir, "anvil.db")
			cfg.Daemon.ControlSocket = filepath.Join(dir, "anvil.sock")
			cfg.Daemon.TempDir = filepath.Join(dir, "tmp")

			err = runDaemon(context.Background(), cfg, options{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("runDaemon = %v, want an error containing %q", err, test.want)
			}
			if _, err := os.Stat(cfg.Daemon.StorePath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the store was opened before the web UI was settled: stat = %v", err)
			}
			// The claims that come first were made and given back.
			if _, err := os.Stat(cfg.Daemon.ControlSocket); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the control socket was left behind: stat = %v", err)
			}
		})
	}
}

func TestClaimWebBindsWithoutServing(t *testing.T) {
	claim, err := claimWeb(context.Background(), config.Config{Web: config.WebConfig{Enabled: true, Listen: "127.0.0.1:0", Password: "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if claim.password != "secret" {
		t.Errorf("password = %q", claim.password)
	}
	// The address is taken from now on, which is the point of claiming it.
	if second, err := net.Listen("tcp", claim.listener.Addr().String()); err == nil {
		_ = second.Close() //nolint:errcheck // the test has failed already
		t.Error("the claimed address could be bound a second time")
	}
	if err := claim.release(); err != nil {
		t.Fatal(err)
	}
	// Releasing a listener the server has already closed is not an error.
	if err := claim.release(); err != nil {
		t.Errorf("second release = %v", err)
	}
}
