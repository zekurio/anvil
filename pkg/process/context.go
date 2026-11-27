package process

import (
	"context"
	"strings"
	"time"
)

type loggerKey struct{}
type stepKey struct{}
type mediaDurationKey struct{}
type mediaFramesKey struct{}

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

// WithMediaDuration tells process loggers how much media the commands run
// under ctx work through in total, so a logger that sees an output timestamp
// can turn it into a percentage. Set it only where one command covers the
// whole duration; a non-positive duration leaves ctx unchanged.
func WithMediaDuration(ctx context.Context, duration time.Duration) context.Context {
	if duration <= 0 {
		return ctx
	}
	return context.WithValue(ctx, mediaDurationKey{}, duration)
}

// MediaDuration returns the duration set by WithMediaDuration, or 0.
func MediaDuration(ctx context.Context) time.Duration {
	duration, _ := ctx.Value(mediaDurationKey{}).(time.Duration)
	return duration
}

// WithMediaFrames tells process loggers how many video frames the commands
// run under ctx write in total. ffmpeg's output timestamp trails the slowest
// output stream, so it stops at the last packet of a sparse subtitle track;
// the frame counter does not. Set it under the same condition as
// WithMediaDuration; a non-positive count leaves ctx unchanged.
func WithMediaFrames(ctx context.Context, frames int64) context.Context {
	if frames <= 0 {
		return ctx
	}
	return context.WithValue(ctx, mediaFramesKey{}, frames)
}

// MediaFrames returns the count set by WithMediaFrames, or 0.
func MediaFrames(ctx context.Context) int64 {
	frames, _ := ctx.Value(mediaFramesKey{}).(int64)
	return frames
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
