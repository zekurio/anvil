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
	for _, value := range []string{"one.mkv", "two.mkv", "two.av1.mkv", "encoder failed", "destination", "Showing 3 of 5"} {
		if !strings.Contains(text, value) {
			t.Errorf("missing %q\n%s", value, text)
		}
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
}
