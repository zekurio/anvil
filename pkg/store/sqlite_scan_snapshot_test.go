package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func scanStore(t testing.TB) *SQLiteStore {
	t.Helper()
	state, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	mustCreateLibrary(t, state, "test")
	return state
}

func scanToken(t testing.TB, s *SQLiteStore) ScanToken {
	t.Helper()
	token, err := s.BeginLibraryScan(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func applyEntries(t testing.TB, s *SQLiteStore, token ScanToken, paths []string, entries ...ScanEntry) ApplyScanResult {
	t.Helper()
	result, err := s.ApplyLibraryScan(context.Background(), token, ApplyScanInput{LibraryName: "test", SourcePaths: paths, RequeueExisting: true, Entries: entries, CompletedAt: time.Unix(token.Sequence, 0)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func pathEntry(path string, size int64) ScanEntry {
	e := scanTestEntry(size)
	e.SourceRelativePath = path
	e.AssetRelativePath = path
	return e
}

func TestPartialScanOrdering(t *testing.T) {
	s := scanStore(t)
	applyEntries(t, s, scanToken(t, s), nil, pathEntry("a", 1), pathEntry("b", 1))
	olderFull, olderA, newerB := scanToken(t, s), scanToken(t, s), scanToken(t, s)
	applyEntries(t, s, newerB, []string{"b"}, pathEntry("b", 2))
	applyEntries(t, s, olderA, []string{"a"}, pathEntry("a", 2))
	applyEntries(t, s, olderFull, nil, pathEntry("a", 1), pathEntry("b", 1), pathEntry("c", 1))
	for _, path := range []string{"a", "b"} {
		source, err := s.GetMediaSourceByPath(context.Background(), "test", path)
		if err != nil || source.Fingerprint.SizeBytes != 2 {
			t.Fatalf("%s: %+v, %v", path, source, err)
		}
	}
	oldC, removeB := scanToken(t, s), scanToken(t, s)
	applyEntries(t, s, removeB, []string{"b"})
	applyEntries(t, s, oldC, []string{"c"}, pathEntry("c", 2))
	source, err := s.GetMediaSourceByPath(context.Background(), "test", "a")
	if err != nil || source.Status == domain.MediaSourceMissing {
		t.Fatalf("partial removal affected a: %+v %v", source, err)
	}
	staleB := applyEntries(t, s, newerB, []string{"b"}, pathEntry("b", 3))
	if staleB.Sources != 0 {
		t.Fatalf("stale partial applied: %+v", staleB)
	}
	old := scanToken(t, s)
	applyEntries(t, s, scanToken(t, s), nil)
	if got := applyEntries(t, s, old, []string{"a"}, pathEntry("a", 3)); got.Applied {
		t.Fatal("older partial superseded full scan")
	}
}

func TestUnchangedScanPreservesOccurrences(t *testing.T) {
	s := scanStore(t)
	first := applyEntries(t, s, scanToken(t, s), nil, pathEntry("a", 1))
	second := applyEntries(t, s, scanToken(t, s), nil, pathEntry("a", 1))
	if first.EnqueuedJobs != 1 || second.ExistingJobs != 1 || second.EnqueuedJobs != 0 {
		t.Fatalf("results: %+v %+v", first, second)
	}
	source, err := s.GetMediaSourceByPath(context.Background(), "test", "a")
	if err != nil {
		t.Fatal(err)
	}
	if source.Generation != 1 || !source.FirstSeenAt.Equal(time.Unix(1, 0)) || !source.LastSeenAt.Equal(time.Unix(2, 0)) || source.Status != domain.MediaSourceActive {
		t.Fatalf("source changed: %+v", source)
	}
	select {
	case <-s.WorkAvailable():
	default:
		t.Fatal("missing queue signal")
	}
	select {
	case <-s.WorkAvailable():
		t.Fatal("queue signal was not coalesced")
	default:
	}
}

func largeScanEntries(n int, packages bool) []ScanEntry {
	entries := make([]ScanEntry, n)
	for i := range entries {
		entries[i] = pathEntry(fmt.Sprintf("%05d.mkv", i), 1)
		if packages {
			entries[i].SourceKind = domain.SourceKindPackage
			entries[i].SourceRelativePath = "package"
		}
	}
	return entries
}

func TestLargeScanHasNoSQLVariableLimit(t *testing.T) {
	for _, packages := range []bool{false, true} {
		t.Run(fmt.Sprint(packages), func(t *testing.T) {
			s := scanStore(t)
			entries := largeScanEntries(33000, packages)
			applyEntries(t, s, scanToken(t, s), nil, entries...)
			result := applyEntries(t, s, scanToken(t, s), nil, entries...)
			if result.Assets != len(entries) || result.ExistingJobs != len(entries) {
				t.Fatalf("result: %+v", result)
			}
			applyEntries(t, s, scanToken(t, s), nil, entries[:len(entries)-1]...)
			var current int
			if err := s.db.QueryRow(`SELECT count(*) FROM media_assets WHERE is_current = 1`).Scan(&current); err != nil {
				t.Fatal(err)
			}
			if current != len(entries)-1 {
				t.Fatalf("current assets %d", current)
			}
		})
	}
}

func BenchmarkUnchangedLibraryScan(b *testing.B) {
	s := scanStore(b)
	entries := largeScanEntries(10000, false)
	applyEntries(b, s, scanToken(b, s), nil, entries...)
	b.ResetTimer()
	for b.Loop() {
		applyEntries(b, s, scanToken(b, s), nil, entries...)
	}
}

func TestForceSupersedesPendingScan(t *testing.T) {
	s := scanStore(t)
	old := scanToken(t, s)
	_, err := s.ForceOccurrence(context.Background(), ForceOccurrenceInput{LibraryName: "test", SourceKind: domain.SourceKindFile, SourceRelativePath: "a", AssetRelativePath: "a", SourceFingerprint: domain.FileFingerprint{SizeBytes: 2}, AssetFingerprint: domain.FileFingerprint{SizeBytes: 2}})
	if err != nil {
		t.Fatal(err)
	}
	applyEntries(t, s, old, nil, pathEntry("a", 1))
	source, err := s.GetMediaSourceByPath(context.Background(), "test", "a")
	if err != nil || source.Fingerprint.SizeBytes != 2 {
		t.Fatalf("force overwritten: %+v %v", source, err)
	}
}

func BenchmarkOnePathLibraryScan(b *testing.B) {
	s := scanStore(b)
	entries := largeScanEntries(33000, false)
	applyEntries(b, s, scanToken(b, s), nil, entries...)
	entry := entries[len(entries)/2]
	b.ResetTimer()
	for b.Loop() {
		applyEntries(b, s, scanToken(b, s), []string{entry.SourceRelativePath}, entry)
	}
}

func TestScopedScanUsesPathIndex(t *testing.T) {
	s := scanStore(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT `+mediaSourceColumns+` FROM media_sources WHERE is_current = 1 AND library_name = ? AND relative_path IN (SELECT value FROM json_each(?))`, "test", `["a"]`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	found := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
		if strings.Contains(detail, "library_name=? AND relative_path=?") {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("scoped query does not seek by source path")
	}
}

func TestUpgradeSchemaSixToSeven(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Version 7 adds only source_scans. Remove that addition to build the
	// previous schema, including its existing full-scan watermark.
	start := strings.Index(schemaVersion7, "CREATE TABLE source_scans (")
	end := start + strings.Index(schemaVersion7[start:], "CREATE TABLE media_sources (")
	schemaSix := schemaVersion7[:start] + schemaVersion7[end:]
	_, setupErr := db.ExecContext(ctx, schemaSix+`INSERT INTO schema_migrations VALUES (6, '2026-09-05T00:00:00Z'); INSERT INTO library_scans VALUES ('test', 4, 3, NULL);`)
	closeErr := db.Close()
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	var version int
	if err := s.db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	mustCreateLibrary(t, s, "test")
	if version != currentSchemaVersion {
		t.Fatalf("schema version %d", version)
	}
	if result := applyEntries(t, s, ScanToken{LibraryName: "test", Sequence: 2}, []string{"a"}, pathEntry("a", 1)); result.Applied {
		t.Fatal("upgrade lost full watermark")
	}
	applyEntries(t, s, scanToken(t, s), []string{"a"}, pathEntry("a", 1))
}

func TestRetryTransitionSignalsWork(t *testing.T) {
	s := scanStore(t)
	ctx := context.Background()
	applyEntries(t, s, scanToken(t, s), nil, pathEntry("a", 1))
	select {
	case <-s.WorkAvailable():
	default:
		t.Fatal("missing initial signal")
	}
	now := time.Now()
	job, err := s.LeaseNextJob(ctx, "worker", now.Add(time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil {
		t.Fatal("missing job")
	}
	if _, err := s.TransitionJob(ctx, job.ID, domain.JobStateRetrying, now, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.WorkAvailable():
		t.Fatal("retrying job is not runnable")
	default:
	}
	if _, err := s.TransitionJob(ctx, job.ID, domain.JobStatePending, now, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.WorkAvailable():
	default:
		t.Fatal("missing retry signal")
	}
}

// A rule change reaches files the library already tracks: the next scan
// reports them as denied, and a job that was queued while the rules still
// allowed its file must not run. Only a job that never started is touched.
func TestScanSkipsQueuedJobsOfDeniedFiles(t *testing.T) {
	ctx := context.Background()
	s := scanStore(t)
	first := applyEntries(t, s, scanToken(t, s), nil, pathEntry("waiting.mkv", 1), pathEntry("started.mkv", 1), pathEntry("allowed.mkv", 1), pathEntry("unstable.mkv", 1))
	if first.EnqueuedJobs != 4 || first.DeniedJobs != 0 {
		t.Fatalf("first scan = %+v, want four queued jobs", first)
	}
	jobOf := func(path string) domain.Job {
		t.Helper()
		source, err := s.GetMediaSourceByPath(ctx, "test", path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		jobs, err := s.ListJobs(ctx, JobListFilter{LibraryName: "test"})
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range jobs {
			if job.Job.SourceID == source.ID {
				return job.Job
			}
		}
		t.Fatalf("%s has no job", path)
		return domain.Job{}
	}
	// One job has an attempt behind it and waits in the queue again.
	now := time.Unix(100, 0).UTC()
	started := jobOf("started.mkv")
	if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET attempt_count = 1 WHERE id = ?`, int64(started.ID)); err != nil {
		t.Fatal(err)
	}

	denied := func(path string) ScanEntry {
		entry := pathEntry(path, 1)
		entry.Persist, entry.Enqueue = false, false
		entry.DeniedReason = `denied by rule 1 (size lt "5 MB")`
		return entry
	}
	// A download that is still being written is not persisted either, and
	// must not be mistaken for a denied file.
	unstable := pathEntry("unstable.mkv", 1)
	unstable.Persist, unstable.Enqueue = false, false
	second, err := s.ApplyLibraryScan(ctx, scanToken(t, s), ApplyScanInput{
		LibraryName: "test", RequeueExisting: true, CompletedAt: now,
		Entries: []ScanEntry{denied("waiting.mkv"), denied("started.mkv"), denied("never-seen.mkv"), pathEntry("allowed.mkv", 1), unstable},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.DeniedJobs != 1 {
		t.Fatalf("second scan = %+v, want exactly one job skipped", second)
	}
	waiting := jobOf("waiting.mkv")
	if waiting.State != domain.JobStateSkipped || waiting.LastError != `denied by rule 1 (size lt "5 MB")` || waiting.CompletedAt == nil {
		t.Fatalf("job of the denied file = %+v, want it skipped with the rule as its reason", waiting)
	}
	for _, path := range []string{"started.mkv", "allowed.mkv", "unstable.mkv"} {
		if job := jobOf(path); job.State != domain.JobStatePending {
			t.Errorf("job of %s = %s, want it left pending", path, job.State)
		}
	}
	// The denied file keeps its occurrence, so it stays listed with its reason
	// and its job can be retried once the rules allow the file again.
	if source, err := s.GetMediaSourceByPath(ctx, "test", "waiting.mkv"); err != nil || !source.Current || source.Status != domain.MediaSourceActive {
		t.Fatalf("occurrence of the denied file = %+v, %v", source, err)
	}
	if _, err := s.GetMediaSourceByPath(ctx, "test", "never-seen.mkv"); err == nil {
		t.Fatal("a denied file the library never tracked was recorded")
	}
	if _, err := s.RetryJob(ctx, waiting.ID, now); err != nil {
		t.Fatalf("retry of the skipped job: %v", err)
	}
	// A repeated scan skips it again: it never started.
	third, err := s.ApplyLibraryScan(ctx, scanToken(t, s), ApplyScanInput{
		LibraryName: "test", RequeueExisting: true, CompletedAt: now,
		Entries: []ScanEntry{denied("waiting.mkv"), denied("started.mkv"), pathEntry("allowed.mkv", 1), unstable},
	})
	if err != nil || third.DeniedJobs != 1 {
		t.Fatalf("third scan = %+v, %v", third, err)
	}
}
