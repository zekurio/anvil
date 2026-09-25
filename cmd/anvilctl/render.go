package main

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/internal/textout"
	"github.com/zekurio/anvil/pkg/control"
)

const timeFormat = "2006-01-02T15:04:05Z07:00"

func writeJSON(out io.Writer, value any) error {
	return textout.WriteJSON(out, value)
}

func writeStatus(out io.Writer, response control.StatusResponse) error {
	return textout.WriteReport(out, func(w *textout.Writer) {
		w.Heading("Anvil  " + w.State(response.Daemon.State))
		w.Printf("  %d/%d workers active  ·  %s  ·  started %s\n", response.Workers.Active, response.Workers.Configured, response.Daemon.Version, response.Daemon.StartedAt.Format(timeFormat))
		names := make([]string, 0, len(response.Queue))
		for name := range response.Queue {
			names = append(names, name)
		}
		sort.Strings(names)
		w.Println()
		w.Heading("Queue")
		if len(names) == 0 {
			w.Println("  No jobs.")
		}
		for _, name := range names {
			w.Printf("  %5d  %s\n", response.Queue[name], w.State(name))
		}
	})
}

func writeVersion(out io.Writer, report versionReport) error {
	return textout.WriteReport(out, func(w *textout.Writer) {
		w.Heading("Anvil")
		w.Field("Client", report.Client)
		if report.DaemonError != "" {
			w.Field("Daemon", "unreachable: "+report.DaemonError)
		} else {
			w.Field("Daemon", report.Daemon)
		}
		w.Printf("  Protocol %d  ·  API %s\n", report.ProtocolVersion, report.APIVersion)
		w.Field("Socket", report.Socket)
	})
}

func writeJobs(out io.Writer, response control.JobListResponse) error {
	// Preserve first-seen library order and server order within each library.
	type group struct {
		library string
		jobs    []control.JobResponse
	}
	var groups []group
	indexes := make(map[string]int)
	for _, job := range response.Jobs {
		index, ok := indexes[job.Library]
		if !ok {
			index = len(groups)
			indexes[job.Library] = index
			groups = append(groups, group{library: job.Library})
		}
		groups[index].jobs = append(groups[index].jobs, job)
	}
	return textout.WriteReport(out, func(w *textout.Writer) {
		if len(groups) == 0 {
			w.Println("No matching jobs.")
		}
		for i, group := range groups {
			if i > 0 {
				w.Println()
			}
			count := "1 job"
			if len(group.jobs) != 1 {
				count = fmt.Sprintf("%d jobs", len(group.jobs))
			}
			w.Heading(group.library + "  ·  " + count)
			for _, job := range group.jobs {
				w.Println()
				w.Paragraph(fmt.Sprintf("#%d  %s  ·  updated %s", job.ID, w.State(job.State), job.UpdatedAt.Local().Format("Jan 02 15:04")))
				name := "(unknown source)"
				if source := jobInputPath(job); source != "" {
					name = filepath.Base(source)
				}
				w.Paragraph(name)
				if job.LastError != "" {
					w.Field("Last error", job.LastError)
				}
				if len(job.MatchedOn) > 0 {
					w.Field("Matched", formatMatchedOn(job.MatchedOn))
				}
			}
		}
		if len(response.Jobs) > 0 {
			w.Println()
			w.Paragraph("Full paths and history: anvilctl show <ID>")
		}
		if response.PathOutsideLibraries {
			w.Println("Path is outside configured library roots; an existing job may not own it.")
		}
		writeJobStreamSelections(w, response.Jobs)
		if response.Truncated {
			w.Printf("\nShowing %d of %d matching jobs. Use --limit 0 to show all.\n", len(response.Jobs), response.Matched)
		}
	})
}

// A download source can be a whole package; its asset identifies the video.
func jobInputPath(job control.JobResponse) string {
	if job.Asset != nil && job.Asset.AbsolutePath != "" {
		return job.Asset.AbsolutePath
	}
	if job.Source.AbsolutePath != "" {
		return job.Source.AbsolutePath
	}
	return job.Source.Path
}

func formatMatchedOn(sides []control.PathMatchSide) string {
	parts := make([]string, 0, len(sides))
	for _, side := range sides {
		parts = append(parts, string(side))
	}
	return strings.Join(parts, "+")
}

// writeJobStreamSelections renders the recorded decisions below the listing.
// They are far too wide for a table column, and they are only present when the
// caller asked for them.
func writeJobStreamSelections(w *textout.Writer, jobs []control.JobResponse) {
	for _, job := range jobs {
		for _, selection := range job.StreamSelection {
			if selection.DecisionError != "" {
				w.Printf("\n%s stream selection (attempt %d): unreadable: %s\n",
					job.Slug, selection.AttemptID, selection.DecisionError)
				continue
			}
			if selection.Decision == nil {
				continue
			}
			decision := selection.Decision
			w.Printf("\n%s %s selection (attempt %d): rule %s\n",
				job.Slug, decision.Kind, selection.AttemptID, decision.Rule)
			if len(decision.RequestedLanguages) > 0 {
				w.Printf("  requested: %s\n", strings.Join(decision.RequestedLanguages, ", "))
			}
			if len(decision.MissingLanguages) > 0 {
				w.Printf("  missing from source: %s\n", strings.Join(decision.MissingLanguages, ", "))
			}
			for _, stream := range decision.Streams {
				status := "dropped"
				if stream.Kept {
					status = "kept"
				}
				w.Printf("  #%d %s %s %s (%s)\n", stream.Index, stream.Codec, stream.Language, status, stream.Reason)
			}
		}
	}
}

func writeCanceledJobs(out io.Writer, response control.JobCancelResponse) error {
	return textout.WriteReport(out, func(w *textout.Writer) {
		w.Heading(fmt.Sprintf("Canceled %d of %d matching jobs", response.Canceled, response.Matched))
		var rows [][]string
		for _, job := range response.Jobs {
			result := "not canceled"
			if job.Canceled {
				result = "canceled"
			}
			if job.WorkerSignaled {
				result += "; worker signaled"
			}
			if job.SkipReason != "" {
				result += "; " + job.SkipReason
			}
			rows = append(rows, []string{strconv.FormatInt(job.ID, 10), job.Library, w.State(job.PreviousState) + " → " + w.State(job.State), result})
		}
		if len(rows) > 0 {
			w.Table([]string{"ID", "Library", "State", "Result"}, rows)
		}
	})
}

func writeRetriedJobs(out io.Writer, response control.JobRetryResponse) error {
	return textout.WriteReport(out, func(w *textout.Writer) {
		w.Heading("Retry")
		w.Printf("  %d failed jobs requeued  ·  %d named jobs requeued\n", response.RetriedFailed, len(response.Jobs))
		var rows [][]string
		for _, job := range response.Jobs {
			rows = append(rows, []string{strconv.FormatInt(job.ID, 10), job.Library, w.State(job.State)})
		}
		if len(rows) > 0 {
			w.Table([]string{"ID", "Library", "State"}, rows)
		}
	})
}

func writePrunedJobs(out io.Writer, response control.JobPruneResponse) error {
	return textout.WriteReport(out, func(w *textout.Writer) {
		title := "Prune"
		if response.DryRun {
			title += "  (dry run; nothing deleted)"
		}
		w.Heading(title)
		w.Printf("  %d matching jobs  ·  %d sources  ·  %d deleted\n", response.MatchedJobs, response.AffectedSources, response.DeletedJobs)
		states := make([]string, 0, len(response.ByState))
		for state := range response.ByState {
			states = append(states, state)
		}
		sort.Strings(states)
		for _, state := range states {
			w.Printf("  %5d  %s\n", response.ByState[state], w.State(state))
		}
		writeProtectedJobs(w, response.ProtectedJobs)
	})
}

// Keep maintenance refusals visible even when nothing was deleted.
func writeProtectedJobs(w *textout.Writer, jobs []control.ProtectedJob) {
	for _, job := range jobs {
		w.Printf("  Protected #%d (%s): %s\n", job.ID, textout.OrNone(job.Slug), job.Reason)
	}
}

func writeRecoveredJobs(out io.Writer, response control.JobRecoverResponse) error {
	return textout.WriteReport(out, func(w *textout.Writer) { w.Heading(fmt.Sprintf("Recovered %d jobs", response.RecoveredJobs)) })
}

func writeScanResult(out io.Writer, response control.LibraryScanResponse) error {
	return textout.WriteReport(out, func(w *textout.Writer) {
		w.Heading("Scan complete")
		w.Printf("  %d libraries  ·  %d sources  ·  %d assets\n", response.Libraries, response.Sources, response.Assets)
		w.Printf("  %d jobs enqueued  ·  %d existing\n", response.EnqueuedJobs, response.ExistingJobs)
		w.Printf("  Skipped: %d ignored, %d still changing\n", response.SkippedIgnored, response.SkippedUnstable)
		if response.NextStableAt != nil {
			w.Field("Next stable", response.NextStableAt.Format(timeFormat))
		}
	})
}

func writeLibraryStats(out io.Writer, response control.LibraryStatsResponse) error {
	return textout.WriteReport(out, func(w *textout.Writer) {
		if len(response.Libraries) == 0 {
			w.Println("No library statistics yet.")
			return
		}
		w.Heading("Library savings")
		var rows [][]string
		for _, stat := range response.Libraries {
			rows = append(rows, []string{stat.Library, strconv.FormatInt(stat.Jobs, 10), textout.Bytes(stat.InputSizeBytes), textout.Bytes(stat.OutputSizeBytes), textout.Bytes(stat.SavedBytes), textout.Percent(stat.SavedPercent)})
		}
		w.Table([]string{"Library", "Jobs", "Before", "After", "Saved", "%"}, rows)
	})
}

func writeRequeuedOccurrence(out io.Writer, response control.ForceOccurrenceResponse) error {
	return textout.WriteReport(out, func(w *textout.Writer) {
		w.Heading(fmt.Sprintf("Requeued #%d  %s", response.JobID, w.State(response.JobState)))
		w.Field(response.Library, response.Path)
		w.Field("Job", response.JobSlug)
		w.Printf("  Source #%d, generation %d  ·  asset #%d, generation %d\n", response.SourceID, response.SourceGeneration, response.AssetID, response.AssetGeneration)
	})
}

func writeStagingCleanup(out io.Writer, errOut io.Writer, response control.StagingCleanupResponse) error {
	if err := textout.WriteReport(out, func(w *textout.Writer) {
		title := "Staging cleanup"
		if response.DryRun {
			title += "  (dry run; nothing removed)"
		}
		w.Heading(title)
		w.Field("Root", response.Root)
		w.Field("Older than", response.OlderThan)
		w.Printf("  %d candidates  ·  %d removed  ·  %d skipped  ·  %d protected  ·  %d errors\n", response.Candidates, response.Removed, response.Skipped, response.Protected, len(response.Errors))
		if legacy := response.LegacyParts; legacy != nil {
			w.Printf("  Legacy parts: %d candidates, %d removed, %d protected\n", legacy.Candidates, legacy.Removed, legacy.Protected)
		}
		writeProtectedJobs(w, response.ProtectedJobs)
	}); err != nil {
		return err
	}
	if err := textout.WriteReport(errOut, func(w *textout.Writer) {
		for _, message := range response.Errors {
			w.Printf("  Cleanup error: %s\n", message)
		}
	}); err != nil {
		return err
	}
	if len(response.Errors) > 0 {
		return fmt.Errorf("staging cleanup completed with %d errors", len(response.Errors))
	}
	return nil
}

func writeBackup(out io.Writer, response control.StoreBackupResponse) error {
	return textout.WriteReport(out, func(w *textout.Writer) {
		w.Heading("Backup complete")
		w.Field("Path", response.Path)
		w.Printf("  %s  ·  integrity %s\n", textout.Bytes(response.SizeBytes), response.Integrity)
	})
}
