// Command anvilctl is the operator client for a running anvild. It never opens
// the SQLite store and never runs ffmpeg or ab-av1: it asks the daemon that
// owns them over daemon.control_socket, the way systemctl asks systemd. That
// boundary is the point — two processes writing Anvil's database while jobs
// are running is how half-published files happen.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/control"
)

const defaultControlSocket = "/run/anvil/anvild.sock"

// Exit codes are part of the contract, so scripts can branch without parsing
// messages.
const (
	exitOK          = 0
	exitFailed      = 1
	exitUsage       = 2
	exitUnavailable = 3
	exitNotFound    = 4
)

// env carries everything a command needs that is not its own arguments.
type env struct {
	client *control.Client
	out    io.Writer
	errOut io.Writer
	json   bool
}

type options struct {
	socketPath string
	timeout    time.Duration
	json       bool
}

func main() {
	err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	if err == nil {
		os.Exit(exitOK)
	}
	fmt.Fprintf(os.Stderr, "anvilctl: %v\n", err)
	os.Exit(exitCode(err))
}

// exitCode maps the daemon's stable error codes onto exit status. Anything the
// daemon did not classify is a plain failure.
func exitCode(err error) int {
	var usage usageError
	if errors.As(err, &usage) {
		return exitUsage
	}
	var controlErr *control.Error
	if !errors.As(err, &controlErr) {
		return exitFailed
	}
	switch controlErr.Code {
	case control.CodeInvalidArgument, control.CodeUnsupported:
		return exitUsage
	case control.CodeUnavailable, control.CodeVersionMismatch:
		return exitUnavailable
	case control.CodeNotFound:
		return exitNotFound
	default:
		return exitFailed
	}
}

// usageError marks a mistake in the command line itself, so it exits with the
// same status as an argument the daemon rejects.
type usageError struct {
	err error
}

func (e usageError) Error() string {
	return e.err.Error()
}

func (e usageError) Unwrap() error {
	return e.err
}

func usagef(format string, args ...any) error {
	return usageError{err: fmt.Errorf(format, args...)}
}

func run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	opts, remaining, err := parseGlobal(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return writeUsage(stdout)
	}
	if err != nil {
		return usageError{err: err}
	}
	if len(remaining) == 0 {
		return writeUsage(stdout)
	}
	if remaining[0] == "help" {
		return runHelp(remaining[1:], stdout)
	}

	client, err := control.NewClient(opts.socketPath)
	if err != nil {
		return usageError{err: err}
	}
	client.Timeout = opts.timeout
	environment := &env{client: client, out: stdout, errOut: stderr, json: opts.json}

	command, rest := remaining[0], remaining[1:]
	switch command {
	case "status":
		return runStatus(ctx, environment, rest)
	case "version":
		return runVersion(ctx, environment, rest)
	case "jobs":
		return runJobs(ctx, environment, rest)
	case "show":
		return runShow(ctx, environment, rest)
	case "cancel":
		return runCancel(ctx, environment, rest)
	case "retry":
		return runRetry(ctx, environment, rest)
	case "prune":
		return runPrune(ctx, environment, rest)
	case "recover":
		return runRecover(ctx, environment, rest)
	case "scan":
		return runScan(ctx, environment, rest)
	case "stats":
		return runStats(ctx, environment, rest)
	case "requeue":
		return runRequeue(ctx, environment, rest)
	case "staging":
		return runStaging(ctx, environment, rest)
	case "backup":
		return runBackup(ctx, environment, rest)
	default:
		return usagef("unknown command %q; run \"anvilctl help\"", command)
	}
}

func parseGlobal(args []string, stderr io.Writer) (options, []string, error) {
	opts := options{socketPath: defaultSocketPath()}
	flags := flag.NewFlagSet("anvilctl", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.socketPath, "socket", opts.socketPath, "path to the anvild control socket")
	flags.DurationVar(&opts.timeout, "timeout", 0, "override the per-command deadline; 0 uses the command's own default")
	flags.BoolVar(&opts.json, "json", false, "write JSON output")
	flags.BoolVar(&opts.json, "j", false, "write JSON output")
	flags.Usage = func() {}
	if err := flags.Parse(args); err != nil {
		return options{}, nil, err
	}
	if opts.timeout < 0 {
		return options{}, nil, errors.New("--timeout must not be negative")
	}
	return opts, flags.Args(), nil
}

// subcommand parses one leaf command's flags and returns its positional
// arguments. Every leaf gets --json and -j from the same place, so the flags
// exist everywhere and mean the same thing.
//
// Flags may follow positional arguments: "show 42 --json" is what an operator
// types, and stdlib flag parsing would otherwise stop at 42 and report the flag
// as a stray argument.
//
// A bare "--" still ends flag parsing for good. Anvil's arguments are file
// paths, job slugs, and library names, and any of them can legitimately begin
// with a dash; without an escape, such a name could only be passed by renaming
// the file.
func (e *env) subcommand(help commandHelp, args []string, register func(*flag.FlagSet)) (*flag.FlagSet, []string, error) {
	flags := flag.NewFlagSet("anvilctl "+help.name, flag.ContinueOnError)
	flags.SetOutput(e.errOut)
	flags.BoolVar(&e.json, "json", e.json, "write JSON output")
	flags.BoolVar(&e.json, "j", e.json, "write JSON output")
	flags.Usage = func() {}
	if register != nil {
		register(flags)
	}
	flagArgs, literal := splitAtTerminator(args)
	var positional []string
	for {
		if err := flags.Parse(flagArgs); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, nil, writeCommandHelp(e.out, help)
			}
			return nil, nil, usageError{err: err}
		}
		rest := flags.Args()
		if len(rest) == 0 {
			return flags, append(positional, literal...), nil
		}
		positional = append(positional, rest[0])
		flagArgs = rest[1:]
	}
}

// splitAtTerminator returns the arguments before the first bare "--" and the
// ones after it. The interleaved parse above re-parses what follows each
// positional argument, which would otherwise resurrect flag parsing for
// arguments the operator already terminated.
func splitAtTerminator(args []string) ([]string, []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// noArguments rejects stray positional arguments, so a mistyped flag is not
// silently swallowed as an argument the command ignores.
func noArguments(name string, positional []string) error {
	if len(positional) > 0 {
		return usagef("%s does not accept arguments: %v", name, positional)
	}
	return nil
}

func defaultSocketPath() string {
	if value := strings.TrimSpace(os.Getenv("ANVIL_CONTROL_SOCKET")); value != "" {
		return value
	}
	return defaultControlSocket
}

func runVersion(ctx context.Context, e *env, args []string) error {
	flags, positional, err := e.subcommand(versionHelp, args, nil)
	if flags == nil {
		return err
	}
	if err := noArguments("version", positional); err != nil {
		return err
	}
	// The daemon is contacted but a failure is reported inline rather than
	// returned: "which client am I running" has to be answerable while the
	// daemon is down, which is exactly when someone asks.
	response := versionReport{
		Client:          control.BuildVersion,
		ProtocolVersion: uint64(control.ProtocolVersion),
		APIVersion:      control.Version,
		Socket:          e.client.SocketPath(),
	}
	status, statusErr := e.client.Status(ctx)
	if statusErr != nil {
		response.DaemonError = statusErr.Error()
	} else {
		response.Daemon = status.Daemon.Version
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeVersion(e.out, response)
}

type versionReport struct {
	Client          string `json:"client"`
	Daemon          string `json:"daemon,omitempty"`
	DaemonError     string `json:"daemon_error,omitempty"`
	ProtocolVersion uint64 `json:"protocol_version"`
	APIVersion      string `json:"api_version"`
	Socket          string `json:"socket"`
}
