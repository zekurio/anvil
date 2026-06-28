# Anvil Plans

This directory is the working plan for getting Anvil from a successful mock run to a careful first run on the real server.

The old broad design notes have been retired. They were useful while the daemon shape was still forming, but the implementation is now far enough along that the plan should focus on readiness, operational visibility, and the few media-policy gaps that can make a real library run risky.

## Documents

- [Current State](current-state.md): what is implemented and what is intentionally still shallow.
- [First Server Run](first-server-run.md): the readiness gate and rollout protocol for the real server.
- [Tracker](tracker.md): prioritized work items and the Notion tracker link.

## Direction

Anvil should make the first server run boring: narrow scope, reversible outputs, clear logs, explicit validation, and no surprise cleanup. The core pipeline exists; the next work is about confidence and control.
