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
// is created on the destination filesystem under this suffix, so publication
// is a link plus unlink and never a bulk copy, regardless of where the daemon
// temp directory lives. The suffix is Anvil's namespace: the scanner never
// treats it as media, and media managers ignore the unknown extension, so a
// part file is never imported half-written.
const PartSuffix = ".anvil-part"

// HandoffWorkDir is the Anvil-owned directory directly below a download
// library's handoff_path where its artifacts are encoded. An importer that
// takes media from the handoff tree deletes a package directory once it has
// imported what it recognises, and a season pack publishes one episode at a
// time, so an artifact written inside the package directory can be swept
// away while a sibling job is still encoding or validating. The work
// directory is never a package path, so the importer has no reason to touch
// it, and it sits on the handoff filesystem so publication stays a hard link.
const HandoffWorkDir = ".anvil-work"

// ArtifactProtection reports whether an unresolved publish journal owns a
// path. Legacy part cleanup needs this before mutating the filesystem: unlike
// current parts, the first destination-side layout carried no job id in its
// name, so the filename alone cannot prove which job owns it.
type ArtifactProtection interface {
	PublishArtifactProtected(context.Context, string) (bool, error)
}

// PartPath returns the part path for an artifact published to destination,
// directly beside it. The job label keeps the path unique per job: two jobs
// can resolve to the same destination (same input basename handed off with
// preserve_relative_path disabled, say), and a shared part path would let one
// encoder truncate the other's artifact — or keep writing through the inode
// after it was linked under the final name. The publish step's no-clobber
// link then decides which job wins the destination.
func PartPath(destination string, jobLabel string) string {
	return fmt.Sprintf("%s.job-%s%s", destination, jobLabel, PartSuffix)
}

// ArtifactPath returns the working path for the artifact that will be
// published to destination. Media libraries write beside the destination;
// download libraries write under HandoffWorkDir so the artifact never sits in
// an importer-visible package directory before publication. Package-relative
// structure is dropped inside the work directory: the job label already keeps
// the name unique, and a flat directory leaves no empty parents to prune.
func ArtifactPath(library domain.Library, destination string, jobLabel string) string {
	if library.Kind != domain.LibraryKindDownload {
		return PartPath(destination, jobLabel)
	}
	return PartPath(filepath.Join(library.Download.HandoffPath, HandoffWorkDir, filepath.Base(destination)), jobLabel)
}

// PlanDestination resolves the final publish path for a job. It runs at stage
// time so the artifact can be written on the destination filesystem; publish
// consumes the same value from the job context instead of re-deriving it.
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

// PlanArtifactPaths sets the publish and part paths without changing files.
func PlanArtifactPaths(job *pipeline.JobContext) error {
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
	job.DestinationPath = destination
	job.OutputPath = ArtifactPath(job.Library, destination, PartJobLabel(job.Job.ID))
	return nil
}

// PrepareDestination creates the directory the artifact is encoded into and
// removes the job's stale part just before encoding. For download libraries
// that is the handoff work directory, never the package directory: the package
// directory first appears at publish time, so an importer cannot delete it
// under a running encode. Media managers can still remove handoff folders at
// any time, so this must run after the quality search.
func PrepareDestination(job *pipeline.JobContext) error {
	if job == nil || job.Job.ID == 0 {
		return errors.New("destination preparation requires a persisted job")
	}
	if strings.TrimSpace(job.DestinationPath) == "" || strings.TrimSpace(job.OutputPath) == "" {
		return errors.New("destination and output paths are required")
	}
	dir := filepath.Dir(job.OutputPath)
	if job.Library.Kind == domain.LibraryKindDownload {
		root := job.Library.Download.HandoffPath
		if err := prepareHandoffDestination(root, root); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create handoff work dir: %w", err)
		}
	} else if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}
	if err := CleanupPartFiles(job.Library, job.DestinationPath, PartJobLabel(job.Job.ID)); err != nil {
		return fmt.Errorf("remove stale artifact parts: %w", err)
	}
	return nil
}

// PartJobLabel is the part-path label for a persisted job.
func PartJobLabel(id domain.JobID) string {
	return strconv.FormatInt(int64(id), 10)
}

// CleanupPartFiles removes the job's unpublished artifact for destination.
// The job label makes ownership unambiguous, so the published destination,
// legacy unscoped artifacts, and other jobs' parts are untouched. Download
// libraries also reclaim the part beside the destination, where attempts
// that ran before the handoff work directory existed wrote it.
func CleanupPartFiles(library domain.Library, destination string, jobLabel string) error {
	destination = strings.TrimSpace(destination)
	if destination == "" || strings.TrimSpace(jobLabel) == "" {
		return nil
	}
	parts := []string{ArtifactPath(library, destination, jobLabel)}
	if beside := PartPath(destination, jobLabel); beside != parts[0] {
		parts = append(parts, beside)
	}
	var errs []error
	for _, part := range parts {
		if err := os.Remove(part); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove %q: %w", part, err))
		}
	}
	return errors.Join(errs...)
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
