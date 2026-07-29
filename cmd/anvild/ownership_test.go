//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestDaemonOwnershipRefusesASecondDaemon is the guard that has to hold before
// any start-up side effect: a second daemon on the same store used to recover
// stale jobs and sweep staging directories belonging to the running one before
// it ever noticed the live control socket.
func TestDaemonOwnershipRefusesASecondDaemon(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "anvil.db")
	first, err := acquireDaemonOwnership(storePath)
	if err != nil {
		t.Fatalf("acquireDaemonOwnership() error = %v", err)
	}
	t.Cleanup(func() {
		if err := first.release(); err != nil {
			t.Errorf("release() error = %v", err)
		}
	})

	second, err := acquireDaemonOwnership(storePath)
	if err == nil {
		if releaseErr := second.release(); releaseErr != nil {
			t.Errorf("release() error = %v", releaseErr)
		}
		t.Fatal("second acquireDaemonOwnership() error = nil, want the store already owned")
	}
	if !strings.Contains(err.Error(), "already owns the store") {
		t.Fatalf("second acquireDaemonOwnership() error = %v", err)
	}
}

// TestDaemonOwnershipIsReleasedForARestart keeps an orderly shutdown from
// locking the operator out of their own daemon.
func TestDaemonOwnershipIsReleasedForARestart(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "anvil.db")
	first, err := acquireDaemonOwnership(storePath)
	if err != nil {
		t.Fatalf("acquireDaemonOwnership() error = %v", err)
	}
	if err := first.release(); err != nil {
		t.Fatalf("release() error = %v", err)
	}
	second, err := acquireDaemonOwnership(storePath)
	if err != nil {
		t.Fatalf("acquireDaemonOwnership() after release error = %v", err)
	}
	if err := second.release(); err != nil {
		t.Fatalf("release() error = %v", err)
	}
}

// TestDaemonOwnershipGuardsAFileURIStore is the hole the guard used to have:
// "file:" is how an operator sets SQLite pragmas, and skipping it let a second
// daemon run recovery and staging cleanup against a live database.
func TestDaemonOwnershipGuardsAFileURIStore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "anvil.db")
	first, err := acquireDaemonOwnership("file:" + storePath + "?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("acquireDaemonOwnership(uri) error = %v", err)
	}
	t.Cleanup(func() {
		if err := first.release(); err != nil {
			t.Errorf("release() error = %v", err)
		}
	})
	// The plain path names the same database, so it must be refused too.
	second, err := acquireDaemonOwnership(storePath)
	if err == nil {
		if releaseErr := second.release(); releaseErr != nil {
			t.Errorf("release() error = %v", releaseErr)
		}
		t.Fatal("a plain path started beside the same database opened through a file: URI")
	}
	if !strings.Contains(err.Error(), "already owns the store") {
		t.Fatalf("acquireDaemonOwnership() error = %v", err)
	}
}

// TestDaemonOwnershipSkipsStoresWithoutAFilesystemIdentity keeps in-memory
// stores usable, since no lock file could describe them.
func TestDaemonOwnershipSkipsStoresWithoutAFilesystemIdentity(t *testing.T) {
	for _, path := range []string{"", ":memory:", "file:anvil.db?mode=memory"} {
		ownership, err := acquireDaemonOwnership(path)
		if err != nil {
			t.Fatalf("acquireDaemonOwnership(%q) error = %v", path, err)
		}
		if err := ownership.release(); err != nil {
			t.Fatalf("release() error = %v", err)
		}
	}
}
