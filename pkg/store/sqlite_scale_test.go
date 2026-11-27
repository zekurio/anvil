package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

// scaleProbe is the probe of an ordinary remux: one video stream, a handful
// of audio tracks, and a long list of subtitles, each with its tags.
func scaleProbe(path string) *domain.ProbeResult {
	probe := &domain.ProbeResult{
		Path: path, FormatName: "matroska,webm", DurationSeconds: 7200, SizeBytes: 30 << 30,
		Streams: []domain.MediaStream{{
			Index: 0, Type: "video", Codec: "hevc", Width: 3840, Height: 2160, PixelFormat: "yuv420p10le",
			ColorTransfer: "smpte2084", Tags: map[string]string{"BPS": "58000000", "DURATION": "02:00:00.000000000"},
		}},
	}
	for index := 1; index <= 4; index++ {
		probe.Streams = append(probe.Streams, domain.MediaStream{
			Index: index, Type: "audio", Codec: "truehd", Channels: 8, ChannelLayout: "7.1", Language: "eng",
			Title: "Dolby TrueHD Atmos 7.1", Tags: map[string]string{"BPS": "4500000", "NUMBER_OF_FRAMES": "8640000"},
			Disposition: map[string]bool{"default": index == 1},
		})
	}
	for index := 5; index <= 40; index++ {
		probe.Streams = append(probe.Streams, domain.MediaStream{
			Index: index, Type: "subtitle", Codec: "hdmv_pgs_subtitle", Language: "eng", Title: "English (SDH)",
			Tags:        map[string]string{"BPS": "40000", "NUMBER_OF_FRAMES": "3200", "DURATION": "01:58:00.000000000"},
			Disposition: map[string]bool{"default": false, "forced": false, "hearing_impaired": true},
		})
	}
	return probe
}

// seedScaleLibrary writes a library of files, each with a job that carries a
// probe checkpoint, the way a library looks once every file has been probed.
// The rows go in directly and in one transaction: scanning and probing this
// many files through the daemon's own paths would take minutes.
func seedScaleLibrary(t testing.TB, state *SQLiteStore, library string, files int) {
	t.Helper()
	ctx := context.Background()
	mustCreateLibrary(t, state, library)
	tx, err := state.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	states := []domain.JobState{domain.JobStateComplete, domain.JobStateComplete, domain.JobStatePending, domain.JobStateFailed, domain.JobStateSkipped}
	for index := 1; index <= files; index++ {
		path := fmt.Sprintf("Show %03d/Season %02d/Episode %05d.mkv", index%200, index%7, index)
		stamp := encodeTime(now.Add(time.Duration(index) * time.Second))
		size := int64(index) << 20
		if _, err := tx.ExecContext(ctx, `
INSERT INTO media_sources (id, library_name, kind, relative_path, generation, status, size_bytes, first_seen_at, last_seen_at, updated_at)
VALUES (?, ?, 'file', ?, 1, 'active', ?, ?, ?, ?)`, index, library, path, size, stamp, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO media_assets (id, source_id, relative_path, generation, role, status, size_bytes, first_seen_at, last_seen_at, updated_at)
VALUES (?, ?, ?, 1, 'primary_video', 'active', ?, ?, ?, ?)`, index, index, path, size, stamp, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		probe := scaleProbe("/media/" + library + "/" + path)
		checkpoint, err := json.Marshal(domain.JobPipelineContext{Version: domain.JobPipelineContextVersion, Probe: probe})
		if err != nil {
			t.Fatal(err)
		}
		facts := probeFactsOf(probe)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO jobs (id, slug, source_id, asset_id, library_name, state, input_size_bytes, output_size_bytes, created_at, updated_at,
	probe_format, probe_video_codec, probe_width, probe_height, probe_audio_codec, probe_audio_channels, pipeline_context_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, index, fmt.Sprintf("job-%d", index), index, index, library,
			string(states[index%len(states)]), size, size/2, stamp, stamp,
			facts.FormatName, facts.VideoCodec, facts.Width, facts.Height, facts.AudioCodec, facts.AudioChannels, checkpoint); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// The web UI polls the file list of an open library every five seconds, and
// the library list and the queue as often. The store has one connection, so
// each of these holds up everything else for as long as it runs. With the
// checkpoint in the listing, one page of a 20,000-file library took half a
// second and the whole library six.
func TestListsStayFastOnALargeLibrary(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 20,000 files")
	}
	if raceDetector {
		t.Skip("the time budgets do not hold under the race detector")
	}
	ctx := context.Background()
	state := openTestStore(t)
	const files = 20000
	seedScaleLibrary(t, state, "shows", files)

	page := LibraryFileFilter{LibraryName: "shows", Limit: 100}
	listed, total, err := state.ListLibraryFiles(ctx, page)
	if err != nil || total != files || len(listed) != 100 {
		t.Fatalf("ListLibraryFiles = %d files of %d, %v", len(listed), total, err)
	}
	want := ProbeFacts{FormatName: "matroska,webm", VideoCodec: "hevc", Width: 3840, Height: 2160, AudioCodec: "truehd", AudioChannels: 8}
	if listed[0].Probe != want {
		t.Fatalf("probe facts = %+v, want %+v", listed[0].Probe, want)
	}

	// Budgets are several times what the queries take on a laptop, and well
	// under what they took before.
	const budget = 400 * time.Millisecond
	queries := []struct {
		name   string
		budget time.Duration
		run    func() error
	}{
		{"first page of files by path", budget, func() error {
			_, _, err := state.ListLibraryFiles(ctx, page)
			return err
		}},
		{"failed files, newest first", budget, func() error {
			_, _, err := state.ListLibraryFiles(ctx, LibraryFileFilter{
				LibraryName: "shows", Status: FileStatusFailed, Sort: LibraryFileSortUpdated, Descending: true, Limit: 100,
			})
			return err
		}},
		{"files by size, a late page", budget, func() error {
			_, _, err := state.ListLibraryFiles(ctx, LibraryFileFilter{LibraryName: "shows", Sort: LibraryFileSortSize, Limit: 100, Offset: 15000})
			return err
		}},
		{"file search", budget, func() error {
			_, _, err := state.ListLibraryFiles(ctx, LibraryFileFilter{LibraryName: "shows", Query: "Episode 019", Limit: 100})
			return err
		}},
		// Nothing asks for a whole library at once; this bounds the worst a
		// caller could do.
		{"every file", 4 * budget, func() error {
			_, _, err := state.ListLibraryFiles(ctx, LibraryFileFilter{LibraryName: "shows"})
			return err
		}},
		{"library overviews", budget, func() error {
			_, err := state.LibraryOverviews(ctx)
			return err
		}},
		{"first page of jobs", budget, func() error {
			_, err := state.ListJobs(ctx, JobListFilter{NewestActivityFirst: true, Limit: 100})
			return err
		}},
		{"a late page of a library's jobs", budget, func() error {
			_, err := state.ListJobs(ctx, JobListFilter{LibraryName: "shows", NewestActivityFirst: true, Limit: 100, Offset: 15000})
			return err
		}},
		{"job count", budget, func() error {
			_, err := state.CountJobs(ctx, JobListFilter{LibraryName: "shows", States: []domain.JobState{domain.JobStateFailed}})
			return err
		}},
	}
	for _, query := range queries {
		// The best of three: a budget is about the query, not about what
		// else the machine was doing.
		best := time.Hour
		for range 3 {
			started := time.Now()
			if err := query.run(); err != nil {
				t.Fatalf("%s: %v", query.name, err)
			}
			best = min(best, time.Since(started))
		}
		t.Logf("%-34s %s", query.name, best.Round(100*time.Microsecond))
		if best > query.budget {
			t.Errorf("%s took %s on %d files, over its budget of %s", query.name, best, files, query.budget)
		}
	}
}
