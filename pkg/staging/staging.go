package staging

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/pipeline"
)

type Manager struct {
	Root string
}

type CleanupStaleResult struct {
	Candidates int
	Removed    int
	Skipped    int
	Errors     []string
}

type PathPlan struct {
	StagingDir string
	OutputPath string
}

func (m Manager) Prepare(job *pipeline.JobContext) error {
	if job == nil {
		return errors.New("staging job context is required")
	}
	plan, err := m.Plan(fmt.Sprintf("%d", job.Job.ID), fmt.Sprintf("%d", job.Attempt.ID), job.Profile.Container, job.InputPath)
	if err != nil {
		return err
	}
	dir := plan.StagingDir
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	job.StagingDir = dir
	job.OutputPath = plan.OutputPath
	return nil
}

func (m Manager) Plan(jobLabel string, attemptLabel string, container string, inputPath string) (PathPlan, error) {
	root := strings.TrimSpace(m.Root)
	if root == "" {
		return PathPlan{}, errors.New("staging root is required")
	}
	if strings.TrimSpace(jobLabel) == "" {
		jobLabel = "<new>"
	}
	if strings.TrimSpace(attemptLabel) == "" {
		attemptLabel = "<new>"
	}
	dir := filepath.Join(root, fmt.Sprintf("job-%s-attempt-%s", jobLabel, attemptLabel))
	return PathPlan{
		StagingDir: dir,
		OutputPath: filepath.Join(dir, "output"+containerExt(container, inputPath)),
	}, nil
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

func (m Manager) CleanupStale(olderThan time.Duration, now time.Time, dryRun bool) (CleanupStaleResult, error) {
	var result CleanupStaleResult
	if olderThan <= 0 {
		return result, nil
	}
	root := filepath.Clean(strings.TrimSpace(m.Root))
	if err := safeRoot(root); err != nil {
		return result, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("read staging root: %w", err)
	}
	cutoff := now.UTC().Add(-olderThan)
	for _, entry := range entries {
		if !entry.IsDir() || !stagingDirName(entry.Name()) {
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
		result.Candidates++
		if dryRun {
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

func containerExt(container string, inputPath string) string {
	_, _ = container, inputPath
	return ".mkv"
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

func stagingDirName(name string) bool {
	if !strings.HasPrefix(name, "job-") {
		return false
	}
	rest := strings.TrimPrefix(name, "job-")
	jobID, attempt, ok := strings.Cut(rest, "-attempt-")
	return ok && digits(jobID) && digits(attempt)
}

func digits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
