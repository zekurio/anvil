//go:build linux

package scanner

import (
	"context"
	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
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
	library.config.Exclude = append(library.config.Exclude, "ignored/")
	if filesystemPathRelevant(library, "/library/ignored/film.mkv", false) {
		t.Fatal("directory-only exclusion allowed child")
	}
	if filesystemPathRelevant(library, "/library/excluded/film.mkv", false) {
		t.Fatal("excluded file caused scan")
	}
	w.handleEvent(context.Background(), -1, unix.IN_Q_OVERFLOW, "", batch)
	if batch["download"].Completed || len(triggerPaths(batch["download"])) != 0 {
		t.Fatal("overflow did not force full scan")
	}
}

func TestInotifyMoveRepairAndLaterWrite(t *testing.T) {
	root := t.TempDir()
	incoming := t.TempDir()
	source := filepath.Join(incoming, "package")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "film.mkv"), []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	wake := make([]int, 2)
	if err := unix.Pipe2(wake, unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	triggers := make(chan ScanTrigger, 16)
	repairs := make(chan struct{}, 1)
	w := &inotifyFilesystemWatcher{
		fd: fd, wakeRead: wake[0], wakeWrite: wake[1], completion: &CompletionTracker{}, triggers: triggers,
		reconcileRequests: repairs, triggerReady: make(chan struct{}, 1), pendingTriggers: make(map[domain.LibraryName]ScanTrigger),
		libraries: map[domain.LibraryName]filesystemLibrary{"download": {name: "download", root: root, download: true, config: config.LibraryConfig{Kind: "download"}}},
		wdToWatch: make(map[int]*filesystemWatch), dirToWD: make(map[string]int), ignoredPending: make(map[int]int),
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := w.addRecursive(ctx, root); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	dispatchDone := make(chan struct{})
	go func() { readDone <- w.readEvents(ctx) }()
	go func() { defer close(dispatchDone); w.dispatchTriggers(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := wakeFilesystemReader(wake[1]); err != nil {
			t.Error(err)
		}
		if err := <-readDone; err != nil {
			t.Error(err)
		}
		<-dispatchDone
		for _, descriptor := range []int{fd, wake[0], wake[1]} {
			if err := unix.Close(descriptor); err != nil {
				t.Error(err)
			}
		}
	})
	destination := filepath.Join(root, "package")
	if err := os.Rename(source, destination); err != nil {
		t.Fatal(err)
	}
	select {
	case <-repairs:
	case <-time.After(5 * time.Second):
		t.Fatal("move did not request repair")
	}
	// The first request can precede the reader's completion record. Wait for
	// its trigger, which is sent only after that entire event has been handled.
	select {
	case <-triggers:
	case <-time.After(5 * time.Second):
		t.Fatal("move did not trigger scan")
	}
	if _, err := w.addRecursive(ctx, destination); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	pending, exists := w.directoryCompletions[destination]
	w.readyCompletions = map[string]directoryCompletion{destination: pending}
	w.mu.Unlock()
	if !exists {
		t.Fatal("move completion was not recorded")
	}
	if err := wakeFilesystemReader(wake[1]); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(destination, "film.mkv")
	select {
	case <-triggers:
	case <-time.After(5 * time.Second):
		t.Fatal("repair did not trigger completion scan")
	}
	if !w.completion.CompletedSince(file, pending.at.Add(-time.Second)) {
		t.Fatal("repaired move was not complete")
	}
	writer, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("changed")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-triggers:
	case <-time.After(5 * time.Second):
		t.Fatal("write did not trigger scan")
	}
	if w.completion.CompletedSince(file, pending.at.Add(-time.Second)) {
		t.Fatal("open writer retained directory confidence")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-triggers:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not trigger scan")
	}
	if !w.completion.CompletedSince(file, pending.at) {
		t.Fatal("close did not restore completion")
	}
}
