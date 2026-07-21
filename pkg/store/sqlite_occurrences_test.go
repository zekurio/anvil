package store

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestSourceOccurrenceDisappearanceAndIdenticalReappearance(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	entry := occurrenceFileEntry("Movie.mkv", 100, now)

	first := applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{entry}, now)
	if first.EnqueuedJobs != 1 {
		t.Fatalf("first enqueued jobs = %d, want 1", first.EnqueuedJobs)
	}
	original, _, err := state.FindMediaSourceByPath(ctx, "downloads", "Movie.mkv")
	if err != nil {
		t.Fatalf("find original source: %v", err)
	}

	applyOccurrenceScan(t, ctx, state, "downloads", nil, now.Add(time.Minute))
	missing, _, err := state.FindMediaSourceByPath(ctx, "downloads", "Movie.mkv")
	if err != nil {
		t.Fatalf("find missing source: %v", err)
	}
	if missing.Current || missing.Status != domain.MediaSourceMissing {
		t.Fatalf("missing source = %+v, want non-current missing", missing)
	}

	reappeared := applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{entry}, now.Add(2*time.Minute))
	if reappeared.EnqueuedJobs != 1 {
		t.Fatalf("reappeared enqueued jobs = %d, want 1", reappeared.EnqueuedJobs)
	}
	current, _, err := state.FindMediaSourceByPath(ctx, "downloads", "Movie.mkv")
	if err != nil {
		t.Fatalf("find reappeared source: %v", err)
	}
	if current.ID == original.ID || current.Generation != 2 || !current.Current {
		t.Fatalf("reappeared source = %+v, want distinct current generation 2", current)
	}
}

func TestCompletedRetainedOccurrenceDoesNotLoop(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	entry := occurrenceFileEntry("Movie.mkv", 100, now)
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{entry}, now)
	job, attempt := leaseAndStartAttempt(t, ctx, state, now.Add(time.Second), "worker-retained")
	if _, err := state.TransitionJob(ctx, job.ID, domain.JobStateValidating, now.Add(2*time.Second), ""); err != nil {
		t.Fatalf("transition validating: %v", err)
	}
	finalFingerprint := domain.FileFingerprint{SizeBytes: 70, ModTime: now.Add(2 * time.Second)}
	if _, err := state.CompleteJobOccurrence(ctx, CompleteJobOccurrenceInput{
		JobID: job.ID, AttemptID: attempt.ID, InputSizeBytes: 100, OutputSizeBytes: 70,
		FinalInputFingerprint: &finalFingerprint, CompletedAt: now.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("CompleteJobOccurrence() error = %v", err)
	}
	completedJob, err := state.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	completedAttempt, err := state.GetAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completedJob.State != domain.JobStateComplete || completedJob.InputSizeBytes != 100 || completedJob.OutputSizeBytes != 70 || completedAttempt.State != domain.AttemptStateSucceeded {
		t.Fatalf("transactional completion job=%+v attempt=%+v", completedJob, completedAttempt)
	}

	retained := occurrenceFileEntry("Movie.mkv", finalFingerprint.SizeBytes, finalFingerprint.ModTime)
	rescanned := applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{retained}, now.Add(time.Minute))
	if rescanned.Sources != 1 || rescanned.Assets != 1 || rescanned.EnqueuedJobs != 0 || rescanned.ExistingJobs != 0 {
		t.Fatalf("retained rescan = %+v, want one observed source and asset with no work", rescanned)
	}
	source := mustFindOccurrenceSource(t, ctx, state, "downloads", "Movie.mkv")
	asset := mustFindOccurrenceAsset(t, ctx, state, source.ID, "Movie.mkv")
	if source.Status != domain.MediaSourceProcessed || asset.Status != domain.MediaAssetProcessed {
		t.Fatalf("completed lifecycle source=%q asset=%q, want processed", source.Status, asset.Status)
	}
}

func TestCompletionWithSourceCleanupEndsCurrentFileOccurrence(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	entry := occurrenceFileEntry("Movie.mkv", 100, now)
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{entry}, now)
	job, attempt := leaseAndStartAttempt(t, ctx, state, now.Add(time.Second), "worker-cleanup")
	if _, err := state.TransitionJob(ctx, job.ID, domain.JobStateValidating, now.Add(2*time.Second), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := state.CompleteJobOccurrence(ctx, CompleteJobOccurrenceInput{
		JobID: job.ID, AttemptID: attempt.ID, SourceMediaRemoved: true, CompletedAt: now.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("CompleteJobOccurrence() error = %v", err)
	}
	missing := mustFindOccurrenceSource(t, ctx, state, "downloads", "Movie.mkv")
	if missing.Current || missing.Status != domain.MediaSourceMissing {
		t.Fatalf("cleaned source = %+v, want missing non-current occurrence", missing)
	}
	result := applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{entry}, now.Add(time.Minute))
	current := mustFindOccurrenceSource(t, ctx, state, "downloads", "Movie.mkv")
	if result.EnqueuedJobs != 1 || current.Generation != 2 {
		t.Fatalf("cleanup reappearance result=%+v source=%+v, want generation 2 job", result, current)
	}
}

func TestInPlaceReplacementCreatesNewFileOccurrence(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{occurrenceFileEntry("Movie.mkv", 100, now)}, now)
	oldSource := mustFindOccurrenceSource(t, ctx, state, "downloads", "Movie.mkv")

	changed := occurrenceFileEntry("Movie.mkv", 125, now.Add(time.Minute))
	result := applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{changed}, now.Add(time.Minute))
	if result.Sources != 1 || result.Assets != 1 || result.EnqueuedJobs != 1 {
		t.Fatalf("replacement result = %+v, want one new source, asset, and job", result)
	}
	newSource := mustFindOccurrenceSource(t, ctx, state, "downloads", "Movie.mkv")
	oldSource, err := state.GetMediaSource(ctx, oldSource.ID)
	if err != nil {
		t.Fatalf("get old source: %v", err)
	}
	if newSource.Generation != 2 || newSource.ID == oldSource.ID || oldSource.Current || oldSource.Status != domain.MediaSourceMissing {
		t.Fatalf("old=%+v new=%+v, want retired generation 1 and current generation 2", oldSource, newSource)
	}
	if pending := countOccurrenceJobsInState(t, state, domain.JobStatePending); pending != 1 {
		t.Fatalf("pending jobs = %d, want only the new occurrence", pending)
	}
}

func TestPackageChangesOnlyOneAssetOccurrence(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	initial := []ScanEntry{
		occurrencePackageEntry("Release", "A.mkv", 100, 200, now),
		occurrencePackageEntry("Release", "B.mkv", 100, 200, now),
	}
	applyOccurrenceScan(t, ctx, state, "downloads", initial, now)
	source := mustFindOccurrenceSource(t, ctx, state, "downloads", "Release")
	a := mustFindOccurrenceAsset(t, ctx, state, source.ID, "A.mkv")
	b := mustFindOccurrenceAsset(t, ctx, state, source.ID, "B.mkv")

	changed := []ScanEntry{
		occurrencePackageEntry("Release", "A.mkv", 100, 225, now),
		occurrencePackageEntry("Release", "B.mkv", 125, 225, now.Add(time.Minute)),
	}
	result := applyOccurrenceScan(t, ctx, state, "downloads", changed, now.Add(time.Minute))
	if result.Sources != 1 || result.Assets != 2 || result.EnqueuedJobs != 1 || result.ExistingJobs != 1 {
		t.Fatalf("package change result = %+v, want one observed source, two observed assets, one changed asset job, and one retained job", result)
	}
	currentSource := mustFindOccurrenceSource(t, ctx, state, "downloads", "Release")
	currentA := mustFindOccurrenceAsset(t, ctx, state, source.ID, "A.mkv")
	currentB := mustFindOccurrenceAsset(t, ctx, state, source.ID, "B.mkv")
	if currentSource.ID != source.ID || currentSource.Generation != 1 {
		t.Fatalf("source occurrence changed = %+v, want original package occurrence", currentSource)
	}
	if currentA.ID != a.ID || currentA.Generation != 1 {
		t.Fatalf("unchanged asset = %+v, want original", currentA)
	}
	if currentB.ID == b.ID || currentB.Generation != 2 {
		t.Fatalf("changed asset = %+v, want generation 2", currentB)
	}
	if pending := countOccurrenceJobsInState(t, state, domain.JobStatePending); pending != 2 {
		t.Fatalf("pending jobs = %d, want unchanged A and new B only", pending)
	}
}

func TestMissingPackageAssetReappearsAsNewGeneration(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	a := occurrencePackageEntry("Release", "A.mkv", 100, 200, now)
	b := occurrencePackageEntry("Release", "B.mkv", 100, 200, now)
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{a, b}, now)
	source := mustFindOccurrenceSource(t, ctx, state, "downloads", "Release")
	originalB := mustFindOccurrenceAsset(t, ctx, state, source.ID, "B.mkv")
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{a}, now.Add(time.Minute))
	missingB := mustFindOccurrenceAsset(t, ctx, state, source.ID, "B.mkv")
	if missingB.Current || missingB.Status != domain.MediaAssetMissing {
		t.Fatalf("missing B = %+v, want missing non-current", missingB)
	}
	result := applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{a, b}, now.Add(2*time.Minute))
	currentB := mustFindOccurrenceAsset(t, ctx, state, source.ID, "B.mkv")
	if result.Sources != 1 || result.Assets != 2 || result.EnqueuedJobs != 1 || currentB.ID == originalB.ID || currentB.Generation != 2 {
		t.Fatalf("reappeared B result=%+v asset=%+v, want generation 2", result, currentB)
	}
}

func TestFailedOccurrenceRequiresExplicitRetry(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	entry := occurrenceFileEntry("Movie.mkv", 100, now)
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{entry}, now)
	job, err := state.LeaseNextJob(ctx, "worker-failed", now.Add(time.Minute), now)
	if err != nil || job == nil {
		t.Fatalf("lease job = %+v, err %v", job, err)
	}
	if _, err := state.TransitionJob(ctx, job.ID, domain.JobStateRunning, now, ""); err != nil {
		t.Fatalf("transition running: %v", err)
	}
	if _, err := state.TransitionJob(ctx, job.ID, domain.JobStateFailed, now.Add(time.Second), "encode failed"); err != nil {
		t.Fatalf("transition failed: %v", err)
	}

	result := applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{entry}, now.Add(time.Minute))
	if result.Sources != 1 || result.Assets != 1 || result.EnqueuedJobs != 0 || result.ExistingJobs != 1 {
		t.Fatalf("failed occurrence rescan = %+v, want one observed source and asset with an existing failed target", result)
	}
	stored, err := state.GetJob(ctx, job.ID)
	if err != nil || stored.State != domain.JobStateFailed {
		t.Fatalf("stored job = %+v, err %v, want failed", stored, err)
	}
	retried, err := state.RetryJob(ctx, job.ID, now.Add(2*time.Minute))
	if err != nil || retried.State != domain.JobStatePending {
		t.Fatalf("RetryJob() = %+v, err %v, want pending for active occurrence", retried, err)
	}
}

func TestRetryJobRefusesRetiredOccurrence(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	entry := occurrenceFileEntry("Movie.mkv", 100, now)
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{entry}, now)
	job, err := state.LeaseNextJob(ctx, "worker-retired-retry", now.Add(time.Minute), now)
	if err != nil || job == nil {
		t.Fatalf("LeaseNextJob() = %+v, err %v", job, err)
	}
	if _, err := state.TransitionJob(ctx, job.ID, domain.JobStateRunning, now, ""); err != nil {
		t.Fatalf("transition running: %v", err)
	}
	if _, err := state.TransitionJob(ctx, job.ID, domain.JobStateFailed, now.Add(time.Second), "encode failed"); err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	applyOccurrenceScan(t, ctx, state, "downloads", nil, now.Add(2*time.Minute))

	if _, err := state.RetryJob(ctx, job.ID, now.Add(3*time.Minute)); err == nil {
		t.Fatal("RetryJob() error = nil, want retired occurrence refusal")
	}
	stored, err := state.GetJob(ctx, job.ID)
	if err != nil || stored.State != domain.JobStateFailed {
		t.Fatalf("stored job = %+v, err %v, want failed", stored, err)
	}
}

func TestRetryFailedJobsExcludesRetiredOccurrences(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	a := occurrenceFileEntry("A.mkv", 100, now)
	b := occurrenceFileEntry("B.mkv", 100, now)
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{a, b}, now)
	sourceA := mustFindOccurrenceSource(t, ctx, state, "downloads", "A.mkv")
	assetA := mustFindOccurrenceAsset(t, ctx, state, sourceA.ID, "A.mkv")
	jobA, ok, err := state.FindJobForTarget(ctx, sourceA.ID, assetA.ID)
	if err != nil || !ok {
		t.Fatalf("find A job = %+v, ok=%t err=%v", jobA, ok, err)
	}
	sourceB := mustFindOccurrenceSource(t, ctx, state, "downloads", "B.mkv")
	assetB := mustFindOccurrenceAsset(t, ctx, state, sourceB.ID, "B.mkv")
	jobB, ok, err := state.FindJobForTarget(ctx, sourceB.ID, assetB.ID)
	if err != nil || !ok {
		t.Fatalf("find B job = %+v, ok=%t err=%v", jobB, ok, err)
	}
	for i := 0; i < 2; i++ {
		leased, err := state.LeaseNextJob(ctx, "worker-bulk-retry", now.Add(time.Minute), now)
		if err != nil || leased == nil {
			t.Fatalf("LeaseNextJob(%d) = %+v, err %v", i, leased, err)
		}
		if _, err := state.TransitionJob(ctx, leased.ID, domain.JobStateRunning, now, ""); err != nil {
			t.Fatalf("transition running %d: %v", leased.ID, err)
		}
		if _, err := state.TransitionJob(ctx, leased.ID, domain.JobStateFailed, now.Add(time.Second), "encode failed"); err != nil {
			t.Fatalf("transition failed %d: %v", leased.ID, err)
		}
	}
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{b}, now.Add(2*time.Minute))

	retried, err := state.RetryFailedJobs(ctx, "downloads", now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("RetryFailedJobs() error = %v", err)
	}
	if retried != 1 {
		t.Fatalf("retried = %d, want 1 active occurrence", retried)
	}
	jobA, err = state.GetJob(ctx, jobA.ID)
	if err != nil || jobA.State != domain.JobStateFailed {
		t.Fatalf("retired A job = %+v, err %v, want failed", jobA, err)
	}
	jobB, err = state.GetJob(ctx, jobB.ID)
	if err != nil || jobB.State != domain.JobStatePending {
		t.Fatalf("active B job = %+v, err %v, want pending", jobB, err)
	}
}

func TestReleaseLeasedJobSkipsRetiredOccurrence(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{occurrenceFileEntry("Movie.mkv", 100, now)}, now)
	job, err := state.LeaseNextJob(ctx, "worker-release", now.Add(time.Minute), now)
	if err != nil || job == nil {
		t.Fatalf("LeaseNextJob() = %+v, err %v", job, err)
	}
	applyOccurrenceScan(t, ctx, state, "downloads", nil, now.Add(30*time.Second))

	released, err := state.ReleaseLeasedJob(ctx, job.ID, "worker-release", now.Add(45*time.Second))
	if err != nil {
		t.Fatalf("ReleaseLeasedJob() error = %v", err)
	}
	if released.State != domain.JobStateSkipped || released.LastError != inactiveOccurrenceJobError || released.CompletedAt == nil {
		t.Fatalf("released job = %+v, want terminal skipped inactive occurrence", released)
	}
}

func TestTransitionToPendingSkipsRetiredOccurrence(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{occurrenceFileEntry("Movie.mkv", 100, now)}, now)
	job, err := state.LeaseNextJob(ctx, "worker-transition", now.Add(time.Minute), now)
	if err != nil || job == nil {
		t.Fatalf("LeaseNextJob() = %+v, err %v", job, err)
	}
	if _, err := state.TransitionJob(ctx, job.ID, domain.JobStateRunning, now, ""); err != nil {
		t.Fatalf("transition running: %v", err)
	}
	applyOccurrenceScan(t, ctx, state, "downloads", nil, now.Add(30*time.Second))
	if _, err := state.TransitionJob(ctx, job.ID, domain.JobStateRetrying, now.Add(40*time.Second), "encode failed"); err != nil {
		t.Fatalf("transition retrying: %v", err)
	}

	transitioned, err := state.TransitionJob(ctx, job.ID, domain.JobStatePending, now.Add(45*time.Second), "retry pending")
	if err != nil {
		t.Fatalf("TransitionJob(pending) error = %v", err)
	}
	if transitioned.State != domain.JobStateSkipped || transitioned.LastError != inactiveOccurrenceJobError || transitioned.CompletedAt == nil {
		t.Fatalf("transitioned job = %+v, want terminal skipped inactive occurrence", transitioned)
	}
}

func TestRecoverStaleJobSkipsRetiredOccurrence(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{occurrenceFileEntry("Movie.mkv", 100, now)}, now)
	job, attempt := leaseAndStartAttempt(t, ctx, state, now, "worker-recovery")
	applyOccurrenceScan(t, ctx, state, "downloads", nil, now.Add(30*time.Second))

	recovered, err := state.RecoverStaleJobs(ctx, 3, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("RecoverStaleJobs() error = %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1 skipped job", recovered)
	}
	job, err = state.GetJob(ctx, job.ID)
	if err != nil || job.State != domain.JobStateSkipped || job.LastError != inactiveOccurrenceJobError || job.CompletedAt == nil {
		t.Fatalf("recovered job = %+v, err %v, want terminal skipped inactive occurrence", job, err)
	}
	attempt, err = state.GetAttempt(ctx, attempt.ID)
	if err != nil || attempt.State != domain.AttemptStateCanceled {
		t.Fatalf("recovered attempt = %+v, err %v, want canceled", attempt, err)
	}
}

func TestUnstableEntryIsSeenWithoutPersistence(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	unstable := occurrenceFileEntry("Movie.mkv", 100, now)
	unstable.Persist = false
	unstable.Enqueue = false
	result := applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{unstable}, now)
	if result.Sources != 0 || result.Assets != 0 || result.EnqueuedJobs != 0 {
		t.Fatalf("new unstable result = %+v, want no persistence", result)
	}
	if _, ok, err := state.FindMediaSourceByPath(ctx, "downloads", "Movie.mkv"); err != nil || ok {
		t.Fatalf("unstable source exists=%t err=%v, want absent", ok, err)
	}

	stable := occurrenceFileEntry("Movie.mkv", 100, now)
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{stable}, now.Add(time.Minute))
	unstableResult := applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{unstable}, now.Add(2*time.Minute))
	if unstableResult.Sources != 0 || unstableResult.Assets != 0 || unstableResult.EnqueuedJobs != 0 || unstableResult.ExistingJobs != 0 {
		t.Fatalf("existing unstable result = %+v, want zero persisted counts and work", unstableResult)
	}
	source, ok, err := state.FindMediaSourceByPath(ctx, "downloads", "Movie.mkv")
	if err != nil || !ok || !source.Current || source.Status != domain.MediaSourceActive {
		t.Fatalf("existing source after unstable scan = %+v, ok=%t err=%v", source, ok, err)
	}
}

func TestOlderCompletedScanCannotOverwriteNewerScan(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	older, err := state.BeginLibraryScan(ctx, "downloads")
	if err != nil {
		t.Fatalf("begin older scan: %v", err)
	}
	newer, err := state.BeginLibraryScan(ctx, "downloads")
	if err != nil {
		t.Fatalf("begin newer scan: %v", err)
	}
	newerResult, err := state.ApplyLibraryScan(ctx, newer, ApplyScanInput{LibraryName: "downloads", Entries: []ScanEntry{occurrenceFileEntry("New.mkv", 100, now)}, CompletedAt: now})
	if err != nil || !newerResult.Applied {
		t.Fatalf("apply newer scan = %+v, err %v", newerResult, err)
	}
	olderResult, err := state.ApplyLibraryScan(ctx, older, ApplyScanInput{LibraryName: "downloads", Entries: []ScanEntry{occurrenceFileEntry("Old.mkv", 100, now)}, CompletedAt: now.Add(time.Second)})
	if err != nil || olderResult.Applied {
		t.Fatalf("apply older scan = %+v, err %v, want superseded", olderResult, err)
	}
	if _, ok, err := state.FindMediaSourceByPath(ctx, "downloads", "Old.mkv"); err != nil {
		t.Fatalf("find older scan source: %v", err)
	} else if ok {
		t.Fatal("older scan source was persisted")
	}
	if source, ok, err := state.FindMediaSourceByPath(ctx, "downloads", "New.mkv"); err != nil {
		t.Fatalf("find newer scan source: %v", err)
	} else if !ok || !source.Current {
		t.Fatalf("newer scan source = %+v, ok=%t", source, ok)
	}
}

func TestConcurrentIdenticalScansDeduplicateOccurrenceAndJob(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	const scans = 8
	var wg sync.WaitGroup
	errs := make(chan error, scans)
	for range scans {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := state.BeginLibraryScan(ctx, "downloads")
			if err != nil {
				errs <- err
				return
			}
			_, err = state.ApplyLibraryScan(ctx, token, ApplyScanInput{LibraryName: "downloads", Entries: []ScanEntry{occurrenceFileEntry("Movie.mkv", 100, now)}, CompletedAt: now})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent scan error = %v", err)
		}
	}
	var sources, assets, jobs int
	if err := state.db.QueryRow(`SELECT count(*) FROM media_sources`).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if err := state.db.QueryRow(`SELECT count(*) FROM media_assets`).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err := state.db.QueryRow(`SELECT count(*) FROM jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if sources != 1 || assets != 1 || jobs != 1 {
		t.Fatalf("counts sources/assets/jobs = %d/%d/%d, want 1/1/1", sources, assets, jobs)
	}
}

func TestForceOccurrenceRefusesActiveWorkAndAdvancesGeneration(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t)
	now := testNow()
	entry := occurrenceFileEntry("Movie.mkv", 100, now)
	applyOccurrenceScan(t, ctx, state, "downloads", []ScanEntry{entry}, now)
	input := ForceOccurrenceInput{
		LibraryName: "downloads", SourceKind: domain.SourceKindFile, SourceRelativePath: "Movie.mkv", SourceFingerprint: entry.SourceFingerprint,
		AssetRelativePath: "Movie.mkv", AssetRole: entry.AssetRole, AssetFingerprint: entry.AssetFingerprint, Now: now.Add(time.Minute),
	}
	if _, err := state.ForceOccurrence(ctx, input); err == nil || !strings.Contains(err.Error(), "active work exists") {
		t.Fatalf("ForceOccurrence(active) error = %v, want active-work refusal", err)
	}
	job, err := state.LeaseNextJob(ctx, "worker-force", now.Add(time.Minute), now)
	if err != nil || job == nil {
		t.Fatalf("lease force predecessor = %+v, err %v", job, err)
	}
	if _, err := state.TransitionJob(ctx, job.ID, domain.JobStateRunning, now, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := state.TransitionJob(ctx, job.ID, domain.JobStateFailed, now, "failed"); err != nil {
		t.Fatal(err)
	}
	forced, err := state.ForceOccurrence(ctx, input)
	if err != nil {
		t.Fatalf("ForceOccurrence() error = %v", err)
	}
	if forced.Source.Generation != 2 || forced.Asset.Generation != 1 || forced.Job.State != domain.JobStatePending {
		t.Fatalf("forced occurrence = %+v, want source generation 2 with pending job", forced)
	}
}

func applyOccurrenceScan(t *testing.T, ctx context.Context, state *SQLiteStore, library domain.LibraryName, entries []ScanEntry, now time.Time) ApplyScanResult {
	t.Helper()
	token, err := state.BeginLibraryScan(ctx, library)
	if err != nil {
		t.Fatalf("BeginLibraryScan() error = %v", err)
	}
	result, err := state.ApplyLibraryScan(ctx, token, ApplyScanInput{LibraryName: library, Entries: entries, CompletedAt: now})
	if err != nil {
		t.Fatalf("ApplyLibraryScan() error = %v", err)
	}
	return result
}

func occurrenceFileEntry(path string, size int64, modTime time.Time) ScanEntry {
	fingerprint := domain.FileFingerprint{SizeBytes: size, ModTime: modTime}
	return ScanEntry{
		SourceKind: domain.SourceKindFile, SourceRelativePath: path, SourceFingerprint: fingerprint,
		AssetRelativePath: path, AssetRole: domain.MediaAssetRolePrimaryVideo, AssetFingerprint: fingerprint,
		Persist: true, Enqueue: true,
	}
}

func occurrencePackageEntry(sourcePath, assetPath string, assetSize, sourceSize int64, modTime time.Time) ScanEntry {
	return ScanEntry{
		SourceKind: domain.SourceKindPackage, SourceRelativePath: sourcePath,
		SourceFingerprint: domain.FileFingerprint{SizeBytes: sourceSize, ModTime: modTime},
		AssetRelativePath: assetPath, AssetRole: domain.MediaAssetRolePrimaryVideo,
		AssetFingerprint: domain.FileFingerprint{SizeBytes: assetSize, ModTime: modTime},
		Persist:          true, Enqueue: true,
	}
}

func countOccurrenceJobsInState(t *testing.T, state *SQLiteStore, jobState domain.JobState) int {
	t.Helper()
	var count int
	if err := state.db.QueryRow(`SELECT count(*) FROM jobs WHERE state = ?`, string(jobState)).Scan(&count); err != nil {
		t.Fatalf("count jobs in state %q: %v", jobState, err)
	}
	return count
}

func mustFindOccurrenceSource(t *testing.T, ctx context.Context, state *SQLiteStore, library domain.LibraryName, path string) domain.MediaSource {
	t.Helper()
	source, ok, err := state.FindMediaSourceByPath(ctx, library, path)
	if err != nil || !ok {
		t.Fatalf("find source %q = %+v, ok=%t err=%v", path, source, ok, err)
	}
	return source
}

func mustFindOccurrenceAsset(t *testing.T, ctx context.Context, state *SQLiteStore, sourceID domain.MediaSourceID, path string) domain.MediaAsset {
	t.Helper()
	asset, ok, err := state.FindMediaAssetByPath(ctx, sourceID, path)
	if err != nil || !ok {
		t.Fatalf("find asset %q = %+v, ok=%t err=%v", path, asset, ok, err)
	}
	return asset
}
