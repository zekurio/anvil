//go:build !unix

package main

import "log/slog"

// daemonOwnership has no exclusive-lock implementation outside unix. Anvil
// deploys on Linux only, but the code still builds elsewhere for development,
// so rather than failing to compile the guard degrades to a loud warning: the
// control socket still refuses a second daemon once it starts listening, which
// happens before any start-up side effect.
type daemonOwnership struct{}

func acquireDaemonOwnership(storePath string) (*daemonOwnership, error) {
	slog.Warn("daemon ownership locking is unavailable on this platform; a second daemon on the same store is only stopped by the control socket", "store", storePath)
	return &daemonOwnership{}, nil
}

func (o *daemonOwnership) release() error {
	return nil
}
