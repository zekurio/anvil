# Repository Guidelines

- Anvil is a Linux-only Go daemon that re-encodes user-defined media libraries
  with ffmpeg, AV1 first. `anvild` (`cmd/anvild`) owns config, the SQLite
  store, scanning, scheduling, encoding, and publication, and serves the web UI
  and its JSON API (`pkg/webapi`, `127.0.0.1:8484` by default). `anvilctl`
  (`cmd/anvilctl`) is the terminal client and talks to the daemon over a Unix
  socket. The scanner uses recursive inotify watches, including close-write and
  moved-in completion signals for download libraries. Orchestration packages
  live under `pkg/*`; `internal/textout` holds shared operator output helpers.
- The unit of behavior is the flow, not a profile. Flows, libraries, and Arr
  connections live in the store and are edited in the web UI. `pkg/flow` models
  a flow as a graph of built-in nodes and evaluates it into the effective
  `domain.Profile` for one probed file.
- Data flow: `pkg/scanner` applies each library's allow/deny rules and enqueues
  jobs into `pkg/store`, `pkg/scheduler` leases them under a thread budget
  (`pkg/resources`) and leaves paused libraries out, `pkg/worker` runs a
  `pkg/pipeline` of named blocks (`probe`, `flow`, `crop-detect`,
  `audio-cleanup`, `subtitle-cleanup`, `stage`, `crf-search`, `encode`,
  `validate`, `publish`, `cleanup`). Each block lives in its own package and
  registers itself in `worker.DefaultPipeline`. `pkg/flowstep` is the `flow`
  block: it sits right after `probe`, sets `job.Profile`, and every later
  block reads that.
- The daemon's loops read libraries, Arr connections, and daemon settings
  from a runtime snapshot, not from the store. `cmd/anvild/runtime.go` merges
  the `[daemon]` values saved in the web UI into `anvil.toml` as last loaded
  (`config.Config.WithDaemonOverrides`), then projects the stored rows into
  the same `config.Config`. Every rebuild merges the same way: the web API
  refreshes after every write it persists, `SIGHUP` loads the file again and
  merges the store back in (a reload never reverts a UI edit), start-up merges
  right after the store opens, and all of them set the live log level and
  wake the scanner and scheduler through `Reconfigured` channels. Flows
  are not in the snapshot: the worker reads the library's flow from the store
  when an attempt starts and records it on the attempt.
- The web UI lives in `web/` (SolidJS 2, TypeScript, Vite). `npm run build`
  writes `web/dist`, which `web/embed.go` embeds. A checkout without a build
  embeds only `web/dist/.gitkeep`, and the daemon then serves the API alone.
- Deployment is Linux-only, but the dev shell and `go build` must keep working
  on darwin: platform corners carry build tags (`pkg/scanner/filesystem_*`,
  `pkg/process/kill_unix.go`, `cmd/anvild/ownership_*`), with inotify stubbed
  out off Linux and `flake.nix` exposing `devShells` for darwin while
  `packages`/`apps` stay Linux-only.
- The default branch is `main` and it is the only long-lived branch; use `main`
  or `origin/main` for diffs.
- Go `1.26.4` with a deliberately small dependency set: `BurntSushi/toml`,
  `doublestar/v4`, `golang.org/x/sys`, `modernc.org/sqlite`, and Lip Gloss
  (`charm.land/lipgloss/v2`, `colorprofile`, `charmbracelet/x/term`), which
  only `internal/textout` imports. SQLite must stay pure-Go: no CGo, ever. No
  CLI framework and no HTTP router or framework: stdlib `flag` and `net/http`.
- These must pass before a coding task is complete: `make fmt`
  (`go fmt ./...`), `make lint` (`golangci-lint run ./...` inside
  `nix develop`), and `go test ./...`. Add `make build` (`bin/anvild`,
  `bin/anvilctl`) when entrypoints, package wiring, command construction, or
  build tags changed, and `make all` when the embedded UI is part of the
  change. When `web/` changed, also run `npm --prefix web run typecheck` and
  `npm --prefix web run build` (`make web-check` runs both). `make lint-fix`
  applies autofixes.
- Lint policy is `.golangci.yml`: `errcheck` (including blank assignments),
  `govet`, `ineffassign`, `staticcheck`, `unused`. Format with `gofmt` only;
  never hand-format.
- Tests live beside the code as `*_test.go`. Add or update them with the
  behavior you change. Tests that run real ffmpeg are opt-in:
  `ANVIL_MEDIA_TEST=1 go test ./pkg/ffmpeg ./pkg/search` inside the dev shell.
  The frontend has no test runner; its checks are the typecheck and the build.
- External tools (`ffmpeg`/`ffprobe`) and Node.js are provided by the Nix dev
  shell (`flake.nix`, `devenv.nix`, `direnv allow`). Bump `vendorHash` in
  `flake.nix` whenever Go dependencies change. JavaScript dependencies need no
  hash: the flake reads `web/package-lock.json` through `importNpmLock`. The
  `goSrc` fileset in `flake.nix` is an allow-list, so add every new top-level
  Go directory to it or the Nix build will not see it.
- Early WIP; sweeping changes that improve long-term maintainability are
  encouraged. Priorities in order: reliability, data safety (replace, handoff,
  cleanup), predictable behavior under cancellation and retries, then
  performance. Prefer correctness, debuggability, and operator trust.

## Branch Names

Use a short branch name of at most three words, separated by hyphens. Do not use slashes or type prefixes such as `feat/` or `fix/`.

Examples: `session-recovery`, `fix-scroll-state`, `publish-journal`.

## Commits and PR Titles

Use conventional commit-style messages and PR titles: `type(scope): summary`.

Valid types are `feat`, `fix`, `docs`, `chore`, `refactor`, and `test`. Scopes are optional; use the affected package or area, e.g. `cmd`, `config`, `store`, `scanner`, `scheduler`, `worker`, `pipeline`, `flow`, `ffmpeg`, `control`, `webapi`, `web`, `nix`, or `docs`.

Examples: `fix(worker): tolerate a raced cancel`, `feat(flow): add a bitrate check`, `docs(nix): clarify control client installation`.

## Style Guide

### General Principles

- Early returns, no unnecessary `else`. `switch` over long if-chains.
- Inline single-use logic instead of extracting a helper preemptively; extract
  only when the name describes a real concept or the logic is genuinely reused.
- Keep the exported surface minimal. Grouped imports, no dot imports, no
  aliased imports except to disambiguate a real collision (`replacepkg`).
- No goroutines unless the operation is genuinely concurrent.
- Comment non-obvious constraints and surprising behavior, not obvious control
  flow. Every package carries a `doc.go` where the package name alone is not
  self-explanatory.
- Prefer changing existing code over bolting a local fix onto it; extract
  shared logic into the owning package rather than duplicating it.

### Errors

Wrap with `%w`, lowercase, no trailing punctuation. Use sentinels for expected conditions and check them with `errors.Is`; merge cleanup failures with `errors.Join`.

```go
// Good
if err := s.publish(ctx, job); err != nil {
    return fmt.Errorf("publish job %d: %w", job.ID, err)
}

// Bad
if err := s.publish(ctx, job); err != nil {
    slog.Error("Publish failed.", "error", err)
    return err
}
```

Log **or** return, never both. Logging happens at process, job, and request boundaries (`cmd/*`, `pkg/worker`, `pkg/scheduler`, and the response writer of `pkg/webapi`), not in leaf packages.

Text the web UI shows is the exception to the lowercase rule. Flow issues and `webapi` error messages are full sentences written for the operator, and they say what is valid.

```go
// Good
return invalidf("flow_id", "There is no flow %d. Choose one of the existing flows.", library.FlowID)

// Bad
return invalidf("flow_id", "invalid flow")
```

### Context

`ctx context.Context` is the first parameter on anything that blocks, spawns a process, or touches the store. Never store a context in a struct. Use `context.WithoutCancel(ctx)` deliberately where cleanup must outlive cancellation, and say why.

```go
// Good — the lease must be released even though ctx is already canceled
releaseErr := s.releaseLeased(context.WithoutCancel(ctx), leased)
```

### Dependency Injection

Constructors return concrete structs; behavior is injected through fields. No functional-options APIs. A nil dependency field falls back to the real implementation so the zero value is usable.

```go
// Good
type FFProbe struct {
    Runner process.Runner
}

func (p FFProbe) Probe(ctx context.Context, path string) (domain.ProbeResult, error) {
    runner := p.Runner
    if runner == nil {
        runner = process.OSRunner{}
    }
    ...
}

// Bad
func NewFFProbe(opts ...Option) *FFProbe { ... }
```

### Interfaces

Interfaces are defined consumer-side, in the package that needs them, and stay as small as that consumer requires (`scheduler.Store`, `worker.Store`, `scanner.Store`, `webapi.Store`, `webapi.Jobs`, `pipeline.EventRecorder`, `probe.Prober`). Concrete persistence lives only in `pkg/store`.

### External Processes

`pkg/process` is the only place external commands run. Build the argument list in a pure function and execute it separately, so command construction is inspectable without spawning anything. The flow dry run depends on this: it shows the encode command without running it.

```go
// Good
func Args(plan domain.EncodePlan) []string { ... }
result, err := runner.Run(ctx, process.Command{Name: "ffmpeg", Args: Args(plan)})
```

Always respect `ctx` cancellation, and capture enough metadata (command, exit code, duration, byte counts) to diagnose a failure after the fact.

### Logging

Use `log/slog` with stable messages and variable data as attributes. Large
process output goes to per-attempt log files plus artifact events
(`pkg/worker/process_logs.go`), never into the log stream.

## Repo Patterns

- `pkg/domain` holds core job/attempt/media/profile/encode types and must
  stay free of persistence, ffmpeg, HTTP, and CLI concerns. `domain.Library`
  is snapshotted on every attempt and is part of the checkpoint fingerprint,
  so it holds only what processing a file depends on. Pausing, priority,
  concurrency, scan rules, and credentials do not belong in it.
- `pkg/config` is the only place raw TOML is parsed. `Load` decodes into the
  pointer-bearing raw structs in `pkg/config/raw.go` and `resolve.go` fills
  defaults in one pass; never reparse config elsewhere. At runtime only
  `[daemon]` and `[web]` count, and the `[daemon]` keys of
  `config.DaemonKeys` (`worker_count` through `log_level`) may also be saved
  in the web UI. A key the file sets pins it: `Load` records it in
  `Config.DaemonSources`, `anvild` adds the keys `--shutdown-policy` and
  `--shutdown-timeout` override, and the web API refuses a value for a pinned
  key. Precedence is option, file, store, default.
  `DaemonOverrides.Problems` and `Validate` share one set of daemon rules.
  `media_roots`, the paths, and `[web]` stay file-only: a signed-in browser
  must never widen its own confinement. `[libraries]`, `[profiles]`, and `[arrs]`
  decode into `Config.Legacy` and are import-only: `store.ImportLegacyConfig`
  reads them once, and nothing at runtime may look at them.
  `Config.Libraries` and `Config.Arrs` are the snapshot of the store, which
  `Load` leaves empty. Keep the field doc comments in
  `pkg/config/schema.go` and `examples/anvil-reference.toml` in sync. The Nix
  module passes TOML-shaped settings to Go.
- `pkg/config` also owns the rules a library, an Arr connection, a rule list,
  and video or crop settings must meet (`LibraryConfig.Problems`,
  `LibraryPathConflicts`, `ValidateRules`, `VideoConfig.Problems`,
  `CropConfig.Problems`). The web API and flow validation call these. Do not
  restate a rule in a handler, a node, or the frontend. `LibraryPathConflicts`
  also keeps library and handoff folders out of `temp_dir`, and the legacy
  import goes through it too: a conflicting library is imported paused.
- `[daemon] media_roots` confines what a browser can name. When it is not
  empty, `GET /fs` lists nothing outside the roots and `checkLibrary` refuses
  a library or handoff path that does not resolve inside one
  (`pathWithinMediaRoots`, symlinks resolved). A folder an update leaves
  unchanged is not checked again, so an imported library outside the roots
  stays editable. Empty means unrestricted. The
  Nix module fills it from `mediaPaths`.
- Pipeline step order is fixed in `worker.DefaultPipeline`. A block's `Name()`
  is its event and checkpoint name. The publish block selects replace or
  handoff from the library kind. Staging, validation, and the journaled
  publish are engine-owned: no flow node may skip or weaken them.
- `pkg/store` is SQLite via `modernc.org/sqlite` with `SetMaxOpenConns(1)`, WAL,
  and `foreign_keys` pragmas, split into `sqlite_*.go` by concern. Schema
  changes are versioned in-code migrations in `pkg/store/migrations.go`; bump
  `currentSchemaVersion` and add a migration entry, never edit an old one.
  Bump `minReadOnlySchemaVersion` too when the migration changes something
  `preflight` reads.
- Schema version 8 added `flows`, `libraries`, `arrs`, and `meta`. `meta`
  records one-time actions (the seeded `Default AV1` flow, the legacy import)
  so they never run twice: a deleted default flow stays deleted, and a TOML
  file edited later is never imported again. It also holds the one setting
  kept there, `daemon_settings`: the JSON of `config.DaemonOverrides` saved in
  the web UI (`DaemonSettings`, `SetDaemonSettings`), replaced as a whole and
  removed when nothing is saved. Flows carry a revision, and
  `UpdateFlow` refuses a stale one with `ErrRevisionConflict`. `DeleteLibrary`
  removes the library with its jobs, sources, and scan records in one
  transaction. Jobs that only wait go with it. It refuses with a
  `LibraryBusyError` (`ErrActiveWork`) in two cases: a worker holds a job
  (`leased`, `running`, `validating`, `replacing`), or a publish journal is at
  `prepared`, `published`, or `source_cleaned`. A journal in `conflict` does
  not refuse it. The web API reads the part files of the library's jobs
  before the delete (`controlapi.Service.LibraryParts`) and removes them
  after it. A flow or Arr connection still assigned to a library is not
  deleted (`InUseError`).
- `UpdateLibrary` writes a changed priority to the library's jobs in the same
  transaction. The scheduler leases by `jobs.priority`, so the two must not
  drift.
- `pkg/store/protection.go` defines the jobs maintenance must not disturb: any
  job that is not terminal, and any job whose publish journal has not reached
  `committed`. Staging cleanup and job pruning depend on it. Deleting such a
  job row would cascade its journal away and strand the files it names.
  Library deletion is the operator's decision and uses the narrower
  `publishInFlightCondition`, which lets a `conflict` journal go.
- The store sets its database, `-wal`, and `-shm` files to `0600` on every
  open and creates a missing parent directory `0700`. It can hold Arr API
  keys; do not loosen either mode.
- A list query never reads or decodes `pipeline_context_json`. The probe
  facts the library file list shows live in the `probe_*` columns of `jobs`,
  which `SaveJobPipelineContext` writes in the same statement as the
  checkpoint. A new fact for a list gets a column there.
- `pkg/worker/resume.go` checkpoints only reusable steps (`probe`,
  `audio-cleanup`, `crop-detect`, `crf-search`) as JSON in
  `jobs.pipeline_context_json`. Two guards protect reuse. The checkpoint is
  matched against a fingerprint of the input file, the library snapshot, the
  metadata, and `flow.Canonical` of the library's flow. Then, right after the
  `flow` step, the audio, crop, and search results are dropped when the
  profile the flow resolves now differs from the one they were computed under.
  The `flow` step itself is never checkpointed. Attempt-local output is never
  resumed except through the publish journal.
- `validate` is observational: `validate.Block.Run` logs and returns `nil` even
  on `ErrValidationFailed`. A missing artifact is the one hard failure. Native CRF search in `pkg/search` is the encode acceptance authority. A fixed CRF skips the search and is checked against the encoder's range (`video.QualityRange`).
- HDR signalling (color tags, HDR10 static metadata, Dolby Vision record and
  RPU) comes from `probe.FFProbe`, which also decodes the first frames of the
  primary video. Re-encodes drop the Dolby Vision RPU, so
  `ffmpeg.videoCopyState` copies video whose base layer is not viewable on its
  own (profile 5, 10.0), whatever the flow resolved.
- `pkg/audio` plans each kept track as copy or encode. The per-codec layout
  and bitrate limits in `pkg/audio/plan.go` are measured against real ffmpeg;
  change them only with a run of the `ANVIL_MEDIA_TEST` tests. `pkg/ffmpeg`
  sets channels with an `aformat` filter, never `-ac`.
- Publication (`pkg/replace`) goes through a durable journal
  (`prepared → published → source_cleaned → committed`, or `conflict`). Never
  overwrite an existing destination, and record intent before mutating the
  filesystem. The `stage` step plans the destination (`replace.PlanDestination`)
  and the artifact is written on the destination filesystem as
  `<name>.job-<id>.anvil-part` (beside the destination for media libraries,
  under `<handoff_path>/.anvil-work` for download libraries, so an importer
  never sees it), so publish is fsync + hardlink + unlink, never a bulk copy; `pkg/staging` keeps only
  scratch (search samples) under `temp_dir`.
- `anvild` takes an exclusive `flock` on `<store_path>.lock`, then claims the
  control socket, and only then opens the store, recovers jobs, sweeps
  staging, runs the legacy import, and builds the runtime snapshot. Nothing
  with a side effect may run ahead of those two claims. The web password is
  read and the web listener bound (`claimWeb`) right after them and before the
  store is opened, so a start with a taken address or an unreadable password
  file fails without migrating the store. The listener serves last, once the
  snapshot it serves from exists.
- Live progress (`domain.JobProgress`) is in memory only. The worker writes it
  to `worker.ProgressTracker` and the web API reads it from there; it is never
  persisted and never read from the store. Encode progress follows ffmpeg's
  frame counter wherever it is ahead of the output timestamp, which stalls at
  the end of a sparse subtitle track. A step that knows its input sets
  `process.WithMediaFrames` under the same condition as
  `process.WithMediaDuration`.
- Operator output goes through `internal/textout` (error-carrying writer,
  tables, JSON, byte/percent formatting) so both binaries stay consistent.
- Filesystem mutation is conservative by default: explicit safety checks and
  clear logs over implicit behavior.

## Flows

- `pkg/flow` is pure. It reads no file, runs no process, and touches no store.
  It imports `pkg/config`, `pkg/domain`, `pkg/language`, and `pkg/video`, and
  must never import `pkg/pipeline`, `pkg/store`, `pkg/worker`, or
  `pkg/webapi`. Pipeline concerns belong in `pkg/flowstep`.
- Node type IDs, option keys, and enum values are stored in graphs. Never
  rename or reuse one. Labels, hints, and descriptions are free to change.

```go
// Good: the ID stays, and only what the operator reads changes
ID:    typeSubtitlesDropImage, // "subtitles.drop_bitmap"
Label: "Remove image subtitles",

// Bad: every stored flow that uses the node stops validating
const typeSubtitlesDropImage = "subtitles.drop_image"
```

- One node type owns one part of `domain.Profile`, and its `apply` writes only
  that part. This is what makes "the last node on the path wins" true.
  `video.encode` and `video.copy` share the video part on purpose.
- Whatever no node sets comes from `baseProfile`: a remux that copies and
  keeps everything. A new profile field needs a base value that leaves the
  file alone.
- Defaults live in the catalog (`Field.Default`) and come from the `pkg/config`
  constants. The frontend never hardcodes one. A default is part of what a
  stored flow means when the option is absent, so changing one changes
  existing flows.
- Validation reuses the `pkg/config` rule functions through `ruleProblems`.
  Add a `check` only for what field definitions and those rules cannot
  express, and a `warn` for options that are valid but will not do what they
  say.
- A graph has at most `flow.MaxNodes` (500) nodes and `flow.MaxEdges` (2000)
  edges. `Validate` reports a larger one with a single issue and reads no
  further, so every analysis may assume the bound. Keep new analyses linear
  in the size of the graph.
- `video.encode`'s `ffmpeg_args` is an allow-list
  (`ffmpeg.ValidateEncoderArgs`): the three encoder parameter bundles and a
  fixed set of value-only tuning options. Add an option only when its value
  cannot name a file, URL, device, stream, or filter. Do not turn the list
  into a deny-list.
- `flow.Canonical` fingerprints checkpoints and tells flows apart. A change in
  what a graph does must change its canonical form. Position, label, node and
  edge order, and spelling out a default must not.
- Evaluate a flow in one place per use: `flowstep.Block` for a job, the dry
  run in `pkg/webapi/flow_dry_run.go` for a test. Both call `flow.Evaluate`,
  and the dry run builds its command through the same selectors and
  `ffmpeg.BuildPlanFromRequest` the pipeline uses. Do not write a second
  interpretation of a graph.

Adding a node type:

1. Add a `nodeDef` to `nodeDefs()` in palette order, with its `NodeType`,
   a `summary` that is never empty, and exactly one of `test` (condition),
   `apply` (action), or `outcome` (output).
2. If it sets something new, extend the plan lines in `pkg/flow/evaluate.go`,
   and `FromProfile` in `pkg/flow/generate.go` when a legacy profile field
   maps onto it.
3. Add tests. `catalog_test.go` lists every node type, and evaluation,
   validation, and canonical encoding each have a test file.
4. Mirror the entry in `web/mock/fixtures/catalog.ts`, and its behavior in
   `web/mock/flowcheck.ts`.

The real UI needs no change. The palette and every options form render from
`GET /api/v1/node-types`. Only a new `Kind` needs frontend work: an icon in
`NODE_KINDS` (`web/src/ui/icon.tsx`).

## Web API and Control

- `pkg/webapi` uses stdlib `net/http` and `http.ServeMux` patterns only. No
  router, framework, or middleware dependency. No JSON-RPC, `net/rpc`,
  websocket, or SSE: the UI polls.
- `pkg/webapi/types.go` is the contract with the UI. `web/src/api/types.ts`
  mirrors it 1:1 and `web/mock/` implements it. Change all three in the same
  commit. Field names are snake_case, times are RFC 3339 UTC, lists are never
  null, and durations are Go duration strings.
- Every route is a `handler` that returns `(status, body, error)` and is
  registered through `route` in `Server.Handler`. Errors leave through
  `writeError` as an `ErrorResponse` with a code from `types.go`. Do not write
  to the `ResponseWriter` from a handler, except to set a cookie.

```go
// Good
func (s *Server) getLibrary(_ http.ResponseWriter, r *http.Request) (int, any, error) {
    stored, err := s.storedLibrary(r.Context(), r.PathValue("name"))
    if err != nil {
        return fail(err)
    }
    ...
    return ok(response)
}

// Bad
func (s *Server) getLibrary(w http.ResponseWriter, r *http.Request) {
    http.Error(w, "not found", http.StatusNotFound)
}
```

- The guards are not optional. Every API route sits behind `guardAPI` (same
  origin, `Content-Type: application/json` on mutating requests, a session
  when a password is set), everything sits behind `guardHost` (the DNS
  rebinding check of a daemon without a password), and every response carries
  the security headers. Never register a route on a mux that bypasses them,
  and never exempt a route other than `/api/v1/session` from the session
  check.
- The Content Security Policy allows no inline `<script>` or `<style>`
  elements and no third-party origin. The one inline script in `index.html` is
  allowed by its hash. Do not add a CDN, an inline script, or a library that
  injects style elements.
- A sign-in failure must never fall back to an open UI: an unreadable or empty
  password file is an error at start and on reload.
- Sign-in is throttled per remote address (`auth.login`): from the fifth
  failure, each one earns a fixed `loginDelay`. An attempt during the delay is
  refused before the password is compared and must not extend the delay, or a
  peer could lock the operator out. `X-Forwarded-For` is not read.
- An Arr API key enters through `ArrWrite.APIKey` only. No response carries a
  key (`api_key_set` says whether there is one), and the API cannot set
  `api_key_file`; an imported key file stays until a literal key replaces it.
  `metadata.Resolver` refuses redirects and reads at most `maxResponseBytes`
  of a response.
- Paths that come from a browser are untrusted. The flow dry run only probes a
  regular file inside a library root, with symlinks resolved, and checks the
  library before it asks the filesystem, so a path outside every library gets
  one answer whatever is there. Keep any new route that makes the daemon open
  a path as strict.
- Changes to flows, libraries, Arr connections, and the daemon settings
  (`PUT /api/v1/settings/daemon`) hold `Server.mutations`, validate against
  the stored state, write, call `refresh`, and only then answer.
- Job and scan operations are implemented once, in `controlapi.Service`. The
  control socket and the web API (`webapi.Jobs`) call the same value, so a
  cancel, retry, scan, or requeue follows the same rules from either side.
  Do not reimplement one in a handler. Library, flow, Arr, and daemon-setting
  edits exist only in the web API; `anvilctl` lists libraries and flows and
  edits nothing.
  Maintenance commands (`prune`, `recover`, `staging cleanup`, `backup`) exist
  only in `anvilctl`.
- The control protocol (`pkg/control`) is private and versioned per frame.
  Bump `control.ProtocolVersion` when the wire format changes. The stable
  contract is `anvilctl` syntax, `--json` shapes, error codes, and exit status
  (`0` ok, `1` failed, `2` usage, `3` unreachable, `4` not found).
- Only local read-only commands (`check-config`, `preflight`) stay on `anvild`.
  Anything that changes live state goes through the running daemon.

## Frontend

`web/README.md` has the commands, the directory layout, and the page
conventions. The rules that matter most:

- This is Solid 2.0, not 1.x. Most Solid examples are 1.x and do not compile
  here. After `npm ci`, `web/node_modules/solid-js/CHEATSHEET.md` is the
  reference for the installed version.
  - Import from `solid-js` and `@solidjs/web`. `render`, `Dynamic`, and the
    `JSX` types come from `@solidjs/web`. There is no `solid-js/web` and no
    `solid-js/store`; `createStore` is in `solid-js`.
  - Async state is an async `createMemo` read under `<Loading>` and
    `<Errored>`. There is no `createResource`, `<Suspense>`, or
    `<ErrorBoundary>`. Use `createQuery` from `web/src/api`, which wraps this,
    and `isPending` for a refreshing hint.
  - `createEffect` takes two functions: the first tracks and returns a value,
    the second runs the side effect and may return a cleanup.
  - `onSettled` replaces `onMount`, `omit` replaces `splitProps`, and `merge`
    replaces `mergeProps`. There is no `batch` and no `<Index>`.
  - `<For>` passes the raw item and an index accessor. With `keyed={false}`
    the item is an accessor instead, and with `keyed={(item) => key}` both
    are.
  - Store setters take a function that mutates a draft.
  - A write is not readable until the next flush: `setX(v); x()` still returns
    the old value. Call `flush()` when the next line depends on it.

```tsx
// Good
const flows = createQuery(() => keys.flowList, async () => (await api.flows.list()).flows, { poll: POLL_LIST });
createEffect(title, (value) => {
  document.title = value;
});

// Bad: Solid 1.x
const [flows] = createResource(async () => (await api.flows.list()).flows);
createEffect(() => {
  document.title = title();
});
```

- Components come from `web/src/ui`, the design system, and pages and
  features import from `ui`. UI SDK primitives (`@ui-sdk/solid`) are wrapped
  there. The one direct import outside it is the popover in
  `pages/flow-editor/EditorTopbar.tsx`; do not add more. No Tailwind, no
  CSS-in-JS, no component kit.
- Styles are plain CSS with the tokens in `web/src/ui/styles/tokens.css`.
  Class names carry the `av-` prefix; the flow editor's page styles use `fe-`.
  Colors exist only in the token file: no raw color anywhere else, and no new
  ones. Spacing, radii, and type come from the tokens. Both themes must work.
  The accent is Go's gopher blue. The favicon and app icons are the one
  exception to the colour rule: static files in `web/public`, rendered once
  from the SVG sources described in `web/icons/README.md`, never at build
  time.
- Copy: the product is `anvil`, lowercase, in the UI. Labels are sentence
  case, never all caps. Say a thing once, in as few words as do the job: no
  hint that restates its label, no section description that restates its
  heading, no empty state that repeats the section description, and no hint
  at all when the field is obvious. Errors and confirmations are short full
  sentences that say what happened and what the operator can do next. No
  exclamation marks. A destructive action says what happens to the media
  files.
- Pages live in `web/src/pages/<name>/index.tsx` with one entry in
  `web/src/routes.tsx`. What several pages share lives in `web/src/features`.
  `web/src/shell` is the app frame.
- Reads go through `createQuery` with a key from `api/keys.ts`. Writes go
  through `run` from `lib/mutate`, which refetches and then toasts. Forms over
  polled data use `createDraft`.
- The mock API (`web/mock/`, `make web-mock`) is part of the contract work:
  routes in `mock/handlers/`, state in `mock/db.ts`, seed data in
  `mock/fixtures/`. A new or changed route lands in the mock in the same
  commit. Mock-only routes carry an underscore (`_mock/...`, `_touch`).
- npm with the committed `web/package-lock.json`. Install with `npm ci`. Pin
  exact versions, and commit the lockfile with any dependency change.
- `localStorage` is for per-browser conveniences only, such as the theme.
  Everything else is server state.
