package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zekurio/anvil/pkg/domain"
)

type BackupResult struct {
	Path      string
	SizeBytes int64
	Integrity string
}

type PruneMissingSourceJobsOptions struct {
	LibraryName domain.LibraryName
	States      []domain.JobState
	Apply       bool
}

type PruneMissingSourceJobsResult struct {
	DryRun          bool
	MatchedJobs     int64
	AffectedSources int64
	DeletedJobs     int64
	ByState         map[domain.JobState]int64
	// ProtectedJobs lists jobs that matched every other prune condition but
	// still own an unresolved publish journal. Deleting them would cascade the
	// journal row away and strand the staged artifact, destination, or backup
	// file it is the only remaining record of.
	ProtectedJobs []ProtectedJob
}

func (s *SQLiteStore) Backup(ctx context.Context, destination string) (result BackupResult, err error) {
	destination, err = safeBackupDestination(ctx, s.db, destination)
	if err != nil {
		return BackupResult{}, err
	}

	parent := filepath.Dir(destination)
	temporary, err := os.CreateTemp(parent, ".anvil-backup-*.db")
	if err != nil {
		return BackupResult{}, fmt.Errorf("reserve backup temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return BackupResult{}, errors.Join(fmt.Errorf("close backup temporary file: %w", err), os.Remove(temporaryPath))
	}
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && err == nil {
			err = fmt.Errorf("remove backup temporary file: %w", removeErr)
		}
	}()

	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, temporaryPath); err != nil {
		return BackupResult{}, fmt.Errorf("create sqlite backup: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return BackupResult{}, fmt.Errorf("restrict backup permissions: %w", err)
	}
	if err := syncFile(temporaryPath); err != nil {
		return BackupResult{}, err
	}
	integrity, err := checkSQLiteIntegrity(ctx, temporaryPath)
	if err != nil {
		return BackupResult{}, err
	}
	if integrity != "ok" {
		return BackupResult{}, fmt.Errorf("sqlite backup integrity check returned %q", integrity)
	}

	if err := os.Link(temporaryPath, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return BackupResult{}, fmt.Errorf("backup destination %q already exists", destination)
		}
		return BackupResult{}, fmt.Errorf("install backup at %q without replacement: %w", destination, err)
	}
	if err := syncDirectory(parent); err != nil {
		return BackupResult{}, fmt.Errorf("backup installed at %q but directory sync failed: %w", destination, err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		return BackupResult{}, fmt.Errorf("stat installed backup: %w", err)
	}
	return BackupResult{Path: destination, SizeBytes: info.Size(), Integrity: integrity}, nil
}

func safeBackupDestination(ctx context.Context, db *sql.DB, destination string) (string, error) {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return "", errors.New("backup destination is required")
	}
	if destination == ":memory:" || strings.HasPrefix(destination, "file:") {
		return "", fmt.Errorf("backup destination %q must be a filesystem path", destination)
	}
	absDestination, err := filepath.Abs(destination)
	if err != nil {
		return "", fmt.Errorf("resolve backup destination: %w", err)
	}
	if _, err := os.Lstat(absDestination); err == nil {
		return "", fmt.Errorf("backup destination %q already exists", absDestination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect backup destination %q: %w", absDestination, err)
	}

	parent := filepath.Dir(absDestination)
	info, err := os.Stat(parent)
	if err != nil {
		return "", fmt.Errorf("inspect backup destination directory %q: %w", parent, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("backup destination parent %q is not a directory", parent)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve backup destination directory %q: %w", parent, err)
	}
	resolvedDestination := filepath.Join(resolvedParent, filepath.Base(absDestination))

	sourcePath, err := sqliteMainPath(ctx, db)
	if err != nil {
		return "", err
	}
	if sourcePath != "" {
		resolvedSource, err := filepath.EvalSymlinks(sourcePath)
		if err != nil {
			return "", fmt.Errorf("resolve sqlite store path %q: %w", sourcePath, err)
		}
		if filepath.Clean(resolvedSource) == filepath.Clean(resolvedDestination) {
			return "", fmt.Errorf("backup destination %q is the live sqlite store", absDestination)
		}
	}
	return absDestination, nil
}

func sqliteMainPath(ctx context.Context, db *sql.DB) (path string, err error) {
	rows, err := db.QueryContext(ctx, `PRAGMA database_list`)
	if err != nil {
		return "", fmt.Errorf("list sqlite databases: %w", err)
	}
	defer closeRows(rows, &err, "close sqlite database list")
	for rows.Next() {
		var sequence int
		var name string
		var file string
		if err := rows.Scan(&sequence, &name, &file); err != nil {
			return "", fmt.Errorf("scan sqlite database list: %w", err)
		}
		if name == "main" {
			return file, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate sqlite database list: %w", err)
	}
	return "", errors.New("sqlite main database is unavailable")
}

func checkSQLiteIntegrity(ctx context.Context, path string) (string, error) {
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return "", fmt.Errorf("open sqlite backup for integrity check: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close sqlite backup after integrity check: %w", closeErr)
		}
	}()
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return "", fmt.Errorf("check sqlite backup integrity: %w", err)
	}
	return integrity, nil
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open sqlite backup for sync: %w", err)
	}
	defer file.Close() //nolint:errcheck // the sync result is the durability boundary
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync sqlite backup: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close() //nolint:errcheck // the sync result is the durability boundary
	return directory.Sync()
}

func (s *SQLiteStore) PruneMissingSourceJobs(ctx context.Context, options PruneMissingSourceJobsOptions) (PruneMissingSourceJobsResult, error) {
	states, err := terminalPruneStates(options.States)
	if err != nil {
		return PruneMissingSourceJobsResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PruneMissingSourceJobsResult{}, fmt.Errorf("begin missing-source job prune transaction: %w", err)
	}
	defer rollback(tx)

	where, args := missingSourceJobsWhere(options.LibraryName, states)
	result := PruneMissingSourceJobsResult{
		DryRun:  !options.Apply,
		ByState: make(map[domain.JobState]int64),
	}
	protectedWhere, protectedArgs := missingSourceJobsPublishHoldWhere(options.LibraryName, states)
	protected, err := scanProtectedJobs(ctx, tx, protectedWhere, protectedArgs)
	if err != nil {
		return PruneMissingSourceJobsResult{}, err
	}
	result.ProtectedJobs = protected
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*), COUNT(DISTINCT j.source_id)
FROM jobs j
JOIN media_sources s ON s.id = j.source_id
WHERE `+where, args...).Scan(&result.MatchedJobs, &result.AffectedSources); err != nil {
		return PruneMissingSourceJobsResult{}, fmt.Errorf("count missing-source jobs to prune: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
SELECT j.state, COUNT(*)
FROM jobs j
JOIN media_sources s ON s.id = j.source_id
WHERE `+where+`
GROUP BY j.state`, args...)
	if err != nil {
		return PruneMissingSourceJobsResult{}, fmt.Errorf("count missing-source jobs by state: %w", err)
	}
	for rows.Next() {
		var state domain.JobState
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			closeErr := rows.Close()
			return PruneMissingSourceJobsResult{}, errors.Join(
				fmt.Errorf("scan missing-source job state count: %w", err),
				wrapCloseError(closeErr, "close missing-source job state counts"),
			)
		}
		result.ByState[state] = count
	}
	if err := rows.Err(); err != nil {
		closeErr := rows.Close()
		return PruneMissingSourceJobsResult{}, errors.Join(
			fmt.Errorf("iterate missing-source job state counts: %w", err),
			wrapCloseError(closeErr, "close missing-source job state counts"),
		)
	}
	if err := rows.Close(); err != nil {
		return PruneMissingSourceJobsResult{}, fmt.Errorf("close missing-source job state counts: %w", err)
	}

	if !options.Apply {
		return result, nil
	}
	deleted, err := tx.ExecContext(ctx, `
DELETE FROM jobs
WHERE id IN (
	SELECT j.id
	FROM jobs j
	JOIN media_sources s ON s.id = j.source_id
	WHERE `+where+`
)`, args...)
	if err != nil {
		return PruneMissingSourceJobsResult{}, fmt.Errorf("delete missing-source jobs: %w", err)
	}
	result.DeletedJobs, err = deleted.RowsAffected()
	if err != nil {
		return PruneMissingSourceJobsResult{}, fmt.Errorf("count deleted missing-source jobs: %w", err)
	}
	if result.DeletedJobs != result.MatchedJobs {
		return PruneMissingSourceJobsResult{}, fmt.Errorf("missing-source job prune deleted %d jobs after matching %d", result.DeletedJobs, result.MatchedJobs)
	}
	if err := tx.Commit(); err != nil {
		return PruneMissingSourceJobsResult{}, fmt.Errorf("commit missing-source job prune: %w", err)
	}
	return result, nil
}

func wrapCloseError(err error, operation string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func terminalPruneStates(states []domain.JobState) ([]domain.JobState, error) {
	if len(states) == 0 {
		return []domain.JobState{
			domain.JobStateComplete, domain.JobStateFailed,
			domain.JobStateSkipped, domain.JobStateCanceled,
		}, nil
	}
	seen := make(map[domain.JobState]struct{}, len(states))
	result := make([]domain.JobState, 0, len(states))
	for _, state := range states {
		if !state.Terminal() {
			return nil, fmt.Errorf("job state %q is not terminal and cannot be pruned", state)
		}
		if _, ok := seen[state]; ok {
			continue
		}
		seen[state] = struct{}{}
		result = append(result, state)
	}
	return result, nil
}

// missingSourceJobsWhere selects prunable jobs. The publish guard is part of
// the selector rather than a separate pre-check, so the count, the per-state
// breakdown, and the delete can never disagree about which jobs are in scope.
func missingSourceJobsWhere(library domain.LibraryName, states []domain.JobState) (string, []any) {
	where, args := missingSourceJobsBaseWhere(library, states)
	return where + " AND " + noUnresolvedPublishCondition, append(args, committedPublishStage())
}

// missingSourceJobsPublishHoldWhere selects the jobs the guard excludes.
func missingSourceJobsPublishHoldWhere(library domain.LibraryName, states []domain.JobState) (string, []any) {
	where, args := missingSourceJobsBaseWhere(library, states)
	return where + " AND " + unresolvedPublishCondition, append(args, committedPublishStage())
}

func missingSourceJobsBaseWhere(library domain.LibraryName, states []domain.JobState) (string, []any) {
	where := "s.status = ? AND j.state IN (" + placeholders(len(states)) + ")"
	args := make([]any, 0, len(states)+3)
	args = append(args, string(domain.MediaSourceMissing))
	for _, state := range states {
		args = append(args, string(state))
	}
	if library != "" {
		where += " AND j.library_name = ?"
		args = append(args, string(library))
	}
	return where, args
}

func scanProtectedJobs(ctx context.Context, tx *sql.Tx, where string, args []any) (protected []ProtectedJob, err error) {
	rows, err := tx.QueryContext(ctx, `
SELECT j.id, j.slug, j.state
FROM jobs j
JOIN media_sources s ON s.id = j.source_id
WHERE `+where+`
ORDER BY j.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs held by a publish journal: %w", err)
	}
	defer closeRows(rows, &err, "close jobs held by a publish journal")
	for rows.Next() {
		job := ProtectedJob{Reason: JobProtectedPublishJournal}
		var slug *string
		if err := rows.Scan(&job.JobID, &slug, &job.State); err != nil {
			return nil, fmt.Errorf("scan job held by a publish journal: %w", err)
		}
		if slug != nil {
			job.Slug = *slug
		}
		protected = append(protected, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs held by a publish journal: %w", err)
	}
	return protected, nil
}
