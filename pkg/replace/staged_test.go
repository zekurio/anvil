package replace

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestStagedDestinationsUseAttemptSnapshots(t *testing.T) {
	job := domain.Job{ID: 42}
	source := domain.MediaSource{Kind: domain.SourceKindFile, RelativePath: "season/movie.mp4"}
	library := domain.Library{Path: "/source"}
	oldRoot := filepath.Join(t.TempDir(), "old")
	newRoot := filepath.Join(t.TempDir(), "new")
	attempts := []domain.Attempt{
		snapshotAttempt(t, 1, library, moveProfile(oldRoot)),
		snapshotAttempt(t, 2, library, moveProfile(newRoot)),
		snapshotAttempt(t, 3, library, moveProfile(newRoot)),
		// The flow changed to replace in place between attempts.
		snapshotAttempt(t, 4, library, domain.Profile{Name: "encode", Container: "mkv", Publish: domain.PublishPolicy{Mode: domain.PublishReplace}}),
		{Number: 5}, // Resolution failed before the pipeline ran, so it never staged.
		{Number: 6, ResolvedLibrary: []byte("{"), ResolvedFlow: []byte("{}"), ResolvedProfile: []byte("{}")},
		{Number: 7, ResolvedLibrary: []byte("{}"), ResolvedFlow: []byte("{}"), ResolvedProfile: []byte("{")},
	}

	destinations, err := StagedDestinations(job, source, domain.MediaAsset{}, attempts)
	if err == nil || !strings.Contains(err.Error(), "decode attempt 6 resolved library") || !strings.Contains(err.Error(), "decode attempt 7 resolved profile") {
		t.Fatalf("StagedDestinations error = %v, want malformed snapshot errors", err)
	}
	want := []string{
		filepath.Join(oldRoot, "season", "movie.mkv"),
		filepath.Join(newRoot, "season", "movie.mkv"),
		filepath.Join("/source", "season", "movie.mkv"),
	}
	var paths []string
	for _, destination := range destinations {
		paths = append(paths, destination.Path)
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("StagedDestinations = %#v, want %#v", paths, want)
	}
}

// moveProfile is a profile whose flow moves to folder, keeping the library's
// folders.
func moveProfile(folder string) domain.Profile {
	return domain.Profile{
		Name: "encode", Container: "mkv",
		Publish: domain.PublishPolicy{Mode: domain.PublishMove, Destination: folder, PreserveStructure: true},
	}
}

// snapshotAttempt is an attempt that recorded library and profile, as the
// worker does once the flow step resolved the profile.
func snapshotAttempt(t *testing.T, number int, library domain.Library, profile domain.Profile) domain.Attempt {
	t.Helper()
	marshal := func(value any) []byte {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		return data
	}
	return domain.Attempt{Number: number, ResolvedLibrary: marshal(library), ResolvedProfile: marshal(profile)}
}
