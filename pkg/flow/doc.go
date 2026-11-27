// Package flow models encoding flows: directed graphs of built-in nodes that
// decide, per file, what Anvil does with it. A flow is evaluated once per
// attempt, after the probe, into the effective domain.Profile the fixed
// pipeline then runs. Staging, validation, and the journaled publish stay
// engine-owned, so no flow can weaken them.
package flow
