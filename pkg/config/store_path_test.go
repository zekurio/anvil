package config

import (
	"errors"
	"strings"
	"testing"
)

func fixedHome(home string) func() (string, error) {
	return func() (string, error) { return home, nil }
}

func noHome() (string, error) {
	return "", errors.New("$HOME is not defined")
}

func TestDefaultStorePath(t *testing.T) {
	for _, test := range []struct {
		name string
		host stateHost
		want string
	}{
		{name: "root", host: stateHost{euid: 0, xdgStateHome: "/home/root/.state", home: fixedHome("/root")}, want: "/var/lib/anvil/anvil.db"},
		{name: "root without a home", host: stateHost{euid: 0, home: noHome}, want: "/var/lib/anvil/anvil.db"},
		{name: "XDG_STATE_HOME", host: stateHost{euid: 1000, xdgStateHome: "/srv/state", home: noHome}, want: "/srv/state/anvil/anvil.db"},
		{name: "XDG_STATE_HOME unset", host: stateHost{euid: 1000, home: fixedHome("/home/operator")}, want: "/home/operator/.local/state/anvil/anvil.db"},
		{name: "XDG_STATE_HOME not absolute", host: stateHost{euid: 1000, xdgStateHome: "state", home: fixedHome("/home/operator")}, want: "/home/operator/.local/state/anvil/anvil.db"},
		{name: "no home", host: stateHost{euid: 1000, home: noHome}, want: ""},
		{name: "home not absolute", host: stateHost{euid: 1000, home: fixedHome("operator")}, want: ""},
	} {
		if got := test.host.defaultStorePath(); got != test.want {
			t.Errorf("%s: defaultStorePath() = %q, want %q", test.name, got, test.want)
		}
	}
}

// Without a home there is nowhere persistent to put the store, and a temporary
// directory is no substitute: loading fails and names the key to set.
func TestResolveWithoutHomeRequiresStorePath(t *testing.T) {
	cfg, err := resolve(rawConfig{}, stateHost{euid: 1000, home: noHome})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Daemon.StorePath != "" {
		t.Fatalf("StorePath = %q, want none", cfg.Daemon.StorePath)
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "daemon.store_path is required") {
		t.Fatalf("Validate() = %v, want a daemon.store_path error", err)
	}

	explicit, err := resolve(rawConfig{Daemon: rawDaemonConfig{StorePath: "/srv/anvil/anvil.db"}}, stateHost{euid: 1000, home: noHome})
	if err != nil {
		t.Fatal(err)
	}
	if err := explicit.Validate(); err != nil {
		t.Fatalf("explicit store_path: %v", err)
	}
}

func TestDefaultsKeepControlSocketOutOfStoreDirectory(t *testing.T) {
	for name, host := range map[string]stateHost{
		"root":           {euid: 0},
		"XDG_STATE_HOME": {euid: 1000, xdgStateHome: "/srv/state", home: noHome},
		"home":           {euid: 1000, home: fixedHome("/home/operator")},
	} {
		cfg, err := resolve(rawConfig{}, host)
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s: defaults do not validate: %v", name, err)
		}
	}
}

func TestValidateRefusesControlSocketInStoreDirectory(t *testing.T) {
	for _, test := range []struct {
		name   string
		store  string
		socket string
		refuse bool
	}{
		{name: "same directory", store: "/var/lib/anvil/anvil.db", socket: "/var/lib/anvil/anvild.sock", refuse: true},
		{name: "below the store directory", store: "/var/lib/anvil/anvil.db", socket: "/var/lib/anvil/run/anvild.sock", refuse: true},
		{name: "file URI", store: "file:/var/lib/anvil/anvil.db?_pragma=busy_timeout(5000)", socket: "/var/lib/anvil/anvild.sock", refuse: true},
		{name: "separate directory", store: "/var/lib/anvil/anvil.db", socket: "/run/anvil/anvild.sock"},
		{name: "parent of the store directory", store: "/var/lib/anvil/state/anvil.db", socket: "/var/lib/anvil/anvild.sock"},
		{name: "sibling with a shared prefix", store: "/var/lib/anvil/anvil.db", socket: "/var/lib/anvil-run/anvild.sock"},
		{name: "in memory", store: ":memory:", socket: "/var/lib/anvil/anvild.sock"},
	} {
		cfg := Default()
		cfg.Daemon.StorePath = test.store
		cfg.Daemon.ControlSocket = test.socket
		cfg.Daemon.TempDir = "/var/cache/anvil"
		err := cfg.Validate()
		refused := err != nil && strings.Contains(err.Error(), "inside the store's directory")
		if refused != test.refuse {
			t.Errorf("%s: Validate() = %v, want refused %v", test.name, err, test.refuse)
		}
		if refused && !strings.Contains(err.Error(), "put the socket in a directory of its own") {
			t.Errorf("%s: message does not say where to put the socket: %v", test.name, err)
		}
	}
}

func TestStoreFile(t *testing.T) {
	for _, test := range []struct {
		store string
		file  string
		ok    bool
	}{
		{store: "/var/lib/anvil/anvil.db", file: "/var/lib/anvil/anvil.db", ok: true},
		{store: " relative.db ", file: "relative.db", ok: true},
		{store: "file:/var/lib/anvil/anvil.db?_pragma=busy_timeout(5000)", file: "/var/lib/anvil/anvil.db", ok: true},
		{store: "file:relative.db", file: "relative.db", ok: true},
		{store: ":memory:"},
		{store: "file::memory:"},
		{store: "file:shared.db?mode=memory"},
		{store: "file:"},
		{store: ""},
	} {
		file, ok, err := StoreFile(test.store)
		if err != nil {
			t.Errorf("StoreFile(%q): %v", test.store, err)
			continue
		}
		if file != test.file || ok != test.ok {
			t.Errorf("StoreFile(%q) = %q, %v; want %q, %v", test.store, file, ok, test.file, test.ok)
		}
	}
	if _, _, err := StoreFile("file:anvil.db?%zz"); err == nil {
		t.Error("StoreFile accepted a malformed query")
	}
}
