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

// Prepare creates the per-attempt scratch directory and primes the publish
// destination: the artifact is written next to its final path as a part file
// (see replace.PartPath), never into scratch.
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
	return replace.PrepareDestination(job)
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
	Manager            Manager
	ArtifactProtection replace.ArtifactProtection
}

func (StageBlock) Name() string {
	return "stage"
}

func (b StageBlock) Run(ctx context.Context, job *pipeline.JobContext) error {
	if err := b.Manager.Prepare(job); err != nil {
		return err
	}
	if err := replace.CleanupLegacyPartFiles(ctx, b.ArtifactProtection, job.DestinationPath); err != nil {
		return fmt.Errorf("remove stale legacy artifact parts: %w", err)
	}
	return nil
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
