package main

import (
	"context"
	"os"
	"sort"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/store"
)

func runBackupCommand(ctx context.Context, cfg config.Config, opts options) error {
	state, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)

	result, err := state.Backup(ctx, opts.backupPath)
	if err != nil {
		return err
	}
	return writeOutput(os.Stdout, func(w *outputWriter) {
		w.printf("backup=%s size_bytes=%d integrity=%s\n", result.Path, result.SizeBytes, result.Integrity)
	})
}

func runPruneJobsCommand(ctx context.Context, cfg config.Config, opts options) error {
	state, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStore(state)

	result, err := state.PruneMissingSourceJobs(ctx, store.PruneMissingSourceJobsOptions{
		LibraryName: domain.LibraryName(opts.libraryName),
		States:      opts.jobStates,
		Apply:       opts.pruneApply,
	})
	if err != nil {
		return err
	}
	return writeOutput(os.Stdout, func(w *outputWriter) {
		w.printf("dry_run=%t matched_jobs=%d affected_sources=%d deleted_jobs=%d", result.DryRun, result.MatchedJobs, result.AffectedSources, result.DeletedJobs)
		states := make([]string, 0, len(result.ByState))
		for state := range result.ByState {
			states = append(states, string(state))
		}
		sort.Strings(states)
		for _, state := range states {
			w.printf(" state_%s=%d", state, result.ByState[domain.JobState(state)])
		}
		w.println()
	})
}
