package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/control"
)

func TestShowNamesTheFlowAndDoesNotCallAFixedCRFASearch(t *testing.T) {
	report := control.JobShowResponse{
		Job: control.JobDetail{ID: 7, Slug: "calm-otter", State: "complete", Library: "movies", AttemptCount: 1},
		PipelineContext: &control.PipelineContextDetail{
			Version: 8, QualityMode: "fixed", SearchCRF: 30,
		},
		Flow: &control.JobFlowDetail{
			AttemptID: 3, AttemptNumber: 1, FlowID: 2, FlowName: "Skip HEVC", Revision: 4, Outcome: "process",
			Path:      []string{"in", "is-hevc", "encode", "publish"},
			Decisions: []string{"Video is hevc? → false (video codec is h264)"},
			Plan:      []string{"Video: encode to AV1 at CRF 30"},
		},
		Attempts: []control.AttemptDetail{},
	}
	var out bytes.Buffer
	if err := writeJobShow(&out, report); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	t.Log("\n" + text)
	for _, value := range []string{
		"Flow (attempt 1)", "Flow: Skip HEVC (revision 4)", "Outcome: process",
		"Path: in → is-hevc → encode → publish", "Decision: Video is hevc? → false",
		"Plan: Video: encode to AV1 at CRF 30", "Quality: fixed CRF 30",
	} {
		if !strings.Contains(text, value) {
			t.Errorf("missing %q\n%s", value, text)
		}
	}
	if strings.Contains(text, "Search:") {
		t.Fatalf("a fixed CRF is reported as a search result\n%s", text)
	}

	// A searched CRF still reads as one, with its score.
	report.PipelineContext = &control.PipelineContextDetail{Version: 8, SearchCRF: 34, SearchMetric: "vmaf", SearchVMAF: 95.2}
	report.Flow = nil
	out.Reset()
	if err := writeJobShow(&out, report); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	if !strings.Contains(text, "Search: CRF 34 VMAF 95.20") || strings.Contains(text, "Quality: fixed") || strings.Contains(text, "Flow (attempt") {
		t.Fatalf("searched job\n%s", text)
	}
}

func TestLibraryAndFlowTables(t *testing.T) {
	scanned := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	if err := writeLibraries(&out, control.LibraryListResponse{Libraries: []control.LibraryEntry{
		{
			Name: "movies", Kind: "media", Path: "/srv/media/movies", FlowID: 1, FlowName: "Default AV1", Files: 120,
			Jobs: map[string]int64{"pending": 4, "retrying": 1, "running": 2, "complete": 90, "failed": 3}, SavedBytes: 5 << 30, LastScanAt: &scanned,
		},
		{Name: "usenet", Kind: "download", Path: "/srv/downloads", HandoffPath: "/srv/import", FlowID: 2, FlowName: "Skip HEVC", Paused: true, Jobs: map[string]int64{}},
	}}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	t.Log("\n" + text)
	for _, value := range []string{"movies", "Default AV1", "active", "120", "/srv/media/movies", "usenet", "paused", "Skip HEVC", "Queued", "Running", "Failed", "/srv/downloads", "/srv/import", "Hands off to"} {
		if !strings.Contains(text, value) {
			t.Errorf("libraries: missing %q\n%s", value, text)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		// Queued is pending plus retrying, running stands alone.
		if strings.Contains(line, "media ") && (!strings.Contains(line, " 5 ") || !strings.Contains(line, " 2 ") || !strings.Contains(line, " 90 ")) {
			t.Errorf("libraries: job counts are off in %q", line)
		}
	}

	out.Reset()
	if err := writeLibraries(&out, control.LibraryListResponse{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "web UI") {
		t.Fatalf("an empty list should say where libraries come from: %q", out.String())
	}

	out.Reset()
	if err := writeFlows(&out, control.FlowListResponse{Flows: []control.FlowEntry{
		{ID: 1, Name: "Default AV1", Revision: 1, NodeCount: 7, Libraries: []string{"movies", "shows"}},
		{ID: 2, Name: "Imported", Revision: 3, NodeCount: 9, Libraries: []string{}, Errors: 2, Warnings: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	text = out.String()
	t.Log("\n" + text)
	for _, value := range []string{"Default AV1", "movies, shows", "ok", "Imported", "2 errors"} {
		if !strings.Contains(text, value) {
			t.Errorf("flows: missing %q\n%s", value, text)
		}
	}
}
