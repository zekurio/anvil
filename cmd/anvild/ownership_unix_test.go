//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The ownership lock runs before the store opens, so on a fresh install it is
// what creates the store directory. That directory and the lock must be as
// closed as the store itself, which can hold Arr API keys.
func TestAcquireDaemonOwnershipCreatesPrivateStoreDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "anvil")
	storePath := filepath.Join(dir, "anvil.db")
	ownership, err := acquireDaemonOwnership(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ownership.release(); err != nil {
			t.Error(err)
		}
	})

	for path, want := range map[string]os.FileMode{
		filepath.Dir(dir):   0o700,
		dir:                 0o700,
		storePath + ".lock": 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", path, got, want)
		}
	}

	if _, err := acquireDaemonOwnership(storePath); err == nil {
		t.Fatal("a second daemon acquired the same store")
	}
}
