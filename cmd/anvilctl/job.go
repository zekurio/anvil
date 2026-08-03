package main

import (
	"context"
	"flag"
	"strings"

	"github.com/zekurio/anvil/pkg/control"
)

func runStatus(ctx context.Context, e *env, args []string) error {
	flags, positional, err := e.subcommand(statusHelp, args, nil)
	if flags == nil {
		return err
	}
	if err := noArguments("status", positional); err != nil {
		return err
	}
	response, err := e.client.Status(ctx)
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeStatus(e.out, response)
}

func runJobs(ctx context.Context, e *env, args []string) error {
	var library, path, absolutePath, states string
	var currentOnly, withSelection bool
	var limit int
	flags, positional, err := e.subcommand(jobsHelp, args, func(f *flag.FlagSet) {
		f.StringVar(&library, "library", "", "filter by configured library")
		f.StringVar(&path, "path", "", "exact library-relative source or media path")
		f.StringVar(&absolutePath, "absolute-path", "", "exact absolute source, asset, or destination path")
		f.StringVar(&states, "state", "", "comma-separated job states")
		f.BoolVar(&currentOnly, "current-only", false, "restrict to current source and asset occurrences")
		f.IntVar(&limit, "limit", 0, "maximum jobs to return; 0 means no limit")
		f.BoolVar(&withSelection, "with-selection", false, "include the recorded audio and subtitle stream selection")
	})
	if flags == nil {
		return err
	}
	if err := noArguments("jobs", positional); err != nil {
		return err
	}
	limitSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "limit" {
			limitSet = true
		}
	})
	// An unbounded listing is only useful when the operator already narrowed it
	// to an exact path; otherwise the default keeps a browse from scrolling the
	// whole queue past them.
	if !limitSet && strings.TrimSpace(path) == "" && strings.TrimSpace(absolutePath) == "" {
		limit = 20
	}
	response, err := e.client.ListJobs(ctx, control.JobQuery{
		Library: library, Path: path, AbsolutePath: absolutePath,
		States: splitStates(states), CurrentOnly: currentOnly, Limit: limit,
		WithSelection: withSelection,
	})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeJobs(e.out, response)
}

func runShow(ctx context.Context, e *env, args []string) error {
	flags, positional, err := e.subcommand(showHelp, args, nil)
	if flags == nil {
		return err
	}
	if len(positional) != 1 {
		return usagef("show requires exactly one job id or slug")
	}
	response, err := e.client.ShowJob(ctx, control.JobShowRequest{Reference: positional[0]})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeJobShow(e.out, response)
}

func runCancel(ctx context.Context, e *env, args []string) error {
	var library, path, absolutePath, states, reason string
	var currentOnly bool
	flags, positional, err := e.subcommand(cancelHelp, args, func(f *flag.FlagSet) {
		f.StringVar(&library, "library", "", "filter by configured library")
		f.StringVar(&path, "path", "", "exact library-relative source or media path")
		f.StringVar(&absolutePath, "absolute-path", "", "exact absolute source, asset, or destination path")
		f.StringVar(&states, "state", "", "comma-separated job states")
		f.BoolVar(&currentOnly, "current-only", false, "restrict to current source and asset occurrences")
		f.StringVar(&reason, "reason", "", "reason recorded on the canceled jobs")
	})
	if flags == nil {
		return err
	}
	request := control.JobCancelRequest{
		Library: library, Path: path, AbsolutePath: absolutePath,
		States: splitStates(states), CurrentOnly: currentOnly,
		References: positional, Reason: reason,
	}
	if !request.HasSelector() {
		return usagef("cancel requires at least one job or narrowing selector")
	}
	response, err := e.client.CancelJobs(ctx, request)
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeCanceledJobs(e.out, response)
}

func runRetry(ctx context.Context, e *env, args []string) error {
	var library string
	var failed bool
	flags, positional, err := e.subcommand(retryHelp, args, func(f *flag.FlagSet) {
		f.BoolVar(&failed, "failed", false, "retry every failed job")
		f.StringVar(&library, "library", "", "limit --failed to one library")
	})
	if flags == nil {
		return err
	}
	if !failed && len(positional) == 0 {
		return usagef("retry requires job ids or slugs, or --failed")
	}
	response, err := e.client.RetryJobs(ctx, control.JobRetryRequest{
		References: positional, Failed: failed, Library: library,
	})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeRetriedJobs(e.out, response)
}

func runPrune(ctx context.Context, e *env, args []string) error {
	var library, states string
	var apply bool
	flags, positional, err := e.subcommand(pruneHelp, args, func(f *flag.FlagSet) {
		f.StringVar(&library, "library", "", "limit pruning to one library")
		f.StringVar(&states, "state", "", "comma-separated terminal job states; defaults to complete,failed,skipped,canceled")
		f.BoolVar(&apply, "apply", false, "delete matching jobs; without this flag the command is a dry run")
	})
	if flags == nil {
		return err
	}
	if err := noArguments("prune", positional); err != nil {
		return err
	}
	response, err := e.client.PruneJobs(ctx, control.JobPruneRequest{
		Library: library, States: splitStates(states), Apply: apply,
	})
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writePrunedJobs(e.out, response)
}

func runRecover(ctx context.Context, e *env, args []string) error {
	flags, positional, err := e.subcommand(recoverHelp, args, nil)
	if flags == nil {
		return err
	}
	if err := noArguments("recover", positional); err != nil {
		return err
	}
	response, err := e.client.RecoverJobs(ctx)
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeRecoveredJobs(e.out, response)
}

// splitStates keeps the comma-separated flag value intact; the daemon splits and
// validates it, so the two never disagree about what "pending, failed" means.
func splitStates(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return []string{value}
}
