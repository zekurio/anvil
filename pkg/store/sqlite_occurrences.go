package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func (s *SQLiteStore) ForceOccurrence(ctx context.Context, input ForceOccurrenceInput) (ForceOccurrenceResult, error) {
	if strings.TrimSpace(string(input.LibraryName)) == "" || strings.TrimSpace(input.SourceRelativePath) == "" || strings.TrimSpace(input.AssetRelativePath) == "" {
		return ForceOccurrenceResult{}, errors.New("force occurrence requires library, source path, and asset path")
	}
	if input.SourceKind != domain.SourceKindFile && input.SourceKind != domain.SourceKindPackage {
		return ForceOccurrenceResult{}, fmt.Errorf("invalid source kind %q", input.SourceKind)
	}
	now := defaultNow(input.Now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ForceOccurrenceResult{}, fmt.Errorf("begin force occurrence: %w", err)
	}
	defer rollback(tx)

	active, err := activeWorkForOccurrencePathTx(ctx, tx, input.LibraryName, input.SourceRelativePath, input.AssetRelativePath)
	if err != nil {
		return ForceOccurrenceResult{}, err
	}
	if active {
		return ForceOccurrenceResult{}, fmt.Errorf("force occurrence refused for %q: %w", input.SourceRelativePath, ErrActiveWork)
	}

	source, found, err := currentSourceTx(ctx, tx, input.LibraryName, input.SourceRelativePath)
	if err != nil {
		return ForceOccurrenceResult{}, err
	}
	if input.SourceKind == domain.SourceKindFile && found {
		if err := retireSourceTx(ctx, tx, source.ID, now); err != nil {
			return ForceOccurrenceResult{}, err
		}
		found = false
	}
	if !found {
		source, err = insertSourceOccurrenceTx(ctx, tx, domain.MediaSource{
			LibraryName: input.LibraryName, Kind: input.SourceKind, RelativePath: input.SourceRelativePath,
			Status: domain.MediaSourceActive, Fingerprint: input.SourceFingerprint,
			FirstSeenAt: now, LastSeenAt: now,
		})
		if err != nil {
			return ForceOccurrenceResult{}, err
		}
	} else {
		asset, assetFound, err := currentAssetTx(ctx, tx, source.ID, input.AssetRelativePath)
		if err != nil {
			return ForceOccurrenceResult{}, err
		}
		if assetFound {
			if err := retireAssetTx(ctx, tx, asset.ID, now); err != nil {
				return ForceOccurrenceResult{}, err
			}
		}
	}

	asset, err := insertAssetOccurrenceTx(ctx, tx, domain.MediaAsset{
		SourceID: source.ID, RelativePath: input.AssetRelativePath, Role: defaultAssetRole(input.AssetRole),
		Status: domain.MediaAssetActive, Fingerprint: input.AssetFingerprint,
		FirstSeenAt: now, LastSeenAt: now,
	})
	if err != nil {
		return ForceOccurrenceResult{}, err
	}
	job, _, err := enqueueJobTx(ctx, tx, EnqueueJobInput{
		SourceID: source.ID, AssetID: asset.ID, LibraryName: input.LibraryName, Priority: input.Priority, Now: now,
	})
	if err != nil {
		return ForceOccurrenceResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_sources SET status = ?, updated_at = ? WHERE id = ?`, string(domain.MediaSourceActive), encodeTime(now), int64(source.ID)); err != nil {
		return ForceOccurrenceResult{}, fmt.Errorf("activate forced source occurrence: %w", err)
	}
	if input.SourceKind == domain.SourceKindPackage {
		if err := updateSourceFingerprintTx(ctx, tx, source.ID, input.SourceFingerprint, now); err != nil {
			return ForceOccurrenceResult{}, err
		}
	}
	source, err = getMediaSourceTx(ctx, tx, source.ID)
	if err != nil {
		return ForceOccurrenceResult{}, err
	}
	// A force command must supersede observations that started before it.
	token, err := issueScanToken(ctx, tx, input.LibraryName)
	if err != nil {
		return ForceOccurrenceResult{}, fmt.Errorf("advance forced scan token: %w", err)
	}
	if err := recordSourceScanTx(ctx, tx, input.LibraryName, input.SourceRelativePath, token); err != nil {
		return ForceOccurrenceResult{}, fmt.Errorf("record forced scan token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ForceOccurrenceResult{}, fmt.Errorf("commit force occurrence: %w", err)
	}
	s.notifyWork()
	return ForceOccurrenceResult{Source: source, Asset: asset, Job: job}, nil
}

func currentSourceTx(ctx context.Context, tx *sql.Tx, libraryName domain.LibraryName, relativePath string) (domain.MediaSource, bool, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+mediaSourceColumns+` FROM media_sources WHERE library_name = ? AND relative_path = ? AND is_current = 1`, string(libraryName), relativePath)
	source, err := scanMediaSource(row)
	if errors.Is(err, ErrNotFound) {
		return domain.MediaSource{}, false, nil
	}
	if err != nil {
		return domain.MediaSource{}, false, fmt.Errorf("get current source occurrence: %w", err)
	}
	return source, true, nil
}

func sourcePathKnownTx(ctx context.Context, tx *sql.Tx, libraryName domain.LibraryName, relativePath string) (bool, error) {
	var known bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM media_sources WHERE library_name = ? AND relative_path = ?)`, string(libraryName), relativePath).Scan(&known); err != nil {
		return false, fmt.Errorf("check source path history: %w", err)
	}
	return known, nil
}

func currentAssetTx(ctx context.Context, tx *sql.Tx, sourceID domain.MediaSourceID, relativePath string) (domain.MediaAsset, bool, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+mediaAssetColumns+` FROM media_assets WHERE source_id = ? AND relative_path = ? AND is_current = 1`, int64(sourceID), relativePath)
	asset, err := scanMediaAsset(row)
	if errors.Is(err, ErrNotFound) {
		return domain.MediaAsset{}, false, nil
	}
	if err != nil {
		return domain.MediaAsset{}, false, fmt.Errorf("get current asset occurrence: %w", err)
	}
	return asset, true, nil
}

func insertSourceOccurrenceTx(ctx context.Context, tx *sql.Tx, source domain.MediaSource) (domain.MediaSource, error) {
	var generation int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation), 0) + 1 FROM media_sources WHERE library_name = ? AND relative_path = ?`, string(source.LibraryName), source.RelativePath).Scan(&generation); err != nil {
		return domain.MediaSource{}, fmt.Errorf("next source generation: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO media_sources (
	library_name, kind, relative_path, generation, is_current, status, size_bytes, mod_time,
	hash_algorithm, hash_value, first_seen_at, last_seen_at, updated_at
) VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
`, string(source.LibraryName), string(source.Kind), source.RelativePath, generation, string(source.Status),
		source.Fingerprint.SizeBytes, encodeTimePtr(source.Fingerprint.ModTime), source.Fingerprint.HashAlgorithm, source.Fingerprint.HashValue,
		encodeTime(source.FirstSeenAt), encodeTime(source.LastSeenAt), encodeTime(source.LastSeenAt))
	if err != nil {
		return domain.MediaSource{}, fmt.Errorf("insert source occurrence: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return domain.MediaSource{}, fmt.Errorf("source occurrence id: %w", err)
	}
	return getMediaSourceTx(ctx, tx, domain.MediaSourceID(id))
}

func insertAssetOccurrenceTx(ctx context.Context, tx *sql.Tx, asset domain.MediaAsset) (domain.MediaAsset, error) {
	var generation int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation), 0) + 1 FROM media_assets WHERE source_id = ? AND relative_path = ?`, int64(asset.SourceID), asset.RelativePath).Scan(&generation); err != nil {
		return domain.MediaAsset{}, fmt.Errorf("next asset generation: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
INSERT INTO media_assets (
	source_id, relative_path, generation, is_current, role, status, size_bytes, mod_time,
	hash_algorithm, hash_value, first_seen_at, last_seen_at, updated_at
) VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, int64(asset.SourceID), asset.RelativePath, generation, string(asset.Role), string(asset.Status),
		asset.Fingerprint.SizeBytes, encodeTimePtr(asset.Fingerprint.ModTime), asset.Fingerprint.HashAlgorithm, asset.Fingerprint.HashValue,
		encodeTime(asset.FirstSeenAt), encodeTime(asset.LastSeenAt), encodeTime(asset.LastSeenAt))
	if err != nil {
		return domain.MediaAsset{}, fmt.Errorf("insert asset occurrence: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return domain.MediaAsset{}, fmt.Errorf("asset occurrence id: %w", err)
	}
	return getMediaAssetTx(ctx, tx, domain.MediaAssetID(id))
}

func retireSourceTx(ctx context.Context, tx *sql.Tx, sourceID domain.MediaSourceID, now time.Time) error {
	if err := skipPendingOccurrenceJobsTx(ctx, tx, `source_id = ?`, []any{int64(sourceID)}, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_assets SET is_current = 0, status = ?, updated_at = ? WHERE source_id = ? AND is_current = 1`, string(domain.MediaAssetMissing), encodeTime(now), int64(sourceID)); err != nil {
		return fmt.Errorf("retire source assets: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_sources SET is_current = 0, status = ?, updated_at = ? WHERE id = ?`, string(domain.MediaSourceMissing), encodeTime(now), int64(sourceID)); err != nil {
		return fmt.Errorf("retire source occurrence: %w", err)
	}
	return nil
}

func retireAssetTx(ctx context.Context, tx *sql.Tx, assetID domain.MediaAssetID, now time.Time) error {
	if err := skipPendingOccurrenceJobsTx(ctx, tx, `asset_id = ?`, []any{int64(assetID)}, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_assets SET is_current = 0, status = ?, updated_at = ? WHERE id = ?`, string(domain.MediaAssetMissing), encodeTime(now), int64(assetID)); err != nil {
		return fmt.Errorf("retire asset occurrence: %w", err)
	}
	return nil
}

func skipPendingOccurrenceJobsTx(ctx context.Context, tx *sql.Tx, predicate string, predicateArgs []any, now time.Time) error {
	args := []any{string(domain.JobStateSkipped), "input occurrence is no longer present", encodeTime(now), encodeTime(now)}
	args = append(args, predicateArgs...)
	args = append(args, string(domain.JobStatePending), string(domain.JobStateRetrying))
	if _, err := tx.ExecContext(ctx, `
UPDATE jobs SET state = ?, lease_owner = '', lease_deadline = NULL, heartbeat_at = NULL,
	last_error = ?, updated_at = ?, completed_at = COALESCE(completed_at, ?)
WHERE `+predicate+` AND state IN (?, ?)
`, args...); err != nil {
		return fmt.Errorf("skip job for retired occurrence: %w", err)
	}
	return nil
}

func updateSourceFingerprintTx(ctx context.Context, tx *sql.Tx, sourceID domain.MediaSourceID, fingerprint domain.FileFingerprint, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE media_sources SET size_bytes = ?, mod_time = ?, hash_algorithm = ?, hash_value = ?, last_seen_at = ?, updated_at = ? WHERE id = ?
`, fingerprint.SizeBytes, encodeTimePtr(fingerprint.ModTime), fingerprint.HashAlgorithm, fingerprint.HashValue, encodeTime(now), encodeTime(now), int64(sourceID)); err != nil {
		return fmt.Errorf("update source occurrence fingerprint: %w", err)
	}
	return nil
}

func updateAssetFingerprintTx(ctx context.Context, tx *sql.Tx, assetID domain.MediaAssetID, fingerprint domain.FileFingerprint, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE media_assets SET size_bytes = ?, mod_time = ?, hash_algorithm = ?, hash_value = ?, last_seen_at = ?, updated_at = ? WHERE id = ?
`, fingerprint.SizeBytes, encodeTimePtr(fingerprint.ModTime), fingerprint.HashAlgorithm, fingerprint.HashValue, encodeTime(now), encodeTime(now), int64(assetID)); err != nil {
		return fmt.Errorf("update asset occurrence fingerprint: %w", err)
	}
	return nil
}

func refreshSourceLifecycleTx(ctx context.Context, tx *sql.Tx, sourceID domain.MediaSourceID, now time.Time) error {
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM media_assets WHERE source_id = ? AND is_current = 1 AND status = ?`, int64(sourceID), string(domain.MediaAssetActive)).Scan(&active); err != nil {
		return fmt.Errorf("count active source assets: %w", err)
	}
	status := domain.MediaSourceProcessed
	if active > 0 {
		status = domain.MediaSourceActive
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_sources SET status = ?, updated_at = ? WHERE id = ? AND is_current = 1`, string(status), encodeTime(now), int64(sourceID)); err != nil {
		return fmt.Errorf("refresh source lifecycle: %w", err)
	}
	return nil
}

func enqueueJobTx(ctx context.Context, tx *sql.Tx, input EnqueueJobInput) (domain.Job, bool, error) {
	now := defaultNow(input.Now)
	for range 100 {
		slug, err := newJobSlug()
		if err != nil {
			return domain.Job{}, false, err
		}
		result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO jobs (slug, source_id, asset_id, library_name, priority, state, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, slug, int64(input.SourceID), nullableAssetID(input.AssetID), string(input.LibraryName), input.Priority,
			string(domain.JobStatePending), encodeTime(now), encodeTime(now))
		if err != nil {
			return domain.Job{}, false, fmt.Errorf("enqueue occurrence job: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return domain.Job{}, false, fmt.Errorf("enqueue occurrence job rows affected: %w", err)
		}
		job, err := getJobForTargetTx(ctx, tx, input.SourceID, input.AssetID)
		if err == nil {
			return job, rows > 0, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return domain.Job{}, false, err
		}
	}
	return domain.Job{}, false, errors.New("could not allocate unique job slug")
}

func getJobForTargetTx(ctx context.Context, tx *sql.Tx, sourceID domain.MediaSourceID, assetID domain.MediaAssetID) (domain.Job, error) {
	row := tx.QueryRowContext(ctx, `
SELECT id, slug, source_id, asset_id, library_name, priority, state, lease_owner,
	lease_deadline, heartbeat_at, attempt_count, last_error, input_size_bytes,
	output_size_bytes, created_at, updated_at, completed_at
FROM jobs WHERE source_id = ? AND ifnull(asset_id, 0) = ? ORDER BY id DESC LIMIT 1
`, int64(sourceID), int64(assetID))
	return scanJob(row)
}

func getMediaSourceTx(ctx context.Context, tx *sql.Tx, id domain.MediaSourceID) (domain.MediaSource, error) {
	return scanMediaSource(tx.QueryRowContext(ctx, `SELECT `+mediaSourceColumns+` FROM media_sources WHERE id = ?`, int64(id)))
}

func getMediaAssetTx(ctx context.Context, tx *sql.Tx, id domain.MediaAssetID) (domain.MediaAsset, error) {
	return scanMediaAsset(tx.QueryRowContext(ctx, `SELECT `+mediaAssetColumns+` FROM media_assets WHERE id = ?`, int64(id)))
}

func activeWorkForOccurrencePathTx(ctx context.Context, tx *sql.Tx, libraryName domain.LibraryName, sourcePath, assetPath string) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
SELECT count(*)
FROM jobs j
JOIN media_sources s ON s.id = j.source_id
LEFT JOIN media_assets a ON a.id = j.asset_id
WHERE j.library_name = ? AND s.relative_path = ?
	AND (? = '' OR a.relative_path = ?)
	AND j.state IN (?, ?, ?, ?, ?, ?)
`, string(libraryName), sourcePath, assetPath, assetPath,
		string(domain.JobStatePending), string(domain.JobStateLeased), string(domain.JobStateRunning),
		string(domain.JobStateValidating), string(domain.JobStateReplacing), string(domain.JobStateRetrying)).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check active occurrence work: %w", err)
	}
	return count > 0, nil
}

func fingerprintsEqual(left, right domain.FileFingerprint) bool {
	return left.SizeBytes == right.SizeBytes && left.ModTime.Equal(right.ModTime) &&
		left.HashAlgorithm == right.HashAlgorithm && left.HashValue == right.HashValue
}

func defaultAssetRole(role domain.MediaAssetRole) domain.MediaAssetRole {
	if role == "" {
		return domain.MediaAssetRoleUnknown
	}
	return role
}
