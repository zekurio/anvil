package controlapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/store"
)

// countingStore counts the per-job lookups a listing performs. Those lookups,
// not the job query itself, are what make a listing expensive.
type countingStore struct {
	Store
	sources int
}

func (c *countingStore) GetMediaSource(ctx context.Context, id domain.MediaSourceID) (domain.MediaSource, error) {
	c.sources++
	return c.Store.GetMediaSource(ctx, id)
}

// enqueueSiblingJobs adds jobs beside the fixture's, in the same library, so a
// listing has something to truncate.
func enqueueSiblingJobs(t *testing.T, ctx context.Context, state *store.SQLiteStore, paths ...string) {
	t.Helper()
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	for i, relative := range paths {
		if _, err := state.ForceOccurrence(ctx, store.ForceOccurrenceInput{
			LibraryName: "downloads", SourceKind: domain.SourceKindPackage,
			SourceRelativePath: relative, AssetRelativePath: "Season/Episode.mkv",
			AssetRole:         domain.MediaAssetRolePrimaryVideo,
			SourceFingerprint: domain.FileFingerprint{SizeBytes: 1, ModTime: now},
			AssetFingerprint:  domain.FileFingerprint{SizeBytes: 1, ModTime: now},
			Now:               now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("ForceOccurrence(%s) error = %v", relative, err)
		}
	}
}

// TestListJobsOnlyHydratesWhatItReturns keeps a browse from paying for the
// whole queue. A limited listing with no path selector is already answered by
// the job query, so the jobs past the limit are counted, not loaded.
func TestListJobsOnlyHydratesWhatItReturns(t *testing.T) {
	ctx := context.Background()
	service, state, _, _ := testService(t, ctx)
	enqueueSiblingJobs(t, ctx, state, "Second", "Third", "Fourth")
	counting := &countingStore{Store: service.Store}
	service.Store = counting

	response, err := service.ListJobs(ctx, control.JobQuery{Library: "downloads", Limit: 2})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if response.Matched != 4 || len(response.Jobs) != 2 || !response.Truncated {
		t.Fatalf("ListJobs() matched = %d, returned = %d, truncated = %t; want the full count and a truncated page",
			response.Matched, len(response.Jobs), response.Truncated)
	}
	if counting.sources != 2 {
		t.Fatalf("hydrated %d jobs for a page of 2; a limited listing must not load the rest", counting.sources)
	}

	// A path selector has to look at every job before it can decide, so it
	// still hydrates all of them — and still reports the true match count.
	counting.sources = 0
	filtered, err := service.ListJobs(ctx, control.JobQuery{Library: "downloads", Path: "Release/Season/Episode.mkv"})
	if err != nil {
		t.Fatalf("ListJobs(path) error = %v", err)
	}
	if filtered.Matched != 1 || counting.sources != 4 {
		t.Fatalf("path listing matched = %d after %d lookups, want an exact match found by inspecting every job",
			filtered.Matched, counting.sources)
	}
}

// TestCancelJobsIgnoresTheDisplayLimit is the selector-semantics rule that
// matters most here: job list truncates for readability, and a cancel that
// inherited that truncation would silently spare work the operator selected.
func TestCancelJobsIgnoresTheDisplayLimit(t *testing.T) {
	ctx := context.Background()
	service, state, _, _ := testService(t, ctx)
	enqueueSiblingJobs(t, ctx, state, "Second", "Third", "Fourth")

	// The listing an operator would have seen first is truncated.
	listed, err := service.ListJobs(ctx, control.JobQuery{Library: "downloads", Limit: 1})
	if err != nil {
		t.Fatalf("ListJobs() error = %v", err)
	}
	if !listed.Truncated {
		t.Fatal("fixture did not produce a truncated listing")
	}

	response, err := service.CancelJobs(ctx, control.JobCancelRequest{Library: "downloads"})
	if err != nil {
		t.Fatalf("CancelJobs() error = %v", err)
	}
	if response.Matched != 4 || response.Canceled != 4 {
		t.Fatalf("CancelJobs() matched = %d, canceled = %d; want every selected job", response.Matched, response.Canceled)
	}
}

// TestListJobsRejectsALibraryThatIsNotConfigured keeps a typo from being
// answered with an empty result, which reads like "there is nothing there".
func TestListJobsRejectsALibraryThatIsNotConfigured(t *testing.T) {
	ctx := context.Background()
	service, _, _, _ := testService(t, ctx)

	calls := map[string]func() error{
		"job list": func() error {
			_, err := service.ListJobs(ctx, control.JobQuery{Library: "nope"})
			return err
		},
		"library stats": func() error {
			_, err := service.LibraryStats(ctx, control.LibraryStatsRequest{Library: "nope"})
			return err
		},
		"job prune": func() error {
			_, err := service.PruneJobs(ctx, control.JobPruneRequest{Library: "nope"})
			return err
		},
		"library scan": func() error {
			_, err := service.ScanLibraries(ctx, control.LibraryScanRequest{Library: "nope"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			var controlErr *control.Error
			if !errors.As(err, &controlErr) || controlErr.Code != control.CodeNotFound {
				t.Fatalf("%s error = %v, want not_found", name, err)
			}
		})
	}

	// A configured library with no jobs is still a legitimate empty answer.
	response, err := service.LibraryStats(ctx, control.LibraryStatsRequest{Library: "archive"})
	if err != nil {
		t.Fatalf("LibraryStats(archive) error = %v", err)
	}
	if len(response.Libraries) != 0 {
		t.Fatalf("LibraryStats(archive) = %+v, want an empty answer rather than a refusal", response)
	}
}
