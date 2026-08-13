package replace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupPartFilesRemovesJobAndLegacyArtifactsOnly(t *testing.T) {
	// Brackets are common in release names and must be treated literally,
	// rather than as filepath.Glob character classes.
	destination := filepath.Join(t.TempDir(), "[Group] movie.mkv")
	jobPart := PartPath(destination, "42")
	otherJobPart := PartPath(destination, "43")
	legacyPart := destination + PartSuffix
	legacyVariant := legacyPart + ".pre-dovi"
	unrelated := destination + ".partial"

	for _, path := range []string{destination, jobPart, otherJobPart, legacyPart, legacyVariant, unrelated} {
		if err := os.WriteFile(path, []byte(path), 0o600); err != nil {
			t.Fatalf("write %q: %v", path, err)
		}
	}
	if err := CleanupPartFiles(destination, "42"); err != nil {
		t.Fatalf("CleanupPartFiles: %v", err)
	}
	for _, path := range []string{jobPart, legacyPart, legacyVariant} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("artifact %q still exists (stat error %v)", path, err)
		}
	}
	for _, path := range []string{destination, otherJobPart, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("preserved path %q: %v", path, err)
		}
	}
}
