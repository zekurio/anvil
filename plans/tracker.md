# Tracker

Notion tracker: https://app.notion.com/p/38dfeb1fe5b581d7ab16e698f36e8e3e

Database: https://app.notion.com/p/cfcd5386a96045759a77c5b81e03a5e3

## Seeded Items

The Notion tracker has been created like the Alloy tracker, with `Name`, `Status`, and `Zuweisen` fields plus a board view grouped by status.

Seeded first-server-run items:

- add stronger validation gates
- add dry-run and preflight mode

## Priority Backlog

### P0 Before Real Replace Mode

- Add attempt/artifact CLI inspection so failed jobs point directly to captured process logs.
- Add dry-run/preflight for scan candidates, resolved config, planned destinations, and cleanup behavior.
- Add stronger validation gates for codec, pixel format, stream layout, Anvil marker presence, and size policy.
- Wire `daemon.log_level` into slog configuration.

### P1 Before Wider Batch Runs

- Persist structured probe/search/encode/validation summaries outside raw attempt events.
- Define requeue semantics for changed source files after terminal jobs.
- Harden Arr metadata beyond list lookup and parse fallback, especially ambiguous or missing matches.
- Tighten subtitle, attachment, chapter, metadata, and HDR policy.

### P2 After First Controlled Runs

- Package the daemon as a first-class Nix package and harden the NixOS service.
- Add nice/ionice or systemd resource knobs.
- Decide which smoke coverage belongs in CI and which remains local/manual because it invokes real encodes.
- Consider notification hooks after the operational CLI is useful.
