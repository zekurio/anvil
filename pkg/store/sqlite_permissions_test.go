package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSecuresNewStoreFiles(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(parent, "anvil.db")
	state, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	for _, candidate := range []string{parent, path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("stat %s: %v", candidate, err)
		}
		want := os.FileMode(0o600)
		if candidate == parent {
			want = 0o700
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %04o, want %04o", candidate, got, want)
		}
	}
}

func TestOpenLeavesExistingStoreDirectoryMode(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(parent, 0o750); err != nil {
		t.Fatal(err)
	}
	state, err := Open(context.Background(), filepath.Join(parent, "anvil.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("existing directory mode = %04o, want 0750", got)
	}
}
