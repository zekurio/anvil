package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

type attemptSnapshotStore struct {
	Store
	attempts []domain.Attempt
}

func (s attemptSnapshotStore) ListAttemptsForJob(context.Context, domain.JobID) ([]domain.Attempt, error) {
	return s.attempts, nil
}

func TestStagedDestinationsUseAttemptSnapshots(t *testing.T) {
	job := domain.Job{ID: 42}
	source := domain.MediaSource{Kind: domain.SourceKindFile, RelativePath: "season/movie.mp4"}
	flow := domain.Flow{Name: "download", Steps: []domain.FlowStep{{Name: "handoff"}}}
	profile := domain.Profile{Name: "encode", Container: "mkv"}
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	attempts := []domain.Attempt{
		snapshotAttempt(t, 1, domain.Library{Kind: domain.LibraryKindDownload, Path: "/source", Download: domain.DownloadLibraryPolicy{HandoffPath: oldRoot, PreserveRelativePath: true}}, flow, profile),
		snapshotAttempt(t, 2, domain.Library{Kind: domain.LibraryKindDownload, Path: "/source", Download: domain.DownloadLibraryPolicy{HandoffPath: newRoot, PreserveRelativePath: true}}, flow, profile),
		snapshotAttempt(t, 3, domain.Library{Kind: domain.LibraryKindDownload, Path: "/source", Download: domain.DownloadLibraryPolicy{HandoffPath: newRoot, PreserveRelativePath: true}}, flow, profile),
		{Number: 4}, // Resolution failed before the pipeline ran, so it never staged.
		{Number: 5, ResolvedLibrary: []byte("{"), ResolvedFlow: []byte("{}"), ResolvedProfile: []byte("{}")},
	}

	destinations, err := stagedDestinations(context.Background(), attemptSnapshotStore{attempts: attempts}, job, source, domain.MediaAsset{})
	if err == nil || !strings.Contains(err.Error(), "decode attempt 5 resolved library") {
		t.Fatalf("stagedDestinations error = %v, want malformed snapshot error", err)
	}
	want := []string{
		filepath.Join(oldRoot, "season", "movie.mkv"),
		filepath.Join(newRoot, "season", "movie.mkv"),
	}
	if !reflect.DeepEqual(destinations, want) {
		t.Fatalf("stagedDestinations = %#v, want %#v", destinations, want)
	}
}

func TestStagedDestinationsPropagatesListError(t *testing.T) {
	store := listAttemptsErrorStore{err: errors.New("store unavailable")}
	_, err := stagedDestinations(context.Background(), store, domain.Job{ID: 42}, domain.MediaSource{}, domain.MediaAsset{})
	if err == nil || !strings.Contains(err.Error(), "list attempts: store unavailable") {
		t.Fatalf("stagedDestinations error = %v, want wrapped store error", err)
	}
}

type listAttemptsErrorStore struct {
	Store
	err error
}

func (s listAttemptsErrorStore) ListAttemptsForJob(context.Context, domain.JobID) ([]domain.Attempt, error) {
	return nil, s.err
}

func snapshotAttempt(t *testing.T, number int, library domain.Library, flow domain.Flow, profile domain.Profile) domain.Attempt {
	t.Helper()
	marshal := func(value any) []byte {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		return data
	}
	return domain.Attempt{
		Number:          number,
		ResolvedLibrary: marshal(library),
		ResolvedFlow:    marshal(flow),
		ResolvedProfile: marshal(profile),
	}
}
