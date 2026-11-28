package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/config"
)

// Arr is a stored Radarr or Sonarr connection.
type Arr struct {
	config.ArrConfig
	CreatedAt time.Time
	UpdatedAt time.Time
}

const arrColumns = `name, type, base_url, api_key, api_key_file, created_at, updated_at`

func (s *SQLiteStore) ListArrs(ctx context.Context) (arrs []Arr, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+arrColumns+` FROM arrs ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list arrs: %w", err)
	}
	defer closeRows(rows, &err, "close arrs")
	arrs = []Arr{}
	for rows.Next() {
		item, err := scanArr(rows)
		if err != nil {
			return nil, err
		}
		arrs = append(arrs, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate arrs: %w", err)
	}
	return arrs, nil
}

func (s *SQLiteStore) GetArr(ctx context.Context, name string) (Arr, error) {
	return getArr(ctx, s.db, name)
}

func getArr(ctx context.Context, q rowQuerier, name string) (Arr, error) {
	return scanArr(q.QueryRowContext(ctx, `SELECT `+arrColumns+` FROM arrs WHERE name = ?`, name))
}

func (s *SQLiteStore) CreateArr(ctx context.Context, arr config.ArrConfig, now time.Time) (Arr, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Arr{}, fmt.Errorf("begin arr creation: %w", err)
	}
	defer rollback(tx)
	created, err := createArrTx(ctx, tx, arr, defaultNow(now))
	if err != nil {
		return Arr{}, err
	}
	if err := tx.Commit(); err != nil {
		return Arr{}, fmt.Errorf("commit arr creation: %w", err)
	}
	return created, nil
}

func createArrTx(ctx context.Context, tx *sql.Tx, arr config.ArrConfig, now time.Time) (Arr, error) {
	name := strings.TrimSpace(arr.Name)
	if name == "" {
		return Arr{}, errors.New("arr name is required")
	}
	if _, err := getArr(ctx, tx, name); err == nil {
		return Arr{}, fmt.Errorf("%w: an Arr connection named %q already exists", ErrNameTaken, name)
	} else if !errors.Is(err, ErrNotFound) {
		return Arr{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO arrs (name, type, base_url, api_key, api_key_file, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, name, arr.Type, arr.BaseURL, arr.APIKey, arr.APIKeyFile, encodeTime(now), encodeTime(now)); err != nil {
		return Arr{}, fmt.Errorf("create arr %q: %w", name, err)
	}
	return getArr(ctx, tx, name)
}

// UpdateArr replaces a connection. The name is its identity and stays.
func (s *SQLiteStore) UpdateArr(ctx context.Context, arr config.ArrConfig, now time.Time) (Arr, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE arrs SET type = ?, base_url = ?, api_key = ?, api_key_file = ?, updated_at = ? WHERE name = ?
`, arr.Type, arr.BaseURL, arr.APIKey, arr.APIKeyFile, encodeTime(defaultNow(now)), arr.Name)
	if err != nil {
		return Arr{}, fmt.Errorf("update arr %q: %w", arr.Name, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Arr{}, fmt.Errorf("update arr rows affected: %w", err)
	}
	if rows == 0 {
		return Arr{}, ErrNotFound
	}
	return s.GetArr(ctx, arr.Name)
}

// DeleteArr removes a connection no library uses. One in use is refused with
// an *InUseError naming the libraries.
func (s *SQLiteStore) DeleteArr(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin arr deletion: %w", err)
	}
	defer rollback(tx)
	if _, err := getArr(ctx, tx, name); err != nil {
		return err
	}
	libraries, err := libraryNamesTx(ctx, tx, `arr_name = ?`, name)
	if err != nil {
		return err
	}
	if len(libraries) > 0 {
		return &InUseError{Libraries: libraries}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM arrs WHERE name = ?`, name); err != nil {
		return fmt.Errorf("delete arr %q: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit arr deletion: %w", err)
	}
	return nil
}

// ArrLibraries maps every Arr connection in use to the names of the libraries
// using it, sorted.
func (s *SQLiteStore) ArrLibraries(ctx context.Context) (usage map[string][]string, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT arr_name, name FROM libraries WHERE arr_name IS NOT NULL ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list arr usage: %w", err)
	}
	defer closeRows(rows, &err, "close arr usage")
	usage = make(map[string][]string)
	for rows.Next() {
		var arr, library string
		if err := rows.Scan(&arr, &library); err != nil {
			return nil, fmt.Errorf("scan arr usage: %w", err)
		}
		usage[arr] = append(usage[arr], library)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate arr usage: %w", err)
	}
	return usage, nil
}

func scanArr(row scanner) (Arr, error) {
	var item Arr
	var createdAt, updatedAt string
	err := row.Scan(&item.Name, &item.Type, &item.BaseURL, &item.APIKey, &item.APIKeyFile, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Arr{}, ErrNotFound
	}
	if err != nil {
		return Arr{}, fmt.Errorf("scan arr: %w", err)
	}
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}
