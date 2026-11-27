package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Keys of the meta table.
const (
	// metaDefaultFlowSeeded records that the starter flow was created once,
	// so deleting it does not bring it back at the next start.
	metaDefaultFlowSeeded = "default_flow_seeded_at"
	// metaLegacyConfigImported records that the libraries, profiles, and Arr
	// connections of anvil.toml were looked at once. It is set even when
	// there was nothing to import, so a TOML file edited later is never
	// imported silently.
	metaLegacyConfigImported = "legacy_config_imported_at"
)

// queryer is satisfied by both *sql.DB and *sql.Tx.
type queryer interface {
	rowQuerier
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func getMeta(ctx context.Context, q rowQuerier, key string) (string, bool, error) {
	var value string
	err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read meta %q: %w", key, err)
	}
	return value, true, nil
}

func setMeta(ctx context.Context, q queryer, key, value string) error {
	if _, err := q.ExecContext(ctx, `
INSERT INTO meta (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value
`, key, value); err != nil {
		return fmt.Errorf("write meta %q: %w", key, err)
	}
	return nil
}
