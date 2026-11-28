package replace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

type artifactProtectionFunc func(context.Context, string) (bool, error)

func (f artifactProtectionFunc) PublishArtifactProtected(ctx context.Context, path string) (bool, error) {
	return f(ctx, path)
}

// movePolicy is a move to folder that keeps the library's folders.
func movePolicy(folder string) domain.PublishPolicy {
	return domain.PublishPolicy{Mode: domain.PublishMove, Destination: folder, PreserveStructure: true}
}

func TestPlanDestinationFollowsThePublishPolicy(t *testing.T) {
	libraryRoot := t.TempDir()
	mediaInput := filepath.Join(libraryRoot, "Show", "Season 1", "episode.mp4")
	handoffRoot := t.TempDir()
	source := domain.MediaSource{Kind: domain.SourceKindFile, RelativePath: "Show/Season 1/episode.mp4"}
	flat := movePolicy(handoffRoot)
	flat.PreserveStructure = false
	tests := []struct {
		name    string
		publish domain.PublishPolicy
		want    string
	}{
		{"zero value replaces", domain.PublishPolicy{}, strings.TrimSuffix(mediaInput, ".mp4") + ".mkv"},
		{"replace", domain.PublishPolicy{Mode: domain.PublishReplace}, strings.TrimSuffix(mediaInput, ".mp4") + ".mkv"},
		{"move keeping the folders", movePolicy(handoffRoot), filepath.Join(handoffRoot, "Show", "Season 1", "episode.mkv")},
		{"move without the folders", flat, filepath.Join(handoffRoot, "episode.mkv")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := &pipeline.JobContext{
				Library:   domain.Library{Path: libraryRoot},
				Source:    source,
				InputPath: mediaInput,
				Profile:   domain.Profile{Publish: test.publish},
			}
			got, err := PlanDestination(job)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("PlanDestination() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPlanDestinationRefusesAMoveWithoutAnAbsoluteFolder(t *testing.T) {
	for _, folder := range []string{"", "relative/imports"} {
		job := &pipeline.JobContext{
			InputPath: filepath.Join(t.TempDir(), "movie.mkv"),
			Profile:   domain.Profile{Publish: domain.PublishPolicy{Mode: domain.PublishMove, Destination: folder}},
		}
		if got, err := PlanDestination(job); err == nil {
			t.Errorf("PlanDestination() with folder %q = %q, want an error", folder, got)
		}
	}
}

func TestPlanHandoffFollowsKeepSource(t *testing.T) {
	job := &pipeline.JobContext{
		InputPath:       "/downloads/Show/episode.mkv",
		DestinationPath: "/imports/Show/episode.mkv",
	}
	tests := []struct {
		name    string
		publish domain.PublishPolicy
		want    HandoffPlan
	}{
		{"delete the source", domain.PublishPolicy{Mode: domain.PublishMove}, HandoffPlan{Action: handoffActionMove, CleanupSourceMedia: true}},
		{"delete and prune", domain.PublishPolicy{Mode: domain.PublishMove, PruneEmptyDirs: true}, HandoffPlan{Action: handoffActionMove, CleanupSourceMedia: true, PruneEmptyDirs: true}},
		{"keep the source", domain.PublishPolicy{Mode: domain.PublishMove, KeepSource: true, PruneEmptyDirs: true}, HandoffPlan{Action: handoffActionCopy}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job.Profile.Publish = test.publish
			got, err := PlanHandoff(job)
			if err != nil {
				t.Fatal(err)
			}
			test.want.Destination = job.DestinationPath
			test.want.SourceMediaPath = job.InputPath
			test.want.PruneStart = "/downloads/Show"
			if got != test.want {
				t.Fatalf("PlanHandoff() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestCleanupPartFilesRemovesOnlyTheJobsArtifact(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "movie.mkv")
	jobPart := PartPath(destination, "42")
	otherJobPart := PartPath(destination, "43")
	legacyPart := destination + PartSuffix

	writePaths(t, destination, jobPart, otherJobPart, legacyPart)
	if err := CleanupPartFiles(domain.PublishPolicy{}, destination, "42"); err != nil {
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

func TestArtifactPathKeepsMovedArtifactsOutOfPackageDirectories(t *testing.T) {
	handoffRoot := t.TempDir()
	destination := filepath.Join(handoffRoot, "Show.S01", "Show.S01E02.mkv")

	got := ArtifactPath(movePolicy(handoffRoot), destination, "42")
	want := filepath.Join(handoffRoot, HandoffWorkDir, "Show.S01E02.mkv.job-42"+PartSuffix)
	if got != want {
		t.Fatalf("ArtifactPath() = %q, want %q", got, want)
	}
	if replaced := ArtifactPath(domain.PublishPolicy{Mode: domain.PublishReplace}, destination, "42"); replaced != PartPath(destination, "42") {
		t.Fatalf("replace ArtifactPath() = %q, want %q", replaced, PartPath(destination, "42"))
	}
}

func TestPrepareDestinationDoesNotCreateHandoffPackageDirectory(t *testing.T) {
	handoffRoot := filepath.Join(t.TempDir(), "converted")
	job := &pipeline.JobContext{
		Job:     domain.Job{ID: 42},
		Profile: domain.Profile{Publish: movePolicy(handoffRoot)},
	}
	job.DestinationPath = filepath.Join(handoffRoot, "Show.S01", "Show.S01E02.mkv")
	job.OutputPath = ArtifactPath(job.Profile.Publish, job.DestinationPath, PartJobLabel(job.Job.ID))
	stale := job.OutputPath
	if err := os.MkdirAll(filepath.Dir(stale), 0o750); err != nil {
		t.Fatal(err)
	}
	writePaths(t, stale)

	if err := PrepareDestination(job); err != nil {
		t.Fatalf("PrepareDestination: %v", err)
	}
	assertMissing(t, stale, filepath.Dir(job.DestinationPath))
	info, err := os.Stat(filepath.Dir(job.OutputPath))
	if err != nil || !info.IsDir() {
		t.Fatalf("handoff work dir: %v", err)
	}
}

func TestCleanupPartFilesReclaimsMovedPartBesideDestination(t *testing.T) {
	handoffRoot := t.TempDir()
	library := movePolicy(handoffRoot)
	destination := filepath.Join(handoffRoot, "movie.mkv")
	workPart := ArtifactPath(library, destination, "42")
	if err := os.MkdirAll(filepath.Dir(workPart), 0o750); err != nil {
		t.Fatal(err)
	}
	besidePart := PartPath(destination, "42")
	otherJobPart := ArtifactPath(library, destination, "43")
	writePaths(t, destination, workPart, besidePart, otherJobPart)

	if err := CleanupPartFiles(library, destination, "42"); err != nil {
		t.Fatalf("CleanupPartFiles: %v", err)
	}
	assertMissing(t, workPart, besidePart)
	assertPresent(t, destination, otherJobPart)
}
