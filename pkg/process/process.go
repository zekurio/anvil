package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// ErrOutputCapture means required parser output exceeded its capture limit.
var ErrOutputCapture = errors.New("process output capture failed")

// ErrOutputLog means process output could not be recorded completely.
var ErrOutputLog = errors.New("process output log failed")

type Command struct {
	Name  string
	Args  []string
	Dir   string
	Env   []string
	Stdin io.Reader
	// RequireFullStdout and RequireFullStderr preserve parser input up to
	// FullCaptureLimit. Exceeding that limit fails the command.
	RequireFullStdout bool
	RequireFullStderr bool
}

func (c Command) ArgsWithName() []string {
	command := make([]string, 0, len(c.Args)+1)
	command = append(command, c.Name)
	command = append(command, c.Args...)
	return command
}

type Result struct {
	Command     []string
	Stdout      []byte
	Stderr      []byte
	ExitCode    int
	Duration    time.Duration
	StdoutBytes int64
	StderrBytes int64
}

type Runner interface {
	Run(ctx context.Context, command Command) (Result, error)
}

// waitDelay bounds how long a canceled command may keep the captured output
// pipes open after its process group was killed, so a stuck grandchild can
// never hold a canceled job open forever.
const waitDelay = 10 * time.Second

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, command Command) (Result, error) {
	if command.Name == "" {
		return Result{}, errors.New("process command name is required")
	}

	if logger, ok := ctx.Value(loggerKey{}).(SessionLogger); ok {
		session, err := logger.StartProcess(ctx, command)
		if err != nil {
			return Result{}, fmt.Errorf("%w: open process logs: %w", ErrOutputLog, err)
		}
		// A nil session disables capture for this command.
		ctx = context.WithValue(ctx, loggerKey{}, struct{}{})
		ctx = WithLogger(ctx, session)
	}
	started := time.Now()
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	terminateGroup(cmd)
	cmd.WaitDelay = waitDelay
	cmd.Dir = command.Dir
	cmd.Stdin = command.Stdin
	if len(command.Env) > 0 {
		cmd.Env = append(os.Environ(), command.Env...)
	}

	stdout := captureWriter{full: command.RequireFullStdout}
	stderr := captureWriter{full: command.RequireFullStderr}
	stdoutLog := &outputWriter{ctx: ctx, command: command, stream: "stdout"}
	stderrLog := &outputWriter{ctx: ctx, command: command, stream: "stderr"}
	cmd.Stdout = io.MultiWriter(&stdout, stdoutLog)
	cmd.Stderr = io.MultiWriter(&stderr, stderrLog)

	err := cmd.Run()
	result := Result{
		Command:     command.ArgsWithName(),
		Stdout:      stdout.data,
		Stderr:      stderr.data,
		ExitCode:    -1,
		Duration:    time.Since(started),
		StdoutBytes: stdout.total,
		StderrBytes: stderr.total,
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	var runErr error
	if err != nil && ctx.Err() != nil {
		runErr = ctx.Err()
	} else if err != nil {
		runErr = fmt.Errorf("run %q: %w", command.Name, err)
	}
	if stdout.overflow || stderr.overflow {
		runErr = errors.Join(runErr, fmt.Errorf("%w: run %q: required process output exceeds %d bytes", ErrOutputCapture, command.Name, FullCaptureLimit))
	}
	runErr = errors.Join(runErr, stdoutLog.err, stderrLog.err)
	if logErr := recordProcessOutput(ctx, command, result, runErr); logErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("%w: %w", ErrOutputLog, logErr))
	}
	return result, runErr
}

type outputWriter struct {
	ctx     context.Context
	command Command
	stream  string
	err     error
}

func (w *outputWriter) Write(p []byte) (int, error) {
	if w.err == nil {
		if err := streamProcessOutput(w.ctx, w.command, w.stream, p); err != nil {
			w.err = fmt.Errorf("%w: %w", ErrOutputLog, err)
		}
	}
	return len(p), nil
}

// TailCaptureLimit bounds each diagnostic tail retained in memory.
const TailCaptureLimit = 64 * 1024

// FullCaptureLimit bounds output required by structured parsers.
const FullCaptureLimit = 16 * 1024 * 1024

type captureWriter struct {
	data     []byte
	total    int64
	full     bool
	overflow bool
}

func (w *captureWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.total += int64(n)
	limit := TailCaptureLimit
	if w.full {
		limit = FullCaptureLimit
		if len(p) > limit-len(w.data) {
			w.overflow = true
			p = p[:limit-len(w.data)]
		}
	} else if len(p) >= limit {
		w.data = w.data[:0]
		p = p[len(p)-limit:]
	} else if len(w.data)+len(p) > limit {
		drop := len(w.data) + len(p) - limit
		copy(w.data, w.data[drop:])
		w.data = w.data[:len(w.data)-drop]
	}
	w.data = append(w.data, p...)
	return n, nil
}
