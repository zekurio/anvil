package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

type Command struct {
	Name  string
	Args  []string
	Dir   string
	Env   []string
	Stdin io.Reader
}

func (c Command) ArgsWithName() []string {
	command := make([]string, 0, len(c.Args)+1)
	command = append(command, c.Name)
	command = append(command, c.Args...)
	return command
}

type Result struct {
	Command  []string
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
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

	started := time.Now()
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	terminateGroup(cmd)
	cmd.WaitDelay = waitDelay
	cmd.Dir = command.Dir
	cmd.Stdin = command.Stdin
	if len(command.Env) > 0 {
		cmd.Env = append(os.Environ(), command.Env...)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(&stdout, outputWriter{ctx: ctx, command: command, stream: "stdout"})
	cmd.Stderr = io.MultiWriter(&stderr, outputWriter{ctx: ctx, command: command, stream: "stderr"})

	err := cmd.Run()
	result := Result{
		Command:  command.ArgsWithName(),
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: 0,
		Duration: time.Since(started),
	}
	if err == nil {
		recordProcessOutput(ctx, command, result, nil)
		return result, nil
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	var runErr error
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		runErr = ctx.Err()
	} else {
		runErr = fmt.Errorf("run %q: %w", command.Name, err)
	}
	recordProcessOutput(ctx, command, result, runErr)
	return result, runErr
}

type outputWriter struct {
	ctx     context.Context
	command Command
	stream  string
}

func (w outputWriter) Write(p []byte) (int, error) {
	streamProcessOutput(w.ctx, w.command, w.stream, p)
	return len(p), nil
}
