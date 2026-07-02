# Plan 001: Make download-library defaults use handoff, not replace

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 4149ac0..HEAD -- pkg/config/schema.go pkg/config/defaults.go pkg/config/validate.go pkg/config/config_test.go examples/anvil.toml nix/modules/anvil.nix`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `4149ac0`, 2026-07-02

## Why this matters

Anvil treats media libraries and download libraries differently at publish time: media libraries replace or copy beside the source, while download libraries should hand off completed outputs to an import directory. Today, a download library with no explicit `flow` inherits the media default flow, which includes `replace`. That can mutate files inside the download root instead of publishing to the configured handoff path. The examples show the intended safe split, but safety should be enforced by defaults and validation, not by every operator remembering to specify the right flow.

## Current state

Relevant files:

- `pkg/config/schema.go` — names built-in config constants.
- `pkg/config/defaults.go` — builds default flows/profiles and fills blank library settings.
- `pkg/config/validate.go` — validates config consistency after defaults are applied.
- `pkg/config/config_test.go` — config loader/default/validation tests.
- `examples/anvil.toml` — operator-facing example config.
- `nix/modules/anvil.nix` — NixOS module that generates equivalent TOML defaults.

Current excerpts to confirm before editing:

```go
// pkg/config/schema.go:3-8
DefaultFlowName        = "av1-crf-search"
DefaultProfileName     = "default-av1"
DefaultLibraryKind     = "media"
DefaultReplacementMode = "replace"
```

```go
// pkg/config/defaults.go:32-35
Flows: map[string]FlowConfig{
    DefaultFlowName: {
        Steps: []string{"probe", "crop-detect", "audio-cleanup", "subtitle-cleanup", "stage", "crf-search", "encode", "dovi-fix", "validate", "replace", "cleanup"},
    },
},
```

```go
// pkg/config/defaults.go:148-159
for name, library := range c.Libraries {
    library.Name = name
    if strings.TrimSpace(library.Kind) == "" {
        library.Kind = DefaultLibraryKind
    }
    if strings.TrimSpace(library.Flow) == "" {
        library.Flow = DefaultFlowName
    }
    if strings.TrimSpace(library.Profile) == "" {
        library.Profile = DefaultProfileName
    }
    applyLibraryPolicyDefaults(&library)
```

```go
// pkg/config/defaults.go:213-216
func applyLibraryPolicyDefaults(library *LibraryConfig) {
    if strings.TrimSpace(library.Media.ReplacementMode) == "" {
        library.Media.ReplacementMode = DefaultReplacementMode
    }
```

```go
// pkg/config/validate.go:184-201
if !validReplacementMode(library.Media.ReplacementMode) {
    problems = append(problems, fmt.Sprintf("library %q media.replacement_mode %q is invalid", name, library.Media.ReplacementMode))
}
if library.Kind == "download" {
    if strings.TrimSpace(library.Download.HandoffPath) == "" {
        problems = append(problems, fmt.Sprintf("download library %q download.handoff_path is required", name))
    }
    ...
}
```

```nix
# nix/modules/anvil.nix:17-28
defaultFlowSteps = [
  "probe"
  ...
  "validate"
  "replace"
  "cleanup"
];

# nix/modules/anvil.nix:514-517
flow = mkOption {
  type = types.str;
  default = "av1-crf-search";
```

Repo conventions to match:

- Keep config parsing/defaulting in `pkg/config`; downstream packages should receive resolved domain settings.
- Prefer boring, explicit Go; use table-driven tests with named subtests where there is a behavior matrix.
- Run `make fmt`, `make test`, and `make lint` before completion. Because this plan touches the NixOS module, also run a Nix evaluation/check if available.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Targeted config tests | `go test ./pkg/config` | exit 0 |
| Example config check | `go run ./cmd/anvild check-config --config examples/anvil.toml` | exit 0 and logs `config ok` |
| Nix module eval | see the eval snippet in Step 4 (`nixosModules.anvil` is a bare module function; `nix eval --json .#nixosModules.anvil` cannot work) | prints `"download-av1-handoff"` |
| Format | `make fmt` | exit 0 |
| Tests | `make test` | exit 0 |
| Lint | `make lint` | exit 0, no issues |

## Suggested executor toolkit

- Use `golang-code-style`, `golang-error-handling`, `golang-testing`, and `golang-project-layout` if available.
- For the Nix module, keep the generated TOML keys aligned with `pkg/config/schema.go`.

## Scope

**In scope** (the only source/config files you should modify):

- `pkg/config/schema.go`
- `pkg/config/defaults.go`
- `pkg/config/validate.go`
- `pkg/config/config_test.go`
- `examples/anvil.toml`
- `nix/modules/anvil.nix`
- `plans/README.md` status row when done

**Out of scope** (do NOT touch):

- `pkg/replace`, `pkg/worker`, `pkg/pipeline`, `pkg/scanner`, or any filesystem mutation behavior.
- Any schema migration or persisted job data rewrite.
- Any change that makes `replace` safe for download libraries; this plan prevents that flow shape instead.

## Git workflow

- Branch: `download-flow-safety`
- Commit message: `fix(config): default downloads to handoff flow`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Add kind-specific built-in flow defaults

In `pkg/config/schema.go`, add a new constant next to `DefaultFlowName`:

- Keep `DefaultFlowName = "av1-crf-search"` for media-library backward compatibility.
- Add `DefaultDownloadFlowName = "download-av1-handoff"`.

In `pkg/config/defaults.go`:

- Register both built-in flows in `Default()`:
  - `DefaultFlowName`: current steps ending in `"replace", "cleanup"`.
  - `DefaultDownloadFlowName`: same steps but use `"handoff"` instead of `"replace"`.
- In the library defaulting loop, set `library.Kind` before choosing `library.Flow`.
- If `library.Flow` is blank:
  - use `DefaultDownloadFlowName` when `library.Kind == "download"`;
  - otherwise use `DefaultFlowName`.
- Split `applyLibraryPolicyDefaults` by kind:
  - media libraries get default `Media.ReplacementMode`.
  - download libraries get download policy defaults (`stable_for`, `package_mode`, `handoff_mode`, `ignorable_globs`) and do not receive `Media.ReplacementMode`.

Keep behavior for existing media libraries unchanged.

Note on merge semantics (verified at `4149ac0`): `Load` starts from `Default()` and
`toml.DecodeFile` merges user TOML into the pre-populated flows map, so both
built-in flows remain available even when the user defines their own flows.
Registering `DefaultDownloadFlowName` in `Default()` is therefore sufficient.

**Verify**: `go test ./pkg/config` → this may fail until validation/tests are updated, but it must compile. If it does not compile, fix before continuing.

### Step 2: Validate download flow semantics

In `pkg/config/validate.go`, add a small unexported helper near the existing validation helpers, for example:

```go
func flowHasStep(flow FlowConfig, step string) bool {
    for _, configured := range flow.Steps {
        if strings.EqualFold(strings.TrimSpace(configured), step) {
            return true
        }
    }
    return false
}
```

Note: `cmd/anvild/preflight.go` already has its own `flowHasStep` over
`domain.Flow`. The new helper operates on `config.FlowConfig` in a different
package; keep it as a deliberate small duplicate rather than sharing across
packages.

Then update library validation:

- **Trap — do this first**: the `validReplacementMode` check at
  `pkg/config/validate.go:184` currently runs unconditionally. After Step 1,
  download libraries have an empty `Media.ReplacementMode`, which
  `validReplacementMode` rejects. Move that check under a `library.Kind ==
  "media"` (or non-download) branch, or every defaulted download library
  fails validation.
- Look up the referenced flow only when it exists, so unknown-flow errors do not cascade into misleading step errors.
- For media libraries:
  - validate `library.Media.ReplacementMode` as today.
- For download libraries:
  - require `download.handoff_path` as today.
  - require the referenced flow to contain `handoff`.
  - reject referenced flows containing `replace`.
  - reject a non-empty `media.replacement_mode` on download libraries, because it is ignored and invites confusion.

Do not add reciprocal media-library `handoff` rejection in this plan unless an existing test already establishes that as intended; keep the change narrowly targeted at the unsafe download default.

**Verify**: `go test ./pkg/config` → exit 0 once tests from the next step are added.

### Step 3: Add config tests

In `pkg/config/config_test.go`, add focused tests using the existing `writeConfig(t, body)` helper:

1. `TestLoadDefaultsDownloadLibraryToHandoffFlow`
   - TOML: a download library with `kind = "download"`, a `path`, and `[libraries.<name>.download] handoff_path = "/imports/tv"`, but no explicit `flow`.
   - Assert `cfg.Libraries[name].Flow == DefaultDownloadFlowName`.
   - Assert `cfg.Flows[DefaultDownloadFlowName].Steps` contains `"handoff"` and does not contain `"replace"`.

2. `TestLoadRejectsDownloadLibraryUsingReplaceFlow`
   - TOML: define a flow with `steps = ["probe", "stage", "replace", "cleanup"]` and a download library that references it.
   - Assert `Load` returns an error.
   - Assert the error mentions the library and `replace`.

3. `TestLoadRejectsDownloadLibraryMediaReplacementMode`
   - TOML: a download library with a valid handoff path and an explicit `[libraries.<name>.media] replacement_mode = "replace"`.
   - Assert `Load` returns an error.

4. If there is an existing default-flow test, update it to assert both default flows are present. If not, add a compact test for `Default()`.

Use small helper functions for step membership only if they make the tests clearer; keep them local to the test file.

**Verify**: `go test ./pkg/config` → all config tests pass.

### Step 4: Align example and NixOS module defaults

In `examples/anvil.toml` (verified at `4149ac0`: both flows already exist and
the download library already references `download-av1-handoff`, so this is
comment-only work):

- Keep the explicit `av1-replace` and `download-av1-handoff` flows.
- Add a short comment near the download library or download flow saying download libraries must use handoff flows, not replace flows.
- Confirm the file remains accepted by config validation.

In `nix/modules/anvil.nix`:

- Split `defaultFlowSteps` into media and download variants, or add a second `defaultDownloadFlowSteps` next to the existing list.
- Include both flows in the default `services.anvil.flows` attrset:
  - `av1-crf-search.steps = <media replace steps>`
  - `download-av1-handoff.steps = <download handoff steps>`
- Make `library.flow` default based on `library.kind`.
  - Convert `libraryModule = types.submodule { ... }` to a submodule function if needed: `types.submodule ({ config, lib, ... }: { ... })`.
  - Add `mkDefault` to the inherited `lib` names if you use it.
  - Remove the hard-coded `default = "av1-crf-search"` from the `flow` option and set `config.flow = mkDefault (...)` instead.

**Verify**:

- `go run ./cmd/anvild check-config --config examples/anvil.toml` → exit 0 and logs `config ok`.
- Nix eval. `.#nixosModules.anvil` is a bare module function (`import
  ./nix/modules/anvil.nix` in `flake.nix`), so it cannot be evaluated to JSON
  directly. Instead instantiate a minimal NixOS system and assert the
  kind-based flow default:

  ```sh
  nix eval --impure --expr '
    let
      flake = builtins.getFlake (toString ./.);
    in (flake.inputs.nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        flake.nixosModules.anvil
        {
          services.anvil.enable = true;
          services.anvil.libraries.dl = {
            kind = "download";
            path = "/downloads";
            download.handoffPath = "/imports";
          };
        }
      ];
    }).config.services.anvil.libraries.dl.flow
  '
  ```

  Expected output: `"download-av1-handoff"`. This harness was tested at
  `4149ac0` and evaluates cleanly, returning the pre-fix default
  `"av1-crf-search"` — so a wrong answer means the module change is
  incomplete, not that the command is broken. Note the NixOS module options
  are camelCase (`handoffPath`), unlike the TOML keys it renders. If Nix is
  unavailable in the executor environment, record that and continue with Go
  verification.

### Step 5: Run final verification

Run the full required gates:

```sh
make fmt
make test
make lint
```

Expected: all exit 0, with `make lint` reporting no issues.

## Test plan

- New tests live in `pkg/config/config_test.go` and use the existing `writeConfig` pattern near the current download-library tests.
- Cover default download flow selection, rejection of download+replace flow, and rejection of download `media.replacement_mode`.
- Existing tests for media defaults and explicit download handoff configs must continue to pass.

## Done criteria

All must hold:

- [ ] Download libraries with no explicit flow default to `DefaultDownloadFlowName`.
- [ ] The default download flow contains `handoff` and does not contain `replace`.
- [ ] Download libraries cannot validate with a replace flow.
- [ ] Download libraries do not inherit or accept `media.replacement_mode`.
- [ ] NixOS module defaults include a download handoff flow and default download libraries to it.
- [ ] `go run ./cmd/anvild check-config --config examples/anvil.toml` exits 0.
- [ ] `make fmt`, `make test`, and `make lint` exit 0.
- [ ] No files outside the in-scope list are modified, except `plans/README.md` status.

## STOP conditions

Stop and report back if:

- Any current-state excerpt above no longer matches the live code.
- You discover an intentional supported use case for download libraries running `replace`.
- Preserving media-library defaults would require changing pipeline or replace behavior.
- Nix module kind-specific defaults require a larger module redesign than described here.
- Verification fails twice after reasonable fixes.

## Maintenance notes

- Future flow additions should keep media and download completion semantics separate.
- Reviewers should scrutinize validation error messages: they should guide operators toward `handoff`, not just say the config is invalid.
- If a future feature intentionally supports in-place processing of download roots, it should be modeled as a new explicit library mode rather than reusing media `replace` implicitly.
