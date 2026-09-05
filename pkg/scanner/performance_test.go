package scanner

import (
	"context"
	"fmt"
	"github.com/zekurio/anvil/pkg/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompletionBoundedAndOrdered(t *testing.T) {
	tracker := &CompletionTracker{}
	now := time.Now()
	root := t.TempDir()
	child := filepath.Join(root, "package", "film.mkv")
	tracker.MarkDirectory(root, now)
	if !tracker.CompletedSince(child, now) {
		t.Fatal("moved tree not complete")
	}
	tracker.Invalidate(child)
	if tracker.CompletedSince(child, now) {
		t.Fatal("mutation retained tree completion")
	}
	tracker.Mark(child, now)
	if !tracker.CompletedSince(child, now) {
		t.Fatal("close did not restore completion")
	}
	for i := 0; i < completionMarkLimit+100; i++ {
		tracker.Mark(filepath.Join(root, fmt.Sprint(i)), now.Add(time.Duration(i)))
	}
	if len(tracker.marks) != completionMarkLimit || len(tracker.oldest) != completionMarkLimit {
		t.Fatal("completion tracking not bounded")
	}
	if tracker.CompletedSince(child, now) {
		t.Fatal("oldest entry not evicted")
	}
	path := filepath.Join(root, "repeat")
	for i := range 1000 {
		tracker.Mark(path, now.Add(time.Hour+time.Duration(i)))
	}
	if len(tracker.oldest) != len(tracker.marks) {
		t.Fatal("repeated marks grew heap")
	}
	tracker.Reset()
	if tracker.CompletedSince(path, now) || len(tracker.oldest) != 0 {
		t.Fatal("reset retained completion")
	}
}

func TestMergeScanTriggersRetainsPathsAndFullScan(t *testing.T) {
	trigger := ScanTrigger{LibraryName: "media", Path: "/a", Completed: true}
	trigger = mergeScanTrigger(trigger, ScanTrigger{LibraryName: "media", Path: "/b"})
	if len(trigger.Paths) != 2 {
		t.Fatal("lost changed path")
	}
	trigger = mergeScanTrigger(trigger, ScanTrigger{LibraryName: "media"})
	if len(triggerPaths(trigger)) != 0 || trigger.Completed {
		t.Fatal("full scan lost to completion")
	}
}

func TestPartialDiscoveryAndPackageSidecars(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one/film.mkv", "one/info.txt", "two/film.mkv", "unrelated.txt"} {
		file := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("123"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	lib := config.LibraryConfig{Kind: "download"}
	candidates, stats, _, err := discoverPaths(context.Background(), root, lib, nil, []string{"one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || len(stats) != 1 {
		t.Fatalf("candidates %d stats %d", len(candidates), len(stats))
	}
	for _, stat := range stats {
		if stat.sizeBytes != 6 {
			t.Fatal("package sidecar missing")
		}
	}
	lib.Kind = "media"
	_, stats, _, err = discoverCandidates(context.Background(), root, lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatal("unrelated files got source stats")
	}
}

func TestPartialDiscoveryHonorsExcludedAncestorDirectory(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "ignored", "film.mkv")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	library := config.LibraryConfig{Kind: "media", Exclude: []string{"ignored/"}}
	for _, paths := range [][]string{nil, {"ignored/film.mkv"}} {
		candidates, _, _, err := discoverPaths(context.Background(), root, library, nil, paths)
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) != 0 {
			t.Fatal("excluded ancestor admitted a candidate")
		}
	}
}
