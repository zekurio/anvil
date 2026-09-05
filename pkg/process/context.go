package process

import (
	"context"
	"strings"
)

type loggerKey struct{}
type stepKey struct{}

type Logger interface {
	LogProcess(context.Context, Command, Result, error) error
}

// SessionLogger opens a separate output sink for each process invocation.
type SessionLogger interface {
	StartProcess(context.Context, Command) (Logger, error)
}

type StreamLogger interface {
	LogProcessOutput(context.Context, Command, string, []byte) error
}

func WithLogger(ctx context.Context, logger Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey{}, logger)
}

func WithStep(ctx context.Context, step string) context.Context {
	if strings.TrimSpace(step) == "" {
		return ctx
	}
	return context.WithValue(ctx, stepKey{}, step)
}

func Step(ctx context.Context) string {
	step, _ := ctx.Value(stepKey{}).(string)
	return step
}

func recordProcessOutput(ctx context.Context, command Command, result Result, runErr error) error {
	logger, _ := ctx.Value(loggerKey{}).(Logger)
	if logger == nil {
		return nil
	}
	return logger.LogProcess(ctx, command, result, runErr)
}

func streamProcessOutput(ctx context.Context, command Command, stream string, output []byte) error {
	logger, _ := ctx.Value(loggerKey{}).(StreamLogger)
	if logger == nil || len(output) == 0 {
		return nil
	}
	return logger.LogProcessOutput(ctx, command, stream, output)
}
