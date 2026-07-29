package main

import (
	"context"
	"flag"
	"path/filepath"
	"strings"

	"github.com/zekurio/anvil/pkg/controlapi"
)

func runLibrary(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return writeLibraryUsage(e.out)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "help":
		return writeLibraryUsage(e.out)
	case "scan":
		return runLibraryScan(ctx, e, rest)
	case "stats":
		return runLibraryStats(ctx, e, rest)
	default:
		return usagef("unknown library command %q", verb)
	}
}

func runLibraryScan(ctx context.Context, e *env, args []string) error {
	var library string
	flags, positional, err := e.subcommand("library scan", args, func(f *flag.FlagSet) {
		f.StringVar(&library, "library", "", "scan one configured library")
	})
	if flags == nil {
		return err
	}
	name, err := libraryName("library scan", library, positional)
	if err != nil {
		return err
	}
	response, err := e.client.ScanLibraries(ctx, controlapi.LibraryScanRequest{Library: name})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeScanResult(e.out, response)
}

func runLibraryStats(ctx context.Context, e *env, args []string) error {
	var library string
	flags, positional, err := e.subcommand("library stats", args, func(f *flag.FlagSet) {
		f.StringVar(&library, "library", "", "filter by configured library")
	})
	if flags == nil {
		return err
	}
	name, err := libraryName("library stats", library, positional)
	if err != nil {
		return err
	}
	response, err := e.client.LibraryStats(ctx, controlapi.LibraryStatsRequest{Library: name})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeLibraryStats(e.out, response)
}

// libraryName accepts a library as a positional name or as --library. Both read
// naturally, and the old anvild commands used the flag, so refusing one of them
// would only make an operator retype a correct request.
func libraryName(command string, flagValue string, positional []string) (string, error) {
	switch {
	case len(positional) > 1:
		return "", usagef("%s accepts at most one library name: %v", command, positional)
	case len(positional) == 1 && strings.TrimSpace(flagValue) != "":
		return "", usagef("%s takes a library name or --library, not both", command)
	case len(positional) == 1:
		return strings.TrimSpace(positional[0]), nil
	default:
		return strings.TrimSpace(flagValue), nil
	}
}

func runOccurrence(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return writeOccurrenceUsage(e.out)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "help":
		return writeOccurrenceUsage(e.out)
	case "force":
		return runOccurrenceForce(ctx, e, rest)
	default:
		return usagef("unknown occurrence command %q", verb)
	}
}

func runOccurrenceForce(ctx context.Context, e *env, args []string) error {
	var library string
	flags, positional, err := e.subcommand("occurrence force", args, func(f *flag.FlagSet) {
		f.StringVar(&library, "library", "", "configured library containing the target")
	})
	if flags == nil {
		return err
	}
	if strings.TrimSpace(library) == "" {
		return usagef("occurrence force requires --library")
	}
	if len(positional) != 1 {
		return usagef("occurrence force requires exactly one library-relative path")
	}
	response, err := e.client.ForceOccurrence(ctx, controlapi.ForceOccurrenceRequest{
		Library: library, Path: positional[0],
	})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeForcedOccurrence(e.out, response)
}

func runStaging(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return writeStagingUsage(e.out)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "help":
		return writeStagingUsage(e.out)
	case "cleanup":
		return runStagingCleanup(ctx, e, rest)
	default:
		return usagef("unknown staging command %q", verb)
	}
}

func runStagingCleanup(ctx context.Context, e *env, args []string) error {
	var olderThan string
	var dryRun bool
	flags, positional, err := e.subcommand("staging cleanup", args, func(f *flag.FlagSet) {
		f.StringVar(&olderThan, "older-than", "", "remove Anvil staging dirs older than this duration; defaults to daemon.staging_cleanup_age")
		f.BoolVar(&dryRun, "dry-run", false, "show cleanup candidates without deleting them")
	})
	if flags == nil {
		return err
	}
	if err := noArguments("staging cleanup", positional); err != nil {
		return err
	}
	response, err := e.client.CleanupStaging(ctx, controlapi.StagingCleanupRequest{
		OlderThan: olderThan, DryRun: dryRun,
	})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeStagingCleanup(e.out, e.errOut, response)
}

func runStore(ctx context.Context, e *env, args []string) error {
	if len(args) == 0 {
		return writeStoreUsage(e.out)
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "help":
		return writeStoreUsage(e.out)
	case "backup":
		return runStoreBackup(ctx, e, rest)
	default:
		return usagef("unknown store command %q", verb)
	}
}

func runStoreBackup(ctx context.Context, e *env, args []string) error {
	flags, positional, err := e.subcommand("store backup", args, nil)
	if flags == nil {
		return err
	}
	if len(positional) != 1 {
		return usagef("store backup requires exactly one destination path")
	}
	// The daemon writes the file, and its working directory is not the
	// operator's, so the path is made absolute here where "relative to what" is
	// still answerable.
	destination, err := filepath.Abs(positional[0])
	if err != nil {
		return usagef("resolve backup destination %q: %v", positional[0], err)
	}
	response, err := e.client.BackupStore(ctx, controlapi.StoreBackupRequest{Destination: destination})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeBackup(e.out, response)
}
