//go:build linux

package scanner

import (
	"context"
	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"golang.org/x/sys/unix"
	"testing"
	"time"
)

func TestFilesystemEventsFilterArtifactsAndOrderDirectoryCompletion(t *testing.T) {
	library := filesystemLibrary{name: "download", root: "/library", download: true, config: config.LibraryConfig{Kind: "download", Exclude: []string{"excluded/**"}}}
	w := &inotifyFilesystemWatcher{completion: &CompletionTracker{}, libraries: map[domain.LibraryName]filesystemLibrary{"download": library}, wdToWatch: map[int]*filesystemWatch{1: {aliases: map[string]filesystemWatchAlias{"/library": {path: "/library"}}}}, reconcileRequests: make(chan struct{}, 1)}
	batch := make(filesystemTriggerBatch)
	w.handleEvent(context.Background(), 1, unix.IN_MODIFY, "film.job-1.anvil-part", batch)
	if len(batch) != 0 {
		t.Fatal("artifact caused scan")
	}
	w.handleEvent(context.Background(), 1, unix.IN_MOVED_TO|unix.IN_ISDIR, "package", batch)
	pending := w.directoryCompletions["/library/package"]
	if !w.completion.completeDirectory("/library/package", pending.at, pending.generation) {
		t.Fatal("unchanged directory completion rejected")
	}
	if !w.completion.CompletedSince("/library/package/film.mkv", time.Now().Add(-time.Second)) {
		t.Fatal("moved package not complete")
	}
	w.wdToWatch[2] = &filesystemWatch{aliases: map[string]filesystemWatchAlias{"/library/package": {path: "/library/package"}}}
	w.handleEvent(context.Background(), 2, unix.IN_MODIFY, "film.mkv", batch)
	if w.completion.CompletedSince("/library/package/film.mkv", time.Now().Add(-time.Second)) {
		t.Fatal("later mutation retained completion")
	}
	if w.completion.completeDirectory("/library/package", pending.at, pending.generation) {
		t.Fatal("mutation did not fence delayed repair")
	}
	if filesystemPathRelevant(library, "/library/excluded/film.mkv", false) {
		t.Fatal("excluded file caused scan")
	}
	w.handleEvent(context.Background(), -1, unix.IN_Q_OVERFLOW, "", batch)
	if batch["download"].Completed || len(triggerPaths(batch["download"])) != 0 {
		t.Fatal("overflow did not force full scan")
	}
}
