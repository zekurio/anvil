package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/zekurio/anvil/pkg/domain"
)

func (s *SQLiteStore) UpsertMediaSource(ctx context.Context, source domain.MediaSource) (domain.MediaSource, error) {
	now := defaultNow(source.LastSeenAt)
	if source.FirstSeenAt.IsZero() {
		source.FirstSeenAt = now
	}
	if source.LastSeenAt.IsZero() {
		source.LastSeenAt = now
	}
	if source.Status == "" {
		source.Status = domain.MediaSourceActive
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO media_sources (
	library_name, kind, relative_path, status, size_bytes, mod_time,
	hash_algorithm, hash_value, first_seen_at, last_seen_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(library_name, relative_path) DO UPDATE SET
	kind = excluded.kind,
	status = excluded.status,
	size_bytes = excluded.size_bytes,
	mod_time = excluded.mod_time,
	hash_algorithm = excluded.hash_algorithm,
	hash_value = excluded.hash_value,
	last_seen_at = excluded.last_seen_at,
	updated_at = excluded.last_seen_at
`, string(source.LibraryName), string(source.Kind), source.RelativePath, string(source.Status),
		source.Fingerprint.SizeBytes, encodeTimePtr(source.Fingerprint.ModTime),
		source.Fingerprint.HashAlgorithm, source.Fingerprint.HashValue,
		encodeTime(source.FirstSeenAt), encodeTime(source.LastSeenAt), encodeTime(source.LastSeenAt))
	if err != nil {
		return domain.MediaSource{}, fmt.Errorf("upsert media source: %w", err)
	}

	return s.GetMediaSourceByPath(ctx, source.LibraryName, source.RelativePath)
}

func (s *SQLiteStore) GetMediaSourceByPath(ctx context.Context, libraryName domain.LibraryName, relativePath string) (domain.MediaSource, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, library_name, kind, relative_path, status, size_bytes, mod_time,
	hash_algorithm, hash_value, first_seen_at, last_seen_at
FROM media_sources
WHERE library_name = ? AND relative_path = ?
`, string(libraryName), relativePath)
	return scanMediaSource(row)
}

func (s *SQLiteStore) FindMediaSourceByPath(ctx context.Context, libraryName domain.LibraryName, relativePath string) (domain.MediaSource, bool, error) {
	source, err := s.GetMediaSourceByPath(ctx, libraryName, relativePath)
	if errors.Is(err, ErrNotFound) {
		return domain.MediaSource{}, false, nil
	}
	if err != nil {
		return domain.MediaSource{}, false, err
	}
	return source, true, nil
}

func (s *SQLiteStore) GetMediaSource(ctx context.Context, id domain.MediaSourceID) (domain.MediaSource, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, library_name, kind, relative_path, status, size_bytes, mod_time,
	hash_algorithm, hash_value, first_seen_at, last_seen_at
FROM media_sources
WHERE id = ?
`, int64(id))
	return scanMediaSource(row)
}

func (s *SQLiteStore) UpsertMediaAsset(ctx context.Context, asset domain.MediaAsset) (domain.MediaAsset, error) {
	now := defaultNow(asset.LastSeenAt)
	if asset.FirstSeenAt.IsZero() {
		asset.FirstSeenAt = now
	}
	if asset.LastSeenAt.IsZero() {
		asset.LastSeenAt = now
	}
	if asset.Status == "" {
		asset.Status = domain.MediaAssetActive
	}
	if asset.Role == "" {
		asset.Role = domain.MediaAssetRoleUnknown
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO media_assets (
	source_id, relative_path, role, status, size_bytes, mod_time,
	hash_algorithm, hash_value, first_seen_at, last_seen_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(source_id, relative_path) DO UPDATE SET
	role = excluded.role,
	status = excluded.status,
	size_bytes = excluded.size_bytes,
	mod_time = excluded.mod_time,
	hash_algorithm = excluded.hash_algorithm,
	hash_value = excluded.hash_value,
	last_seen_at = excluded.last_seen_at,
	updated_at = excluded.last_seen_at
`, int64(asset.SourceID), asset.RelativePath, string(asset.Role), string(asset.Status),
		asset.Fingerprint.SizeBytes, encodeTimePtr(asset.Fingerprint.ModTime),
		asset.Fingerprint.HashAlgorithm, asset.Fingerprint.HashValue,
		encodeTime(asset.FirstSeenAt), encodeTime(asset.LastSeenAt), encodeTime(asset.LastSeenAt))
	if err != nil {
		return domain.MediaAsset{}, fmt.Errorf("upsert media asset: %w", err)
	}

	return s.GetMediaAssetByPath(ctx, asset.SourceID, asset.RelativePath)
}

func (s *SQLiteStore) GetMediaAssetByPath(ctx context.Context, sourceID domain.MediaSourceID, relativePath string) (domain.MediaAsset, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, source_id, relative_path, role, status, size_bytes, mod_time,
	hash_algorithm, hash_value, first_seen_at, last_seen_at
FROM media_assets
WHERE source_id = ? AND relative_path = ?
`, int64(sourceID), relativePath)
	return scanMediaAsset(row)
}

func (s *SQLiteStore) FindMediaAssetByPath(ctx context.Context, sourceID domain.MediaSourceID, relativePath string) (domain.MediaAsset, bool, error) {
	asset, err := s.GetMediaAssetByPath(ctx, sourceID, relativePath)
	if errors.Is(err, ErrNotFound) {
		return domain.MediaAsset{}, false, nil
	}
	if err != nil {
		return domain.MediaAsset{}, false, err
	}
	return asset, true, nil
}

func (s *SQLiteStore) GetMediaAsset(ctx context.Context, id domain.MediaAssetID) (domain.MediaAsset, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, source_id, relative_path, role, status, size_bytes, mod_time,
	hash_algorithm, hash_value, first_seen_at, last_seen_at
FROM media_assets
WHERE id = ?
`, int64(id))
	return scanMediaAsset(row)
}
