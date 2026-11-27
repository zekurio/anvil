package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zekurio/anvil/pkg/config"
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
	// metaDaemonSettings holds the [daemon] values saved in the web UI, as the
	// JSON of config.DaemonOverrides. It is the one meta key that is a
	// setting rather than a record of something done once.
	metaDaemonSettings = "daemon_settings"
)

// DaemonSettings returns the [daemon] values saved in the web UI. Nothing
// saved is the zero value. A key this version does not know, saved by a newer
// one, is ignored, so going back a version does not keep the daemon from
// starting.
func (s *SQLiteStore) DaemonSettings(ctx context.Context) (config.DaemonOverrides, error) {
	value, found, err := getMeta(ctx, s.db, metaDaemonSettings)
	if err != nil || !found {
		return config.DaemonOverrides{}, err
	}
	var overrides config.DaemonOverrides
	if err := json.Unmarshal([]byte(value), &overrides); err != nil {
		return config.DaemonOverrides{}, fmt.Errorf("decode daemon settings: %w", err)
	}
	return overrides, nil
}

// SetDaemonSettings replaces the [daemon] values saved in the web UI. Saving
// none removes the record, so every key is back to anvil.toml or its default.
// The caller validates: the store keeps what it is given.
func (s *SQLiteStore) SetDaemonSettings(ctx context.Context, overrides config.DaemonOverrides) error {
	if len(overrides.Keys()) == 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, metaDaemonSettings); err != nil {
			return fmt.Errorf("clear daemon settings: %w", err)
		}
		return nil
	}
	data, err := json.Marshal(overrides)
	if err != nil {
		return fmt.Errorf("encode daemon settings: %w", err)
	}
	return setMeta(ctx, s.db, metaDaemonSettings, string(data))
}

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
