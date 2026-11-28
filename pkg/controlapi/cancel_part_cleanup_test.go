package controlapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
)

// moveProfile is a profile whose flow moves to folder, keeping the library's
// folders.
func moveProfile(folder string) domain.Profile {
	return domain.Profile{
		Name: "encode", Container: "mkv",
		Publish: domain.PublishPolicy{Mode: domain.PublishMove, Destination: folder, PreserveStructure: true},
	}
}

func TestCleanupOrphanedPartProtectsAnotherJobsLegacyArtifact(t *testing.T) {
	job := domain.Job{ID: 42, SourceID: 7}
	source := domain.MediaSource{ID: 7, Kind: domain.SourceKindFile, RelativePath: "movie.mp4"}
	handoffRoot := t.TempDir()
	attempt := snapshotAttempt(t, 1, domain.Library{Path: "/source"}, moveProfile(handoffRoot))
	destination := filepath.Join(handoffRoot, "movie.mkv")
	// A part beside the destination predates the handoff work directory and
	// must still be reclaimed alongside the current work-directory part.
	scoped := replacepkg.PartPath(destination, replacepkg.PartJobLabel(job.ID))
	workPart := replacepkg.ArtifactPath(moveProfile(handoffRoot).Publish, destination, replacepkg.PartJobLabel(job.ID))
	if err := os.MkdirAll(filepath.Dir(workPart), 0o750); err != nil {
		t.Fatal(err)
	}
	legacy := destination + replacepkg.PartSuffix
	for _, path := range []string{scoped, workPart, legacy} {
		if err := os.WriteFile(path, []byte(path), 0o600); err != nil {
			t.Fatalf("write %q: %v", path, err)
		}
	}
	store := &cancelCleanupStore{job: job, source: source, attempts: []domain.Attempt{attempt}, protectedPath: legacy}

	Service{Store: store}.cleanupOrphanedPart(context.Background(), job.ID)

	for _, path := range []string{scoped, workPart} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("canceled job part %q still exists (stat error %v)", path, err)
		}
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("journal-owned legacy artifact: %v", err)
	}
	if !store.protectionChecked {
		t.Fatal("legacy artifact protection was not checked")
	}
}

type cancelCleanupStore struct {
	Store
	job               domain.Job
	source            domain.MediaSource
	attempts          []domain.Attempt
	protectedPath     string
	protectionChecked bool
}

func (s *cancelCleanupStore) GetPublishOperation(context.Context, domain.JobID) (replacepkg.PublishOperation, bool, error) {
	return replacepkg.PublishOperation{}, false, nil
}

func (s *cancelCleanupStore) GetJob(context.Context, domain.JobID) (domain.Job, error) {
	return s.job, nil
}

func (s *cancelCleanupStore) GetMediaSource(context.Context, domain.MediaSourceID) (domain.MediaSource, error) {
	return s.source, nil
}

func (s *cancelCleanupStore) ListAttemptsForJob(context.Context, domain.JobID) ([]domain.Attempt, error) {
	return s.attempts, nil
}

func (s *cancelCleanupStore) PublishArtifactProtected(_ context.Context, path string) (bool, error) {
	s.protectionChecked = true
	return path == s.protectedPath, nil
}

func snapshotAttempt(t *testing.T, number int, library domain.Library, profile domain.Profile) domain.Attempt {
	t.Helper()
	marshal := func(value any) []byte {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		return data
	}
	return domain.Attempt{
		Number:          number,
		ResolvedLibrary: marshal(library),
		ResolvedProfile: marshal(profile),
	}
}
