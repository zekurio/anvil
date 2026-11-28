// Package flow models encoding flows: directed graphs of built-in nodes that
// decide, per file, what Anvil does with it. A flow is evaluated once per
// attempt, after the probe, into the effective domain.Profile the fixed
// pipeline then runs. Staging, validation, and the journaled publish stay
// engine-owned, so no flow can weaken them.
//
// The package is pure: it reads no file, runs no process, and touches no
// store. Catalog describes the node types. Validate and Summaries check and
// describe a graph for the editor, Evaluate resolves a graph for one probed
// file, and Canonical gives a graph's meaning a stable encoding. Default and
// FromProfile build graphs out of the TOML profiles that flows replace.
package flow
