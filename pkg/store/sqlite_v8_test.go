package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	replacepkg "github.com/zekurio/anvil/pkg/replace"
)

// schemaVersion7 is the schema as version 7 bootstrapped it, frozen here so
// the migration is tested against what old installations actually hold.
const schemaVersion7 = `
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE library_scans (
	library_name TEXT PRIMARY KEY,
	next_token INTEGER NOT NULL DEFAULT 0,
	applied_token INTEGER NOT NULL DEFAULT 0,
	applied_at TEXT
);
CREATE TABLE source_scans (
 library_name TEXT NOT NULL,
 relative_path TEXT NOT NULL,
 applied_token INTEGER NOT NULL,
 PRIMARY KEY (library_name, relative_path)
);
CREATE TABLE media_sources (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	library_name TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('file', 'package')),
	relative_path TEXT NOT NULL,
	generation INTEGER NOT NULL CHECK (generation >= 1),
	is_current INTEGER NOT NULL DEFAULT 1 CHECK (is_current IN (0, 1)),
	status TEXT NOT NULL CHECK (status IN ('active', 'processed', 'missing')),
	size_bytes INTEGER NOT NULL DEFAULT 0,
	mod_time TEXT,
	hash_algorithm TEXT NOT NULL DEFAULT '',
	hash_value TEXT NOT NULL DEFAULT '',
	first_seen_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (library_name, relative_path, generation)
);
CREATE UNIQUE INDEX media_sources_current_idx ON media_sources(library_name, relative_path) WHERE is_current = 1;
CREATE INDEX media_sources_library_path_idx ON media_sources(library_name, relative_path, generation DESC);
CREATE TABLE media_assets (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	source_id INTEGER NOT NULL REFERENCES media_sources(id) ON DELETE CASCADE,
	relative_path TEXT NOT NULL,
	generation INTEGER NOT NULL CHECK (generation >= 1),
	is_current INTEGER NOT NULL DEFAULT 1 CHECK (is_current IN (0, 1)),
	role TEXT NOT NULL CHECK (role IN ('primary_video', 'video', 'sample', 'subtitle', 'metadata', 'extra', 'unknown')),
	status TEXT NOT NULL CHECK (status IN ('active', 'processed', 'missing')),
	size_bytes INTEGER NOT NULL DEFAULT 0,
	mod_time TEXT,
	hash_algorithm TEXT NOT NULL DEFAULT '',
	hash_value TEXT NOT NULL DEFAULT '',
	first_seen_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (source_id, relative_path, generation)
);
CREATE UNIQUE INDEX media_assets_current_idx ON media_assets(source_id, relative_path) WHERE is_current = 1;
CREATE INDEX media_assets_source_path_idx ON media_assets(source_id, relative_path, generation DESC);
CREATE TABLE jobs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	slug TEXT NOT NULL UNIQUE,
	source_id INTEGER NOT NULL REFERENCES media_sources(id) ON DELETE CASCADE,
	asset_id INTEGER REFERENCES media_assets(id) ON DELETE CASCADE,
	library_name TEXT NOT NULL,
	priority INTEGER NOT NULL DEFAULT 0,
	state TEXT NOT NULL CHECK (state IN ('pending', 'leased', 'running', 'validating', 'replacing', 'complete', 'failed', 'retrying', 'skipped', 'canceled')),
	lease_owner TEXT NOT NULL DEFAULT '',
	lease_deadline TEXT,
	heartbeat_at TEXT,
	attempt_count INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	input_size_bytes INTEGER NOT NULL DEFAULT 0,
	output_size_bytes INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	completed_at TEXT,
	pipeline_context_json BLOB NOT NULL DEFAULT x''
);
CREATE UNIQUE INDEX jobs_target_idx ON jobs(source_id, ifnull(asset_id, 0));
CREATE INDEX jobs_state_priority_idx ON jobs(state, priority DESC, created_at ASC, id ASC);
CREATE INDEX jobs_lease_deadline_idx ON jobs(lease_deadline) WHERE lease_deadline IS NOT NULL;
CREATE TABLE attempts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	number INTEGER NOT NULL,
	worker_id TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('running', 'succeeded', 'failed', 'canceled')),
	resolved_library_json BLOB NOT NULL DEFAULT x'',
	resolved_flow_json BLOB NOT NULL DEFAULT x'',
	resolved_profile_json BLOB NOT NULL DEFAULT x'',
	started_at TEXT NOT NULL,
	finished_at TEXT,
	error TEXT NOT NULL DEFAULT '',
	UNIQUE (job_id, number)
);
CREATE TABLE attempt_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	attempt_id INTEGER NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
	type TEXT NOT NULL CHECK (type IN ('block_started', 'block_finished', 'block_failed', 'artifact')),
	name TEXT NOT NULL,
	message TEXT NOT NULL DEFAULT '',
	payload BLOB NOT NULL DEFAULT x'',
	created_at TEXT NOT NULL
);
CREATE INDEX attempt_events_attempt_idx ON attempt_events(attempt_id, id);
CREATE TABLE publish_operations (
	job_id INTEGER PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
	stage TEXT NOT NULL CHECK (stage IN ('prepared', 'published', 'source_cleaned', 'committed', 'conflict')),
	operation_json BLOB NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
`

// version7Checkpoint is a job checkpoint as a version 7 daemon stored it:
// the probe has cover art ahead of the main video stream.
const version7Checkpoint = `{"version":8,"input_path":"/media/movies/film.mkv","probe":{"Path":"/media/movies/film.mkv","FormatName":"matroska,webm","DurationSeconds":7200,"SizeBytes":1000,"Streams":[` +
	`{"Index":0,"Type":"video","Codec":"mjpeg","Width":600,"Height":900,"Disposition":{"attached_pic":true}},` +
	`{"Index":1,"Type":"video","Codec":"hevc","Width":3840,"Height":1600,"Disposition":{"default":true}},` +
	`{"Index":2,"Type":"audio","Codec":"truehd","Channels":8,"Language":"eng","Disposition":null},` +
	`{"Index":3,"Type":"audio","Codec":"ac3","Channels":6,"Language":"eng","Disposition":null}]}}`

// schemaObjects describes the tables and indexes of a database in a form
// that does not depend on how its statements were spelled or in which order
// they ran: every column with its type, default, and constraints, and every
// index with its definition.
func schemaObjects(t *testing.T, state *SQLiteStore) map[string]string {
	t.Helper()
	ctx := context.Background()
	objects := map[string]string{}
	var tables []string
	err := state.eachRow(ctx, "schema", `SELECT type, name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`, nil, func(rows *sql.Rows) error {
		var kind, name, statement string
		if err := rows.Scan(&kind, &name, &statement); err != nil {
			return err
		}
		if kind == "table" {
			tables = append(tables, name)
			// Checks and foreign keys are only in the statement. A rebuilt
			// table is named in quotes, which says nothing about it.
			statement = strings.Replace(statement, `"`+name+`"`, name, 1)
		}
		statement = strings.Join(strings.Fields(statement), " ")
		objects[kind+" "+name] = strings.NewReplacer("( ", "(", " )", ")").Replace(statement)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var columns []string
		err := state.eachRow(ctx, "columns", `SELECT cid, name, type, "notnull", COALESCE(dflt_value, 'NULL'), pk FROM pragma_table_info(?)`, []any{table}, func(rows *sql.Rows) error {
			var cid, notNull, pk int
			var name, kind, fallback string
			if err := rows.Scan(&cid, &name, &kind, &notNull, &fallback, &pk); err != nil {
				return err
			}
			columns = append(columns, fmt.Sprintf("%d %s %s notnull=%d default=%s pk=%d", cid, name, kind, notNull, fallback, pk))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		objects["columns "+table] = strings.Join(columns, "; ")
	}
	return objects
}

// A database that was migrated has to end up with the schema a fresh one is
// created with, down to the order of the columns: the probe facts of a job
// sit ahead of its checkpoint on purpose.
func TestMigratedSchemaEqualsAFreshOne(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "anvil.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, schemaVersion7+`INSERT INTO schema_migrations (version, applied_at) VALUES (7, '2026-03-01T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := migrated.Close(); err != nil {
			t.Error(err)
		}
	}()
	fresh, upgraded := schemaObjects(t, openTestStore(t)), schemaObjects(t, migrated)
	for name, want := range fresh {
		if got := upgraded[name]; got != want {
			t.Errorf("%s after migration:\n%s\nfresh:\n%s", name, got, want)
		}
	}
	for name := range upgraded {
		if _, known := fresh[name]; !known {
			t.Errorf("the migration leaves %s, which a fresh database does not have", name)
		}
	}
}

func openTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	state, err := Open(context.Background(), filepath.Join(t.TempDir(), "anvil.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	return state
}

func defaultFlowID(t *testing.T, state *SQLiteStore) int64 {
	t.Helper()
	flows, err := state.ListFlows(context.Background())
	if err != nil || len(flows) == 0 {
		t.Fatalf("ListFlows = %v, %v", flows, err)
	}
	return flows[0].ID
}

func testLibrary(name string, flowID int64) config.LibraryConfig {
	return config.LibraryConfig{
		Name: name, Kind: "media", Path: "/media/" + name, FlowID: flowID,
		Media: config.MediaLibraryConfig{ReplacementMode: "replace"},
	}
}

// mustCreateLibrary gives a test the library row every scan and requeue
// needs.
func mustCreateLibrary(t testing.TB, state *SQLiteStore, name string) {
	t.Helper()
	flows, err := state.ListFlows(context.Background())
	if err != nil || len(flows) == 0 {
		t.Fatalf("ListFlows = %v, %v", flows, err)
	}
	if _, err := state.CreateLibrary(context.Background(), testLibrary(name, flows[0].ID), time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestScansOfAnUnknownLibraryAreRefused(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	if _, err := state.BeginLibraryScan(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("BeginLibraryScan = %v, want ErrNotFound", err)
	}
	mustCreateLibrary(t, state, "movies")
	token, err := state.BeginLibraryScan(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	// The library is deleted while the scan walks the filesystem: applying
	// its result must not bring sources back.
	if _, err := state.DeleteLibrary(ctx, "movies"); err != nil {
		t.Fatal(err)
	}
	entry := scanTestEntry(1)
	if _, err := state.ApplyLibraryScan(ctx, token, ApplyScanInput{LibraryName: "movies", Entries: []ScanEntry{entry}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ApplyLibraryScan after delete = %v, want ErrNotFound", err)
	}
	if _, err := state.ForceOccurrence(ctx, ForceOccurrenceInput{
		LibraryName: "movies", SourceKind: domain.SourceKindFile, SourceRelativePath: "a.mkv", AssetRelativePath: "a.mkv",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ForceOccurrence after delete = %v, want ErrNotFound", err)
	}
	var sources int
	if err := state.db.QueryRowContext(ctx, `SELECT count(*) FROM media_sources`).Scan(&sources); err != nil || sources != 0 {
		t.Fatalf("%d sources were written for a deleted library, %v", sources, err)
	}
}

func legacyConfig(t *testing.T, text string) config.LegacyConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "anvil.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Legacy
}

func TestImportLegacyConfigOnceAndEquivalently(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	legacy := legacyConfig(t, `
[arrs.radarr]
type = "radarr"
base_url = "http://localhost:7878"
api_key = "secret"

[profiles.fast.video]
preset = "8"
crf_max = 45

[profiles.fast.video.overrides.hevc]
skip_encode = true

[profiles.unused.video]
preset = "2"

[libraries.movies]
path = "/srv/media/movies"
arr = "radarr"
include = ["Films/**"]
exclude = ["**/Extras/**"]
ignore_regex = ['\.sample\.']
priority = 3

[libraries.shows]
path = "/srv/media/shows"
profile = "fast"

[libraries.inbox]
kind = "download"
path = "/srv/downloads"
profile = "fast"

[libraries.inbox.download]
handoff_path = "/srv/import"
stable_for = "90s"
`)
	result, err := state.ImportLegacyConfig(ctx, legacy, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Imported || len(result.Arrs) != 1 || len(result.Libraries) != 3 || len(result.Flows) != 2 || len(result.Skipped) != 0 {
		t.Fatalf("result = %+v", result)
	}

	flows, err := state.ListFlows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]Flow)
	for _, item := range flows {
		byName[item.Name] = item
	}
	// The unmodified default profile is the seeded flow; the unused profile
	// is not imported at all.
	if len(flows) != 2 || byName[DefaultFlowName].ID == 0 || byName["fast"].ID == 0 {
		t.Fatalf("flows = %v", byName)
	}

	libraries, err := state.ListLibraries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(libraries) != 3 {
		t.Fatalf("libraries = %+v", libraries)
	}
	for _, library := range libraries {
		if library.Paused {
			t.Errorf("imported library %q is paused", library.Name)
		}
	}
	movies, err := state.GetLibrary(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	wantRules, wantFallback := config.LegacyRules([]string{"Films/**"}, []string{"**/Extras/**"}, []string{`\.sample\.`})
	if movies.FlowID != byName[DefaultFlowName].ID || movies.Arr != "radarr" || movies.Priority != 3 ||
		movies.RuleFallback != wantFallback || len(movies.Rules) != len(wantRules) || movies.Media.ReplacementMode != "replace" {
		t.Fatalf("movies = %+v", movies)
	}
	for i := range wantRules {
		if movies.Rules[i] != wantRules[i] {
			t.Errorf("movies rule %d = %+v, want %+v", i, movies.Rules[i], wantRules[i])
		}
	}
	inbox, err := state.GetLibrary(ctx, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	if inbox.FlowID != byName["fast"].ID || inbox.Download.HandoffPath != "/srv/import" ||
		inbox.Download.StableFor.Duration != 90*time.Second || len(inbox.Download.IgnorableGlobs) == 0 {
		t.Fatalf("inbox = %+v", inbox)
	}
	if arr, err := state.GetArr(ctx, "radarr"); err != nil || arr.APIKey != "secret" || arr.Type != "radarr" {
		t.Fatalf("arr = %+v, %v", arr, err)
	}

	// The imported flow resolves to what the profile resolved to, for a
	// source the override matches and for one it does not.
	profile := legacy.Profiles["fast"]
	profile.Name = "fast"
	for _, codec := range []string{"hevc", "h264"} {
		probe := domain.ProbeResult{Streams: []domain.MediaStream{{Type: "video", Codec: codec, Width: 1920, Height: 1080}}}
		resolution, err := flow.Evaluate(byName["fast"].Graph, flow.Facts{Probe: probe})
		if err != nil {
			t.Fatal(err)
		}
		want := domain.EffectiveVideoProfile(profile.ToDomain(), domain.JobMetadata{}, codec)
		if resolution.Profile.Video.SkipEncode != want.SkipEncode {
			t.Errorf("%s: flow skips encode = %t, profile = %t", codec, resolution.Profile.Video.SkipEncode, want.SkipEncode)
		}
		if !want.SkipEncode && (resolution.Profile.Video.Preset != want.Preset || resolution.Profile.Video.CRFMax != want.CRFMax) {
			t.Errorf("%s: flow video = %+v, profile = %+v", codec, resolution.Profile.Video, want)
		}
	}

	// A second import does nothing, even with a changed file.
	changed := legacyConfig(t, `
[libraries.extra]
path = "/srv/media/extra"
`)
	again, err := state.ImportLegacyConfig(ctx, changed, time.Now())
	if err != nil || again.Imported || len(again.Libraries) != 0 {
		t.Fatalf("second import = %+v, %v", again, err)
	}
	if libraries, err := state.ListLibraries(ctx); err != nil || len(libraries) != 3 {
		t.Fatalf("libraries after second import = %d, %v", len(libraries), err)
	}
}

func TestImportLegacyConfigMarksAnEmptyFileAsDone(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	if done, err := state.LegacyConfigImported(ctx); err != nil || done {
		t.Fatalf("LegacyConfigImported before import = %t, %v", done, err)
	}
	result, err := state.ImportLegacyConfig(ctx, legacyConfig(t, ``), time.Now())
	if err != nil || !result.Imported || len(result.Libraries)+len(result.Flows)+len(result.Arrs) != 0 {
		t.Fatalf("empty import = %+v, %v", result, err)
	}
	if done, err := state.LegacyConfigImported(ctx); err != nil || !done {
		t.Fatalf("LegacyConfigImported after import = %t, %v", done, err)
	}
	// Libraries added to the file later are not picked up.
	later, err := state.ImportLegacyConfig(ctx, legacyConfig(t, "[libraries.movies]\npath = \"/srv/media/movies\"\n"), time.Now())
	if err != nil || later.Imported {
		t.Fatalf("later import = %+v, %v", later, err)
	}
	if libraries, err := state.ListLibraries(ctx); err != nil || len(libraries) != 0 {
		t.Fatalf("libraries = %+v, %v", libraries, err)
	}
}

// A library whose imported flow does not validate would fail every file it
// holds, three attempts each. It is imported paused instead.
func TestImportLegacyConfigPausesLibrariesOfAnInvalidFlow(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	// crf_min = 0 is valid TOML and below what libsvtav1 accepts.
	legacy := legacyConfig(t, `
[profiles.wide.video]
crf_min = 0

[profiles.fine.video]
preset = "4"

[libraries.movies]
path = "/srv/media/movies"
profile = "wide"

[libraries.shows]
path = "/srv/media/shows"
profile = "wide"

[libraries.music]
path = "/srv/media/music"
profile = "fine"
`)
	result, err := state.ImportLegacyConfig(ctx, legacy, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pausedFor := map[string]string{}
	for _, library := range result.Libraries {
		pausedFor[library.Name] = library.PausedFor
	}
	const issue = "Must be between 1 and 63 for libsvtav1."
	if len(pausedFor) != 3 || pausedFor["movies"] != issue || pausedFor["shows"] != issue || pausedFor["music"] != "" {
		t.Fatalf("libraries = %+v", result.Libraries)
	}
	for name, wantPaused := range map[string]bool{"movies": true, "shows": true, "music": false} {
		library, err := state.GetLibrary(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if library.Paused != wantPaused {
			t.Errorf("library %q paused = %t, want %t", name, library.Paused, wantPaused)
		}
	}
}

func TestImportLegacyConfigKeepsAModifiedDefaultProfileApart(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	legacy := legacyConfig(t, `
[profiles.default-av1.video]
preset = "4"

[libraries.movies]
path = "/srv/media/movies"
`)
	result, err := state.ImportLegacyConfig(ctx, legacy, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Flows) != 1 || !result.Flows[0].Created || result.Flows[0].Flow != config.DefaultProfileName {
		t.Fatalf("flows = %+v", result.Flows)
	}
	movies, err := state.GetLibrary(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	seeded := defaultFlowID(t, state)
	if movies.FlowID == seeded || movies.FlowID != result.Flows[0].FlowID {
		t.Fatalf("movies runs flow %d, seeded flow is %d", movies.FlowID, seeded)
	}
}

func TestFreshDatabaseHasSchema8AndTheDefaultFlow(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	version, exists, err := state.schemaVersion(ctx)
	if err != nil || !exists || version != 8 {
		t.Fatalf("schemaVersion = %d, %t, %v", version, exists, err)
	}
	flows, err := state.ListFlows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 1 || flows[0].Name != DefaultFlowName || flows[0].Revision != 1 {
		t.Fatalf("flows = %+v", flows)
	}
	want, err := flow.Canonical(flow.Default())
	if err != nil {
		t.Fatal(err)
	}
	got, err := flow.Canonical(flows[0].Graph)
	if err != nil || string(got) != string(want) {
		t.Fatalf("seeded flow differs from flow.Default(): %v", err)
	}
	for _, list := range []func() (int, error){
		func() (int, error) { rows, err := state.ListLibraries(ctx); return len(rows), err },
		func() (int, error) { rows, err := state.ListArrs(ctx); return len(rows), err },
	} {
		if count, err := list(); err != nil || count != 0 {
			t.Fatalf("fresh database lists %d rows, %v", count, err)
		}
	}
}

func TestDeletedDefaultFlowStaysDeleted(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "anvil.db")
	state, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.DeleteFlow(ctx, defaultFlowID(t, state)); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	}()
	if flows, err := state.ListFlows(ctx); err != nil || len(flows) != 0 {
		t.Fatalf("flows after reopen = %+v, %v", flows, err)
	}
}

func TestMigratesVersion7WithJobsInPlace(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "anvil.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	now := encodeTime(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	statements := []string{
		schemaVersion7,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (5, '` + now + `'), (6, '` + now + `'), (7, '` + now + `')`,
		`INSERT INTO library_scans (library_name, next_token, applied_token, applied_at) VALUES ('movies', 3, 3, '` + now + `')`,
		`INSERT INTO media_sources (id, library_name, kind, relative_path, generation, status, size_bytes, first_seen_at, last_seen_at, updated_at)
			VALUES (1, 'movies', 'file', 'film.mkv', 1, 'active', 1000, '` + now + `', '` + now + `', '` + now + `')`,
		`INSERT INTO media_assets (id, source_id, relative_path, generation, role, status, size_bytes, first_seen_at, last_seen_at, updated_at)
			VALUES (1, 1, 'film.mkv', 1, 'primary_video', 'active', 1000, '` + now + `', '` + now + `', '` + now + `')`,
		`INSERT INTO media_sources (id, library_name, kind, relative_path, generation, status, size_bytes, first_seen_at, last_seen_at, updated_at)
			VALUES (2, 'movies', 'file', 'second.mkv', 1, 'active', 2000, '` + now + `', '` + now + `', '` + now + `'),
				(3, 'movies', 'file', 'third.mkv', 1, 'active', 3000, '` + now + `', '` + now + `', '` + now + `')`,
		`INSERT INTO media_assets (id, source_id, relative_path, generation, role, status, size_bytes, first_seen_at, last_seen_at, updated_at)
			VALUES (2, 2, 'second.mkv', 1, 'primary_video', 'active', 2000, '` + now + `', '` + now + `', '` + now + `'),
				(3, 3, 'third.mkv', 1, 'primary_video', 'active', 3000, '` + now + `', '` + now + `', '` + now + `')`,
		// The first job holds a checkpoint as the previous version wrote it,
		// the second one that cannot be read, and the third none.
		`INSERT INTO jobs (id, slug, source_id, asset_id, library_name, state, created_at, updated_at, pipeline_context_json)
			VALUES (1, 'quiet-anvil', 1, 1, 'movies', 'pending', '` + now + `', '` + now + `', CAST('` + version7Checkpoint + `' AS BLOB)),
				(2, 'loud-anvil', 2, 2, 'movies', 'failed', '` + now + `', '` + now + `', x'7b00ff'),
				(3, 'plain-anvil', 3, 3, 'movies', 'pending', '` + now + `', '` + now + `', x'')`,
		`INSERT INTO attempts (id, job_id, number, worker_id, state, resolved_library_json, resolved_profile_json, started_at)
			VALUES (1, 1, 1, 'w', 'failed', '{"Name":"movies"}', '{"Name":"default-av1"}', '` + now + `')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare version 7 database: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// A read-only handle cannot migrate, and says so instead of failing on a
	// missing table.
	if _, err := OpenReadOnly(ctx, path); !errors.Is(err, ErrIncompatibleSchema) {
		t.Fatalf("OpenReadOnly on version 7 = %v, want ErrIncompatibleSchema", err)
	}

	state, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	}()
	if version, _, err := state.schemaVersion(ctx); err != nil || version != 8 {
		t.Fatalf("schemaVersion = %d, %v", version, err)
	}
	job, err := state.GetJob(ctx, 1)
	if err != nil || job.LibraryName != "movies" || job.State != domain.JobStatePending || job.Slug != "quiet-anvil" {
		t.Fatalf("job after migration = %+v, %v", job, err)
	}
	// The checkpoint came along unchanged, and the facts a file list shows
	// were taken out of it: of the main video stream, not the cover art, and
	// of the first audio track.
	if checkpoint, found, err := state.GetJobPipelineContext(ctx, 1); err != nil || !found || checkpoint.Probe == nil || len(checkpoint.Probe.Streams) != 4 {
		t.Fatalf("checkpoint after migration = %+v, %t, %v", checkpoint, found, err)
	}
	files, _, err := state.ListLibraryFiles(ctx, LibraryFileFilter{LibraryName: "movies"})
	if err != nil || len(files) != 3 {
		t.Fatalf("files after migration = %+v, %v", files, err)
	}
	wantFacts := ProbeFacts{FormatName: "matroska,webm", VideoCodec: "hevc", Width: 3840, Height: 1600, AudioCodec: "truehd", AudioChannels: 8}
	if files[0].Probe != wantFacts {
		t.Errorf("probe facts after migration = %+v, want %+v", files[0].Probe, wantFacts)
	}
	if files[1].Probe != (ProbeFacts{}) || files[2].Probe != (ProbeFacts{}) {
		t.Errorf("jobs without a readable checkpoint got probe facts: %+v, %+v", files[1].Probe, files[2].Probe)
	}
	attempts, err := state.ListAttemptsForJob(ctx, 1)
	if err != nil || len(attempts) != 1 || string(attempts[0].ResolvedProfile) != `{"Name":"default-av1"}` {
		t.Fatalf("attempts after migration = %+v, %v", attempts, err)
	}
	if libraries, err := state.ListLibraries(ctx); err != nil || len(libraries) != 0 {
		t.Fatalf("the migration invented libraries: %+v, %v", libraries, err)
	}
	// The library name old rows use is free to be created, and its history
	// then belongs to it.
	flowID := defaultFlowID(t, state)
	if _, err := state.CreateLibrary(ctx, testLibrary("movies", flowID), time.Now()); err != nil {
		t.Fatalf("CreateLibrary over existing rows: %v", err)
	}
	overviews, err := state.LibraryOverviews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	overview := overviews["movies"]
	if overview.Files != 3 || overview.TotalBytes != 6000 || overview.Jobs[domain.JobStatePending] != 2 || overview.LastScanAt == nil {
		t.Fatalf("overview = %+v", overview)
	}
}

func TestFlowUpdateNeedsTheStoredRevision(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	created, err := state.CreateFlow(ctx, FlowInput{Name: " Mine ", Description: "d", Graph: flow.Default()}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Mine" || created.Revision != 1 || len(created.Graph.Nodes) == 0 {
		t.Fatalf("created = %+v", created)
	}

	updated, err := state.UpdateFlow(ctx, created.ID, FlowInput{Name: "Mine", Description: "edited", Graph: created.Graph}, 1, time.Now())
	if err != nil || updated.Revision != 2 || updated.Description != "edited" {
		t.Fatalf("UpdateFlow = %+v, %v", updated, err)
	}
	// A second editor still holding revision 1 must not overwrite the first.
	if _, err := state.UpdateFlow(ctx, created.ID, FlowInput{Name: "Mine", Graph: created.Graph}, 1, time.Now()); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update = %v, want ErrRevisionConflict", err)
	}
	if current, err := state.GetFlow(ctx, created.ID); err != nil || current.Description != "edited" || current.Revision != 2 {
		t.Fatalf("flow after stale update = %+v, %v", current, err)
	}

	if _, err := state.CreateFlow(ctx, FlowInput{Name: "Mine", Graph: flow.Default()}, time.Now()); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name = %v, want ErrNameTaken", err)
	}
	if _, err := state.UpdateFlow(ctx, created.ID, FlowInput{Name: DefaultFlowName, Graph: created.Graph}, 2, time.Now()); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("rename onto another flow = %v, want ErrNameTaken", err)
	}
	if _, err := state.UpdateFlow(ctx, 9999, FlowInput{Name: "x", Graph: created.Graph}, 1, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a missing flow = %v, want ErrNotFound", err)
	}
}

func TestFlowAndArrInUseCannotBeDeleted(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	flowID := defaultFlowID(t, state)
	if _, err := state.CreateArr(ctx, config.ArrConfig{Name: "radarr", Type: "radarr", BaseURL: "http://radarr", APIKey: "secret"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shows", "movies"} {
		library := testLibrary(name, flowID)
		library.Arr = "radarr"
		if _, err := state.CreateLibrary(ctx, library, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	var inUse *InUseError
	err := state.DeleteFlow(ctx, flowID)
	if !errors.Is(err, ErrInUse) || !errors.As(err, &inUse) || strings.Join(inUse.Libraries, ",") != "movies,shows" {
		t.Fatalf("DeleteFlow = %v", err)
	}
	err = state.DeleteArr(ctx, "radarr")
	if !errors.As(err, &inUse) || len(inUse.Libraries) != 2 {
		t.Fatalf("DeleteArr = %v", err)
	}
	usage, err := state.FlowLibraries(ctx)
	if err != nil || strings.Join(usage[flowID], ",") != "movies,shows" {
		t.Fatalf("FlowLibraries = %v, %v", usage, err)
	}

	for _, name := range []string{"shows", "movies"} {
		if _, err := state.DeleteLibrary(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.DeleteArr(ctx, "radarr"); err != nil {
		t.Fatal(err)
	}
	if err := state.DeleteFlow(ctx, flowID); err != nil {
		t.Fatal(err)
	}
	if err := state.DeleteFlow(ctx, flowID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteFlow = %v, want ErrNotFound", err)
	}
}

func TestLibraryRoundTripAndImmutableKind(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	flowID := defaultFlowID(t, state)
	download := config.LibraryConfig{
		Name: "inbox", Kind: "download", Path: "/downloads", FlowID: flowID, Paused: true,
		ScanInterval: config.Duration{Duration: 10 * time.Minute}, Priority: 5, ConcurrencyLimit: 2,
		Rules:        []config.Rule{{Action: config.RuleDeny, Field: config.RuleFieldSize, Op: config.RuleOpLess, Value: "200 MB"}},
		RuleFallback: config.RuleAllow,
		Download: config.DownloadLibraryConfig{
			HandoffPath: "/import", StableFor: config.Duration{Duration: 0}, PackageMode: "auto", HandoffMode: "move",
			PreserveRelativePath: true, CleanupSourceMedia: true, PruneEmptyDirs: true, IgnorableGlobs: []string{"**/*.nfo"},
		},
	}
	created, err := state.CreateLibrary(ctx, download, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := state.GetLibrary(ctx, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Kind != "download" || !loaded.Paused || loaded.ScanInterval.Duration != 10*time.Minute ||
		loaded.Priority != 5 || loaded.ConcurrencyLimit != 2 || len(loaded.Rules) != 1 || loaded.Rules[0] != download.Rules[0] ||
		loaded.Download.HandoffPath != "/import" || loaded.Download.StableFor.Duration != 0 || loaded.Download.HandoffMode != "move" ||
		!loaded.Download.PreserveRelativePath || !loaded.Download.CleanupSourceMedia || !loaded.Download.PruneEmptyDirs ||
		len(loaded.Download.IgnorableGlobs) != 1 || loaded.CreatedAt.IsZero() || !loaded.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("loaded = %+v", loaded)
	}

	if _, err := state.CreateLibrary(ctx, download, time.Now()); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate library = %v, want ErrNameTaken", err)
	}
	asMedia := download
	asMedia.Kind = "media"
	if _, err := state.UpdateLibrary(ctx, asMedia, time.Now()); !errors.Is(err, ErrImmutable) {
		t.Fatalf("kind change = %v, want ErrImmutable", err)
	}
	dangling := download
	dangling.FlowID = 9999
	if _, err := state.UpdateLibrary(ctx, dangling, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown flow = %v, want ErrNotFound", err)
	}
	dangling = download
	dangling.Arr = "nope"
	if _, err := state.UpdateLibrary(ctx, dangling, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown arr = %v, want ErrNotFound", err)
	}
	download.Paused = false
	download.Path = "/downloads2"
	updated, err := state.UpdateLibrary(ctx, download, time.Now())
	if err != nil || updated.Paused || updated.Path != "/downloads2" {
		t.Fatalf("UpdateLibrary = %+v, %v", updated, err)
	}
	if _, err := state.UpdateLibrary(ctx, testLibrary("ghost", flowID), time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a missing library = %v, want ErrNotFound", err)
	}
}

// seedLibraryFiles scans three files into a library and returns their jobs
// by path.
func seedLibraryFiles(t *testing.T, state *SQLiteStore, library domain.LibraryName) map[string]domain.Job {
	t.Helper()
	ctx := context.Background()
	token, err := state.BeginLibraryScan(ctx, library)
	if err != nil {
		t.Fatal(err)
	}
	var entries []ScanEntry
	for path, size := range map[string]int64{"A/alpha.mkv": 300, "B/beta_50%.mkv": 100, "gamma.mkv": 200} {
		entries = append(entries, ScanEntry{
			SourceKind: domain.SourceKindFile, SourceRelativePath: path, SourceFingerprint: domain.FileFingerprint{SizeBytes: size},
			AssetRelativePath: path, AssetRole: domain.MediaAssetRolePrimaryVideo, AssetFingerprint: domain.FileFingerprint{SizeBytes: size},
			Persist: true, Enqueue: true,
		})
	}
	if _, err := state.ApplyLibraryScan(ctx, token, ApplyScanInput{LibraryName: library, Entries: entries, CompletedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	summaries, err := state.ListJobs(ctx, JobListFilter{LibraryName: library})
	if err != nil {
		t.Fatal(err)
	}
	jobs := make(map[string]domain.Job, len(summaries))
	for _, summary := range summaries {
		jobs[summary.SourcePath] = summary.Job
	}
	return jobs
}

func filePaths(files []LibraryFile) string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return strings.Join(paths, " ")
}

func TestListLibraryFilesFiltersSortsAndPages(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	if _, err := state.CreateLibrary(ctx, testLibrary("movies", defaultFlowID(t, state)), time.Now()); err != nil {
		t.Fatal(err)
	}
	jobs := seedLibraryFiles(t, state, "movies")
	now := time.Now()
	if _, err := state.TransitionJob(ctx, jobs["gamma.mkv"].ID, domain.JobStateSkipped, now, "skipped by flow"); err != nil {
		t.Fatal(err)
	}
	probe := domain.JobPipelineContext{Probe: &domain.ProbeResult{FormatName: "matroska,webm", Streams: []domain.MediaStream{
		{Type: "video", Codec: "mjpeg", Width: 600, Height: 900, Disposition: map[string]bool{"attached_pic": true}},
		{Type: "video", Codec: "hevc", Width: 1920, Height: 800},
		{Type: "subtitle", Codec: "subrip"},
		{Type: "audio", Codec: "eac3", Channels: 6},
		{Type: "audio", Codec: "aac", Channels: 2},
	}}}
	if err := state.SaveJobPipelineContext(ctx, jobs["A/alpha.mkv"].ID, probe, now); err != nil {
		t.Fatal(err)
	}
	// The list reads the probe facts from their own columns. A checkpoint it
	// cannot even decode makes no difference to it.
	if _, err := state.db.ExecContext(ctx, `UPDATE jobs SET pipeline_context_json = x'00ff' WHERE id = ?`, int64(jobs["A/alpha.mkv"].ID)); err != nil {
		t.Fatal(err)
	}

	all, total, err := state.ListLibraryFiles(ctx, LibraryFileFilter{LibraryName: "movies"})
	if err != nil || total != 3 || filePaths(all) != "A/alpha.mkv B/beta_50%.mkv gamma.mkv" {
		t.Fatalf("all = %q total %d, %v", filePaths(all), total, err)
	}
	wantFacts := ProbeFacts{FormatName: "matroska,webm", VideoCodec: "hevc", Width: 1920, Height: 800, AudioCodec: "eac3", AudioChannels: 6}
	if all[0].Probe != wantFacts || all[1].Probe != (ProbeFacts{}) {
		t.Fatalf("probes = %+v, %+v; want %+v and none", all[0].Probe, all[1].Probe, wantFacts)
	}
	// A checkpoint saved without a probe takes the facts away again.
	if err := state.SaveJobPipelineContext(ctx, jobs["A/alpha.mkv"].ID, domain.JobPipelineContext{}, now); err != nil {
		t.Fatal(err)
	}
	if cleared, _, err := state.ListLibraryFiles(ctx, LibraryFileFilter{LibraryName: "movies", Limit: 1}); err != nil || cleared[0].Probe != (ProbeFacts{}) {
		t.Fatalf("probe after a checkpoint without one = %+v, %v", cleared[0].Probe, err)
	}
	if err := state.SaveJobPipelineContext(ctx, jobs["A/alpha.mkv"].ID, probe, now); err != nil {
		t.Fatal(err)
	}
	if all[0].Status != FileStatusQueued || all[0].JobID == 0 || all[2].Status != FileStatusSkipped || all[2].LastError != "skipped by flow" {
		t.Fatalf("statuses = %+v", all)
	}

	tests := []struct {
		name   string
		filter LibraryFileFilter
		want   string
		total  int64
	}{
		{"status", LibraryFileFilter{Status: FileStatusSkipped}, "gamma.mkv", 1},
		{"status with no file", LibraryFileFilter{Status: FileStatusFailed}, "", 0},
		{"substring ignores case", LibraryFileFilter{Query: "ALPHA"}, "A/alpha.mkv", 1},
		{"percent is literal", LibraryFileFilter{Query: "50%"}, "B/beta_50%.mkv", 1},
		{"underscore is literal", LibraryFileFilter{Query: "a_5"}, "B/beta_50%.mkv", 1},
		{"size descending", LibraryFileFilter{Sort: LibraryFileSortSize, Descending: true}, "A/alpha.mkv gamma.mkv B/beta_50%.mkv", 3},
		{"status rank", LibraryFileFilter{Sort: LibraryFileSortStatus}, "A/alpha.mkv B/beta_50%.mkv gamma.mkv", 3},
		{"path descending", LibraryFileFilter{Descending: true}, "gamma.mkv B/beta_50%.mkv A/alpha.mkv", 3},
		{"second page", LibraryFileFilter{Limit: 2, Offset: 2}, "gamma.mkv", 3},
		{"page past the end", LibraryFileFilter{Limit: 2, Offset: 10}, "", 3},
	}
	for _, test := range tests {
		test.filter.LibraryName = "movies"
		files, total, err := state.ListLibraryFiles(ctx, test.filter)
		if err != nil || filePaths(files) != test.want || total != test.total {
			t.Errorf("%s: files = %q total %d, %v; want %q total %d", test.name, filePaths(files), total, err, test.want, test.total)
		}
	}
	if _, _, err := state.ListLibraryFiles(ctx, LibraryFileFilter{LibraryName: "movies", Sort: "color"}); err == nil {
		t.Error("an unknown sort key was accepted")
	}

	// The job listing pages and counts with the same filter.
	page, err := state.ListJobs(ctx, JobListFilter{LibraryName: "movies", Query: "mkv", Limit: 2, Offset: 2, NewestActivityFirst: true})
	if err != nil || len(page) != 1 {
		t.Fatalf("job page = %+v, %v", page, err)
	}
	if count, err := state.CountJobs(ctx, JobListFilter{LibraryName: "movies", States: []domain.JobState{domain.JobStatePending}}); err != nil || count != 2 {
		t.Fatalf("CountJobs = %d, %v", count, err)
	}
	if count, err := state.CountJobs(ctx, JobListFilter{Query: "beta"}); err != nil || count != 1 {
		t.Fatalf("CountJobs by path = %d, %v", count, err)
	}
}

func TestDeleteLibraryRefusesLiveWorkAndRemovesEverythingElse(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	flowID := defaultFlowID(t, state)
	for _, name := range []string{"movies", "shows"} {
		if _, err := state.CreateLibrary(ctx, testLibrary(name, flowID), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	jobs := seedLibraryFiles(t, state, "movies")
	seedLibraryFiles(t, state, "shows")
	remaining := func(table, column string) int {
		t.Helper()
		var count int
		if err := state.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE `+column+` = 'movies'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	wantBusy := func(err error, running, publishing int64, words string) {
		t.Helper()
		var busy *LibraryBusyError
		if !errors.As(err, &busy) || !errors.Is(err, ErrActiveWork) || busy.Running != running || busy.Publishing != publishing || !strings.Contains(err.Error(), words) {
			t.Fatalf("delete = %v, want it refused for %d running and %d publishing job(s), saying %q", err, running, publishing, words)
		}
		// A refusal deletes nothing.
		if remaining("jobs", "library_name") != 3 || remaining("libraries", "name") != 1 {
			t.Fatal("a refused delete removed rows")
		}
	}

	// A job a worker holds refuses the delete, in every state a worker can
	// hold it in. The two jobs that only wait do not.
	now := time.Now().UTC()
	leased, err := state.LeaseNextJobForLibraries(ctx, "worker-1", now.Add(time.Hour), now, []domain.LibraryName{"movies"})
	if err != nil || leased == nil {
		t.Fatalf("LeaseNextJobForLibraries = %v, %v", leased, err)
	}
	_, err = state.DeleteLibrary(ctx, "movies")
	wantBusy(err, 1, 0, "1 running job(s)")
	attempt, err := state.StartAttempt(ctx, leased.ID, "worker-1", nil, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, held := range []domain.JobState{domain.JobStateRunning, domain.JobStateValidating, domain.JobStateReplacing} {
		if _, err := state.TransitionJob(ctx, leased.ID, held, now, ""); err != nil {
			t.Fatal(err)
		}
		_, err = state.DeleteLibrary(ctx, "movies")
		wantBusy(err, 1, 0, "running job(s)")
	}

	// The attempt fails and the job waits to retry: nothing holds it any
	// more. With an interrupted publish it still keeps its library, because
	// the journal names files on disk only the job's own recovery can
	// resolve.
	if _, err := state.FinishAttempt(ctx, attempt.ID, domain.AttemptStateFailed, "publish interrupted", now); err != nil {
		t.Fatal(err)
	}
	if _, err := state.TransitionJob(ctx, leased.ID, domain.JobStateRetrying, now, "publish interrupted"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, `
INSERT INTO publish_operations (job_id, stage, operation_json, created_at, updated_at) VALUES (?, ?, '{}', ?, ?)
`, int64(leased.ID), string(replacepkg.PublishStagePrepared), encodeTime(now), encodeTime(now)); err != nil {
		t.Fatal(err)
	}
	setStage := func(stage replacepkg.PublishStage) {
		t.Helper()
		if _, err := state.db.ExecContext(ctx, `UPDATE publish_operations SET stage = ? WHERE job_id = ?`, string(stage), int64(leased.ID)); err != nil {
			t.Fatal(err)
		}
	}
	for _, stage := range []replacepkg.PublishStage{replacepkg.PublishStagePrepared, replacepkg.PublishStagePublished, replacepkg.PublishStageSourceCleaned} {
		setStage(stage)
		_, err = state.DeleteLibrary(ctx, "movies")
		// The refusal says what resolves it, and that is not a plain retry.
		wantBusy(err, 0, 1, "an interrupted publish finishes the next time its job runs")
	}
	// A conflict is no interrupted publish: no attempt can move it, so it
	// must not keep the library, or the library could never be deleted.
	setStage(replacepkg.PublishStageConflict)

	// Two pending jobs and one waiting to retry: the library goes, and takes
	// them and their history with it.
	deleted, err := state.DeleteLibrary(ctx, "movies")
	if err != nil {
		t.Fatalf("DeleteLibrary with waiting jobs: %v", err)
	}
	if len(deleted) != len(jobs) {
		t.Fatalf("deleted jobs = %+v, want all %d", deleted, len(jobs))
	}
	for _, job := range deleted {
		if job.ID == 0 || job.Slug == "" {
			t.Errorf("deleted job %+v lacks its id or slug", job)
		}
	}
	for table, column := range map[string]string{
		"jobs": "library_name", "media_sources": "library_name", "library_scans": "library_name",
		"source_scans": "library_name", "libraries": "name",
	} {
		if count := remaining(table, column); count != 0 {
			t.Errorf("%s still has %d rows of the deleted library", table, count)
		}
	}
	for _, table := range []string{"media_assets", "attempts", "publish_operations"} {
		var orphans int
		query := `SELECT count(*) FROM ` + table
		switch table {
		case "media_assets":
			query += ` WHERE source_id NOT IN (SELECT id FROM media_sources)`
		default:
			query += ` WHERE job_id NOT IN (SELECT id FROM jobs)`
		}
		if err := state.db.QueryRowContext(ctx, query).Scan(&orphans); err != nil || orphans != 0 {
			t.Errorf("%s has %d orphaned rows, %v", table, orphans, err)
		}
	}
	// The other library is untouched, and nothing of the deleted one can be
	// leased any more.
	if files, total, err := state.ListLibraryFiles(ctx, LibraryFileFilter{LibraryName: "shows"}); err != nil || total != 3 || len(files) != 3 {
		t.Fatalf("other library after delete: %d files, total %d, %v", len(files), total, err)
	}
	for {
		next, err := state.LeaseNextJob(ctx, "worker-1", now.Add(time.Hour), now)
		if err != nil {
			t.Fatal(err)
		}
		if next == nil {
			break
		}
		if next.LibraryName != "shows" {
			t.Fatalf("leased %+v, a job of the deleted library", next)
		}
	}
	if _, err := state.DeleteLibrary(ctx, "movies"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

func TestArrKeepsItsKeyAndRejectsDuplicates(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	arr := config.ArrConfig{Name: "sonarr", Type: "sonarr", BaseURL: "http://sonarr:8989", APIKey: "k"}
	if _, err := state.CreateArr(ctx, arr, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateArr(ctx, arr, time.Now()); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate arr = %v, want ErrNameTaken", err)
	}
	arr.APIKey, arr.APIKeyFile = "", "/run/secrets/sonarr"
	updated, err := state.UpdateArr(ctx, arr, time.Now())
	if err != nil || updated.APIKey != "" || updated.APIKeyFile != "/run/secrets/sonarr" {
		t.Fatalf("UpdateArr = %+v, %v", updated, err)
	}
	arr.Name = "ghost"
	if _, err := state.UpdateArr(ctx, arr, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a missing arr = %v, want ErrNotFound", err)
	}
	if _, err := state.CreateArr(ctx, config.ArrConfig{Name: "x", Type: "plex", BaseURL: "http://x"}, time.Now()); err == nil {
		t.Fatal("an unknown arr type was stored")
	}
}

func TestSaveAttemptProfile(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	if _, err := state.CreateLibrary(ctx, testLibrary("movies", defaultFlowID(t, state)), time.Now()); err != nil {
		t.Fatal(err)
	}
	seedLibraryFiles(t, state, "movies")
	now := time.Now()
	job, err := state.LeaseNextJob(ctx, "w", now.Add(time.Hour), now)
	if err != nil || job == nil {
		t.Fatalf("LeaseNextJob = %v, %v", job, err)
	}
	attempt, err := state.StartAttempt(ctx, job.ID, "w", []byte(`{}`), []byte(`{"id":1}`), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SaveAttemptProfile(ctx, attempt.ID, []byte(`{"Name":"flow"}`)); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.GetAttempt(ctx, attempt.ID)
	if err != nil || string(loaded.ResolvedProfile) != `{"Name":"flow"}` || string(loaded.ResolvedFlow) != `{"id":1}` {
		t.Fatalf("attempt = %+v, %v", loaded, err)
	}
	if err := state.SaveAttemptProfile(ctx, 9999, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing attempt = %v, want ErrNotFound", err)
	}
}
