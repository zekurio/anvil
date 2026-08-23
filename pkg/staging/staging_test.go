package staging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/replace"
)

type artifactProtectionFunc func(context.Context, string) (bool, error)

func (f artifactProtectionFunc) PublishArtifactProtected(ctx context.Context, path string) (bool, error) {
	return f(ctx, path)
}

func TestStageBlockProtectsJournalOwnedLegacyArtifact(t *testing.T) {
	job, destination := stagingTestJob(t)
	legacy := destination + replace.PartSuffix
	if err := os.WriteFile(legacy, []byte("encoded artifact"), 0o600); err != nil {
		t.Fatalf("write legacy artifact: %v", err)
	}
	protection := artifactProtectionFunc(func(_ context.Context, path string) (bool, error) {
		return path == legacy, nil
	})
	block := StageBlock{Manager: Manager{Root: Root(t.TempDir())}, ArtifactProtection: protection}

	if err := block.Run(context.Background(), job); err != nil {
		t.Fatalf("StageBlock.Run: %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("protected legacy artifact: %v", err)
	}
	wantOutput := replace.PartPath(destination, replace.PartJobLabel(job.Job.ID))
	if job.OutputPath != wantOutput {
		t.Fatalf("OutputPath = %q, want %q", job.OutputPath, wantOutput)
	}
}

func TestStageBlockFailsClosedOnProtectionError(t *testing.T) {
	job, destination := stagingTestJob(t)
	legacy := destination + replace.PartSuffix
	if err := os.WriteFile(legacy, []byte("encoded artifact"), 0o600); err != nil {
		t.Fatalf("write legacy artifact: %v", err)
	}
	lookupErr := errors.New("journal unavailable")
	protection := artifactProtectionFunc(func(context.Context, string) (bool, error) {
		return false, lookupErr
	})
	block := StageBlock{Manager: Manager{Root: Root(t.TempDir())}, ArtifactProtection: protection}

	err := block.Run(context.Background(), job)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("StageBlock.Run error = %v, want %v", err, lookupErr)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy artifact after protection error: %v", err)
	}
}

func stagingTestJob(t *testing.T) (*pipeline.JobContext, string) {
	t.Helper()
	handoffRoot := t.TempDir()
	sourceRoot := t.TempDir()
	job := &pipeline.JobContext{
		Job:     domain.Job{ID: 42},
		Attempt: domain.Attempt{ID: 7},
		Source: domain.MediaSource{
			Kind:         domain.SourceKindFile,
			RelativePath: "movie.mp4",
		},
		Library: domain.Library{
			Kind: domain.LibraryKindDownload,
			Path: sourceRoot,
			Download: domain.DownloadLibraryPolicy{
				HandoffPath:          handoffRoot,
				PreserveRelativePath: true,
			},
		},
		Profile:   domain.Profile{Name: "encode", Container: "mkv"},
		InputPath: filepath.Join(sourceRoot, "movie.mp4"),
	}
	return job, filepath.Join(handoffRoot, "movie.mkv")
}
