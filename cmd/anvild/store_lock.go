package main

import (
	"fmt"
	"net/url"
	"strings"
)

// storeLockPath resolves the advisory lock file that identifies a store on
// disk. The daemon singleton guard depends on it: two daemons that agree on a
// database file must disagree about who owns it, and they can only do that
// through a path both of them compute the same way.
//
// modernc.org/sqlite accepts both a plain filename and a "file:" URI, and the
// URI form is how an operator sets pragmas. Treating a URI as "no filesystem
// identity" is what let a second daemon start against a live database, run
// stale-job recovery, and sweep staging directories out from under the first
// one. Only a store that genuinely has no file — an in-memory or anonymous
// temporary database — is lockless, and that is reported rather than guessed.
func storeLockPath(storePath string) (string, bool, error) {
	storePath = strings.TrimSpace(storePath)
	switch {
	case storePath == "", storePath == ":memory:":
		return "", false, nil
	case !strings.HasPrefix(storePath, "file:"):
		return storePath + ".lock", true, nil
	}

	parsed, err := url.Parse(storePath)
	if err != nil {
		return "", false, fmt.Errorf("parse daemon.store_path %q as a SQLite URI: %w", storePath, err)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", false, fmt.Errorf("parse daemon.store_path %q query: %w", storePath, err)
	}
	if strings.EqualFold(query.Get("mode"), "memory") {
		return "", false, nil
	}
	// SQLite URIs are written both as file:relative.db and file:/absolute.db,
	// which url.Parse splits into Opaque and Path respectively.
	filename := parsed.Opaque
	if filename == "" {
		filename = parsed.Path
	}
	filename = strings.TrimSpace(filename)
	switch filename {
	case "":
		// file: with no filename is an anonymous temporary database, which no
		// second daemon can reach.
		return "", false, nil
	case ":memory:":
		return "", false, nil
	}
	return filename + ".lock", true, nil
}
