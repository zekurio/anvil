package main

import (
	"context"
	"flag"
	"path/filepath"
	"strings"

	"github.com/zekurio/anvil/pkg/control"
)

func runScan(ctx context.Context, e *env, args []string) error {
	flags, positional, err := e.subcommand(scanHelp, args, nil)
	if flags == nil {
		return err
	}
	if len(positional) > 1 {
		return usagef("scan accepts at most one library name: %v", positional)
	}
	library := ""
	if len(positional) == 1 {
		library = strings.TrimSpace(positional[0])
	}
	response, err := e.client.ScanLibraries(ctx, control.LibraryScanRequest{Library: library})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeScanResult(e.out, response)
}

func runStats(ctx context.Context, e *env, args []string) error {
	flags, positional, err := e.subcommand(statsHelp, args, nil)
	if flags == nil {
		return err
	}
	if len(positional) > 1 {
		return usagef("stats accepts at most one library name: %v", positional)
	}
	library := ""
	if len(positional) == 1 {
		library = strings.TrimSpace(positional[0])
	}
	response, err := e.client.LibraryStats(ctx, control.LibraryStatsRequest{Library: library})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeLibraryStats(e.out, response)
}

func runRequeue(ctx context.Context, e *env, args []string) error {
	var library string
	flags, positional, err := e.subcommand(requeueHelp, args, func(f *flag.FlagSet) {
		f.StringVar(&library, "library", "", "configured library containing the target")
	})
	if flags == nil {
		return err
	}
	if strings.TrimSpace(library) == "" {
		return usagef("requeue requires --library")
	}
	if len(positional) != 1 {
		return usagef("requeue requires exactly one library-relative path")
	}
	response, err := e.client.ForceOccurrence(ctx, control.ForceOccurrenceRequest{
		Library: library, Path: positional[0],
	})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeRequeuedOccurrence(e.out, response)
}

func runStaging(ctx context.Context, e *env, args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return writeCommandHelp(e.out, stagingCleanupHelp)
	}
	if len(args) == 0 {
		return usagef("staging requires \"cleanup\"; run \"anvilctl help staging cleanup\"")
	}
	if args[0] != "cleanup" {
		return usagef("unknown staging command %q; run \"anvilctl help staging cleanup\"", args[0])
	}
	return runStagingCleanup(ctx, e, args[1:])
}

func runStagingCleanup(ctx context.Context, e *env, args []string) error {
	var olderThan string
	var dryRun bool
	var legacyParts bool
	flags, positional, err := e.subcommand(stagingCleanupHelp, args, func(f *flag.FlagSet) {
		f.StringVar(&olderThan, "older-than", "", "remove Anvil staging dirs older than this duration; defaults to daemon.staging_cleanup_age")
		f.BoolVar(&dryRun, "dry-run", false, "show cleanup candidates without deleting them")
		f.BoolVar(&legacyParts, "legacy-parts", false, "also sweep old unscoped artifacts in configured output libraries")
	})
	if flags == nil {
		return err
	}
	if err := noArguments("staging cleanup", positional); err != nil {
		return err
	}
	response, err := e.client.CleanupStaging(ctx, control.StagingCleanupRequest{
		OlderThan: olderThan, DryRun: dryRun, LegacyParts: legacyParts,
	})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeStagingCleanup(e.out, e.errOut, response)
}

func runBackup(ctx context.Context, e *env, args []string) error {
	flags, positional, err := e.subcommand(backupHelp, args, nil)
	if flags == nil {
		return err
	}
	if len(positional) != 1 {
		return usagef("backup requires exactly one destination path")
	}
	// The daemon writes the file, and its working directory is not the
	// operator's, so the path is made absolute here where "relative to what" is
	// still answerable.
	destination, err := filepath.Abs(positional[0])
	if err != nil {
		return usagef("resolve backup destination %q: %v", positional[0], err)
	}
	response, err := e.client.BackupStore(ctx, control.StoreBackupRequest{Destination: destination})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeBackup(e.out, response)
}
