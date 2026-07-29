//go:build unix

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// daemonOwnership is an exclusive advisory lock beside the SQLite store. It is
// the daemon singleton guard: the control socket alone cannot be one, because
// two daemons can be configured with different socket paths and the same
// database, and the loser of that race would still have run stale-job recovery
// and staging cleanup against a store another daemon is actively using.
//
// The lock is held for the process lifetime and released by the kernel if the
// process dies, so a crashed daemon never leaves a stale claim behind.
type daemonOwnership struct {
	path string
	file *os.File
}

func acquireDaemonOwnership(storePath string) (*daemonOwnership, error) {
	storePath = strings.TrimSpace(storePath)
	if storePath == "" || storePath == ":memory:" || strings.HasPrefix(storePath, "file:") {
		// A store with no filesystem identity cannot be shared with another
		// daemon in a way this lock could describe.
		return &daemonOwnership{}, nil
	}
	lockPath := storePath + ".lock"
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open daemon ownership lock %q: %w", lockPath, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.Join(
				fmt.Errorf("another anvild already owns the store at %q; refusing to start a second daemon", storePath),
				closeErr,
			)
		}
		return nil, errors.Join(fmt.Errorf("lock daemon ownership %q: %w", lockPath, err), closeErr)
	}
	// The pid is written for humans reading the lock file; nothing depends on
	// it, because the lock itself is the authority.
	if err := file.Truncate(0); err == nil {
		if _, err := file.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
			slog.Debug("record daemon ownership pid", "error", err)
		}
	}
	return &daemonOwnership{path: lockPath, file: file}, nil
}

// release drops the lock. The lock file itself is left in place: removing it
// would let a second daemon create and lock a new file at the same path while
// this one still holds the old inode.
func (o *daemonOwnership) release() error {
	if o == nil || o.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(o.file.Fd()), syscall.LOCK_UN)
	closeErr := o.file.Close()
	o.file = nil
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock daemon ownership %q: %w", o.path, unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close daemon ownership %q: %w", o.path, closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}
