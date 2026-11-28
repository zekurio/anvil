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

func TestStageBlockLeavesLegacyArtifactForMaintenance(t *testing.T) {
	job, destination := stagingTestJob(t)
	legacy := destination + replace.PartSuffix
	if err := os.WriteFile(legacy, []byte("encoded artifact"), 0o600); err != nil {
		t.Fatalf("write legacy artifact: %v", err)
	}
	block := StageBlock{Manager: Manager{Root: Root(t.TempDir())}}

	if err := block.Run(context.Background(), job); err != nil {
		t.Fatalf("StageBlock.Run: %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("protected legacy artifact: %v", err)
	}
	wantOutput := replace.ArtifactPath(job.Profile.Publish, destination, replace.PartJobLabel(job.Job.ID))
	if job.OutputPath != wantOutput {
		t.Fatalf("OutputPath = %q, want %q", job.OutputPath, wantOutput)
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
		Library: domain.Library{Path: sourceRoot},
		Profile: domain.Profile{
			Name: "encode", Container: "mkv",
			Publish: domain.PublishPolicy{Mode: domain.PublishMove, Destination: handoffRoot, PreserveStructure: true},
		},
		InputPath: filepath.Join(sourceRoot, "movie.mp4"),
	}
	return job, filepath.Join(handoffRoot, "movie.mkv")
}

func TestRemoveJobScratchRemovesOnlyWhatTheJobsOwn(t *testing.T) {
	temp := t.TempDir()
	mkdir := func(parts ...string) string {
		t.Helper()
		dir := filepath.Join(append([]string{temp}, parts...)...)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	gone := []string{
		mkdir("staging", "job-7-attempt-1"),
		mkdir("staging", "job-7-attempt-12"),
		mkdir("staging", "job-8-attempt-3"),
		mkdir("process-logs", "calm-teal-otter-0123456789ab", "attempt-1"),
		mkdir("process-logs", "job-8", "attempt-1"),
	}
	kept := []string{
		mkdir("staging", "job-70-attempt-1"),
		mkdir("staging", "job-9-attempt-1"),
		mkdir("staging", "job-07-attempt-1"),
		mkdir("staging", "notes"),
		mkdir("process-logs", "bold-red-fox-ba9876543210", "attempt-1"),
		mkdir("media"),
	}
	// A stored slug that is not a plain name must never be followed.
	outside := mkdir("media", "film")
	jobs := []Job{{ID: 7, Slug: "calm-teal-otter-0123456789ab"}, {ID: 8}, {ID: 9, Slug: "../media"}, {ID: 10, Slug: "../../media/film"}, {ID: 11, Slug: "."}}
	// Job 9 is listed, so its staging goes; only its slug is refused.
	gone = append(gone, kept[1])
	kept = append(kept[:1], kept[2:]...)

	if err := RemoveJobScratch(temp, jobs); err != nil {
		t.Fatalf("RemoveJobScratch: %v", err)
	}
	for _, dir := range gone {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there (%v)", dir, err)
		}
	}
	for _, dir := range append(kept, outside, filepath.Join(temp, "staging"), filepath.Join(temp, "process-logs")) {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s was removed: %v", dir, err)
		}
	}

	// It is safe to repeat, and refuses a temp dir it cannot anchor.
	if err := RemoveJobScratch(temp, jobs); err != nil {
		t.Fatalf("second RemoveJobScratch: %v", err)
	}
	for _, unsafe := range []string{"", "relative/tmp"} {
		if err := RemoveJobScratch(unsafe, jobs); err == nil {
			t.Errorf("RemoveJobScratch(%q) did not refuse", unsafe)
		}
	}
	if err := RemoveJobScratch("", nil); err != nil {
		t.Errorf("RemoveJobScratch with no jobs: %v", err)
	}
}
