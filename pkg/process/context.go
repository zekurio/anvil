package process

import (
	"context"
	"log/slog"
	"strings"
)

type loggerKey struct{}
type stepKey struct{}

type Logger interface {
	LogProcess(context.Context, Command, Result, error) error
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

func recordProcessOutput(ctx context.Context, command Command, result Result, runErr error) {
	logger, _ := ctx.Value(loggerKey{}).(Logger)
	if logger == nil {
		return
	}
	if err := logger.LogProcess(ctx, command, result, runErr); err != nil {
		slog.Warn("record process output failed", "error", err)
	}
}
