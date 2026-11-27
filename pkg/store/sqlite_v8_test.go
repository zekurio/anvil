package store

import (
	"context"
	"database/sql"
	"errors"
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
		`INSERT INTO jobs (id, slug, source_id, asset_id, library_name, state, created_at, updated_at)
			VALUES (1, 'quiet-anvil', 1, 1, 'movies', 'pending', '` + now + `', '` + now + `')`,
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
	if overview.Files != 1 || overview.TotalBytes != 1000 || overview.Jobs[domain.JobStatePending] != 1 || overview.LastScanAt == nil {
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
		if err := state.DeleteLibrary(ctx, name); err != nil {
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
	probe := domain.JobPipelineContext{Probe: &domain.ProbeResult{FormatName: "matroska,webm"}}
	if err := state.SaveJobPipelineContext(ctx, jobs["A/alpha.mkv"].ID, probe, now); err != nil {
		t.Fatal(err)
	}

	all, total, err := state.ListLibraryFiles(ctx, LibraryFileFilter{LibraryName: "movies"})
	if err != nil || total != 3 || filePaths(all) != "A/alpha.mkv B/beta_50%.mkv gamma.mkv" {
		t.Fatalf("all = %q total %d, %v", filePaths(all), total, err)
	}
	if all[0].Probe == nil || all[0].Probe.FormatName != "matroska,webm" || all[1].Probe != nil {
		t.Fatalf("probes = %+v, %+v", all[0].Probe, all[1].Probe)
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

	if err := state.DeleteLibrary(ctx, "movies"); !errors.Is(err, ErrActiveWork) || !strings.Contains(err.Error(), "3 queued or running") {
		t.Fatalf("delete with pending jobs = %v", err)
	}
	ids := make([]domain.JobID, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.ID)
	}
	if _, err := state.CancelJobs(ctx, CancelJobsInput{IDs: ids, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}

	// A terminal job that still owns an unfinished publish names files on
	// disk only its own recovery can resolve.
	journaled := jobs["gamma.mkv"].ID
	if _, err := state.db.ExecContext(ctx, `
INSERT INTO publish_operations (job_id, stage, operation_json, created_at, updated_at) VALUES (?, ?, '{}', ?, ?)
`, int64(journaled), string(replacepkg.PublishStagePublished), encodeTime(time.Now()), encodeTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := state.DeleteLibrary(ctx, "movies"); !errors.Is(err, ErrActiveWork) || !strings.Contains(err.Error(), "unfinished publish") {
		t.Fatalf("delete with a publish journal = %v", err)
	}
	if _, err := state.db.ExecContext(ctx, `UPDATE publish_operations SET stage = 'committed' WHERE job_id = ?`, int64(journaled)); err != nil {
		t.Fatal(err)
	}

	if err := state.DeleteLibrary(ctx, "movies"); err != nil {
		t.Fatalf("DeleteLibrary: %v", err)
	}
	for table, column := range map[string]string{
		"jobs": "library_name", "media_sources": "library_name", "library_scans": "library_name",
		"source_scans": "library_name", "libraries": "name",
	} {
		var remaining int
		if err := state.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE `+column+` = 'movies'`).Scan(&remaining); err != nil || remaining != 0 {
			t.Errorf("%s still has %d rows of the deleted library, %v", table, remaining, err)
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
	// The other library is untouched.
	if files, total, err := state.ListLibraryFiles(ctx, LibraryFileFilter{LibraryName: "shows"}); err != nil || total != 3 || len(files) != 3 {
		t.Fatalf("other library after delete: %d files, total %d, %v", len(files), total, err)
	}
	if err := state.DeleteLibrary(ctx, "movies"); !errors.Is(err, ErrNotFound) {
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
