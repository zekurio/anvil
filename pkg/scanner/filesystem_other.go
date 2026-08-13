//go:build !linux

package scanner

import (
	"context"
	"errors"
)

// errFilesystemEventsUnsupported marks platforms without inotify. The monitor
// keeps running scheduled scans when the event source fails; only the
// filesystem-driven triggers (close-write, moved-in) never fire.
var errFilesystemEventsUnsupported = errors.New("filesystem events require linux (inotify)")

// Run reports the missing inotify support instead of silently never emitting
// triggers, so an operator running the daemon off Linux sees why libraries
// only scan on schedule.
func (s FilesystemEventSource) Run(context.Context, ConfigProvider, chan<- ScanTrigger) error {
	return errFilesystemEventsUnsupported
}
