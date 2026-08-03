package main

import (
	"io"
	"strings"

	"github.com/zekurio/anvil/internal/textout"
)

type commandHelp struct {
	name        string
	description string
	usage       string
	options     []helpOption
	notes       []string
	examples    []string
}

type helpOption struct {
	flag         string
	description  string
	defaultValue string
}

var statusHelp = commandHelp{
	name:        "status",
	description: "Show daemon state, worker usage, and queue counts.",
	usage:       "anvilctl status",
	examples: []string{
		"anvilctl status",
		"anvilctl status --json",
	},
}

var versionHelp = commandHelp{
	name:        "version",
	description: "Show client, daemon, and protocol versions.",
	usage:       "anvilctl version",
	notes: []string{
		"The report includes a daemon error instead of failing when the daemon is unavailable.",
	},
	examples: []string{
		"anvilctl version",
		"anvilctl --socket /run/anvil/anvild.sock version --json",
	},
}

var jobsHelp = commandHelp{
	name:        "jobs",
	description: "List jobs in the queue.",
	usage:       "anvilctl jobs [--library NAME] [--state S,...] [--path P] [--absolute-path P] [--current-only] [--limit N] [--with-selection]",
	options: []helpOption{
		{flag: "--library NAME", description: "filter by configured library", defaultValue: `""`},
		{flag: "--state S,...", description: "filter by comma-separated job states", defaultValue: `""`},
		{flag: "--path P", description: "match one exact library-relative source or media path", defaultValue: `""`},
		{flag: "--absolute-path P", description: "match one exact absolute source, asset, or destination path", defaultValue: `""`},
		{flag: "--current-only", description: "restrict to current source and asset occurrences", defaultValue: "false"},
		{flag: "--limit N", description: "maximum jobs to return; 0 means no limit", defaultValue: "0"},
		{flag: "--with-selection", description: "include recorded audio and subtitle stream decisions", defaultValue: "false"},
	},
	notes: []string{
		"Selectors combine to narrow the result. --path requires --library and cannot be used with --absolute-path.",
		"Without --limit, listings return 20 jobs unless --path or --absolute-path makes an exact query. An explicit --limit 0 is unbounded.",
		"--absolute-path matches source, asset, and destination paths. The MATCHED column identifies the matching side.",
	},
	examples: []string{
		"anvilctl jobs",
		"anvilctl jobs --library movies --state pending,failed",
		"anvilctl jobs --absolute-path /mnt/media/converted/Release/Episode.mkv",
		"anvilctl jobs --library movies --with-selection --limit 5",
	},
}

var showHelp = commandHelp{
	name:        "show",
	description: "Show the full recorded history of one job.",
	usage:       "anvilctl show JOB",
	notes: []string{
		"JOB is a numeric id or job slug.",
	},
	examples: []string{
		"anvilctl show 42",
		"anvilctl show still-foggy-otter --json",
	},
}

var cancelHelp = commandHelp{
	name:        "cancel",
	description: "Cancel selected jobs.",
	usage:       "anvilctl cancel [JOB...] [--library NAME] [--state S,...] [--path P] [--absolute-path P] [--current-only] [--reason R]",
	options: []helpOption{
		{flag: "--library NAME", description: "filter by configured library", defaultValue: `""`},
		{flag: "--state S,...", description: "filter by comma-separated job states", defaultValue: `""`},
		{flag: "--path P", description: "match one exact library-relative source or media path", defaultValue: `""`},
		{flag: "--absolute-path P", description: "match one exact absolute source, asset, or destination path", defaultValue: `""`},
		{flag: "--current-only", description: "restrict to current source and asset occurrences", defaultValue: "false"},
		{flag: "--reason R", description: "record a reason on canceled jobs", defaultValue: `""`},
	},
	notes: []string{
		"JOB is a numeric id or job slug. Job arguments and selectors combine to narrow the result.",
		"At least one JOB or narrowing selector is required. --current-only only refines another selector and is not one by itself.",
		"--path requires --library and cannot be used with --absolute-path.",
		"A matched job that is not canceled reports a SKIPPED reason, such as publish_in_progress.",
	},
	examples: []string{
		"anvilctl cancel 42",
		"anvilctl cancel 42 sleepy-otter --reason duplicate",
		"anvilctl cancel --library usenet-tv --state pending,running",
		"anvilctl cancel --library movies --path Release/Episode.mkv",
	},
}

var retryHelp = commandHelp{
	name:        "retry",
	description: "Requeue failed jobs.",
	usage:       "anvilctl retry [JOB...] [--failed [--library NAME]]",
	options: []helpOption{
		{flag: "--failed", description: "retry every failed job", defaultValue: "false"},
		{flag: "--library NAME", description: "limit --failed to one configured library", defaultValue: `""`},
	},
	notes: []string{
		"JOB is a numeric id or job slug. JOB arguments and --failed combine: the named jobs are retried in addition to every failed job.",
	},
	examples: []string{
		"anvilctl retry 42 sleepy-otter",
		"anvilctl retry --failed --library movies",
	},
}

var pruneHelp = commandHelp{
	name:        "prune",
	description: "Delete terminal jobs whose source is missing.",
	usage:       "anvilctl prune [--library NAME] [--state S,...] [--apply]",
	options: []helpOption{
		{flag: "--library NAME", description: "limit pruning to one configured library", defaultValue: `""`},
		{flag: "--state S,...", description: "limit to terminal states; the daemon defaults to complete, failed, skipped, and canceled", defaultValue: `""`},
		{flag: "--apply", description: "delete matching jobs instead of reporting them", defaultValue: "false"},
	},
	notes: []string{
		"Without --apply, prune is a dry run.",
		"Active jobs and jobs with unresolved publish journals are never touched; they are reported as protected.",
	},
	examples: []string{
		"anvilctl prune --library movies",
		"anvilctl prune --library movies --state complete,failed,canceled --apply",
	},
}

var recoverHelp = commandHelp{
	name:        "recover",
	description: "Release stale job leases.",
	usage:       "anvilctl recover",
	examples: []string{
		"anvilctl recover",
		"anvilctl recover --json",
	},
}

var scanHelp = commandHelp{
	name:        "scan",
	description: "Scan configured libraries now.",
	usage:       "anvilctl scan [LIBRARY]",
	notes: []string{
		"Without LIBRARY, scan every configured library. LIBRARY is positional; scan does not accept --library.",
	},
	examples: []string{
		"anvilctl scan",
		"anvilctl scan movies",
	},
}

var statsHelp = commandHelp{
	name:        "stats",
	description: "Show per-library size savings.",
	usage:       "anvilctl stats [LIBRARY]",
	notes: []string{
		"Without LIBRARY, show every configured library. LIBRARY is positional; stats does not accept --library.",
	},
	examples: []string{
		"anvilctl stats",
		"anvilctl stats movies --json",
	},
}

var requeueHelp = commandHelp{
	name:        "requeue",
	description: "Create the next occurrence for one media path and enqueue it.",
	usage:       "anvilctl requeue --library NAME PATH",
	options: []helpOption{
		{flag: "--library NAME", description: "configured library containing PATH", defaultValue: `""`},
	},
	notes: []string{
		"--library is required. PATH is relative to that library root.",
	},
	examples: []string{
		"anvilctl requeue --library movies Release/Episode.mkv",
		"anvilctl requeue --library usenet-tv Incoming/Season-01/episode.mkv --json",
	},
}

var stagingCleanupHelp = commandHelp{
	name:        "staging cleanup",
	description: "Remove stale Anvil staging directories.",
	usage:       "anvilctl staging cleanup [--older-than DURATION] [--dry-run]",
	options: []helpOption{
		{flag: "--older-than DURATION", description: "remove directories older than this age; an empty value uses daemon.staging_cleanup_age", defaultValue: `""`},
		{flag: "--dry-run", description: "show cleanup candidates without deleting them", defaultValue: "false"},
	},
	notes: []string{
		"Active jobs and jobs with unresolved publish journals are never touched; they are reported as protected.",
	},
	examples: []string{
		"anvilctl staging cleanup --dry-run",
		"anvilctl staging cleanup --older-than 24h",
	},
}

var backupHelp = commandHelp{
	name:        "backup",
	description: "Write a consistent SQLite snapshot.",
	usage:       "anvilctl backup DESTINATION",
	notes: []string{
		"DESTINATION is resolved to an absolute path by the client before the daemon writes the snapshot.",
	},
	examples: []string{
		"anvilctl backup /srv/backups/anvil.db",
		"anvilctl backup ./anvil-$(date +%F).db",
	},
}

var helpHelp = commandHelp{
	name:        "help",
	description: "Show command help.",
	usage:       "anvilctl help [COMMAND]",
	notes: []string{
		"Use two words for the staging cleanup command: anvilctl help staging cleanup.",
	},
	examples: []string{
		"anvilctl help",
		"anvilctl help jobs",
		"anvilctl help staging cleanup",
	},
}

func writeUsage(out io.Writer) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Println("anvilctl controls a running anvild over its Unix control socket.")
		w.Println()
		w.Println("Usage:")
		w.Println("  anvilctl [GLOBAL] COMMAND [OPTIONS]")
		w.Println()
		w.Println("Commands:")
		w.Println("  status                 show daemon state, worker usage, and queue counts")
		w.Println("  version                show client, daemon, and protocol versions")
		w.Println("  jobs                   list jobs")
		w.Println("  show JOB               show the full recorded history of one job")
		w.Println("  cancel [JOB...]        cancel selected jobs")
		w.Println("  retry [JOB...]         requeue failed jobs")
		w.Println("  prune                  delete terminal jobs whose source is missing")
		w.Println("  recover                release stale job leases")
		w.Println("  scan [LIBRARY]         scan configured libraries now")
		w.Println("  stats [LIBRARY]        show per-library size savings")
		w.Println("  requeue --library N P  create the next occurrence for one media path")
		w.Println("  staging cleanup        remove stale Anvil staging directories")
		w.Println("  backup DESTINATION     write a consistent SQLite snapshot")
		w.Println("  help [COMMAND]         show command help")
		w.Println()
		writeGlobalOptions(w)
		w.Println()
		w.Println("Exit status:")
		w.Println("  0  success")
		w.Println("  1  command failed")
		w.Println("  2  usage or argument error")
		w.Println("  3  daemon unreachable or protocol version mismatch")
		w.Println("  4  job, library, or path not found")
		w.Println()
		w.Println("Run \"anvilctl help COMMAND\" for options, notes, and examples.")
	})
}

func runHelp(args []string, out io.Writer) error {
	if len(args) == 0 {
		return writeUsage(out)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return writeCommandHelp(out, helpHelp)
	}
	help, ok := commandHelpFor(args)
	if !ok {
		return usagef("unknown command %q; run \"anvilctl help\"", strings.Join(args, " "))
	}
	return writeCommandHelp(out, help)
}

func commandHelpFor(args []string) (commandHelp, bool) {
	switch strings.Join(args, " ") {
	case "status":
		return statusHelp, true
	case "version":
		return versionHelp, true
	case "jobs":
		return jobsHelp, true
	case "show":
		return showHelp, true
	case "cancel":
		return cancelHelp, true
	case "retry":
		return retryHelp, true
	case "prune":
		return pruneHelp, true
	case "recover":
		return recoverHelp, true
	case "scan":
		return scanHelp, true
	case "stats":
		return statsHelp, true
	case "requeue":
		return requeueHelp, true
	case "staging cleanup":
		return stagingCleanupHelp, true
	case "backup":
		return backupHelp, true
	case "help":
		return helpHelp, true
	default:
		return commandHelp{}, false
	}
}

func writeCommandHelp(out io.Writer, help commandHelp) error {
	return textout.Write(out, func(w *textout.Writer) {
		w.Println(help.description)
		w.Println()
		w.Println("Usage:")
		w.Printf("  %s\n", help.usage)
		w.Println()
		w.Println("Options:")
		w.Println("  -h, --help")
		w.Println("      show this help")
		for _, option := range help.options {
			w.Printf("  %s\n", option.flag)
			w.Printf("      %s (default: %s)\n", option.description, option.defaultValue)
		}
		w.Println()
		writeGlobalOptions(w)
		if len(help.notes) > 0 {
			w.Println()
			w.Println("Notes:")
			for _, note := range help.notes {
				w.Printf("  %s\n", note)
			}
		}
		w.Println()
		w.Println("Examples:")
		for _, example := range help.examples {
			w.Printf("  %s\n", example)
		}
	})
}

func writeGlobalOptions(w *textout.Writer) {
	w.Println("Global options (before COMMAND):")
	w.Println("  --socket PATH")
	w.Println("      control socket (default: $ANVIL_CONTROL_SOCKET or /run/anvil/anvild.sock)")
	w.Println("  --timeout DURATION")
	w.Println("      override the per-command deadline (default: 0s)")
	w.Println("  -j, --json")
	w.Println("      write JSON output; also accepted after COMMAND (default: false)")
}
