package staging

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zekurio/anvil/pkg/pipeline"
)

type Manager struct {
	Root string
}

func (m Manager) Prepare(job *pipeline.JobContext) error {
	if job == nil {
		return errors.New("staging job context is required")
	}
	root := strings.TrimSpace(m.Root)
	if root == "" {
		return errors.New("staging root is required")
	}
	dir := filepath.Join(root, fmt.Sprintf("job-%d-attempt-%d", job.Job.ID, job.Attempt.ID))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	job.StagingDir = dir
	job.OutputPath = filepath.Join(dir, "output"+containerExt(job.Profile.Container, job.InputPath))
	return nil
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
	container = strings.TrimPrefix(strings.TrimSpace(container), ".")
	if container != "" {
		return "." + container
	}
	if ext := filepath.Ext(inputPath); ext != "" {
		return ext
	}
	return ".mkv"
}

func inside(root string, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
