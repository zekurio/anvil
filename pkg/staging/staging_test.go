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

func TestManagerPlanAlwaysUsesMKVWithoutCreatingDirs(t *testing.T) {
	root := t.TempDir()
	plan, err := (Manager{Root: root}).Plan("<new>", "<new>", "mp4", "/media/movie.mkv")
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got, want := plan.StagingDir, filepath.Join(root, "job-<new>-attempt-<new>"); got != want {
		t.Fatalf("staging dir = %q, want %q", got, want)
	}
	if got, want := plan.OutputPath, filepath.Join(root, "job-<new>-attempt-<new>", "output.mkv"); got != want {
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

	result, err := (Manager{Root: root}).CleanupStale(CleanupStaleOptions{OlderThan: 24 * time.Hour, Now: now})
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

	result, err := (Manager{Root: root}).CleanupStale(CleanupStaleOptions{OlderThan: 24 * time.Hour, Now: now, DryRun: true})
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

// TestCleanupStaleKeepsProtectedDirectories covers the case age cannot see: a
// directory's mtime stops moving once its output file exists, so a running
// multi-hour encode looks exactly as stale as an abandoned one. Deleting it
// would destroy the encode in progress.
func TestCleanupStaleKeepsProtectedDirectories(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	live := filepath.Join(root, "job-7-attempt-9")
	abandoned := filepath.Join(root, "job-8-attempt-1")
	for _, dir := range []string{live, abandoned} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		old := now.Add(-30 * time.Hour)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatalf("chtimes %s: %v", dir, err)
		}
	}

	result, err := (Manager{Root: root}).CleanupStale(CleanupStaleOptions{
		OlderThan: 24 * time.Hour, Now: now,
		Protected: func(jobID int64, attemptID int64) bool { return jobID == 7 && attemptID == 9 },
	})
	if err != nil {
		t.Fatalf("CleanupStale() error = %v", err)
	}
	if result.Protected != 1 || len(result.ProtectedJobs) != 1 || result.ProtectedJobs[0] != 7 {
		t.Fatalf("result = %+v, want job 7 protected", result)
	}
	if result.Candidates != 1 || result.Removed != 1 {
		t.Fatalf("result = %+v, want only the abandoned dir removed", result)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("protected staging dir was removed: %v", err)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatalf("abandoned dir stat = %v, want not exist", err)
	}
}

func TestParseStagingDirNameAcceptsOnlyAnvilDirectories(t *testing.T) {
	tests := []struct {
		name    string
		jobID   int64
		wantOK  bool
		attempt int64
	}{
		{name: "job-12-attempt-3", jobID: 12, attempt: 3, wantOK: true},
		{name: "job-<new>-attempt-<new>"},
		{name: "job-012-attempt-3"},
		{name: "job-0-attempt-3"},
		{name: "job--1-attempt-3"},
		{name: "scratch"},
		{name: "job-12"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobID, attemptID, ok := parseStagingDirName(tt.name)
			if ok != tt.wantOK || jobID != tt.jobID || attemptID != tt.attempt {
				t.Fatalf("parseStagingDirName(%q) = %d, %d, %t", tt.name, jobID, attemptID, ok)
			}
		})
	}
}

func TestCleanupStaleRejectsUnsafeRoot(t *testing.T) {
	tests := []struct {
		name string
		root string
	}{
		{name: "empty", root: ""},
		{name: "current directory", root: "."},
		{name: "filesystem root", root: string(filepath.Separator)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (Manager{Root: tt.root}).CleanupStale(CleanupStaleOptions{OlderThan: time.Hour, Now: time.Now()})
			if err == nil {
				t.Fatal("CleanupStale() error = nil, want unsafe root refusal")
			}
		})
	}
}
