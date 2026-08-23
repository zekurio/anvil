package replace

import (
	"context"
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

// ArtifactProtection reports whether an unresolved publish journal owns a
// path. Legacy part cleanup needs this before mutating the filesystem: unlike
// current parts, the first destination-side layout carried no job id in its
// name, so the filename alone cannot prove which job owns it.
type ArtifactProtection interface {
	PublishArtifactProtected(context.Context, string) (bool, error)
}

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

// PlanDestination resolves the final publish path for a job. It runs at stage
// time so the artifact can be written directly
// next to its destination; publish consumes the same value from the job
// context instead of re-deriving it.
func PlanDestination(job *pipeline.JobContext) (string, error) {
	if job == nil {
		return "", errors.New("destination job context is required")
	}
	if job.Library.Kind == domain.LibraryKindDownload {
		return handoffDestination(job, ".mkv")
	}
	plan, err := PlanReplacement(job.InputPath, ".mkv", job.Library.Media.ReplacementMode)
	if err != nil {
		return "", err
	}
	if plan.Action == replacementActionCopy {
		return plan.CopyPath, nil
	}
	return plan.ReplaceTarget, nil
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
	if job.Library.Kind == domain.LibraryKindDownload {
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
// destination. The job label makes ownership unambiguous, so the published
// destination, legacy unscoped artifacts, and other jobs' parts are untouched.
func CleanupPartFiles(destination string, jobLabel string) error {
	destination = strings.TrimSpace(destination)
	if destination == "" || strings.TrimSpace(jobLabel) == "" {
		return nil
	}
	part := PartPath(destination, jobLabel)
	if err := os.Remove(part); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %q: %w", part, err)
	}
	return nil
}

// CleanupLegacyPartFiles reclaims artifacts from the first destination-side
// layout, which wrote <destination>.anvil-part (plus work variants) without a
// job id. Every existing candidate is checked before anything is removed: if
// the journal lookup fails, no legacy artifact is touched.
func CleanupLegacyPartFiles(ctx context.Context, protection ArtifactProtection, destination string) error {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return nil
	}
	legacy := destination + PartSuffix
	dir := filepath.Dir(legacy)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list legacy part directory %q: %w", dir, err)
	}
	base := filepath.Base(legacy)
	prefix := base + "."
	var candidates []string
	for _, entry := range entries {
		if entry.Name() == base || strings.HasPrefix(entry.Name(), prefix) {
			candidates = append(candidates, filepath.Join(dir, entry.Name()))
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	if protection == nil {
		return errors.New("legacy part cleanup requires publish journal protection")
	}
	removable := make([]string, 0, len(candidates))
	for _, path := range candidates {
		protected, err := protection.PublishArtifactProtected(ctx, path)
		if err != nil {
			return fmt.Errorf("check legacy part protection for %q: %w", path, err)
		}
		if !protected {
			removable = append(removable, path)
		}
	}
	var removeErrs []error
	for _, path := range removable {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErrs = append(removeErrs, fmt.Errorf("remove %q: %w", path, err))
		}
	}
	return errors.Join(removeErrs...)
}
