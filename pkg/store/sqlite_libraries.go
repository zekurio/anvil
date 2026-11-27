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

func (s *SQLiteStore) ListLibraries(ctx context.Context) ([]Library, error) {
	return listLibraries(ctx, s.db)
}

// listLibraries reads every library through a *sql.DB or a *sql.Tx.
func listLibraries(ctx context.Context, q interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}) (libraries []Library, err error) {
	rows, err := q.QueryContext(ctx, `SELECT `+libraryColumns+` FROM libraries ORDER BY name`)
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
	// A job carries the priority its library had when the scan enqueued it,
	// and the scheduler leases by that column. Without this a new priority
	// would only reach files found from now on, and the queue the operator
	// wanted to reorder would stay as it was. Jobs that are done follow too,
	// so that one retried later runs at the library's priority. updated_at is
	// left alone: nothing happened to the job.
	if current.Priority != library.Priority {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET priority = ? WHERE library_name = ? AND priority != ?`, library.Priority, library.Name, library.Priority); err != nil {
			return Library{}, fmt.Errorf("update job priority of library %q: %w", library.Name, err)
		}
	}
	updated, err := getLibrary(ctx, tx, library.Name)
	if err != nil {
		return Library{}, err
	}
	if err := tx.Commit(); err != nil {
		return Library{}, fmt.Errorf("commit library update: %w", err)
	}
	return updated, nil
}

// DeletedJob names a job that went away with its library. The daemon uses it
// to remove what the job left under its temp dir.
type DeletedJob struct {
	ID   domain.JobID
	Slug string
}

// LibraryBusyError is why DeleteLibrary refused, in counts. It wraps
// ErrActiveWork.
type LibraryBusyError struct {
	Library string
	// Running counts jobs a worker holds: leased, running, validating, or
	// replacing.
	Running int64
	// Publishing counts jobs whose publish was interrupted part-way and has
	// not been resumed yet, whatever their state: the journal is at prepared,
	// published, or source_cleaned. A publish that stopped in conflict is not
	// counted; see DeleteLibrary.
	Publishing int64
}

func (e *LibraryBusyError) Error() string {
	const resume = "an interrupted publish finishes the next time its job runs (retry a failed job, resume a paused library)"
	switch {
	case e.Running > 0 && e.Publishing > 0:
		return fmt.Sprintf("%v: library %q has %d running job(s) and %d job(s) with an interrupted publish; cancel the running ones or let them finish, and %s", ErrActiveWork, e.Library, e.Running, e.Publishing, resume)
	case e.Running > 0:
		return fmt.Sprintf("%v: library %q has %d running job(s); cancel them or let them finish, then delete it", ErrActiveWork, e.Library, e.Running)
	default:
		return fmt.Sprintf("%v: library %q has %d job(s) with an interrupted publish; %s, then delete it", ErrActiveWork, e.Library, e.Publishing, resume)
	}
}

func (e *LibraryBusyError) Unwrap() error {
	return ErrActiveWork
}

// DeleteLibrary removes a library together with every source, asset, job,
// attempt, and scan record it owns, in one transaction, and returns the jobs
// that went with it. The media files are not touched.
//
// Jobs that only wait go with the library: a pending job has never run, and
// one waiting to retry holds nothing a worker is using. The delete is refused
// with a *LibraryBusyError in exactly two cases, and nothing is deleted then:
//
//   - a worker holds one of the library's jobs (leased, running, validating,
//     or replacing). Its attempt writes a part file and may be about to
//     replace a media file; deleting the row under it would leave that work
//     without a record.
//   - the publish of a job was interrupted part-way: its journal is at
//     prepared, published, or source_cleaned. The journal names a destination
//     file and possibly a backup on disk that only the job's next attempt can
//     finish with, and deleting the row would strand them. This holds whatever
//     the job's state, so a job waiting to retry an interrupted publish keeps
//     its library.
//
// A publish that stopped in conflict does not refuse the delete. No attempt
// can move a conflict, so it would keep its library forever; it waits for the
// operator, like it does for a cancel (see cancelSkipReasonTx), and deleting
// the library is the operator letting go of it. What the conflict left on
// disk stays there: the caller reads the journal before the delete to say
// which files those are.
//
// Checking and deleting share the transaction, and the store has a single
// connection, so no job can be leased between the two.
func (s *SQLiteStore) DeleteLibrary(ctx context.Context, name string) ([]DeletedJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin library deletion: %w", err)
	}
	defer rollback(tx)
	if _, err := getLibrary(ctx, tx, name); err != nil {
		return nil, err
	}

	busy := LibraryBusyError{Library: name}
	held := []domain.JobState{domain.JobStateLeased, domain.JobStateRunning, domain.JobStateValidating, domain.JobStateReplacing}
	args := []any{name}
	for _, state := range held {
		args = append(args, string(state))
	}
	if err := tx.QueryRowContext(ctx, `
SELECT count(*) FROM jobs j WHERE j.library_name = ? AND j.state IN (`+placeholders(len(held))+`)
`, args...).Scan(&busy.Running); err != nil {
		return nil, fmt.Errorf("count running jobs of library %q: %w", name, err)
	}
	if err := tx.QueryRowContext(ctx, `
SELECT count(*) FROM jobs j WHERE j.library_name = ? AND `+publishInFlightCondition,
		append([]any{name}, settledPublishStages()...)...).Scan(&busy.Publishing); err != nil {
		return nil, fmt.Errorf("count journaled jobs of library %q: %w", name, err)
	}
	if busy.Running > 0 || busy.Publishing > 0 {
		return nil, &busy
	}

	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(slug, '') FROM jobs WHERE library_name = ? ORDER BY id`, name)
	if err != nil {
		return nil, fmt.Errorf("list jobs of library %q: %w", name, err)
	}
	var deleted []DeletedJob
	for rows.Next() {
		var job DeletedJob
		if err := rows.Scan(&job.ID, &job.Slug); err != nil {
			return nil, errors.Join(fmt.Errorf("scan job of library %q: %w", name, err), rows.Close())
		}
		deleted = append(deleted, job)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("list jobs of library %q: %w", name, err)
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
			return nil, fmt.Errorf("delete library %q: %w", name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit library deletion: %w", err)
	}
	return deleted, nil
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
