package replace

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/pipeline"
)

type PublishStage string

const (
	PublishStagePrepared      PublishStage = "prepared"
	PublishStagePublished     PublishStage = "published"
	PublishStageSourceCleaned PublishStage = "source_cleaned"
	PublishStageCommitted     PublishStage = "committed"
	PublishStageConflict      PublishStage = "conflict"
)

const (
	publishKindReplacement = "replacement"
	publishKindHandoff     = "handoff"
	digestAlgorithmSHA256  = "sha256"
)

var (
	ErrPublishConflict = errors.New("publish destination conflict")
	ErrPublishPending  = errors.New("publish recovery pending")
)

type FileIdentity struct {
	SizeBytes int64  `json:"size_bytes"`
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
}

type PublishOperation struct {
	JobID               domain.JobID  `json:"job_id"`
	Kind                string        `json:"kind"`
	Mode                string        `json:"mode"`
	Stage               PublishStage  `json:"stage"`
	ArtifactPath        string        `json:"artifact_path"`
	ArtifactStagingDir  string        `json:"artifact_staging_dir,omitempty"`
	DestinationPath     string        `json:"destination_path"`
	BackupPath          string        `json:"backup_path,omitempty"`
	CleanupSourcePath   string        `json:"cleanup_source_path,omitempty"`
	PruneRoot           string        `json:"prune_root,omitempty"`
	PruneStart          string        `json:"prune_start,omitempty"`
	IgnorableGlobs      []string      `json:"ignorable_globs,omitempty"`
	ArtifactIdentity    FileIdentity  `json:"artifact_identity"`
	CleanupIdentity     *FileIdentity `json:"cleanup_identity,omitempty"`
	DigestAlgorithm     string        `json:"digest_algorithm,omitempty"`
	DigestValue         string        `json:"digest_value,omitempty"`
	CleanupArtifact     bool          `json:"cleanup_artifact"`
	CleanupSource       bool          `json:"cleanup_source"`
	CleanupBackup       bool          `json:"cleanup_backup"`
	PruneEmptyDirs      bool          `json:"prune_empty_dirs"`
	SetHandoffModes     bool          `json:"set_handoff_modes"`
	HandoffRoot         string        `json:"handoff_root,omitempty"`
	ConflictDescription string        `json:"conflict_description,omitempty"`
	CreatedAt           time.Time     `json:"created_at"`
	UpdatedAt           time.Time     `json:"updated_at"`
}

type PublishJournal interface {
	GetPublishOperation(context.Context, domain.JobID) (PublishOperation, bool, error)
	CreatePublishOperation(context.Context, PublishOperation) error
	UpdatePublishOperation(context.Context, PublishOperation, PublishStage) error
}

type PublishBoundary string

const (
	BoundaryPrepared           PublishBoundary = "prepared"
	BoundaryBackupLinked       PublishBoundary = "backup_linked"
	BoundaryOriginalBackedUp   PublishBoundary = "original_backed_up"
	BoundaryDestinationCreated PublishBoundary = "destination_created"
	BoundaryPublished          PublishBoundary = "published"
	BoundaryDestinationMode    PublishBoundary = "destination_mode"
	BoundaryArtifactRemoved    PublishBoundary = "artifact_removed"
	BoundarySourceRemoved      PublishBoundary = "source_removed"
	BoundaryDirectoriesPruned  PublishBoundary = "directories_pruned"
	BoundaryBackupRemoved      PublishBoundary = "backup_removed"
	BoundarySourceCleaned      PublishBoundary = "source_cleaned"
	BoundaryCommitted          PublishBoundary = "committed"
)

type Manager struct {
	Journal      PublishJournal
	Now          func() time.Time
	Hook         func(PublishBoundary) error
	LinkArtifact func(string, string) error
	DigestFile   func(context.Context, string) (string, error)
}

type ConflictError struct {
	Destination string
	Reason      string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("publish conflict at %q: %s", e.Destination, e.Reason)
}

func (e *ConflictError) Is(target error) bool {
	return target == ErrPublishConflict || target == ErrPublishPending
}

type pendingError struct {
	err error
}

func (e *pendingError) Error() string {
	return fmt.Sprintf("publish recovery pending: %v", e.err)
}

func (e *pendingError) Unwrap() error {
	return e.err
}

func (e *pendingError) Is(target error) bool {
	return target == ErrPublishPending
}

func (m Manager) Replace(ctx context.Context, job *pipeline.JobContext) (string, error) {
	op, err := m.replacementOperation(job)
	if err != nil {
		return "", err
	}
	return m.execute(ctx, op)
}

func (m Manager) Handoff(ctx context.Context, job *pipeline.JobContext) (string, error) {
	op, err := m.handoffOperation(job)
	if err != nil {
		return "", err
	}
	return m.execute(ctx, op)
}

// Recover resumes a journaled operation before the pipeline touches an input or
// attempt-local staging path that may already have been removed.
func (m Manager) Recover(ctx context.Context, job *pipeline.JobContext) (bool, error) {
	if m.Journal == nil || job == nil || job.Job.ID == 0 {
		return false, nil
	}
	op, ok, err := m.Journal.GetPublishOperation(ctx, job.Job.ID)
	if err != nil {
		return false, fmt.Errorf("load publish operation: %w", err)
	}
	if !ok {
		return false, nil
	}
	finalPath, err := m.resume(ctx, op)
	if err != nil {
		return true, err
	}
	job.FinalPath = finalPath
	return true, nil
}

func (m Manager) execute(ctx context.Context, intended PublishOperation) (string, error) {
	if m.Journal == nil {
		return "", errors.New("publish journal is required")
	}
	if intended.JobID == 0 {
		return "", errors.New("publish job ID is required")
	}

	op, ok, err := m.Journal.GetPublishOperation(ctx, intended.JobID)
	if err != nil {
		return "", fmt.Errorf("load publish operation: %w", err)
	}
	if ok {
		if !samePublishIntent(op, intended) {
			return "", &ConflictError{Destination: op.DestinationPath, Reason: "journaled operation does not match the resolved publish plan"}
		}
		return m.resume(ctx, op)
	}

	now := m.now()
	intended.Stage = PublishStagePrepared
	intended.CreatedAt = now
	intended.UpdatedAt = now
	if err := m.Journal.CreatePublishOperation(ctx, intended); err != nil {
		return "", fmt.Errorf("prepare publish operation: %w", err)
	}
	if err := m.boundary(BoundaryPrepared); err != nil {
		return "", pending(err)
	}
	return m.resume(ctx, intended)
}

func (m Manager) resume(ctx context.Context, op PublishOperation) (string, error) {
	if op.Stage == PublishStageConflict {
		return "", &ConflictError{Destination: op.DestinationPath, Reason: op.ConflictDescription}
	}
	if op.Stage == PublishStagePrepared {
		if err := m.publish(ctx, &op); err != nil {
			return "", pendingUnlessConflict(err)
		}
	}
	if op.Stage == PublishStagePublished {
		if err := m.cleanup(ctx, &op); err != nil {
			return "", pendingUnlessConflict(err)
		}
	}
	if op.Stage == PublishStageSourceCleaned {
		if err := m.advance(ctx, &op, PublishStageCommitted); err != nil {
			return "", pending(err)
		}
		if err := m.boundary(BoundaryCommitted); err != nil {
			return "", pending(err)
		}
	}
	if op.Stage != PublishStageCommitted {
		return "", pending(fmt.Errorf("unexpected publish stage %q", op.Stage))
	}
	return op.DestinationPath, nil
}

func (m Manager) publish(ctx context.Context, op *PublishOperation) error {
	if op.Kind == publishKindReplacement && op.Mode == replacementActionReplace {
		published, err := m.prepareReplacement(ctx, op)
		if err != nil {
			return err
		}
		if published {
			return m.markPublished(ctx, op)
		}
	}
	if op.SetHandoffModes {
		if err := prepareHandoffDestination(op.HandoffRoot, filepath.Dir(op.DestinationPath)); err != nil {
			return err
		}
	}

	if _, err := os.Lstat(op.DestinationPath); err == nil {
		matches, digest, matchErr := m.destinationMatches(ctx, *op)
		if matchErr != nil {
			return matchErr
		}
		if !matches {
			return m.conflict(ctx, op, "destination exists with different artifact content")
		}
		if digest != "" {
			op.DigestAlgorithm = digestAlgorithmSHA256
			op.DigestValue = digest
		}
		return m.markPublished(ctx, op)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect publish destination: %w", err)
	}

	if err := verifyRecordedIdentity(op.ArtifactPath, op.ArtifactIdentity); err != nil {
		return m.conflict(ctx, op, fmt.Sprintf("artifact identity changed: %v", err))
	}
	if err := m.publishArtifact(op); err != nil {
		if errors.Is(err, os.ErrExist) {
			matches, digest, matchErr := m.destinationMatches(ctx, *op)
			if matchErr != nil {
				return matchErr
			}
			if !matches {
				return m.conflict(ctx, op, "destination appeared with different artifact content")
			}
			if digest != "" {
				op.DigestAlgorithm = digestAlgorithmSHA256
				op.DigestValue = digest
			}
		} else {
			return err
		}
	}
	if err := m.boundary(BoundaryDestinationCreated); err != nil {
		return err
	}
	return m.markPublished(ctx, op)
}

func (m Manager) markPublished(ctx context.Context, op *PublishOperation) error {
	if err := m.advance(ctx, op, PublishStagePublished); err != nil {
		return err
	}
	return m.boundary(BoundaryPublished)
}

func (m Manager) prepareReplacement(ctx context.Context, op *PublishOperation) (bool, error) {
	targetExists := pathExists(op.DestinationPath)
	backupExists := pathExists(op.BackupPath)

	if targetExists && backupExists {
		if op.DestinationPath == op.CleanupSourcePath && op.CleanupIdentity != nil {
			targetOriginalErr := verifyRecordedIdentity(op.DestinationPath, *op.CleanupIdentity)
			backupOriginalErr := verifyRecordedIdentity(op.BackupPath, *op.CleanupIdentity)
			if targetOriginalErr == nil && backupOriginalErr == nil {
				if err := removeAndSync(op.CleanupSourcePath); err != nil {
					return false, fmt.Errorf("finish backing up replacement source: %w", err)
				}
				return false, nil
			}
		}
		matches, digest, err := m.destinationMatches(ctx, *op)
		if err != nil {
			return false, err
		}
		if !matches {
			return false, m.conflict(ctx, op, "replacement target exists with different artifact content")
		}
		if digest != "" {
			op.DigestAlgorithm = digestAlgorithmSHA256
			op.DigestValue = digest
		}
		return true, nil
	}
	if targetExists && op.DestinationPath != op.CleanupSourcePath {
		return false, m.conflict(ctx, op, "replacement target already exists")
	}
	if backupExists {
		if op.CleanupIdentity == nil {
			return false, errors.New("replacement source identity is missing")
		}
		if err := verifyRecordedIdentity(op.BackupPath, *op.CleanupIdentity); err != nil {
			return false, m.conflict(ctx, op, fmt.Sprintf("replacement backup identity mismatch: %v", err))
		}
		if pathExists(op.CleanupSourcePath) {
			if err := verifyRecordedIdentity(op.CleanupSourcePath, *op.CleanupIdentity); err != nil {
				return false, m.conflict(ctx, op, fmt.Sprintf("replacement source identity changed: %v", err))
			}
			if err := removeAndSync(op.CleanupSourcePath); err != nil {
				return false, fmt.Errorf("finish backing up replacement source: %w", err)
			}
		}
		return false, nil
	}
	if op.CleanupIdentity == nil {
		return false, errors.New("replacement source identity is missing")
	}
	if err := verifyRecordedIdentity(op.CleanupSourcePath, *op.CleanupIdentity); err != nil {
		return false, m.conflict(ctx, op, fmt.Sprintf("replacement source identity changed: %v", err))
	}
	if err := linkAndSync(op.CleanupSourcePath, op.BackupPath); err != nil {
		return false, fmt.Errorf("backup original before replace: %w", err)
	}
	if err := m.boundary(BoundaryBackupLinked); err != nil {
		return false, err
	}
	if err := removeAndSync(op.CleanupSourcePath); err != nil {
		return false, fmt.Errorf("remove original after backup: %w", err)
	}
	if err := m.boundary(BoundaryOriginalBackedUp); err != nil {
		return false, err
	}
	return false, nil
}

func (m Manager) cleanup(ctx context.Context, op *PublishOperation) error {
	if op.SetHandoffModes {
		if err := os.Chmod(op.DestinationPath, handoffFileMode); err != nil {
			return fmt.Errorf("set handoff destination mode: %w", err)
		}
		if err := syncFile(op.DestinationPath); err != nil {
			return fmt.Errorf("sync handoff destination mode: %w", err)
		}
		if err := m.boundary(BoundaryDestinationMode); err != nil {
			return err
		}
	}
	if op.CleanupArtifact && pathExists(op.ArtifactPath) {
		if err := verifyRecordedIdentity(op.ArtifactPath, op.ArtifactIdentity); err != nil {
			return m.conflict(ctx, op, fmt.Sprintf("artifact changed before cleanup: %v", err))
		}
		if err := removeAndSync(op.ArtifactPath); err != nil {
			return fmt.Errorf("remove published artifact source: %w", err)
		}
		if err := m.boundary(BoundaryArtifactRemoved); err != nil {
			return err
		}
	}
	if op.ArtifactStagingDir != "" {
		if err := os.Remove(op.ArtifactStagingDir); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTEMPTY) {
			return fmt.Errorf("remove empty artifact staging dir: %w", err)
		}
	}
	if op.CleanupSource && pathExists(op.CleanupSourcePath) {
		if op.CleanupIdentity == nil {
			return errors.New("cleanup source identity is missing")
		}
		if err := verifyRecordedIdentity(op.CleanupSourcePath, *op.CleanupIdentity); err != nil {
			return m.conflict(ctx, op, fmt.Sprintf("cleanup source identity changed: %v", err))
		}
		if err := removeAndSync(op.CleanupSourcePath); err != nil {
			return fmt.Errorf("remove source media after handoff: %w", err)
		}
		if err := m.boundary(BoundarySourceRemoved); err != nil {
			return err
		}
	}
	if op.PruneEmptyDirs {
		if err := PruneEmptyDirs(op.PruneRoot, op.PruneStart, op.IgnorableGlobs); err != nil {
			return err
		}
		if err := m.boundary(BoundaryDirectoriesPruned); err != nil {
			return err
		}
	}
	if op.CleanupBackup && pathExists(op.BackupPath) {
		if op.CleanupIdentity == nil {
			return errors.New("replacement backup identity is missing")
		}
		if err := verifyRecordedIdentity(op.BackupPath, *op.CleanupIdentity); err != nil {
			return m.conflict(ctx, op, fmt.Sprintf("replacement backup identity changed: %v", err))
		}
		if err := removeAndSync(op.BackupPath); err != nil {
			return fmt.Errorf("remove replacement backup: %w", err)
		}
		if err := m.boundary(BoundaryBackupRemoved); err != nil {
			return err
		}
	}
	if err := m.advance(ctx, op, PublishStageSourceCleaned); err != nil {
		return err
	}
	return m.boundary(BoundarySourceCleaned)
}

func (m Manager) replacementOperation(job *pipeline.JobContext) (PublishOperation, error) {
	if job == nil {
		return PublishOperation{}, errors.New("replacement job context is required")
	}
	plan, err := PlanReplacement(job.InputPath, job.OutputPath, job.Library.Media.ReplacementMode)
	if err != nil {
		return PublishOperation{}, err
	}
	identity, err := statIdentity(job.OutputPath)
	if err != nil {
		return PublishOperation{}, fmt.Errorf("identify replacement artifact: %w", err)
	}
	op := PublishOperation{
		JobID:              job.Job.ID,
		Kind:               publishKindReplacement,
		ArtifactPath:       job.OutputPath,
		ArtifactStagingDir: job.StagingDir,
		ArtifactIdentity:   identity,
		CleanupArtifact:    true,
	}
	if plan.Action == replacementActionCopy {
		op.Mode = replacementActionCopy
		op.DestinationPath = plan.CopyPath
		return op, nil
	}
	originalIdentity, err := statIdentity(job.InputPath)
	if err != nil {
		return PublishOperation{}, fmt.Errorf("identify replacement source: %w", err)
	}
	op.Mode = replacementActionReplace
	op.DestinationPath = plan.ReplaceTarget
	op.BackupPath = plan.BackupPath
	op.CleanupSourcePath = job.InputPath
	op.CleanupIdentity = &originalIdentity
	op.CleanupBackup = true
	return op, nil
}

func (m Manager) handoffOperation(job *pipeline.JobContext) (PublishOperation, error) {
	plan, err := PlanHandoff(job)
	if err != nil {
		return PublishOperation{}, err
	}
	identity, err := statIdentity(job.OutputPath)
	if err != nil {
		return PublishOperation{}, fmt.Errorf("identify handoff artifact: %w", err)
	}
	op := PublishOperation{
		JobID:              job.Job.ID,
		Kind:               publishKindHandoff,
		Mode:               plan.Action,
		ArtifactPath:       job.OutputPath,
		ArtifactStagingDir: job.StagingDir,
		DestinationPath:    plan.Destination,
		CleanupSourcePath:  plan.SourceMediaPath,
		PruneRoot:          job.Library.Path,
		PruneStart:         plan.PruneStart,
		IgnorableGlobs:     append([]string(nil), job.Library.Download.IgnorableGlobs...),
		ArtifactIdentity:   identity,
		CleanupArtifact:    true,
		CleanupSource:      plan.CleanupSourceMedia,
		PruneEmptyDirs:     plan.CleanupSourceMedia && plan.PruneEmptyDirs,
		SetHandoffModes:    true,
		HandoffRoot:        job.Library.Download.HandoffPath,
	}
	if op.CleanupSource {
		sourceIdentity, err := statIdentity(plan.SourceMediaPath)
		if err != nil {
			return PublishOperation{}, fmt.Errorf("identify handoff cleanup source: %w", err)
		}
		op.CleanupIdentity = &sourceIdentity
	}
	return op, nil
}

func (m Manager) publishArtifact(op *PublishOperation) error {
	if err := os.MkdirAll(filepath.Dir(op.DestinationPath), 0o750); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}
	if op.Mode != replacementActionCopy && op.Mode != handoffActionCopy {
		link := m.LinkArtifact
		if link == nil {
			link = os.Link
		}
		if err := link(op.ArtifactPath, op.DestinationPath); err == nil {
			return syncDir(filepath.Dir(op.DestinationPath))
		} else if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		} else if !errors.Is(err, syscall.EXDEV) && !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.ENOTSUP) {
			return fmt.Errorf("publish artifact link: %w", err)
		}
	}
	return copyFileExclusive(op.ArtifactPath, op.DestinationPath)
}

func (m Manager) destinationMatches(ctx context.Context, op PublishOperation) (bool, string, error) {
	destinationIdentity, err := statIdentity(op.DestinationPath)
	if err != nil {
		return false, "", fmt.Errorf("identify existing destination: %w", err)
	}
	if destinationIdentity.SizeBytes != op.ArtifactIdentity.SizeBytes {
		return false, "", nil
	}
	if sameFileIdentity(destinationIdentity, op.ArtifactIdentity) {
		return true, op.DigestValue, nil
	}
	if op.DigestValue != "" {
		digest, err := m.digest(ctx, op.DestinationPath)
		if err != nil {
			return false, "", err
		}
		return digest == op.DigestValue, op.DigestValue, nil
	}
	if err := verifyRecordedIdentity(op.ArtifactPath, op.ArtifactIdentity); err != nil {
		return false, "", fmt.Errorf("cannot compare existing destination because artifact is unavailable: %w", err)
	}
	expectedDigest, err := m.digest(ctx, op.ArtifactPath)
	if err != nil {
		return false, "", err
	}
	destinationDigest, err := m.digest(ctx, op.DestinationPath)
	if err != nil {
		return false, "", err
	}
	return expectedDigest == destinationDigest, expectedDigest, nil
}

func (m Manager) digest(ctx context.Context, path string) (digest string, err error) {
	if m.DigestFile != nil {
		return m.DigestFile(ctx, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %q for digest: %w", path, err)
	}
	defer closeFile(file, "digest source", &err)
	hash := sha256.New()
	buffer := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			if _, err := hash.Write(buffer[:n]); err != nil {
				return "", fmt.Errorf("hash %q: %w", path, err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("read %q for digest: %w", path, readErr)
		}
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func (m Manager) conflict(ctx context.Context, op *PublishOperation, reason string) error {
	previous := op.Stage
	op.Stage = PublishStageConflict
	op.ConflictDescription = reason
	op.UpdatedAt = m.now()
	if err := m.Journal.UpdatePublishOperation(ctx, *op, previous); err != nil {
		return errors.Join(&ConflictError{Destination: op.DestinationPath, Reason: reason}, fmt.Errorf("record publish conflict: %w", err))
	}
	return &ConflictError{Destination: op.DestinationPath, Reason: reason}
}

func (m Manager) advance(ctx context.Context, op *PublishOperation, stage PublishStage) error {
	previous := op.Stage
	op.Stage = stage
	op.UpdatedAt = m.now()
	if err := m.Journal.UpdatePublishOperation(ctx, *op, previous); err != nil {
		op.Stage = previous
		return fmt.Errorf("advance publish operation from %q to %q: %w", previous, stage, err)
	}
	return nil
}

func (m Manager) boundary(boundary PublishBoundary) error {
	if m.Hook == nil {
		return nil
	}
	if err := m.Hook(boundary); err != nil {
		return fmt.Errorf("after publish boundary %q: %w", boundary, err)
	}
	return nil
}

func (m Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func samePublishIntent(existing, intended PublishOperation) bool {
	existing.Stage = ""
	intended.Stage = ""
	existing.ArtifactPath = ""
	intended.ArtifactPath = ""
	existing.ArtifactStagingDir = ""
	intended.ArtifactStagingDir = ""
	existing.ArtifactIdentity = FileIdentity{}
	intended.ArtifactIdentity = FileIdentity{}
	existing.DigestAlgorithm = ""
	intended.DigestAlgorithm = ""
	existing.DigestValue = ""
	intended.DigestValue = ""
	existing.ConflictDescription = ""
	intended.ConflictDescription = ""
	existing.CreatedAt = time.Time{}
	intended.CreatedAt = time.Time{}
	existing.UpdatedAt = time.Time{}
	intended.UpdatedAt = time.Time{}
	return reflect.DeepEqual(existing, intended)
}

func statIdentity(path string) (FileIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileIdentity{}, err
	}
	identity := FileIdentity{SizeBytes: info.Size()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		identity.Device = uint64(stat.Dev)
		identity.Inode = uint64(stat.Ino)
	}
	return identity, nil
}

func sameFileIdentity(left, right FileIdentity) bool {
	return left.Device != 0 && left.Inode != 0 && left.Device == right.Device && left.Inode == right.Inode && left.SizeBytes == right.SizeBytes
}

func verifyRecordedIdentity(path string, expected FileIdentity) error {
	actual, err := statIdentity(path)
	if err != nil {
		return err
	}
	if actual.SizeBytes != expected.SizeBytes || !sameFileIdentity(actual, expected) {
		return fmt.Errorf("identity for %q is size=%d device=%d inode=%d, want size=%d device=%d inode=%d", path, actual.SizeBytes, actual.Device, actual.Inode, expected.SizeBytes, expected.Device, expected.Inode)
	}
	return nil
}

func pathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Lstat(path)
	return err == nil
}

func copyFileExclusive(src, dst string) (err error) {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat source %q: %w", src, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source %q: %w", src, err)
	}
	defer closeFile(in, "source", &err)

	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary destination for %q: %w", dst, err)
	}
	tmpPath := tmp.Name()
	defer removeTempFile(tmpPath, &err)

	_, copyErr := io.Copy(tmp, in)
	chmodErr := tmp.Chmod(info.Mode().Perm())
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if copyErr != nil {
		return fmt.Errorf("copy %q to temporary destination: %w", src, copyErr)
	}
	if chmodErr != nil {
		return fmt.Errorf("set temporary destination mode: %w", chmodErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync temporary destination: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close temporary destination for %q: %w", dst, closeErr)
	}
	if err := os.Link(tmpPath, dst); err != nil {
		if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		return fmt.Errorf("publish temporary destination %q: %w", dst, err)
	}
	return syncDir(filepath.Dir(dst))
}

func linkAndSync(src, dst string) error {
	if err := os.Link(src, dst); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dst))
}

func removeAndSync(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	if errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.ENOTSUP) {
		syncErr = nil
	}
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}

func pending(err error) error {
	if err == nil {
		return nil
	}
	return &pendingError{err: err}
}

func pendingUnlessConflict(err error) error {
	if errors.Is(err, ErrPublishConflict) || errors.Is(err, ErrPublishPending) {
		return err
	}
	return pending(err)
}
