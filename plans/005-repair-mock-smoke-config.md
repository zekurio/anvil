# Plan 005: Repair the mock smoke fixture configuration drift

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 4149ac0..HEAD -- scripts/mock-library.sh Makefile pkg/config/schema.go pkg/config/validate.go pkg/config/config.go pkg/config/config_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: dx
- **Planned at**: commit `4149ac0`, 2026-07-02

## Why this matters

The mock library smoke fixture is the repo's fastest end-to-end confidence check for scanner, scheduler, worker, pipeline, replace, and handoff behavior. Its generated TOML has drifted from the current semantic video config schema, so `make mock-smoke` can fail before it exercises the daemon. A broken smoke fixture removes an important safety net for exactly the data-safety paths this project prioritizes.

## Current state

Relevant files:

- `scripts/mock-library.sh` — creates the fixture, writes generated TOML, and runs smoke.
- `Makefile` — exposes `mock-library` and `mock-smoke` targets.
- `pkg/config/schema.go` — current TOML schema; no `pixel_format` key exists.
- `pkg/config/validate.go` — validates semantic video codec names.
- `pkg/config/config.go` — rejects unknown TOML keys.
- `pkg/config/config_test.go` — existing tests around config schema drift.

Current excerpts to confirm before editing:

```make
# Makefile:20-24
mock-library:
    scripts/mock-library.sh setup

mock-smoke:
    scripts/mock-library.sh run
```

```bash
# scripts/mock-library.sh:165-180
[flows.mock-copy]
steps = ["probe", "crop-detect", "audio-cleanup", "subtitle-cleanup", "stage", "encode", "dovi-fix", "validate", "replace", "cleanup"]

[flows.mock-handoff]
steps = ["probe", "crop-detect", "audio-cleanup", "subtitle-cleanup", "stage", "encode", "dovi-fix", "validate", "handoff", "cleanup"]

[profiles.mock-av1.video]
codec = "libsvtav1"
preset = "13"
pixel_format = "yuv420p10le"
crf_min = 45
crf_max = 45
target_vmaf = 0
```

```go
// pkg/config/schema.go:89-101
VideoConfig struct {
    Codec              string            `toml:"codec"`
    Accelerator        string            `toml:"accelerator"`
    Preset             string            `toml:"preset"`
    BitDepth           int               `toml:"bit_depth"`
    CRFMin             int               `toml:"crf_min"`
    CRFMax             int               `toml:"crf_max"`
    TargetVMAF         float64           `toml:"target_vmaf"`
    ...
}
```

```go
// pkg/config/validate.go:73-80
if !validVideoCodec(profile.Video.Codec) {
    problems = append(problems, fmt.Sprintf("profile %q video.codec %q is invalid (must be av1, hevc, h265, h264, or avc)", name, profile.Video.Codec))
}
if !validAccelerator(profile.Video.Accelerator) {
    problems = append(problems, fmt.Sprintf("profile %q video.accelerator %q is invalid (must be software, qsv, vaapi, or amf)", name, profile.Video.Accelerator))
}
```

```go
// pkg/config/config.go:13-20
meta, err := toml.DecodeFile(path, &cfg)
...
if undecoded := meta.Undecoded(); len(undecoded) > 0 {
    return Config{}, fmt.Errorf("load config %q: unknown config keys: %s", path, formatUndecodedKeys(undecoded))
}
```

Important safety rule: do not reproduce or change mock API key values in plan text or commit messages. If you touch nearby script lines, keep fixture credentials disposable and local to `tmp/mock-library`.

Repo conventions to match:

- Keep examples and commands accurate.
- Do not relax config validation to make stale fixtures pass; update fixtures to the current schema.
- Run `make fmt`, `make test`, and `make lint`; run `make mock-smoke` when fixture/external-tool behavior changes and required tools are available.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Shell syntax | `bash -n scripts/mock-library.sh` | exit 0 |
| Config unit tests | `go test ./pkg/config ./cmd/anvild` | exit 0 |
| Generated config check | `make mock-config-check` | exit 0 after you add the target |
| Format | `make fmt` | exit 0 |
| Tests | `make test` | exit 0 |
| Lint | `make lint` | exit 0, no issues |
| Smoke | `make mock-smoke` | exit 0 if external tools are available |

## Suggested executor toolkit

- Use `golang-testing` for any Go tests you add.
- Use shellcheck if it is already available, but do not add it as a new dependency in this plan.

## Scope

**In scope** (the only source files you should modify):

- `scripts/mock-library.sh`
- `Makefile`
- `pkg/config/config_test.go` only if you add or update config drift tests
- `plans/README.md` status row when done

**Read-only reference files**:

- `pkg/config/schema.go`
- `pkg/config/validate.go`
- `pkg/config/config.go`

**Out of scope** (do NOT touch):

- Relaxing config schema or validation to accept `pixel_format` or concrete ffmpeg encoder names.
- Changing production defaults in `pkg/config/defaults.go`.
- Reworking the smoke fixture media generation beyond what is needed for config validity.
- Adding new external tool dependencies to ordinary `make test`.

## Git workflow

- Branch: `repair-mock-smoke`
- Commit message: `fix(scripts): update mock smoke config schema`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Add a config-only script mode

In `scripts/mock-library.sh`:

- Update `usage()` to include a config-only command, for example:
  - `scripts/mock-library.sh config [root]`
- Add a `config)` branch in `main()` that runs `mkdir -p "$root"`, then `write_config "$root"` and `print_paths "$root"`.
  - The `mkdir -p` is required: `write_config` does `cat > "$root/config.toml"` and only `setup_library` currently creates `$root`. Without it the new command fails on a fresh checkout.
- Do not call `setup_library` from this command. The point is to validate generated TOML without requiring `ffmpeg` or generating sample media. Verified at `4149ac0`: config validation neither stats library paths nor reads `api_key_file` contents (`pkg/config/validate.go:148` only requires the key setting to be non-empty), so `check-config` passes without the media tree or secrets files existing.
- Keep `setup`, `run`, `serve-arrs`, `reset`, and `paths` behavior unchanged.

**Verify**: `bash -n scripts/mock-library.sh` → exit 0.

### Step 2: Update the generated mock profile to the current schema

In the TOML emitted by `write_config` in `scripts/mock-library.sh`, update `[profiles.mock-av1.video]`:

- Replace `codec = "libsvtav1"` with `codec = "av1"`.
- Add `accelerator = "software"`.
- Remove `pixel_format = "yuv420p10le"`.
- Add `bit_depth = 10`.
- Preserve the fixture's fast encode intent unless smoke verification proves it invalid:
  - keep `preset = "13"`;
  - keep `crf_min = 45` and `crf_max = 45`;
  - keep `target_vmaf = 0`.

The mock flows currently skip `crf-search`; if that is intentional for speed, add a short TOML comment above the mock flow definitions explaining that the fixture bypasses CRF search and uses the configured CRF bounds directly. Do not add `crf-search` unless you also update tool requirements and smoke timing expectations.

**Verify**:

```sh
tmp="$(mktemp -d)"
scripts/mock-library.sh config "$tmp"
go run ./cmd/anvild check-config --config "$tmp/config.toml"
rm -rf "$tmp"
```

Expected: `check-config` exits 0 and logs `config ok`.

### Step 3: Add a repeatable Makefile gate for generated config

In `Makefile`:

- Add `mock-config-check` to `.PHONY`.
- Add a target that creates a temporary directory, runs the new script config mode, validates it, and removes the temporary directory.

Suggested target shape:

```make
mock-config-check:
	@tmp="$$(mktemp -d)"; \
	trap 'rm -rf "$$tmp"' EXIT; \
	scripts/mock-library.sh config "$$tmp" >/dev/null; \
	go run ./cmd/anvild check-config --config "$$tmp/config.toml"
```

Keep tabs for Makefile recipe lines.

**Verify**: `make mock-config-check` → exit 0 and logs `config ok`.

### Step 4: Add or update config drift tests if appropriate

Check `pkg/config/config_test.go` for existing tests around stale video config keys and concrete ffmpeg encoder names.

- If there is already a test rejecting concrete ffmpeg encoder names, ensure it covers `libsvtav1`.
- If there is already a test rejecting `pixel_format`, leave it intact.
- Do not add a Go test that shells out to `scripts/mock-library.sh` unless it stays fast and does not require external tools. The `mock-config-check` target is the primary generated-fixture gate.

**Verify**: `go test ./pkg/config ./cmd/anvild` → exit 0.

### Step 5: Run final verification, including smoke when available

Run:

```sh
bash -n scripts/mock-library.sh
make mock-config-check
make fmt
make test
make lint
```

Expected: all exit 0.

Then run:

```sh
make mock-smoke
```

Expected: exit 0 and prints `Mock smoke completed.` If this fails because required external tools or encoders are missing in the executor environment, record the missing tool/environment failure in your completion notes and do not weaken config validation to make it pass. If it fails after jobs start due scanner/scheduler/worker/pipeline behavior, stop and report; that is beyond fixture schema drift.

## Test plan

- `bash -n scripts/mock-library.sh` catches shell syntax errors.
- `make mock-config-check` validates generated TOML without generating sample media.
- `go test ./pkg/config ./cmd/anvild` keeps config validation tests passing.
- `make mock-smoke` remains the end-to-end gate when external media tools are available.

## Done criteria

All must hold:

- [ ] `scripts/mock-library.sh config <tmpdir>` writes a config without generating sample media.
- [ ] Generated mock config uses semantic `codec = "av1"`, `accelerator = "software"`, and `bit_depth = 10`.
- [ ] Generated mock config no longer includes `pixel_format` or concrete ffmpeg encoder names as config values.
- [ ] `make mock-config-check` exits 0.
- [ ] `bash -n scripts/mock-library.sh`, `make fmt`, `make test`, and `make lint` exit 0.
- [ ] `make mock-smoke` is run if tools are available; outcome is recorded.
- [ ] No files outside the in-scope list are modified, except `plans/README.md` status.

## STOP conditions

Stop and report back if:

- Any current-state excerpt above no longer matches the live code.
- The only way to pass validation appears to be accepting stale `pixel_format` or concrete encoder config.
- `make mock-config-check` still fails after updating the generated config.
- `make mock-smoke` fails because of behavior outside fixture config drift.
- You need to expose or commit any real credential value.

## Maintenance notes

- Keep `mock-config-check` cheap so it can be added to future CI without requiring media tools.
- The smoke fixture should track the public config schema, not private ffmpeg command details.
- If future profile schema changes occur, update this fixture and `make mock-config-check` in the same change.
- Interaction with Plan 001 (verified at `4149ac0`): the fixture's download library already uses the `mock-handoff` flow and sets no `media.replacement_mode`, so Plan 001's new download-library validation does not break this fixture. If both plans land, run `make mock-config-check` once more afterward.
