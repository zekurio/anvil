package main

import (
	"io"

	"github.com/zekurio/anvil/internal/textout"
)

func writeUsage(out io.Writer) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Println(`anvilctl controls a running anvild over its Unix control socket.

Usage:
  anvilctl [GLOBAL] COMMAND [OPTIONS]

Global options:
  --socket PATH       control socket (default $ANVIL_CONTROL_SOCKET or /run/anvil/anvild.sock)
  --timeout DURATION  override the per-command deadline
  --json              write JSON output

Commands:
  status                                    daemon state, worker usage, and queue counts
  version                                   client, daemon, and protocol versions
  job list [SELECTORS]                      list jobs
  job show JOB_ID_OR_SLUG                   full recorded history of one job
  job cancel [SELECTORS] [JOB...]           cancel jobs
  job retry JOB... | --failed [--library N] requeue jobs
  job prune [--library N] [--state S,...] [--apply]
                                            delete terminal jobs whose source is missing
  job recover                               release stale leases
  library scan [NAME | --library NAME]      scan libraries now
  library stats [NAME | --library NAME]     per-library size savings
  occurrence force --library NAME PATH      force the next occurrence of one media path
  staging cleanup [--older-than D] [--dry-run]
                                            remove stale staging directories
  store backup DESTINATION                  write a consistent SQLite snapshot

Job selectors (job list and job cancel):
  --library NAME        configured library
  --path PATH           exact library-relative source or media path; requires --library
  --absolute-path PATH  exact absolute source, asset, or destination path
  --state STATE,...     comma-separated job states
  --current-only        restrict to current source and asset occurrences
  --limit N             maximum jobs to return (job list only; 0 means no limit)
  --with-selection      include recorded audio and subtitle stream decisions (job list only)

Jobs are named by numeric id or slug anywhere a JOB argument is accepted.

--absolute-path matches a job's source, asset, and destination paths, so a
converted file resolves back to the job that produced it. The MATCHED column
reports which side matched.

Job cancellation requires at least one narrowing selector, so a bare
"job cancel" is rejected; --current-only only refines another selector and is
not one itself. A matched job that was not canceled is reported with a SKIPPED
reason such as publish_in_progress.

job prune and staging cleanup never touch a job that is still active or still
owns an unresolved publish journal; those are reported as protected.

Exit status:
  0 success
  1 the command failed
  2 usage or argument error
  3 the daemon is unreachable, or the two binaries speak different protocols
  4 the job, library, or path was not found

Compatibility: the old anvild subcommand names still work here, so "anvilctl
jobs", "inspect", "retry", "recover", "scan", "stats", "prune-jobs",
"cleanup-staging", "backup", and "force-occurrence" map onto the tree above.`)
	})
}

func writeJobUsage(out io.Writer) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Println("Usage: anvilctl job list|show|cancel|retry|prune|recover [OPTIONS]")
	})
}

func writeLibraryUsage(out io.Writer) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Println("Usage: anvilctl library scan|stats [NAME | --library NAME]")
	})
}

func writeOccurrenceUsage(out io.Writer) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Println("Usage: anvilctl occurrence force --library NAME RELATIVE_PATH")
	})
}

func writeStagingUsage(out io.Writer) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Println("Usage: anvilctl staging cleanup [--older-than DURATION] [--dry-run]")
	})
}

func writeStoreUsage(out io.Writer) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Println("Usage: anvilctl store backup DESTINATION")
	})
}
