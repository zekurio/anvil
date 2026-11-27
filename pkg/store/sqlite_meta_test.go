package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
)

func TestDaemonSettingsRoundTripAndClear(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "anvil.db")
	state, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	empty, err := state.DaemonSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if keys := empty.Keys(); len(keys) != 0 {
		t.Fatalf("a new store holds daemon settings %v", keys)
	}

	workers, policy := 6, "cancel"
	interval := config.Duration{Duration: 45 * time.Minute}
	if err := state.SetDaemonSettings(ctx, config.DaemonOverrides{WorkerCount: &workers, ScanInterval: &interval, ShutdownPolicy: &policy}); err != nil {
		t.Fatal(err)
	}
	// The values survive a restart of the store.
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if state, err = Open(ctx, path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	saved, err := state.DaemonSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if saved.WorkerCount == nil || *saved.WorkerCount != 6 || saved.ScanInterval == nil || saved.ScanInterval.Duration != 45*time.Minute || saved.ShutdownPolicy == nil || *saved.ShutdownPolicy != "cancel" || saved.LogLevel != nil {
		t.Fatalf("saved = %+v", saved)
	}

	// Saving fewer keys replaces the set: the one left out is back to its default.
	if err := state.SetDaemonSettings(ctx, config.DaemonOverrides{WorkerCount: &workers}); err != nil {
		t.Fatal(err)
	}
	if saved, err = state.DaemonSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if saved.WorkerCount == nil || saved.ScanInterval != nil || saved.ShutdownPolicy != nil {
		t.Fatalf("after replacing: %+v", saved)
	}

	// Saving none clears the record.
	if err := state.SetDaemonSettings(ctx, config.DaemonOverrides{}); err != nil {
		t.Fatal(err)
	}
	if saved, err = state.DaemonSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if keys := saved.Keys(); len(keys) != 0 {
		t.Fatalf("after clearing: %v", keys)
	}
	if _, found, err := getMeta(ctx, state.db, metaDaemonSettings); err != nil || found {
		t.Fatalf("the cleared record is still there: found %t, %v", found, err)
	}
}

// A value written by a newer daemon that this one does not know must not keep
// it from starting.
func TestDaemonSettingsIgnoreUnknownKeys(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	if err := setMeta(ctx, state.db, metaDaemonSettings, `{"max_attempts":4,"future_key":"x"}`); err != nil {
		t.Fatal(err)
	}
	saved, err := state.DaemonSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if saved.MaxAttempts == nil || *saved.MaxAttempts != 4 {
		t.Fatalf("saved = %+v", saved)
	}
}
