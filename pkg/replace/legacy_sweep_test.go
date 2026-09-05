package replace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacySweepPreservesCurrentAndProtectedParts(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old.mkv.anvil-part.pre-dovi")
	held := filepath.Join(root, "held.mkv.anvil-part")
	current := PartPath(filepath.Join(root, "movie.mkv.anvil-part.name.mkv"), "42")
	fresh := filepath.Join(root, "fresh.mkv.anvil-part")
	unknown := filepath.Join(root, "movie.mkv.anvil-part.backup.mkv")
	writePaths(t, old, held, current, fresh, unknown)
	now := time.Now()
	for _, path := range []string{old, held, current, unknown} {
		if err := os.Chtimes(path, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	protection := artifactProtectionFunc(func(_ context.Context, path string) (bool, error) { return path == held, nil })
	options := LegacySweepOptions{Roots: []string{root, root}, OlderThan: time.Hour, Now: now, DryRun: true}
	result, err := SweepLegacyParts(context.Background(), protection, options)
	if err != nil || result.Candidates != 1 || result.Protected != 1 || result.Removed != 0 {
		t.Fatalf("preview %+v, %v", result, err)
	}
	assertPresent(t, old, held, current, fresh)
	options.DryRun = false
	result, err = SweepLegacyParts(context.Background(), protection, options)
	if err != nil || result.Removed != 1 {
		t.Fatalf("sweep %+v, %v", result, err)
	}
	assertMissing(t, old)
	assertPresent(t, held, current, fresh, unknown)
}

func TestLegacySweepChecksAllProtectionBeforeRemoval(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "a.mkv.anvil-part")
	second := filepath.Join(root, "b.mkv.anvil-part")
	writePaths(t, first, second)
	want := errors.New("store unavailable")
	protection := artifactProtectionFunc(func(_ context.Context, path string) (bool, error) {
		if path == second {
			return false, want
		}
		return false, nil
	})
	_, err := SweepLegacyParts(context.Background(), protection, LegacySweepOptions{Roots: []string{root}, OlderThan: time.Hour, Now: time.Now().Add(2 * time.Hour)})
	if !errors.Is(err, want) {
		t.Fatalf("error %v", err)
	}
	assertPresent(t, first, second)
}

func TestLegacySweepDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "movie.mkv.anvil-part")
	writePaths(t, target)
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "linked.mkv.anvil-part")); err != nil {
		t.Fatal(err)
	}
	protection := artifactProtectionFunc(func(context.Context, string) (bool, error) {
		t.Fatal("symlink was considered for deletion")
		return false, nil
	})
	result, err := SweepLegacyParts(context.Background(), protection, LegacySweepOptions{Roots: []string{root}, OlderThan: time.Hour, Now: time.Now().Add(2 * time.Hour)})
	if err != nil || result.Candidates != 0 {
		t.Fatalf("result %+v, %v", result, err)
	}
	assertPresent(t, target)
}
