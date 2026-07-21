package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

type scanSourceGroup struct {
	kind        domain.SourceKind
	path        string
	fingerprint domain.FileFingerprint
	entries     map[string]ScanEntry
	persist     bool
}

func (s *SQLiteStore) BeginLibraryScan(ctx context.Context, libraryName domain.LibraryName) (ScanToken, error) {
	if strings.TrimSpace(string(libraryName)) == "" {
		return ScanToken{}, errors.New("scan library name is required")
	}
	var token int64
	err := s.db.QueryRowContext(ctx, `
INSERT INTO library_scans (library_name, next_token)
VALUES (?, 1)
ON CONFLICT(library_name) DO UPDATE SET next_token = next_token + 1
RETURNING next_token
`, string(libraryName)).Scan(&token)
	if err != nil {
		return ScanToken{}, fmt.Errorf("begin library scan: %w", err)
	}
	return ScanToken{LibraryName: libraryName, Sequence: token}, nil
}

func (s *SQLiteStore) ApplyLibraryScan(ctx context.Context, token ScanToken, input ApplyScanInput) (ApplyScanResult, error) {
	if token.Sequence <= 0 {
		return ApplyScanResult{}, errors.New("scan token is required")
	}
	if strings.TrimSpace(string(input.LibraryName)) == "" {
		return ApplyScanResult{}, errors.New("scan library name is required")
	}
	if token.LibraryName != input.LibraryName {
		return ApplyScanResult{}, fmt.Errorf("scan token belongs to library %q, not %q", token.LibraryName, input.LibraryName)
	}
	now := defaultNow(input.CompletedAt)
	groups, err := groupScanEntries(input.Entries)
	if err != nil {
		return ApplyScanResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyScanResult{}, fmt.Errorf("begin scan application: %w", err)
	}
	defer rollback(tx)

	var issued int64
	var applied int64
	err = tx.QueryRowContext(ctx, `
SELECT next_token, applied_token FROM library_scans WHERE library_name = ?
`, string(input.LibraryName)).Scan(&issued, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return ApplyScanResult{}, fmt.Errorf("apply library scan: %w", ErrNotFound)
	}
	if err != nil {
		return ApplyScanResult{}, fmt.Errorf("read library scan token: %w", err)
	}
	if token.Sequence > issued {
		return ApplyScanResult{}, fmt.Errorf("scan token %d was not issued for library %q", token.Sequence, input.LibraryName)
	}
	if token.Sequence <= applied {
		if err := tx.Commit(); err != nil {
			return ApplyScanResult{}, fmt.Errorf("commit superseded scan: %w", err)
		}
		return ApplyScanResult{}, nil
	}

	result := ApplyScanResult{Applied: true}
	seenSources := make(map[string]struct{}, len(groups))
	paths := make([]string, 0, len(groups))
	for sourcePath := range groups {
		paths = append(paths, sourcePath)
	}
	sort.Strings(paths)

	for _, sourcePath := range paths {
		group := groups[sourcePath]
		seenSources[sourcePath] = struct{}{}
		source, found, err := currentSourceTx(ctx, tx, input.LibraryName, sourcePath)
		if err != nil {
			return ApplyScanResult{}, err
		}
		if !group.persist {
			continue
		}
		result.Sources++

		if found && (source.Kind != group.kind || source.Kind == domain.SourceKindFile && !fingerprintsEqual(source.Fingerprint, group.fingerprint)) {
			if err := retireSourceTx(ctx, tx, source.ID, now); err != nil {
				return ApplyScanResult{}, err
			}
			found = false
		}
		if !found {
			source, err = insertSourceOccurrenceTx(ctx, tx, domain.MediaSource{
				LibraryName:  input.LibraryName,
				Kind:         group.kind,
				RelativePath: sourcePath,
				Status:       domain.MediaSourceActive,
				Fingerprint:  group.fingerprint,
				FirstSeenAt:  now,
				LastSeenAt:   now,
			})
			if err != nil {
				return ApplyScanResult{}, err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `
UPDATE media_sources
SET kind = ?, size_bytes = ?, mod_time = ?, hash_algorithm = ?, hash_value = ?,
	last_seen_at = ?, updated_at = ?
WHERE id = ?
`, string(group.kind), group.fingerprint.SizeBytes, encodeTimePtr(group.fingerprint.ModTime),
				group.fingerprint.HashAlgorithm, group.fingerprint.HashValue, encodeTime(now), encodeTime(now), int64(source.ID)); err != nil {
				return ApplyScanResult{}, fmt.Errorf("refresh source occurrence: %w", err)
			}
		}

		seenAssets := make(map[string]struct{}, len(group.entries))
		assetPaths := make([]string, 0, len(group.entries))
		for assetPath := range group.entries {
			assetPaths = append(assetPaths, assetPath)
		}
		sort.Strings(assetPaths)
		for _, assetPath := range assetPaths {
			entry := group.entries[assetPath]
			seenAssets[assetPath] = struct{}{}
			if !entry.Persist {
				continue
			}
			result.Assets++
			asset, assetFound, err := currentAssetTx(ctx, tx, source.ID, assetPath)
			if err != nil {
				return ApplyScanResult{}, err
			}
			if assetFound && !fingerprintsEqual(asset.Fingerprint, entry.AssetFingerprint) {
				if err := retireAssetTx(ctx, tx, asset.ID, now); err != nil {
					return ApplyScanResult{}, err
				}
				assetFound = false
			}
			if !assetFound {
				asset, err = insertAssetOccurrenceTx(ctx, tx, domain.MediaAsset{
					SourceID:     source.ID,
					RelativePath: assetPath,
					Role:         defaultAssetRole(entry.AssetRole),
					Status:       domain.MediaAssetActive,
					Fingerprint:  entry.AssetFingerprint,
					FirstSeenAt:  now,
					LastSeenAt:   now,
				})
				if err != nil {
					return ApplyScanResult{}, err
				}
			} else {
				if _, err := tx.ExecContext(ctx, `
UPDATE media_assets
SET role = ?, last_seen_at = ?, updated_at = ?
WHERE id = ?
`, string(defaultAssetRole(entry.AssetRole)), encodeTime(now), encodeTime(now), int64(asset.ID)); err != nil {
					return ApplyScanResult{}, fmt.Errorf("refresh asset occurrence: %w", err)
				}
			}

			if entry.Enqueue && asset.Status == domain.MediaAssetActive {
				_, inserted, err := enqueueJobTx(ctx, tx, EnqueueJobInput{
					SourceID: source.ID, AssetID: asset.ID, LibraryName: input.LibraryName,
					Priority: input.Priority, Now: now,
				})
				if err != nil {
					return ApplyScanResult{}, err
				}
				if inserted {
					result.EnqueuedJobs++
				} else {
					result.ExistingJobs++
				}
			}
		}
		if err := markUnseenAssetsMissingTx(ctx, tx, source.ID, seenAssets, now); err != nil {
			return ApplyScanResult{}, err
		}
		if err := refreshSourceLifecycleTx(ctx, tx, source.ID, now); err != nil {
			return ApplyScanResult{}, err
		}
	}

	if err := markUnseenSourcesMissingTx(ctx, tx, input.LibraryName, seenSources, now); err != nil {
		return ApplyScanResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE library_scans SET applied_token = ?, applied_at = ? WHERE library_name = ?
`, token.Sequence, encodeTime(now), string(input.LibraryName)); err != nil {
		return ApplyScanResult{}, fmt.Errorf("record applied library scan: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ApplyScanResult{}, fmt.Errorf("commit library scan: %w", err)
	}
	return result, nil
}

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
		return ForceOccurrenceResult{}, fmt.Errorf("force occurrence refused for %q: active work exists", input.SourceRelativePath)
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
	if err := tx.Commit(); err != nil {
		return ForceOccurrenceResult{}, fmt.Errorf("commit force occurrence: %w", err)
	}
	return ForceOccurrenceResult{Source: source, Asset: asset, Job: job}, nil
}

func groupScanEntries(entries []ScanEntry) (map[string]*scanSourceGroup, error) {
	groups := make(map[string]*scanSourceGroup)
	for _, entry := range entries {
		if strings.TrimSpace(entry.SourceRelativePath) == "" || strings.TrimSpace(entry.AssetRelativePath) == "" {
			return nil, errors.New("scan entry requires source and asset paths")
		}
		if entry.SourceKind != domain.SourceKindFile && entry.SourceKind != domain.SourceKindPackage {
			return nil, fmt.Errorf("scan entry has invalid source kind %q", entry.SourceKind)
		}
		group, ok := groups[entry.SourceRelativePath]
		if !ok {
			group = &scanSourceGroup{kind: entry.SourceKind, path: entry.SourceRelativePath, entries: make(map[string]ScanEntry)}
			groups[entry.SourceRelativePath] = group
		}
		if group.kind != entry.SourceKind {
			return nil, fmt.Errorf("scan source %q has conflicting kinds", entry.SourceRelativePath)
		}
		if _, duplicate := group.entries[entry.AssetRelativePath]; duplicate {
			return nil, fmt.Errorf("scan source %q has duplicate asset %q", entry.SourceRelativePath, entry.AssetRelativePath)
		}
		group.entries[entry.AssetRelativePath] = entry
		if entry.Persist {
			group.persist = true
			group.fingerprint = entry.SourceFingerprint
		}
	}
	return groups, nil
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

func markUnseenSourcesMissingTx(ctx context.Context, tx *sql.Tx, libraryName domain.LibraryName, seen map[string]struct{}, now time.Time) error {
	query := `SELECT id FROM media_sources WHERE library_name = ? AND is_current = 1`
	args := []any{string(libraryName)}
	if len(seen) > 0 {
		paths := sortedKeys(seen)
		query += ` AND relative_path NOT IN (` + placeholders(len(paths)) + `)`
		for _, path := range paths {
			args = append(args, path)
		}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("list missing source occurrences: %w", err)
	}
	var ids []domain.MediaSourceID
	for rows.Next() {
		var id domain.MediaSourceID
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan missing source occurrence: %w", errors.Join(err, rows.Close()))
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close missing source occurrences: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate missing source occurrences: %w", err)
	}
	for _, id := range ids {
		if err := retireSourceTx(ctx, tx, id, now); err != nil {
			return err
		}
	}
	return nil
}

func markUnseenAssetsMissingTx(ctx context.Context, tx *sql.Tx, sourceID domain.MediaSourceID, seen map[string]struct{}, now time.Time) error {
	query := `SELECT id FROM media_assets WHERE source_id = ? AND is_current = 1`
	args := []any{int64(sourceID)}
	if len(seen) > 0 {
		paths := sortedKeys(seen)
		query += ` AND relative_path NOT IN (` + placeholders(len(paths)) + `)`
		for _, path := range paths {
			args = append(args, path)
		}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("list missing asset occurrences: %w", err)
	}
	var ids []domain.MediaAssetID
	for rows.Next() {
		var id domain.MediaAssetID
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan missing asset occurrence: %w", errors.Join(err, rows.Close()))
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close missing asset occurrences: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate missing asset occurrences: %w", err)
	}
	for _, id := range ids {
		if err := retireAssetTx(ctx, tx, id, now); err != nil {
			return err
		}
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

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
