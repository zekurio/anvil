package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestControlClientLinksNoDaemonInternals is the architectural boundary, made
// checkable. anvilctl asks the running daemon for everything; it must never be
// able to open the SQLite store, walk a library, or delete a staging directory
// itself, because a second process writing Anvil's database while jobs are
// running is how half-published files happen.
//
// The guard is on the client binary's import graph rather than on a convention,
// because the failure mode is silent: one import of a daemon-side package pulls
// the whole engine back in, and nothing about the client's behavior changes
// until the day it is used to run the wrong thing.
//
// The test binary itself deliberately links the daemon service to drive a real
// socket, so the check asks about the non-test build.
func TestControlClientLinksNoDaemonInternals(t *testing.T) {
	forbidden := map[string]string{
		"modernc.org/sqlite":                      "the SQLite driver: only the daemon may open the store",
		"github.com/zekurio/anvil/pkg/store":      "the store: control commands are asked of the daemon, not executed locally",
		"github.com/zekurio/anvil/pkg/scanner":    "the scanner: scans run inside the daemon so they share its occurrence bookkeeping",
		"github.com/zekurio/anvil/pkg/staging":    "the staging manager: only the daemon may delete a staging directory",
		"github.com/zekurio/anvil/pkg/worker":     "the worker: the client never runs a pipeline",
		"github.com/zekurio/anvil/pkg/controlapi": "the daemon-side control service, which transitively drags all of the above in",
	}

	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go tool is unavailable, so the import graph cannot be inspected: %v", err)
	}
	output, err := exec.Command(goTool, "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps . error = %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		dependency := strings.TrimSpace(line)
		if reason, banned := forbidden[dependency]; banned {
			t.Errorf("anvilctl links %s, which is %s", dependency, reason)
		}
	}
}
