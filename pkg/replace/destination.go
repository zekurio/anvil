package replace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

// PartSuffix marks an artifact that is still being written. The encode output
// is created next to its publish destination under this suffix, so publication
// is a same-directory link plus unlink and never a bulk copy, regardless of
// where the daemon temp directory lives. The suffix is Anvil's namespace: the
// scanner never treats it as media, and media managers ignore the unknown
// extension, so a part file is never imported half-written.
const PartSuffix = ".anvil-part"

// PartPath returns the working path for the artifact that will be published
// to destination. The job label keeps the path unique per job: two jobs can
// resolve to the same destination (same input basename handed off with
// preserve_relative_path disabled, say), and a shared part path would let one
// encoder truncate the other's artifact — or keep writing through the inode
// after it was linked under the final name. The publish step's no-clobber
// link then decides which job wins the destination.
func PartPath(destination string, jobLabel string) string {
	return fmt.Sprintf("%s.job-%s%s", destination, jobLabel, PartSuffix)
}

// IsAnvilPartPath reports whether path is an unpublished Anvil artifact.
func IsAnvilPartPath(path string) bool {
	return strings.HasSuffix(strings.ToLower(filepath.Base(path)), PartSuffix)
}

// PlanDestination resolves the final publish path for a job from its flow's
// publish step. It runs at stage time so the artifact can be written directly
// next to its destination; publish consumes the same value from the job
// context instead of re-deriving it.
func PlanDestination(job *pipeline.JobContext) (string, error) {
	if job == nil {
		return "", errors.New("destination job context is required")
	}
	ext := containerExt(job.Profile.Container)
	for _, step := range job.Flow.Steps {
		switch strings.ToLower(strings.TrimSpace(step.Name)) {
		case "handoff":
			return handoffDestination(job, ext)
		case "replace":
			plan, err := PlanReplacement(job.InputPath, ext, job.Library.Media.ReplacementMode)
			if err != nil {
				return "", err
			}
			if plan.Action == replacementActionCopy {
				return plan.CopyPath, nil
			}
			return plan.ReplaceTarget, nil
		}
	}
	return "", fmt.Errorf("flow %q has no publish step (replace or handoff)", job.Flow.Name)
}

// PrepareDestination plans the publish destination and primes it for the
// encode: the destination directory is created (with handoff permissions for
// download libraries), leftovers of a crashed earlier attempt are removed,
// and the job context points at the part path the artifact is written to.
func PrepareDestination(job *pipeline.JobContext) error {
	if job == nil {
		return errors.New("destination job context is required")
	}
	if job.Job.ID == 0 {
		return errors.New("destination planning requires a persisted job")
	}
	destination, err := PlanDestination(job)
	if err != nil {
		return err
	}
	dir := filepath.Dir(destination)
	if flowHasHandoff(job.Flow) {
		if err := prepareHandoffDestination(job.Library.Download.HandoffPath, dir); err != nil {
			return err
		}
	} else if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}
	jobLabel := PartJobLabel(job.Job.ID)
	if err := CleanupPartFiles(destination, jobLabel); err != nil {
		return fmt.Errorf("remove stale artifact parts: %w", err)
	}
	job.DestinationPath = destination
	job.OutputPath = PartPath(destination, jobLabel)
	return nil
}

// PartJobLabel is the part-path label for a persisted job.
func PartJobLabel(id domain.JobID) string {
	return strconv.FormatInt(int64(id), 10)
}

// CleanupPartFiles removes the job's unpublished artifact beside its
// destination. The part namespace is Anvil's own; the published destination
// and other jobs' parts are never touched.
func CleanupPartFiles(destination string, jobLabel string) error {
	destination = strings.TrimSpace(destination)
	if destination == "" || strings.TrimSpace(jobLabel) == "" {
		return nil
	}
	paths := []string{PartPath(destination, jobLabel)}
	// The first destination-side layout wrote <destination>.anvil-part (plus
	// work variants) without a job label; current code never writes that
	// name, so reclaiming pre-upgrade orphans here is safe.
	legacy := destination + PartSuffix
	paths = append(paths, legacy)
	variants, err := filepath.Glob(legacy + ".*")
	if err != nil {
		return fmt.Errorf("match legacy part variants: %w", err)
	}
	paths = append(paths, variants...)
	var removeErrs []error
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErrs = append(removeErrs, fmt.Errorf("remove %q: %w", path, err))
		}
	}
	return errors.Join(removeErrs...)
}

func flowHasHandoff(flow domain.Flow) bool {
	for _, step := range flow.Steps {
		if strings.EqualFold(strings.TrimSpace(step.Name), "handoff") {
			return true
		}
	}
	return false
}

// containerExt maps the profile container to its file extension. Anvil
// outputs MKV only (config validation enforces it); the mapping exists so a
// second container has exactly one place to land.
func containerExt(container string) string {
	_ = container
	return ".mkv"
}
