//go:build !linux

package scanner

import (
	"context"
	"time"
)

func runCloseWriteWatcher(ctx context.Context, _ ConfigProvider, _ chan<- ScanTrigger, _ *CompletionTracker, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}
