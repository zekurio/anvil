package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/scanner"
	"github.com/zekurio/anvil/pkg/store"
)

func TestCleanLibraryRelativePathRefusesEscapesAndAbsolutePaths(t *testing.T) {
	for _, value := range []string{"", ".", "..", "../Movie.mkv", "/library/Movie.mkv", "Movie\x00.mkv"} {
		if _, err := cleanLibraryRelativePath(value); err == nil {
			t.Fatalf("cleanLibraryRelativePath(%q) error = nil, want unsafe-path refusal", value)
		}
	}
	if got, err := cleanLibraryRelativePath("Release/../Movie.mkv"); err != nil || got != "Movie.mkv" {
		t.Fatalf("cleanLibraryRelativePath() = %q, %v; want Movie.mkv", got, err)
	}
}

func TestResolveForceOccurrenceCandidateUsesExactConfiguredPath(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeForceTestFile(t, filepath.Join(root, "Movie.mkv"))
	library := forceTestLibrary(root)

	candidate, err := resolveForceOccurrenceCandidate(ctx, library, "Movie.mkv")
	if err != nil {
		t.Fatalf("resolveForceOccurrenceCandidate() error = %v", err)
	}
	if candidate.LibraryRelativePath != "Movie.mkv" || candidate.SourceRelativePath != "Movie.mkv" || candidate.AssetRelativePath != "Movie.mkv" {
		t.Fatalf("candidate paths = library %q source %q asset %q", candidate.LibraryRelativePath, candidate.SourceRelativePath, candidate.AssetRelativePath)
	}
	if candidate.SourceKind != domain.SourceKindFile || !candidate.Enqueueable {
		t.Fatalf("candidate = %+v, want enqueueable file source", candidate)
	}
}

func TestResolveForceOccurrenceCandidateRefusesIgnoredAndUnstableTargets(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeForceTestFile(t, filepath.Join(root, "Movie.mkv"))

	ignored := forceTestLibrary(root)
	ignored.Exclude = []string{"Movie.mkv"}
	if _, err := resolveForceOccurrenceCandidate(ctx, ignored, "Movie.mkv"); err == nil || !strings.Contains(err.Error(), "ignored") {
		t.Fatalf("ignored candidate error = %v, want ignored refusal", err)
	}

	unstable := forceTestLibrary(root)
	unstable.Kind = "download"
	unstable.Download.PackageMode = "file"
	unstable.Download.StableFor = "1h"
	if _, err := resolveForceOccurrenceCandidate(ctx, unstable, "Movie.mkv"); err == nil || !strings.Contains(err.Error(), "unstable") {
		t.Fatalf("unstable candidate error = %v, want unstable refusal", err)
	}
}

func TestForceOccurrenceRefusesActiveWorkThenUsesStoreGenerationAPI(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeForceTestFile(t, filepath.Join(root, "Movie.mkv"))
	library := forceTestLibrary(root)
	state, err := store.Open(ctx, filepath.Join(t.TempDir(), "anvil.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Fatalf("state.Close() error = %v", err)
		}
	})

	now := time.Now().UTC().Add(time.Second)
	if _, err := (scanner.Scanner{Store: state, Now: func() time.Time { return now }}).ScanLibrary(ctx, library); err != nil {
		t.Fatalf("ScanLibrary() error = %v", err)
	}
	candidate, err := resolveForceOccurrenceCandidate(ctx, library, "Movie.mkv")
	if err != nil {
		t.Fatalf("resolveForceOccurrenceCandidate() error = %v", err)
	}
	if _, err := forceOccurrence(ctx, state, library, candidate, now.Add(time.Second)); err == nil || !strings.Contains(err.Error(), "active work exists") {
		t.Fatalf("forceOccurrence() active error = %v, want active-work refusal", err)
	}

	leased, err := state.LeaseNextJob(ctx, "worker-1", now.Add(time.Minute), now)
	if err != nil || leased == nil {
		t.Fatalf("LeaseNextJob() = %v, %v", leased, err)
	}
	if _, err := state.TransitionJob(ctx, leased.ID, domain.JobStateRunning, now, ""); err != nil {
		t.Fatalf("TransitionJob(running) error = %v", err)
	}
	if _, err := state.TransitionJob(ctx, leased.ID, domain.JobStateFailed, now, "test terminal job"); err != nil {
		t.Fatalf("TransitionJob(failed) error = %v", err)
	}

	result, err := forceOccurrence(ctx, state, library, candidate, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("forceOccurrence() after terminal job error = %v", err)
	}
	if result.Source.Generation != 2 || result.Asset.Generation != 1 || result.Job.State != domain.JobStatePending {
		t.Fatalf("force occurrence result = source gen %d asset gen %d job %q", result.Source.Generation, result.Asset.Generation, result.Job.State)
	}
}

func forceTestLibrary(root string) config.LibraryConfig {
	return config.LibraryConfig{
		Name:    "movies",
		Kind:    "media",
		Path:    root,
		Flow:    config.DefaultFlowName,
		Profile: config.DefaultProfileName,
	}
}

func writeForceTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("media"), 0o600); err != nil {
		t.Fatalf("write force-occurrence test file: %v", err)
	}
}
