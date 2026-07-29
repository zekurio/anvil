// Command anvilctl is the operator client for a running anvild. It never opens
// the SQLite store and never runs ffmpeg, ab-av1, dovi_tool, or mkvtoolnix: it
// asks the daemon that owns them over daemon.control_socket, the way systemctl
// asks systemd. That boundary is the point — two processes writing Anvil's
// database while jobs are running is how half-published files happen.
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

	"github.com/zekurio/anvil/pkg/controlapi"
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
	client *controlapi.Client
	out    io.Writer
	errOut io.Writer
	json   bool
}

type options struct {
	socketPath string
	timeout    time.Duration
	json       bool
}

// aliases keep the command forms operators already type working after the noun
// and verb tree landed. The left side is what anvild's old subcommands were
// called; the right side is where that work lives now.
var aliases = map[string][]string{
	"jobs":             {"job", "list"},
	"inspect":          {"job", "show"},
	"cancel":           {"job", "cancel"},
	"retry":            {"job", "retry"},
	"recover":          {"job", "recover"},
	"prune-jobs":       {"job", "prune"},
	"scan":             {"library", "scan"},
	"stats":            {"library", "stats"},
	"force-occurrence": {"occurrence", "force"},
	"cleanup-staging":  {"staging", "cleanup"},
	"backup":           {"store", "backup"},
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
	var controlErr *controlapi.Error
	if !errors.As(err, &controlErr) {
		return exitFailed
	}
	switch controlErr.Code {
	case controlapi.CodeInvalidArgument, controlapi.CodeUnsupported:
		return exitUsage
	case controlapi.CodeUnavailable, controlapi.CodeVersionMismatch:
		return exitUnavailable
	case controlapi.CodeNotFound:
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
	if len(remaining) == 0 || remaining[0] == "help" {
		return writeUsage(stdout)
	}
	if expanded, ok := aliases[remaining[0]]; ok {
		remaining = append(append([]string(nil), expanded...), remaining[1:]...)
	}

	client, err := controlapi.NewClient(opts.socketPath)
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
	case "job":
		return runJob(ctx, environment, rest)
	case "library":
		return runLibrary(ctx, environment, rest)
	case "occurrence":
		return runOccurrence(ctx, environment, rest)
	case "staging":
		return runStaging(ctx, environment, rest)
	case "store":
		return runStore(ctx, environment, rest)
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
	if err := flags.Parse(args); err != nil {
		return options{}, nil, err
	}
	if opts.timeout < 0 {
		return options{}, nil, errors.New("--timeout must not be negative")
	}
	return opts, flags.Args(), nil
}

// subcommand parses one leaf command's flags and returns its positional
// arguments. Every leaf gets --json from the same place, so the flag exists
// everywhere and means the same thing.
//
// Flags may follow positional arguments: "job show 42 --json" is what an
// operator types, and stdlib flag parsing would otherwise stop at 42 and report
// the flag as a stray argument.
func (e *env) subcommand(name string, args []string, register func(*flag.FlagSet)) (*flag.FlagSet, []string, error) {
	flags := flag.NewFlagSet("anvilctl "+name, flag.ContinueOnError)
	flags.SetOutput(e.errOut)
	flags.BoolVar(&e.json, "json", e.json, "write JSON output")
	if register != nil {
		register(flags)
	}
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, nil, writeUsage(e.out)
			}
			return nil, nil, usageError{err: err}
		}
		rest := flags.Args()
		if len(rest) == 0 {
			return flags, positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
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
	flags, positional, err := e.subcommand("version", args, nil)
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
		Client:          controlapi.BuildVersion,
		ProtocolVersion: uint64(controlapi.ProtocolVersion),
		APIVersion:      controlapi.Version,
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
