package staging

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/pipeline"
	"github.com/zekurio/anvil/pkg/replace"
)

type Manager struct {
	Root string
}

// Root reports the staging root inside a daemon temp dir. Every component that
// creates, cleans, or reports staging directories resolves it here, so a
// cleanup sweep can never point at a different directory than the encodes do.
func Root(tempDir string) string {
	return filepath.Join(tempDir, "staging")
}

// ProcessLogRoot reports where per-attempt process logs live inside a daemon
// temp dir: one directory per job, named by the job's slug.
func ProcessLogRoot(tempDir string) string {
	return filepath.Join(tempDir, "process-logs")
}

// Job names a job whose scratch RemoveJobScratch removes.
type Job struct {
	ID   int64
	Slug string
}

// RemoveJobScratch deletes what jobs that no longer exist left under a daemon
// temp dir: their per-attempt staging directories and their process logs.
// The caller must know that no attempt of these jobs can be running.
//
// It only ever removes a directory directly below the staging root or the
// process log root whose name is exactly one Anvil gives such a directory for
// one of the jobs. Media files live elsewhere and are never touched: nothing
// here follows a path stored in a job, and a slug that could name anything
// but a plain directory is refused. Every job is tried; the failures are
// returned together.
func RemoveJobScratch(tempDir string, jobs []Job) error {
	tempDir = strings.TrimSpace(tempDir)
	if len(jobs) == 0 {
		return nil
	}
	if tempDir == "" || !filepath.IsAbs(tempDir) {
		return fmt.Errorf("refusing to remove job scratch below temp dir %q: it must be an absolute path", tempDir)
	}
	stagingRoot := filepath.Clean(Root(tempDir))
	if err := safeRoot(stagingRoot); err != nil {
		return err
	}
	ids := make(map[int64]struct{}, len(jobs))
	for _, job := range jobs {
		ids[job.ID] = struct{}{}
	}

	var failures []error
	entries, err := os.ReadDir(stagingRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		failures = append(failures, fmt.Errorf("read staging root: %w", err))
	}
	for _, entry := range entries {
		jobID, _, owned := parseStagingDirName(entry.Name())
		if _, wanted := ids[jobID]; !owned || !wanted || !entry.IsDir() {
			continue
		}
		if err := os.RemoveAll(filepath.Join(stagingRoot, entry.Name())); err != nil {
			failures = append(failures, fmt.Errorf("remove staging of job %d: %w", jobID, err))
		}
	}

	logRoot := filepath.Clean(ProcessLogRoot(tempDir))
	for _, job := range jobs {
		// The worker names the directory by the slug, or job-<id> for a job
		// without one.
		for _, label := range []string{job.Slug, fmt.Sprintf("job-%d", job.ID)} {
			if !plainDirName(label) {
				continue
			}
			dir := filepath.Join(logRoot, label)
			info, err := os.Lstat(dir)
			if errors.Is(err, os.ErrNotExist) || (err == nil && !info.IsDir()) {
				continue
			}
			if err == nil {
				err = os.RemoveAll(dir)
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("remove process logs of job %d: %w", job.ID, err))
			}
		}
	}
	return errors.Join(failures...)
}

// plainDirName reports whether a label can only name one entry directly
// inside a directory: non-empty, made of the characters job slugs use, and so
// neither ".", "..", nor a path.
func plainDirName(label string) bool {
	if label == "" {
		return false
	}
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// CleanupStaleOptions describes one stale-staging sweep.
type CleanupStaleOptions struct {
	OlderThan time.Duration
	Now       time.Time
	DryRun    bool
	// Protected reports a staging directory that must survive the sweep even
	// though it is old enough to remove. Age alone is not proof that a
	// directory is abandoned: a directory's mtime only moves when an entry is
	// created or removed in it, so a multi-hour encode writing into an
	// already-created output file keeps a stale mtime the whole time. Only the
	// daemon knows which jobs are live or still own an unresolved publish
	// journal, so it supplies that knowledge here.
	//
	// A nil predicate protects nothing, which is only safe when the caller
	// knows no attempt can be running.
	Protected func(jobID int64, attemptID int64) bool
}

type CleanupStaleResult struct {
	Candidates int
	Removed    int
	Skipped    int
	// Protected counts directories old enough to remove that were kept because
	// the caller still owns them.
	Protected int
	// ProtectedJobs lists the job ids behind Protected, so an operator can see
	// which work is holding staging space instead of guessing.
	ProtectedJobs []int64
	Errors        []string
}

// Prepare creates the per-attempt scratch directory and plans the artifact
// paths. The encode step creates the output directory when it is ready to write.
func (m Manager) Prepare(job *pipeline.JobContext) error {
	if job == nil {
		return errors.New("staging job context is required")
	}
	dir, err := m.Plan(fmt.Sprintf("%d", job.Job.ID), fmt.Sprintf("%d", job.Attempt.ID))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	job.StagingDir = dir
	return replace.PlanArtifactPaths(job)
}

func (m Manager) Plan(jobLabel string, attemptLabel string) (string, error) {
	root := strings.TrimSpace(m.Root)
	if root == "" {
		return "", errors.New("staging root is required")
	}
	if strings.TrimSpace(jobLabel) == "" {
		jobLabel = "<new>"
	}
	if strings.TrimSpace(attemptLabel) == "" {
		attemptLabel = "<new>"
	}
	return filepath.Join(root, fmt.Sprintf("job-%s-attempt-%s", jobLabel, attemptLabel)), nil
}

func (m Manager) Cleanup(job *pipeline.JobContext) error {
	if job == nil || job.StagingDir == "" {
		return nil
	}
	root := filepath.Clean(m.Root)
	dir := filepath.Clean(job.StagingDir)
	if root == "." || root == string(filepath.Separator) {
		return errors.New("refusing to clean unsafe staging root")
	}
	if !inside(root, dir) {
		return fmt.Errorf("staging dir %q is outside root %q", dir, root)
	}
	return os.RemoveAll(dir)
}

func (m Manager) CleanupStale(options CleanupStaleOptions) (CleanupStaleResult, error) {
	var result CleanupStaleResult
	root := filepath.Clean(strings.TrimSpace(m.Root))
	if err := safeRoot(root); err != nil {
		return result, err
	}
	if options.OlderThan <= 0 {
		return result, nil
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("read staging root: %w", err)
	}
	cutoff := options.Now.UTC().Add(-options.OlderThan)
	for _, entry := range entries {
		jobID, attemptID, ok := parseStagingDirName(entry.Name())
		if !entry.IsDir() || !ok {
			result.Skipped++
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if info.ModTime().After(cutoff) {
			result.Skipped++
			continue
		}
		if options.Protected != nil && options.Protected(jobID, attemptID) {
			result.Protected++
			result.ProtectedJobs = append(result.ProtectedJobs, jobID)
			continue
		}
		result.Candidates++
		if options.DryRun {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		result.Removed++
	}
	return result, nil
}

type StageBlock struct {
	Manager Manager
}

func (StageBlock) Name() string {
	return "stage"
}

func (b StageBlock) Run(_ context.Context, job *pipeline.JobContext) error {
	return b.Manager.Prepare(job)
}

type CleanupBlock struct {
	Manager Manager
}

func (CleanupBlock) Name() string {
	return "cleanup"
}

func (b CleanupBlock) Run(_ context.Context, job *pipeline.JobContext) error {
	return b.Manager.Cleanup(job)
}

func inside(root string, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func safeRoot(root string) error {
	if root == "" || root == "." || root == string(filepath.Separator) {
		return errors.New("refusing to clean unsafe staging root")
	}
	return nil
}

// parseStagingDirName recognizes a directory Anvil created and reports the job
// and attempt it belongs to. Anything else is left alone: the staging root is
// operator-visible, and a sweep must never delete a directory it cannot prove
// it owns.
func parseStagingDirName(name string) (int64, int64, bool) {
	rest, ok := strings.CutPrefix(name, "job-")
	if !ok {
		return 0, 0, false
	}
	jobLabel, attemptLabel, ok := strings.Cut(rest, "-attempt-")
	if !ok {
		return 0, 0, false
	}
	jobID, ok := parseStagingID(jobLabel)
	if !ok {
		return 0, 0, false
	}
	attemptID, ok := parseStagingID(attemptLabel)
	if !ok {
		return 0, 0, false
	}
	return jobID, attemptID, true
}

// parseStagingID accepts only the exact decimal form Prepare writes, so a
// hand-made directory that merely looks similar is not treated as Anvil's.
func parseStagingID(label string) (int64, bool) {
	value, err := strconv.ParseInt(label, 10, 64)
	if err != nil || value <= 0 || strconv.FormatInt(value, 10) != label {
		return 0, false
	}
	return value, true
}
