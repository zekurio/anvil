package store

import (
	"context"
	"errors"

	"github.com/zekurio/anvil/pkg/domain"
)

const mediaSourceColumns = `
id, library_name, kind, relative_path, generation, is_current, status, size_bytes, mod_time,
hash_algorithm, hash_value, first_seen_at, last_seen_at
`

const mediaAssetColumns = `
id, source_id, relative_path, generation, is_current, role, status, size_bytes, mod_time,
hash_algorithm, hash_value, first_seen_at, last_seen_at
`

func (s *SQLiteStore) GetMediaSourceByPath(ctx context.Context, libraryName domain.LibraryName, relativePath string) (domain.MediaSource, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT `+mediaSourceColumns+`
FROM media_sources
WHERE library_name = ? AND relative_path = ?
ORDER BY generation DESC
LIMIT 1
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
SELECT `+mediaSourceColumns+`
FROM media_sources
WHERE id = ?
`, int64(id))
	return scanMediaSource(row)
}

func (s *SQLiteStore) GetMediaAssetByPath(ctx context.Context, sourceID domain.MediaSourceID, relativePath string) (domain.MediaAsset, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT `+mediaAssetColumns+`
FROM media_assets
WHERE source_id = ? AND relative_path = ?
ORDER BY generation DESC
LIMIT 1
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
SELECT `+mediaAssetColumns+`
FROM media_assets
WHERE id = ?
`, int64(id))
	return scanMediaAsset(row)
}
