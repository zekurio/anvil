package replace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// to destination.
func PartPath(destination string) string {
	return destination + PartSuffix
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
	if err := CleanupPartFiles(destination); err != nil {
		return fmt.Errorf("remove stale artifact parts: %w", err)
	}
	job.DestinationPath = destination
	job.OutputPath = PartPath(destination)
	return nil
}

// CleanupPartFiles removes the unpublished artifact and its Dolby Vision work
// variants for a destination. Anything matching the part namespace is Anvil's
// own residue from a failed attempt; the published destination itself is
// never touched.
func CleanupPartFiles(destination string) error {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return nil
	}
	part := PartPath(destination)
	paths := []string{part}
	variants, err := filepath.Glob(part + ".*")
	if err != nil {
		return fmt.Errorf("match artifact part variants: %w", err)
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
