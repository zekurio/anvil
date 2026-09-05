package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zekurio/anvil/pkg/config"
)

func TestDownloadStableForKeepsZero(t *testing.T) {
	library := config.LibraryConfig{Kind: "download"}
	if got := downloadStableFor(library); got != 0 {
		t.Fatalf("downloadStableFor = %v, want 0", got)
	}
}

func TestDiscoverCandidatesSkipsHardlinks(t *testing.T) {
	original := filepath.Join(t.TempDir(), "episode.mkv")
	if err := os.WriteFile(original, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Link(original, filepath.Join(root, "episode.mkv")); err != nil {
		t.Fatal(err)
	}

	candidates, _, skipped, err := discoverCandidates(context.Background(), root, config.LibraryConfig{Kind: "media"}, &CompletionTracker{})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 || skipped != 1 {
		t.Fatalf("candidates=%d skipped=%d, want 0 and 1", len(candidates), skipped)
	}
}
