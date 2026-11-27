package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
)

// ErrImmutable reports an update that tried to change a part of a library
// that is fixed once it exists: its kind.
var ErrImmutable = errors.New("store: the value cannot change after creation")

// Library is a stored library: its settings in the shape every loop of the
// daemon reads them in, plus when the row was written.
type Library struct {
	config.LibraryConfig
	CreatedAt time.Time
	UpdatedAt time.Time
}

// librarySettings is the JSON kept in libraries.settings_json: everything
// about a library no query filters, joins, or sorts on.
type librarySettings struct {
	ScanInterval     string                  `json:"scan_interval"`
	Priority         int                     `json:"priority"`
	ConcurrencyLimit int                     `json:"concurrency_limit"`
	Rules            []config.Rule           `json:"rules"`
	RuleFallback     string                  `json:"rule_fallback"`
	ReplacementMode  string                  `json:"replacement_mode"`
	Download         libraryDownloadSettings `json:"download"`
}

type libraryDownloadSettings struct {
	HandoffPath          string   `json:"handoff_path"`
	StableFor            string   `json:"stable_for"`
	PackageMode          string   `json:"package_mode"`
	HandoffMode          string   `json:"handoff_mode"`
	PreserveRelativePath bool     `json:"preserve_relative_path"`
	CleanupSourceMedia   bool     `json:"cleanup_source_media"`
	PruneEmptyDirs       bool     `json:"prune_empty_dirs"`
	IgnorableGlobs       []string `json:"ignorable_globs"`
}

const libraryColumns = `name, kind, path, flow_id, arr_name, paused, settings_json, created_at, updated_at`

func (s *SQLiteStore) ListLibraries(ctx context.Context) (libraries []Library, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+libraryColumns+` FROM libraries ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list libraries: %w", err)
	}
	defer closeRows(rows, &err, "close libraries")
	libraries = []Library{}
	for rows.Next() {
		item, err := scanLibrary(rows)
		if err != nil {
			return nil, err
		}
		libraries = append(libraries, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate libraries: %w", err)
	}
	return libraries, nil
}

func (s *SQLiteStore) GetLibrary(ctx context.Context, name string) (Library, error) {
	return getLibrary(ctx, s.db, name)
}

func getLibrary(ctx context.Context, q rowQuerier, name string) (Library, error) {
	return scanLibrary(q.QueryRowContext(ctx, `SELECT `+libraryColumns+` FROM libraries WHERE name = ?`, name))
}

func (s *SQLiteStore) CreateLibrary(ctx context.Context, library config.LibraryConfig, now time.Time) (Library, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Library{}, fmt.Errorf("begin library creation: %w", err)
	}
	defer rollback(tx)
	created, err := createLibraryTx(ctx, tx, library, defaultNow(now))
	if err != nil {
		return Library{}, err
	}
	if err := tx.Commit(); err != nil {
		return Library{}, fmt.Errorf("commit library creation: %w", err)
	}
	return created, nil
}

func createLibraryTx(ctx context.Context, tx *sql.Tx, library config.LibraryConfig, now time.Time) (Library, error) {
	name := strings.TrimSpace(library.Name)
	if name == "" {
		return Library{}, errors.New("library name is required")
	}
	if _, err := getLibrary(ctx, tx, name); err == nil {
		return Library{}, fmt.Errorf("%w: a library named %q already exists", ErrNameTaken, name)
	} else if !errors.Is(err, ErrNotFound) {
		return Library{}, err
	}
	if err := libraryReferencesExistTx(ctx, tx, library); err != nil {
		return Library{}, err
	}
	settings, err := encodeLibrarySettings(library)
	if err != nil {
		return Library{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO libraries (name, kind, path, flow_id, arr_name, paused, settings_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, name, library.Kind, library.Path, library.FlowID, nullableName(library.Arr), library.Paused, settings, encodeTime(now), encodeTime(now)); err != nil {
		return Library{}, fmt.Errorf("create library %q: %w", name, err)
	}
	return getLibrary(ctx, tx, name)
}

// UpdateLibrary replaces a library's settings. The name is its identity and
// the kind decides how its media is published, so neither changes.
func (s *SQLiteStore) UpdateLibrary(ctx context.Context, library config.LibraryConfig, now time.Time) (Library, error) {
	settings, err := encodeLibrarySettings(library)
	if err != nil {
		return Library{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Library{}, fmt.Errorf("begin library update: %w", err)
	}
	defer rollback(tx)
	current, err := getLibrary(ctx, tx, library.Name)
	if err != nil {
		return Library{}, err
	}
	if current.Kind != library.Kind {
		return Library{}, fmt.Errorf("%w: library %q is a %s library", ErrImmutable, current.Name, current.Kind)
	}
	if err := libraryReferencesExistTx(ctx, tx, library); err != nil {
		return Library{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE libraries
SET path = ?, flow_id = ?, arr_name = ?, paused = ?, settings_json = ?, updated_at = ?
WHERE name = ?
`, library.Path, library.FlowID, nullableName(library.Arr), library.Paused, settings, encodeTime(defaultNow(now)), library.Name); err != nil {
		return Library{}, fmt.Errorf("update library %q: %w", library.Name, err)
	}
	updated, err := getLibrary(ctx, tx, library.Name)
	if err != nil {
		return Library{}, err
	}
	if err := tx.Commit(); err != nil {
		return Library{}, fmt.Errorf("commit library update: %w", err)
	}
	// A library that was just resumed may have pending work waiting.
	s.notifyWork()
	return updated, nil
}

// DeleteLibrary removes a library together with every source, asset, job,
// attempt, and scan record it owns, in one transaction. The media files are
// not touched.
//
// It refuses with ErrActiveWork while the library has a job that is queued or
// running, or one that maintenance must not disturb (see ProtectedJobs): a job
// with an unresolved publish journal owns a destination file and possibly a
// backup on disk, and deleting its row would strand them.
func (s *SQLiteStore) DeleteLibrary(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin library deletion: %w", err)
	}
	defer rollback(tx)
	if _, err := getLibrary(ctx, tx, name); err != nil {
		return err
	}

	terminal := terminalJobStates()
	args := []any{name}
	for _, state := range terminal {
		args = append(args, string(state))
	}
	var active int64
	if err := tx.QueryRowContext(ctx, `
SELECT count(*) FROM jobs j WHERE j.library_name = ? AND j.state NOT IN (`+placeholders(len(terminal))+`)
`, args...).Scan(&active); err != nil {
		return fmt.Errorf("count active jobs of library %q: %w", name, err)
	}
	if active > 0 {
		return fmt.Errorf("%w: library %q has %d queued or running job(s); cancel them or let them finish, then delete it", ErrActiveWork, name, active)
	}
	var journaled int64
	if err := tx.QueryRowContext(ctx, `
SELECT count(*) FROM jobs j WHERE j.library_name = ? AND `+unresolvedPublishCondition,
		name, committedPublishStage()).Scan(&journaled); err != nil {
		return fmt.Errorf("count journaled jobs of library %q: %w", name, err)
	}
	if journaled > 0 {
		return fmt.Errorf("%w: library %q has %d job(s) with an unfinished publish; retry them so the publish completes, then delete it", ErrActiveWork, name, journaled)
	}

	// Jobs go first by name: a job row always carries its library, also when
	// the source it points at was recorded under another one by mistake.
	// Attempts, attempt events, publish journals, and assets follow by cascade.
	for _, statement := range []string{
		`DELETE FROM jobs WHERE library_name = ?`,
		`DELETE FROM media_sources WHERE library_name = ?`,
		`DELETE FROM source_scans WHERE library_name = ?`,
		`DELETE FROM library_scans WHERE library_name = ?`,
		`DELETE FROM libraries WHERE name = ?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, name); err != nil {
			return fmt.Errorf("delete library %q: %w", name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit library deletion: %w", err)
	}
	return nil
}

// LibraryOverview summarizes one library: what a scan last found, how its
// jobs stand, and what completed jobs saved.
type LibraryOverview struct {
	// Files and TotalBytes cover the media files a scan currently knows.
	Files      int64
	TotalBytes int64
	// Jobs counts the library's jobs by state.
	Jobs map[domain.JobState]int64
	// SavedBytes is input minus output size over completed jobs that
	// recorded both.
	SavedBytes int64
	LastScanAt *time.Time
}

// LibraryOverviews returns the overview of every library that has anything
// recorded, keyed by name. A library absent from the result has no files, no
// jobs, and no finished scan.
func (s *SQLiteStore) LibraryOverviews(ctx context.Context) (map[string]LibraryOverview, error) {
	overviews := make(map[string]LibraryOverview)
	entry := func(name string) LibraryOverview {
		overview := overviews[name]
		if overview.Jobs == nil {
			overview.Jobs = make(map[domain.JobState]int64)
		}
		return overview
	}
	err := s.eachRow(ctx, "library file totals", `
SELECT s.library_name, count(*), COALESCE(sum(a.size_bytes), 0)
FROM media_sources s
JOIN media_assets a ON a.source_id = s.id AND a.is_current = 1 AND a.status != 'missing'
WHERE s.is_current = 1 AND s.status != 'missing'
GROUP BY s.library_name`, nil, func(rows *sql.Rows) error {
		var name string
		var files, bytes int64
		if err := rows.Scan(&name, &files, &bytes); err != nil {
			return err
		}
		overview := entry(name)
		overview.Files, overview.TotalBytes = files, bytes
		overviews[name] = overview
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = s.eachRow(ctx, "library job totals", `
SELECT library_name, state, count(*),
	COALESCE(sum(CASE WHEN state = 'complete' AND input_size_bytes > 0 AND output_size_bytes > 0
		THEN input_size_bytes - output_size_bytes ELSE 0 END), 0)
FROM jobs
GROUP BY library_name, state`, nil, func(rows *sql.Rows) error {
		var name string
		var state domain.JobState
		var count, saved int64
		if err := rows.Scan(&name, &state, &count, &saved); err != nil {
			return err
		}
		overview := entry(name)
		overview.Jobs[state] = count
		overview.SavedBytes += saved
		overviews[name] = overview
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = s.eachRow(ctx, "library scan times", `
SELECT library_name, applied_at FROM library_scans WHERE applied_at IS NOT NULL`, nil, func(rows *sql.Rows) error {
		var name string
		var appliedAt sql.NullString
		if err := rows.Scan(&name, &appliedAt); err != nil {
			return err
		}
		overview := entry(name)
		overview.LastScanAt = parseNullTimePtr(appliedAt)
		overviews[name] = overview
		return nil
	})
	if err != nil {
		return nil, err
	}
	return overviews, nil
}

// eachRow runs a query and hands every row to scan.
func (s *SQLiteStore) eachRow(ctx context.Context, what, query string, args []any, scan func(*sql.Rows) error) (err error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query %s: %w", what, err)
	}
	defer closeRows(rows, &err, "close "+what)
	for rows.Next() {
		if err := scan(rows); err != nil {
			return fmt.Errorf("scan %s: %w", what, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate %s: %w", what, err)
	}
	return nil
}

// libraryNamesTx lists the libraries matching a condition, sorted.
func libraryNamesTx(ctx context.Context, tx *sql.Tx, condition string, args ...any) (names []string, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM libraries WHERE `+condition+` ORDER BY name`, args...)
	if err != nil {
		return nil, fmt.Errorf("list libraries: %w", err)
	}
	defer closeRows(rows, &err, "close library names")
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan library name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate library names: %w", err)
	}
	return names, nil
}

// libraryReferencesExistTx turns a dangling flow or Arr reference into a
// worded error instead of a foreign-key failure.
func libraryReferencesExistTx(ctx context.Context, tx *sql.Tx, library config.LibraryConfig) error {
	if _, err := getFlow(ctx, tx, library.FlowID); errors.Is(err, ErrNotFound) {
		return fmt.Errorf("library %q: flow %d: %w", library.Name, library.FlowID, ErrNotFound)
	} else if err != nil {
		return err
	}
	if library.Arr == "" {
		return nil
	}
	if _, err := getArr(ctx, tx, library.Arr); errors.Is(err, ErrNotFound) {
		return fmt.Errorf("library %q: arr %q: %w", library.Name, library.Arr, ErrNotFound)
	} else if err != nil {
		return err
	}
	return nil
}

func nullableName(name string) any {
	if name == "" {
		return nil
	}
	return name
}

func encodeLibrarySettings(library config.LibraryConfig) ([]byte, error) {
	settings := librarySettings{
		Priority:         library.Priority,
		ConcurrencyLimit: library.ConcurrencyLimit,
		Rules:            library.Rules,
		RuleFallback:     config.NormalizeRuleFallback(library.RuleFallback),
		ReplacementMode:  library.Media.ReplacementMode,
		Download: libraryDownloadSettings{
			HandoffPath:          library.Download.HandoffPath,
			PackageMode:          library.Download.PackageMode,
			HandoffMode:          library.Download.HandoffMode,
			PreserveRelativePath: library.Download.PreserveRelativePath,
			CleanupSourceMedia:   library.Download.CleanupSourceMedia,
			PruneEmptyDirs:       library.Download.PruneEmptyDirs,
			IgnorableGlobs:       library.Download.IgnorableGlobs,
		},
	}
	if library.ScanInterval.Duration > 0 {
		settings.ScanInterval = library.ScanInterval.String()
	}
	if library.IsDownload() {
		settings.Download.StableFor = library.Download.StableFor.String()
	}
	if settings.Rules == nil {
		settings.Rules = []config.Rule{}
	}
	if settings.Download.IgnorableGlobs == nil {
		settings.Download.IgnorableGlobs = []string{}
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("encode settings of library %q: %w", library.Name, err)
	}
	return data, nil
}

func scanLibrary(row scanner) (Library, error) {
	var item Library
	var arr sql.NullString
	var raw []byte
	var createdAt, updatedAt string
	err := row.Scan(&item.Name, &item.Kind, &item.Path, &item.FlowID, &arr, &item.Paused, &raw, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Library{}, ErrNotFound
	}
	if err != nil {
		return Library{}, fmt.Errorf("scan library: %w", err)
	}
	var settings librarySettings
	if err := json.Unmarshal(raw, &settings); err != nil {
		return Library{}, fmt.Errorf("decode settings of library %q: %w", item.Name, err)
	}
	scanInterval, err := parseStoredDuration(settings.ScanInterval)
	if err != nil {
		return Library{}, fmt.Errorf("library %q scan_interval: %w", item.Name, err)
	}
	stableFor, err := parseStoredDuration(settings.Download.StableFor)
	if err != nil {
		return Library{}, fmt.Errorf("library %q stable_for: %w", item.Name, err)
	}
	item.Arr = arr.String
	item.ScanInterval = config.Duration{Duration: scanInterval}
	item.Priority = settings.Priority
	item.ConcurrencyLimit = settings.ConcurrencyLimit
	item.Rules = settings.Rules
	if item.Rules == nil {
		item.Rules = []config.Rule{}
	}
	item.RuleFallback = config.NormalizeRuleFallback(settings.RuleFallback)
	item.Media.ReplacementMode = settings.ReplacementMode
	item.Download = config.DownloadLibraryConfig{
		HandoffPath:          settings.Download.HandoffPath,
		StableFor:            config.Duration{Duration: stableFor},
		PackageMode:          settings.Download.PackageMode,
		HandoffMode:          settings.Download.HandoffMode,
		PreserveRelativePath: settings.Download.PreserveRelativePath,
		CleanupSourceMedia:   settings.Download.CleanupSourceMedia,
		PruneEmptyDirs:       settings.Download.PruneEmptyDirs,
		IgnorableGlobs:       settings.Download.IgnorableGlobs,
	}
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}

func parseStoredDuration(text string) (time.Duration, error) {
	if text == "" {
		return 0, nil
	}
	return time.ParseDuration(text)
}
