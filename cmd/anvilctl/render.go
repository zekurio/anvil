package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/zekurio/anvil/internal/textout"
	"github.com/zekurio/anvil/pkg/control"
)

const timeFormat = "2006-01-02T15:04:05Z07:00"

func writeJSON(out io.Writer, value any) error {
	return textout.WriteJSON(out, value)
}

func writeStatus(out io.Writer, response control.StatusResponse) error {
	return textout.WriteTable(out, func(w *textout.Writer) {
		w.Printf("DAEMON\t%s\n", response.Daemon.State)
		w.Printf("STARTED\t%s\n", response.Daemon.StartedAt.Format(timeFormat))
		w.Printf("VERSION\t%s\n", response.Daemon.Version)
		w.Printf("WORKERS\t%d/%d active\n", response.Workers.Active, response.Workers.Configured)
		w.Printf("QUEUE\t%s\n", formatQueue(response.Queue))
	})
}

// formatQueue keeps every state visible, including the zero ones: an empty
// "failed" count is a fact worth showing, and hiding it makes a queue look
// healthier than it is.
func formatQueue(queue map[string]int64) string {
	names := make([]string, 0, len(queue))
	for name := range queue {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, queue[name]))
	}
	if len(parts) == 0 {
		return "<empty>"
	}
	return strings.Join(parts, " ")
}

func writeVersion(out io.Writer, report versionReport) error {
	return textout.WriteTable(out, func(w *textout.Writer) {
		w.Printf("CLIENT\t%s\n", report.Client)
		if report.DaemonError != "" {
			w.Printf("DAEMON\tunreachable: %s\n", report.DaemonError)
		} else {
			w.Printf("DAEMON\t%s\n", report.Daemon)
		}
		w.Printf("PROTOCOL\t%d\n", report.ProtocolVersion)
		w.Printf("API\t%s\n", report.APIVersion)
		w.Printf("SOCKET\t%s\n", report.Socket)
	})
}

func writeJobs(out io.Writer, response control.JobListResponse) error {
	// The MATCHED column only carries meaning for an absolute-path query, so it
	// is omitted rather than rendered permanently blank.
	matched := false
	for _, job := range response.Jobs {
		if len(job.MatchedOn) > 0 {
			matched = true
			break
		}
	}
	if err := textout.WriteTable(out, func(w *textout.Writer) {
		header := "JOB\tID\tSTATE\tLIBRARY\tUPDATED\tSOURCE\tDESTINATION\tERROR"
		if matched {
			header = "JOB\tID\tSTATE\tLIBRARY\tUPDATED\tMATCHED\tSOURCE\tDESTINATION\tERROR"
		}
		w.Println(header)
		for _, job := range response.Jobs {
			columns := []any{
				job.Slug, job.ID, job.State, job.Library, job.UpdatedAt.Format(timeFormat),
			}
			format := "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n"
			if matched {
				format = "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n"
				columns = append(columns, formatMatchedOn(job.MatchedOn))
			}
			columns = append(columns, job.Source.AbsolutePath, job.DestinationPath, job.LastError)
			w.Printf(format, columns...)
		}
	}); err != nil {
		return err
	}
	return textout.Write(out, func(w *textout.Writer) {
		if response.PathOutsideLibraries {
			w.Println("path resolves under no configured library root, so it is unlikely any job owns it")
		}
		writeJobStreamSelections(w, response.Jobs)
		if response.Truncated {
			w.Printf("showing %d of %d matching jobs\n", len(response.Jobs), response.Matched)
		}
	})
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
	if err := textout.WriteTable(out, func(w *textout.Writer) {
		w.Println("JOB\tID\tLIBRARY\tPREVIOUS\tSTATE\tCANCELED\tWORKER SIGNALED\tSKIPPED")
		for _, job := range response.Jobs {
			w.Printf("%s\t%d\t%s\t%s\t%s\t%t\t%t\t%s\n",
				job.Slug, job.ID, job.Library, job.PreviousState, job.State,
				job.Canceled, job.WorkerSignaled, job.SkipReason)
		}
	}); err != nil {
		return err
	}
	return textout.Write(out, func(w *textout.Writer) {
		w.Printf("canceled %d of %d matching jobs\n", response.Canceled, response.Matched)
	})
}

func writeRetriedJobs(out io.Writer, response control.JobRetryResponse) error {
	if len(response.Jobs) > 0 {
		if err := textout.WriteTable(out, func(w *textout.Writer) {
			w.Println("JOB\tID\tLIBRARY\tSTATE")
			for _, job := range response.Jobs {
				w.Printf("%s\t%d\t%s\t%s\n", job.Slug, job.ID, job.Library, job.State)
			}
		}); err != nil {
			return err
		}
	}
	return textout.Write(out, func(w *textout.Writer) {
		w.Printf("retried_failed_jobs=%d retried_jobs=%d\n", response.RetriedFailed, len(response.Jobs))
	})
}

func writePrunedJobs(out io.Writer, response control.JobPruneResponse) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Printf("dry_run=%t matched_jobs=%d affected_sources=%d deleted_jobs=%d",
			response.DryRun, response.MatchedJobs, response.AffectedSources, response.DeletedJobs)
		states := make([]string, 0, len(response.ByState))
		for state := range response.ByState {
			states = append(states, state)
		}
		sort.Strings(states)
		for _, state := range states {
			w.Printf(" state_%s=%d", state, response.ByState[state])
		}
		w.Println()
		writeProtectedJobs(w, response.ProtectedJobs)
	})
}

// writeProtectedJobs names the work maintenance refused to touch. An operator
// who is told "0 deleted" without being told why cannot act on it.
func writeProtectedJobs(w *textout.Writer, jobs []control.ProtectedJob) {
	for _, job := range jobs {
		w.Printf("protected job=%s id=%d reason=%s\n", textout.OrNone(job.Slug), job.ID, job.Reason)
	}
}

func writeRecoveredJobs(out io.Writer, response control.JobRecoverResponse) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Printf("recovered_jobs=%d\n", response.RecoveredJobs)
	})
}

func writeScanResult(out io.Writer, response control.LibraryScanResponse) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Printf("libraries=%d sources=%d assets=%d enqueued_jobs=%d existing_jobs=%d skipped_ignored=%d skipped_unstable=%d",
			response.Libraries, response.Sources, response.Assets, response.EnqueuedJobs,
			response.ExistingJobs, response.SkippedIgnored, response.SkippedUnstable)
		if response.NextStableAt != nil {
			w.Printf(" next_stable_at=%s", response.NextStableAt.Format(time.RFC3339))
		}
		w.Println()
	})
}

func writeLibraryStats(out io.Writer, response control.LibraryStatsResponse) error {
	return textout.WriteTable(out, func(w *textout.Writer) {
		w.Println("LIBRARY\tJOBS\tBEFORE\tAFTER\tSAVED\tSAVED%")
		for _, stat := range response.Libraries {
			w.Printf("%s\t%d\t%s\t%s\t%s\t%s\n",
				stat.Library, stat.Jobs,
				textout.Bytes(stat.InputSizeBytes), textout.Bytes(stat.OutputSizeBytes),
				textout.Bytes(stat.SavedBytes), textout.Percent(stat.SavedPercent))
		}
	})
}

func writeRequeuedOccurrence(out io.Writer, response control.ForceOccurrenceResponse) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Printf("library=%s path=%s source_id=%d source_generation=%d asset_id=%d asset_generation=%d job=%s id=%d state=%s\n",
			response.Library, response.Path,
			response.SourceID, response.SourceGeneration,
			response.AssetID, response.AssetGeneration,
			response.JobSlug, response.JobID, response.JobState)
	})
}

func writeStagingCleanup(out io.Writer, errOut io.Writer, response control.StagingCleanupResponse) error {
	if err := textout.Write(out, func(w *textout.Writer) {
		w.Printf("dry_run=%t root=%s older_than=%s candidates=%d removed=%d skipped=%d protected=%d errors=%d\n",
			response.DryRun, response.Root, response.OlderThan,
			response.Candidates, response.Removed, response.Skipped, response.Protected, len(response.Errors))
		if legacy := response.LegacyParts; legacy != nil {
			w.Printf("legacy_candidates=%d legacy_removed=%d legacy_protected=%d\n", legacy.Candidates, legacy.Removed, legacy.Protected)
		}
		writeProtectedJobs(w, response.ProtectedJobs)
	}); err != nil {
		return err
	}
	if err := textout.Write(errOut, func(w *textout.Writer) {
		for _, message := range response.Errors {
			w.Printf("cleanup_error=%q\n", message)
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
	return textout.Write(out, func(w *textout.Writer) {
		w.Printf("backup=%s size_bytes=%d integrity=%s\n", response.Path, response.SizeBytes, response.Integrity)
	})
}
