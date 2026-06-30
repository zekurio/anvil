package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func encodeTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func encodeTimePtr(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return encodeTime(t)
}

func nullableTimePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return encodeTime(*t)
}

func nullableAssetID(id domain.MediaAssetID) any {
	if id == 0 {
		return nil
	}
	return int64(id)
}

func emptyBytesIfNil(value []byte) []byte {
	if value == nil {
		return []byte{}
	}
	return value
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	parts := make([]string, count)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func parseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseNullTime(value sql.NullString) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return parseTime(value.String)
}

func parseNullTimePtr(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	t := parseTime(value.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

func defaultNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback() //nolint:errcheck // rollback is best-effort; callers return the primary error
}

func closeRows(rows *sql.Rows, err *error, operation string) {
	if closeErr := rows.Close(); closeErr != nil && *err == nil {
		*err = fmt.Errorf("%s: %w", operation, closeErr)
	}
}

func ensureParentDir(path string) error {
	if path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create store directory %q: %w", dir, err)
	}
	return nil
}

func readOnlyDSN(path string) string {
	if strings.HasPrefix(path, "file:") {
		parsed, err := url.Parse(path)
		if err != nil {
			return path
		}
		query := parsed.Query()
		query.Set("mode", "ro")
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	uri := url.URL{
		Scheme: "file",
		Path:   path,
	}
	query := uri.Query()
	query.Set("mode", "ro")
	uri.RawQuery = query.Encode()
	return uri.String()
}
