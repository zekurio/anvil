package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/zekurio/anvil/pkg/controlapi"
)

const defaultControlSocket = "/run/anvil/anvild.sock"

type options struct {
	socketPath   string
	jsonOutput   bool
	library      string
	path         string
	absolutePath string
	states       string
	currentOnly  bool
	limit        int
	reason       string
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "anvilctl: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	opts, command, remaining, err := parseGlobal(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return writeUsage(stdout)
	}
	if err != nil {
		return err
	}
	if command == "" || command == "help" {
		return writeUsage(stdout)
	}
	client, err := controlapi.NewClient(opts.socketPath)
	if err != nil {
		return err
	}
	switch command {
	case "status":
		return runStatus(ctx, client, opts, remaining, stdout, stderr)
	case "job":
		return runJob(ctx, client, opts, remaining, stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func parseGlobal(args []string, stderr io.Writer) (options, string, []string, error) {
	opts := options{socketPath: defaultSocketPath()}
	flags := flag.NewFlagSet("anvilctl", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.socketPath, "socket", opts.socketPath, "path to the anvild control socket")
	if err := flags.Parse(args); err != nil {
		return options{}, "", nil, err
	}
	remaining := flags.Args()
	if len(remaining) == 0 {
		return opts, "", nil, nil
	}
	return opts, remaining[0], remaining[1:], nil
}

func runStatus(ctx context.Context, client *controlapi.Client, opts options, args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("anvilctl status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.BoolVar(&opts.jsonOutput, "json", false, "write JSON output")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return writeStatusUsage(stdout)
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("status does not accept arguments: %v", flags.Args())
	}
	response, err := client.Status(ctx)
	if err != nil {
		return err
	}
	if opts.jsonOutput {
		return writeJSON(stdout, response)
	}
	return writeStatus(stdout, response)
}

func runJob(ctx context.Context, client *controlapi.Client, opts options, args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		return writeJobUsage(stdout)
	}
	switch args[0] {
	case "list":
		return runJobList(ctx, client, opts, args[1:], stdout, stderr)
	case "cancel":
		return runJobCancel(ctx, client, opts, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown job command %q", args[0])
	}
}

func runJobList(ctx context.Context, client *controlapi.Client, opts options, args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("anvilctl job list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.library, "library", "", "filter by configured library")
	flags.StringVar(&opts.path, "path", "", "exact library-relative source or media path")
	flags.StringVar(&opts.absolutePath, "absolute-path", "", "exact absolute source, asset, or destination path")
	flags.StringVar(&opts.states, "state", "", "comma-separated job states")
	flags.BoolVar(&opts.currentOnly, "current-only", false, "restrict to current source and asset occurrences")
	flags.IntVar(&opts.limit, "limit", 0, "maximum jobs to return; 0 means no limit")
	flags.BoolVar(&opts.jsonOutput, "json", false, "write JSON output")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return writeJobListUsage(stdout)
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("job list does not accept arguments: %v", flags.Args())
	}
	limitSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "limit" {
			limitSet = true
		}
	})
	if !limitSet && strings.TrimSpace(opts.path) == "" && strings.TrimSpace(opts.absolutePath) == "" {
		opts.limit = 20
	}
	query := controlapi.JobQuery{
		Library: opts.library, Path: opts.path, AbsolutePath: opts.absolutePath,
		States: splitStates(opts.states), CurrentOnly: opts.currentOnly, Limit: opts.limit,
	}
	response, err := client.ListJobs(ctx, query)
	if err != nil {
		return err
	}
	if opts.jsonOutput {
		return writeJSON(stdout, response)
	}
	return writeJobs(stdout, response)
}

func runJobCancel(ctx context.Context, client *controlapi.Client, opts options, args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("anvilctl job cancel", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.library, "library", "", "filter by configured library")
	flags.StringVar(&opts.path, "path", "", "exact library-relative source or media path")
	flags.StringVar(&opts.absolutePath, "absolute-path", "", "exact absolute source, asset, or destination path")
	flags.StringVar(&opts.states, "state", "", "comma-separated job states")
	flags.BoolVar(&opts.currentOnly, "current-only", false, "restrict to current source and asset occurrences")
	flags.StringVar(&opts.reason, "reason", "", "reason recorded on the canceled jobs")
	flags.BoolVar(&opts.jsonOutput, "json", false, "write JSON output")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return writeJobCancelUsage(stdout)
		}
		return err
	}
	ids, err := parseJobIDs(flags.Args())
	if err != nil {
		return err
	}
	request := controlapi.JobCancelRequest{
		Library: opts.library, Path: opts.path, AbsolutePath: opts.absolutePath,
		States: splitStates(opts.states), CurrentOnly: opts.currentOnly,
		IDs: ids, Reason: opts.reason,
	}
	response, err := client.CancelJobs(ctx, request)
	if err != nil {
		return err
	}
	if opts.jsonOutput {
		return writeJSON(stdout, response)
	}
	return writeCanceledJobs(stdout, response)
}

func parseJobIDs(args []string) ([]int64, error) {
	ids := make([]int64, 0, len(args))
	for _, arg := range args {
		id, err := strconv.ParseInt(strings.TrimSpace(arg), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid job id %q", arg)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func splitStates(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return []string{value}
}

func defaultSocketPath() string {
	if value := strings.TrimSpace(os.Getenv("ANVIL_CONTROL_SOCKET")); value != "" {
		return value
	}
	return defaultControlSocket
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}

func writeStatus(out io.Writer, response controlapi.StatusResponse) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintf(w, "DAEMON\t%s\nSTARTED\t%s\nVERSION\t%s\nWORKERS\t%d/%d active\n",
		response.Daemon.State,
		response.Daemon.StartedAt.Format("2006-01-02T15:04:05Z07:00"),
		response.Daemon.Version,
		response.Workers.Active,
		response.Workers.Configured,
	); err != nil {
		return err
	}
	return w.Flush()
}

func writeJobs(out io.Writer, response controlapi.JobListResponse) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "JOB\tID\tSTATE\tLIBRARY\tUPDATED\tSOURCE\tDESTINATION\tERROR"); err != nil {
		return err
	}
	for _, job := range response.Jobs {
		if _, err := fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			job.Slug, job.ID, job.State, job.Library,
			job.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
			job.Source.AbsolutePath, job.DestinationPath, job.LastError,
		); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if response.Truncated {
		_, err := fmt.Fprintf(out, "showing %d of %d matching jobs\n", len(response.Jobs), response.Matched)
		return err
	}
	return nil
}

func writeCanceledJobs(out io.Writer, response controlapi.JobCancelResponse) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "JOB\tID\tLIBRARY\tPREVIOUS\tSTATE\tCANCELED\tWORKER SIGNALED\tSKIPPED"); err != nil {
		return err
	}
	for _, job := range response.Jobs {
		if _, err := fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%t\t%t\t%s\n",
			job.Slug, job.ID, job.Library, job.PreviousState, job.State, job.Canceled, job.WorkerSignaled, job.SkipReason,
		); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "canceled %d of %d matching jobs\n", response.Canceled, response.Matched)
	return err
}

func writeUsage(out io.Writer) error {
	_, err := fmt.Fprintln(out, `Usage:
  anvilctl [--socket PATH] status [--json]
  anvilctl [--socket PATH] job list [--library NAME] [--path PATH | --absolute-path PATH]
           [--state STATE,...] [--current-only] [--limit N] [--json]
  anvilctl [--socket PATH] job cancel [--library NAME] [--path PATH | --absolute-path PATH]
           [--state STATE,...] [--current-only] [--reason TEXT] [--json] [JOB_ID...]

Job cancellation requires at least one narrowing selector, so a bare "job cancel"
is rejected; --current-only only refines another selector and is not one itself.
A matched job that was not canceled is reported with a SKIPPED reason such as
publish_in_progress.`)
	return err
}

func writeStatusUsage(out io.Writer) error {
	_, err := fmt.Fprintln(out, "Usage: anvilctl [--socket PATH] status [--json]")
	return err
}

func writeJobUsage(out io.Writer) error {
	_, err := fmt.Fprintln(out, "Usage: anvilctl [--socket PATH] job list|cancel [OPTIONS]")
	return err
}

func writeJobListUsage(out io.Writer) error {
	return writeUsage(out)
}

func writeJobCancelUsage(out io.Writer) error {
	return writeUsage(out)
}
