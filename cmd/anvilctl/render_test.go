package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/control"
)

func TestJobListingGroupsWithoutLosingIdentity(t *testing.T) {
	report := control.JobListResponse{Matched: 5, Truncated: true}
	for i, source := range []string{"/media/Season 1/one.mkv", "/other/Season 1/one.mkv", "/media/Season 1/two.mkv"} {
		report.Jobs = append(report.Jobs, control.JobResponse{
			ID: int64(i + 1), Slug: "job-slug", Library: "tv", State: "pending",
			Source:    control.OccurrenceResponse{AbsolutePath: source},
			UpdatedAt: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
		})
	}
	report.Jobs[2].State = "failed"
	report.Jobs[2].LastError = "encoder failed"
	report.Jobs[2].DestinationPath = "/media/Season 1/two.av1.mkv"
	report.Jobs[2].MatchedOn = []control.PathMatchSide{control.PathMatchDestination}
	var out bytes.Buffer
	if err := writeJobs(&out, report); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	t.Log("\n" + text)
	for _, parent := range []string{"/media/Season 1", "/other/Season 1"} {
		if strings.Count(text, parent) != 1 {
			t.Fatalf("folder not shown exactly once: %q\n%s", parent, text)
		}
	}
	for _, value := range []string{"one.mkv", "two.mkv", "encoder failed", "destination", "Showing 3 of 5"} {
		if !strings.Contains(text, value) {
			t.Errorf("missing %q\n%s", value, text)
		}
	}
	if strings.Contains(text, "two.av1.mkv") || strings.Contains(text, "Destination") {
		t.Fatal("media listing includes destination information")
	}
	if strings.Contains(text, "\x1b") {
		t.Fatal("ANSI in redirected output")
	}
	if strings.Index(text, "two.mkv") > strings.Index(text, "/other/Season 1") {
		t.Fatal("same-folder jobs not grouped together")
	}

	out.Reset()
	if err := writeJSON(&out, report); err != nil {
		t.Fatal(err)
	}
	var decoded control.JobListResponse
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, report) {
		t.Fatal("JSON lost data or reordered jobs")
	}

	// Package jobs use the video asset, not the package directory, as the
	// filename. Different handoff destinations must remain separate groups.
	for i := range report.Jobs {
		job := &report.Jobs[i]
		job.Source.AbsolutePath = "/downloads/Season 1"
		job.Asset = &control.OccurrenceResponse{AbsolutePath: "/downloads/Season 1/episode.mkv"}
		job.DestinationPath = "/handoff/Season 1/episode.mkv"
	}
	report.Jobs[2].DestinationPath = "/handoff/other/episode.mkv"
	out.Reset()
	if err := writeJobs(&out, report); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	for _, folder := range []string{"/handoff/Season 1", "/handoff/other"} {
		if strings.Count(text, folder) != 1 {
			t.Fatalf("handoff folder not shown exactly once: %q\n%s", folder, text)
		}
	}
	if strings.Count(text, "episode.mkv") != len(report.Jobs) {
		t.Fatalf("expected only input filenames in rows\n%s", text)
	}
}
