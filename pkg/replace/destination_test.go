package replace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type artifactProtectionFunc func(context.Context, string) (bool, error)

func (f artifactProtectionFunc) PublishArtifactProtected(ctx context.Context, path string) (bool, error) {
	return f(ctx, path)
}

func TestCleanupPartFilesRemovesOnlyTheJobsArtifact(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "movie.mkv")
	jobPart := PartPath(destination, "42")
	otherJobPart := PartPath(destination, "43")
	legacyPart := destination + PartSuffix

	writePaths(t, destination, jobPart, otherJobPart, legacyPart)
	if err := CleanupPartFiles(destination, "42"); err != nil {
		t.Fatalf("CleanupPartFiles: %v", err)
	}
	assertMissing(t, jobPart)
	assertPresent(t, destination, otherJobPart, legacyPart)
}

func TestCleanupLegacyPartFilesProtectsJournalOwnedArtifact(t *testing.T) {
	// Brackets are common in release names and must be treated literally,
	// rather than as filepath.Glob character classes.
	destination := filepath.Join(t.TempDir(), "[Group] movie.mkv")
	legacyPart := destination + PartSuffix
	legacyVariant := legacyPart + ".pre-dovi"
	otherJobPart := PartPath(destination, "43")
	writePaths(t, destination, legacyPart, legacyVariant, otherJobPart)

	checked := make(map[string]bool)
	protection := artifactProtectionFunc(func(_ context.Context, path string) (bool, error) {
		checked[path] = true
		return path == legacyPart, nil
	})
	if err := CleanupLegacyPartFiles(context.Background(), protection, destination); err != nil {
		t.Fatalf("CleanupLegacyPartFiles: %v", err)
	}
	assertMissing(t, legacyVariant)
	assertPresent(t, destination, legacyPart, otherJobPart)
	for _, path := range []string{legacyPart, legacyVariant} {
		if !checked[path] {
			t.Errorf("protection was not checked for %q", path)
		}
	}
}

func TestCleanupLegacyPartFilesRemovesUnprotectedArtifacts(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "movie.mkv")
	legacyPart := destination + PartSuffix
	legacyVariant := legacyPart + ".dovi-fixed"
	unrelated := destination + ".partial"
	writePaths(t, legacyPart, legacyVariant, unrelated)

	unprotected := artifactProtectionFunc(func(context.Context, string) (bool, error) {
		return false, nil
	})
	if err := CleanupLegacyPartFiles(context.Background(), unprotected, destination); err != nil {
		t.Fatalf("CleanupLegacyPartFiles: %v", err)
	}
	assertMissing(t, legacyPart, legacyVariant)
	assertPresent(t, unrelated)
}

func TestCleanupLegacyPartFilesRequiresProtection(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "movie.mkv")
	legacyPart := destination + PartSuffix
	writePaths(t, legacyPart)

	err := CleanupLegacyPartFiles(context.Background(), nil, destination)
	if err == nil || !strings.Contains(err.Error(), "requires publish journal protection") {
		t.Fatalf("CleanupLegacyPartFiles error = %v, want protection error", err)
	}
	assertPresent(t, legacyPart)
}

func TestCleanupLegacyPartFilesDoesNotMutateOnProtectionError(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "movie.mkv")
	legacyPart := destination + PartSuffix
	legacyVariant := legacyPart + ".pre-dovi"
	writePaths(t, legacyPart, legacyVariant)

	lookupErr := errors.New("journal unavailable")
	protection := artifactProtectionFunc(func(_ context.Context, path string) (bool, error) {
		if strings.HasSuffix(path, ".pre-dovi") {
			return false, lookupErr
		}
		return false, nil
	})
	err := CleanupLegacyPartFiles(context.Background(), protection, destination)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("CleanupLegacyPartFiles error = %v, want %v", err, lookupErr)
	}
	assertPresent(t, legacyPart, legacyVariant)
}

func writePaths(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.WriteFile(path, []byte(path), 0o600); err != nil {
			t.Fatalf("write %q: %v", path, err)
		}
	}
}

func assertMissing(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("path %q still exists (stat error %v)", path, err)
		}
	}
}

func assertPresent(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("preserved path %q: %v", path, err)
		}
	}
}
