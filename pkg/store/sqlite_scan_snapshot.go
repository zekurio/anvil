package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

type scanSnapshot struct {
	sources map[string]domain.MediaSource
	assets  map[domain.MediaSourceID]map[string]domain.MediaAsset
	jobs    map[domain.MediaAssetID]bool
	tokens  map[string]int64
	scope   map[string]bool
}

// Read each current occurrence once. Scope stays in one JSON parameter, so
// library size cannot exceed SQLite's bound parameter limit.
func readScanSnapshot(ctx context.Context, tx *sql.Tx, input ApplyScanInput) (scanSnapshot, error) {
	result := scanSnapshot{
		sources: make(map[string]domain.MediaSource), assets: make(map[domain.MediaSourceID]map[string]domain.MediaAsset),
		jobs: make(map[domain.MediaAssetID]bool), tokens: make(map[string]int64), scope: make(map[string]bool),
	}
	for _, path := range input.SourcePaths {
		result.scope[path] = true
	}
	paths, err := json.Marshal(input.SourcePaths)
	if err != nil {
		return result, fmt.Errorf("encode scan scope: %w", err)
	}
	predicate := `library_name = ? AND (? OR relative_path IN (SELECT value FROM json_each(?)))`
	args := []any{string(input.LibraryName), input.SourcePaths == nil, string(paths)}
	read := func(query string, scan func(*sql.Rows) error) (err error) {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer closeRows(rows, &err, "close scan snapshot")
		for rows.Next() {
			if err := scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	if err := read(`SELECT `+mediaSourceColumns+` FROM media_sources WHERE is_current = 1 AND `+predicate, func(rows *sql.Rows) error {
		source, err := scanMediaSource(rows)
		if err == nil {
			result.sources[source.RelativePath] = source
		}
		return err
	}); err != nil {
		return result, fmt.Errorf("read scan sources: %w", err)
	}
	sourceIDs := `SELECT id FROM media_sources WHERE is_current = 1 AND ` + predicate
	if err := read(`SELECT `+mediaAssetColumns+` FROM media_assets WHERE is_current = 1 AND source_id IN (`+sourceIDs+`)`, func(rows *sql.Rows) error {
		asset, err := scanMediaAsset(rows)
		if err == nil {
			if result.assets[asset.SourceID] == nil {
				result.assets[asset.SourceID] = make(map[string]domain.MediaAsset)
			}
			result.assets[asset.SourceID][asset.RelativePath] = asset
		}
		return err
	}); err != nil {
		return result, fmt.Errorf("read scan assets: %w", err)
	}
	if input.RequeueExisting {
		if err := read(`SELECT DISTINCT asset_id FROM jobs WHERE asset_id IS NOT NULL AND source_id IN (`+sourceIDs+`)`, func(rows *sql.Rows) error {
			var id domain.MediaAssetID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			result.jobs[id] = true
			return nil
		}); err != nil {
			return result, fmt.Errorf("read scan jobs: %w", err)
		}
	}
	if err := read(`SELECT relative_path, applied_token FROM source_scans WHERE `+predicate, func(rows *sql.Rows) error {
		var path string
		var token int64
		if err := rows.Scan(&path, &token); err != nil {
			return err
		}
		result.tokens[path] = token
		return nil
	}); err != nil {
		return result, fmt.Errorf("read source scan tokens: %w", err)
	}
	return result, nil
}

func touchScanRows(ctx context.Context, tx *sql.Tx, table string, ids []int64, now time.Time) error {
	for len(ids) > 0 {
		n := min(len(ids), 500)
		args := []any{encodeTime(now), encodeTime(now)}
		for _, id := range ids[:n] {
			args = append(args, id)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET last_seen_at = ?, updated_at = ? WHERE id IN (`+placeholders(n)+`)`, args...); err != nil {
			return fmt.Errorf("refresh scan timestamps: %w", err)
		}
		ids = ids[n:]
	}
	return nil
}
