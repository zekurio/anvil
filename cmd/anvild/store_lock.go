package main

import "github.com/zekurio/anvil/pkg/config"

// storeLockPath resolves the advisory lock file that identifies a store on
// disk. The daemon singleton guard depends on it: two daemons that agree on a
// database file must disagree about who owns it, and they can only do that
// through a path both of them compute the same way.
//
// A "file:" URI names the same file as its plain filename. Treating a URI as
// "no filesystem identity" is what let a second daemon start against a live
// database, run stale-job recovery, and sweep staging directories out from
// under the first one. Only a store that genuinely has no file — an in-memory
// or anonymous temporary database — is lockless, and that is reported rather
// than guessed.
func storeLockPath(storePath string) (string, bool, error) {
	file, ok, err := config.StoreFile(storePath)
	if err != nil || !ok {
		return "", false, err
	}
	return file + ".lock", true, nil
}
