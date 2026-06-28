package staging

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

func TestManagerPrepareCreatesOutputPath(t *testing.T) {
	root := t.TempDir()
	job := &pipeline.JobContext{
		Job:       domain.Job{ID: 12},
		Attempt:   domain.Attempt{ID: 3},
		Profile:   domain.Profile{Container: "mkv"},
		InputPath: "/media/movie.mp4",
	}
	if err := (Manager{Root: root}).Prepare(job); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if job.StagingDir == "" || job.OutputPath == "" {
		t.Fatalf("staging dir/output path were not set: %+v", job)
	}
	if _, err := os.Stat(job.StagingDir); err != nil {
		t.Fatalf("staging dir stat: %v", err)
	}
	if filepath.Ext(job.OutputPath) != ".mkv" {
		t.Fatalf("output path = %q, want .mkv extension", job.OutputPath)
	}
}

func TestManagerPlanUsesPlaceholdersWithoutCreatingDirs(t *testing.T) {
	root := t.TempDir()
	plan, err := (Manager{Root: root}).Plan("<new>", "<new>", "mp4", "/media/movie.mkv")
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got, want := plan.StagingDir, filepath.Join(root, "job-<new>-attempt-<new>"); got != want {
		t.Fatalf("staging dir = %q, want %q", got, want)
	}
	if got, want := plan.OutputPath, filepath.Join(root, "job-<new>-attempt-<new>", "output.mp4"); got != want {
		t.Fatalf("output path = %q, want %q", got, want)
	}
	if _, err := os.Stat(plan.StagingDir); !os.IsNotExist(err) {
		t.Fatalf("staging dir stat = %v, want not exist", err)
	}
}

func TestCleanupStaleRemovesOnlyOldAnvilStagingDirs(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	oldDir := filepath.Join(root, "job-1-attempt-2")
	newDir := filepath.Join(root, "job-3-attempt-4")
	otherDir := filepath.Join(root, "scratch")
	for _, dir := range []string{oldDir, newDir, otherDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	oldTime := now.Add(-25 * time.Hour)
	newTime := now.Add(-time.Hour)
	if err := os.Chtimes(oldDir, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old: %v", err)
	}
	if err := os.Chtimes(newDir, newTime, newTime); err != nil {
		t.Fatalf("chtimes new: %v", err)
	}

	result, err := (Manager{Root: root}).CleanupStale(24*time.Hour, now, false)
	if err != nil {
		t.Fatalf("CleanupStale() error = %v", err)
	}
	if result.Candidates != 1 || result.Removed != 1 || len(result.Errors) != 0 {
		t.Fatalf("result = %+v, want one removed candidate with no errors", result)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old dir stat error = %v, want not exist", err)
	}
	for _, dir := range []string{newDir, otherDir} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("expected %s to remain: %v", dir, err)
		}
	}
}

func TestCleanupStaleDryRunKeepsCandidates(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "job-1-attempt-2")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	oldTime := now.Add(-25 * time.Hour)
	if err := os.Chtimes(dir, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	result, err := (Manager{Root: root}).CleanupStale(24*time.Hour, now, true)
	if err != nil {
		t.Fatalf("CleanupStale() error = %v", err)
	}
	if result.Candidates != 1 || result.Removed != 0 {
		t.Fatalf("result = %+v, want one dry-run candidate", result)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("expected dry-run dir to remain: %v", err)
	}
}
