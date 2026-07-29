package main

import "testing"

// TestStoreLockPathResolvesEveryOnDiskStore is the singleton guard's real
// requirement. A "file:" DSN is how an operator sets SQLite pragmas, and it
// used to be treated as a store with no filesystem identity — so a second
// daemon started, recovered stale jobs, and swept staging directories out from
// under the running one before it ever noticed the live control socket.
func TestStoreLockPathResolvesEveryOnDiskStore(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		lockPath string
		lockable bool
	}{
		{name: "plain path", path: "/var/lib/anvil/anvil.db", lockPath: "/var/lib/anvil/anvil.db.lock", lockable: true},
		{name: "relative path", path: "state/anvil.db", lockPath: "state/anvil.db.lock", lockable: true},
		{name: "absolute uri", path: "file:/var/lib/anvil/anvil.db", lockPath: "/var/lib/anvil/anvil.db.lock", lockable: true},
		{name: "relative uri", path: "file:state/anvil.db", lockPath: "state/anvil.db.lock", lockable: true},
		{
			name:     "uri with pragmas",
			path:     "file:/var/lib/anvil/anvil.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
			lockPath: "/var/lib/anvil/anvil.db.lock",
			lockable: true,
		},
		{
			name:     "percent-encoded uri",
			path:     "file:/var/lib/anvil/my%20store.db",
			lockPath: "/var/lib/anvil/my store.db.lock",
			lockable: true,
		},
		{name: "empty", path: "  "},
		{name: "memory", path: ":memory:"},
		{name: "memory uri", path: "file:anvil.db?mode=memory&cache=shared"},
		{name: "memory uri uppercase mode", path: "file:anvil.db?mode=MEMORY"},
		{name: "anonymous temporary uri", path: "file:"},
		{name: "memory named uri", path: "file::memory:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lockPath, lockable, err := storeLockPath(tt.path)
			if err != nil {
				t.Fatalf("storeLockPath(%q) error = %v", tt.path, err)
			}
			if lockable != tt.lockable {
				t.Fatalf("storeLockPath(%q) lockable = %t, want %t", tt.path, lockable, tt.lockable)
			}
			if lockable && lockPath != tt.lockPath {
				t.Fatalf("storeLockPath(%q) = %q, want %q", tt.path, lockPath, tt.lockPath)
			}
			if !lockable && lockPath != "" {
				t.Fatalf("storeLockPath(%q) = %q, want no lock path", tt.path, lockPath)
			}
		})
	}
}

// TestStoreLockPathRefusesAnUnreadableURI keeps a store Anvil cannot identify
// from being silently treated as unlockable, which is the failure the guard
// exists to prevent.
func TestStoreLockPathRefusesAnUnreadableURI(t *testing.T) {
	if _, _, err := storeLockPath("file:/var/lib/anvil/anvil.db?_pragma=%zz"); err == nil {
		t.Fatal("storeLockPath() error = nil, want a refusal to guess at an unreadable DSN")
	}
}
