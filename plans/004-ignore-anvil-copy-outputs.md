# Plan 004: Ignore copy-mode Anvil outputs during scanning

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 4149ac0..HEAD -- pkg/replace/replace.go pkg/replace/replace_test.go pkg/scanner/scanner.go pkg/scanner/scanner_test.go cmd/anvild/preflight.go cmd/anvild/preflight_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `4149ac0`, 2026-07-02

## Why this matters

When a media library uses `replacement_mode = "copy"`, Anvil writes the processed output beside the source as `*.anvil.mkv`. The scanner currently treats that filename as another ordinary media candidate unless users configure explicit excludes. That can recursively enqueue Anvil's own copy outputs and produce repeated `.anvil` copies. The scanner should have a built-in guard for Anvil-generated copy outputs while still allowing legitimate titles that merely contain the word “Anvil”.

## Current state

Relevant files:

- `pkg/replace/replace.go` — defines copy-mode output naming.
- `pkg/replace/replace_test.go` — replacement path tests.
- `pkg/scanner/scanner.go` — discovers media candidates and applies ignore/exclude rules.
- `pkg/scanner/scanner_test.go` — scanner discovery and enqueue tests.
- `cmd/anvild/preflight.go` — currently warns but does not suppress `.anvil` candidates.
- `cmd/anvild/preflight_test.go` — preflight report tests.

Current excerpts to confirm before editing:

```go
// pkg/replace/replace.go:220-225
func replacementCopyPath(inputPath string, ext string) string {
    if ext == "" {
        ext = filepath.Ext(inputPath)
    }
    base := strings.TrimSuffix(inputPath, filepath.Ext(inputPath))
    return base + ".anvil" + ext
}
```

```go
// pkg/scanner/scanner.go:351-372
if !likelyMediaFile(rel) {
    return nil
}

included, err := includedBy(library.Include, rel)
...
role := classifyMediaAsset(rel)
if role == domain.MediaAssetRoleSample {
    candidates = append(candidates, buildCandidate(library, rel, info, true, "sample"))
    skipped++
    return nil
}

candidates = append(candidates, buildCandidate(library, rel, info, false, ""))
```

```go
// pkg/scanner/scanner.go:524-527
func likelyMediaFile(rel string) bool {
    switch strings.ToLower(path.Ext(rel)) {
    case ".mkv", ".mp4", ".m4v", ".mov", ".avi", ".webm", ".ts", ".m2ts":
        return true
```

```go
// cmd/anvild/preflight.go:545-550
func preflightExcludeWarnings(candidate scanner.CandidatePlan) []string {
    lower := strings.ToLower(candidate.LibraryRelativePath)
    var warnings []string
    if strings.Contains(lower, ".anvil") && !candidate.Ignored {
        warnings = append(warnings, "candidate path looks like an Anvil output; add an explicit exclude for .anvil outputs")
    }
```

Repo conventions to match:

- Avoid duplicate policy logic when it can drift; copy-output naming and copy-output detection must share the same suffix rule.
- Keep scanning cheap. Do not probe media files during discovery.
- Be conservative around filesystem mutation; this plan only prevents future enqueueing and must not delete files or mutate existing jobs.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Targeted tests | `go test ./pkg/replace ./pkg/scanner ./cmd/anvild` | exit 0 |
| Scanner test filter | `go test ./pkg/scanner -run 'Anvil|Ignore|Scan'` | exit 0 |
| Preflight test filter | `go test ./cmd/anvild -run Preflight` | exit 0 |
| Format | `make fmt` | exit 0 |
| Tests | `make test` | exit 0 |
| Lint | `make lint` | exit 0, no issues |
| Build | `make build` | exit 0 if you add a new exported helper or package import |

## Suggested executor toolkit

- Use `golang-code-style`, `golang-testing`, and `golang-project-layout` if available.
- Keep helpers small and close to the package that owns the naming rule.

## Scope

**In scope** (the only source files you should modify):

- `pkg/replace/replace.go`
- `pkg/replace/replace_test.go`
- `pkg/scanner/scanner.go`
- `pkg/scanner/scanner_test.go`
- `cmd/anvild/preflight.go`
- `cmd/anvild/preflight_test.go`
- `plans/README.md` status row when done

**Out of scope** (do NOT touch):

- Store schema or cleanup of already-enqueued `.anvil` jobs.
- Deleting, moving, or renaming any media files.
- Changing copy-mode output naming beyond centralizing the existing `.anvil` suffix.
- Adding ffprobe/metadata probing to scanner discovery.

## Git workflow

- Branch: `ignore-anvil-outputs`
- Commit message: `fix(scanner): ignore Anvil copy outputs`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Centralize copy-output suffix detection

In `pkg/replace/replace.go`:

- Add a named suffix constant near the replacement action constants, for example `anvilCopySuffix = ".anvil"`.
- Update `replacementCopyPath` to use that constant instead of the string literal.
- Add an exported helper, for example:

```go
func IsAnvilCopyOutputPath(path string) bool
```

Required helper behavior:

- Use only the basename and extension; do not stat the file.
- Return true for `Movie.anvil.mkv` and `Movie.ANVIL.mkv`.
- Return false for `The.Anvil.2020.mkv`.
- Return false for paths with no extension.
- Do not decide whether the extension is a media extension; scanner already owns that check.

Implementation hint:

```go
base := filepath.Base(path)
ext := filepath.Ext(base)
stem := strings.TrimSuffix(base, ext)
return ext != "" && strings.HasSuffix(strings.ToLower(stem), anvilCopySuffix)
```

Add table-driven tests in `pkg/replace/replace_test.go` for the helper and keep the existing copy-path tests passing.

**Verify**: `go test ./pkg/replace` → exit 0.

### Step 2: Ignore Anvil copy outputs during candidate discovery

In `pkg/scanner/scanner.go`:

- Import `replace` with an alias such as `replacepkg`. Verified at `4149ac0`:
  `go list -deps ./pkg/replace` shows it depends only on `pkg/domain` and
  `pkg/pipeline`, so a `scanner → replace` import cannot create a cycle.
- Use the literal ignore reason `"anvil_output"` inline, matching the existing
  inline-string reasons in this file (`"excluded"`, `"sample"`,
  `"not_included"`, `"ignore_regex"`). Do not introduce a lone constant for
  just this reason.
- In `discoverCandidates`, after configured regex/exclude checks and after confirming the entry is a regular file, but before `recordSourceStat`, add the built-in guard:
  - if `likelyMediaFile(rel)` and `replacepkg.IsAnvilCopyOutputPath(rel)`, append an ignored candidate with reason `"anvil_output"`, increment `skipped`, and return nil.

Keep configured excludes and regexes before this built-in guard so explicitly excluded paths keep their existing `IgnoreReason`.

Do not record Anvil copy outputs into `sourceStats`; they should not refresh the source fingerprint for the original media.

**Verify**: `go test ./pkg/scanner` → exit 0.

### Step 3: Update scanner tests for built-in ignored outputs

In `pkg/scanner/scanner_test.go`, add a test using `t.TempDir()` and existing scanner helper patterns.

Test cases to cover:

- `Movie.mkv` is enqueueable.
- `Movie.anvil.mkv` is returned as ignored with `IgnoreReason == "anvil_output"` and is not enqueued.
- `Movie.ANVIL.mkv` is also ignored.
- `The.Anvil.2020.mkv` is not ignored solely because it contains `.Anvil` in the title.
- Use a library without explicit `.anvil` excludes, or the reason will be `excluded` instead of `anvil_output`.

If existing helpers make it easier to test `PlanLibrary` than `ScanLibrary`, prefer `PlanLibrary` for precise candidate assertions, then add one `ScanLibrary` assertion that ignored outputs are not enqueued.

**Verify**: `go test ./pkg/scanner -run 'Anvil|Ignore|Scan'` → exit 0.

### Step 4: Align preflight warnings with the scanner guard

In `cmd/anvild/preflight.go` (verified at `4149ac0`: this file already imports
`pkg/replace` as `replacepkg`, so no new import is needed):

- Stop using broad `strings.Contains(lower, ".anvil")` for unignored warnings.
- Use the same precise helper (`replacepkg.IsAnvilCopyOutputPath`) if the candidate is not ignored.
- If `candidate.IgnoreReason == "anvil_output"`, either emit no warning or emit a clear informational warning such as `Anvil copy output skipped by built-in guard`.
- Do not tell users to “add an explicit exclude” for paths the scanner now ignores by default.

Update `cmd/anvild/preflight_test.go`:

- Add a preflight test with `Movie.mkv`, `Movie.anvil.mkv`, and `The.Anvil.2020.mkv` in a library without explicit `.anvil` excludes.
- Assert the `.anvil` copy output is ignored and not counted in `WouldEnqueue`.
- Assert the old “add an explicit exclude” warning is absent for the ignored copy output.
- Assert `The.Anvil.2020.mkv` is not false-warned solely because of its title.

**Verify**: `go test ./cmd/anvild -run Preflight` → exit 0.

### Step 5: Run final verification

Run:

```sh
make fmt
make test
make lint
make build
```

Expected: all exit 0, with `make lint` reporting no issues. `make build` is included because this plan may add an exported helper and new cross-package import.

## Test plan

- `pkg/replace/replace_test.go`: helper recognizes exactly copy-output suffixes.
- `pkg/scanner/scanner_test.go`: scanner ignores copy outputs by default while not ignoring legitimate titles containing “Anvil”.
- `cmd/anvild/preflight_test.go`: preflight reports the new ignored state and removes stale exclude guidance.

## Done criteria

All must hold:

- [ ] `replacementCopyPath` and scanner detection share the same suffix rule.
- [ ] `Movie.anvil.mkv` and case variants are ignored by scanner without explicit excludes.
- [ ] `The.Anvil.2020.mkv` is still treated as a normal media file when it otherwise matches include rules.
- [ ] Preflight no longer tells users to add explicit excludes for built-in ignored Anvil copy outputs.
- [ ] No existing queued jobs or media files are modified by this change.
- [ ] `make fmt`, `make test`, `make lint`, and `make build` exit 0.
- [ ] No files outside the in-scope list are modified, except `plans/README.md` status.

## STOP conditions

Stop and report back if:

- Any current-state excerpt above no longer matches the live code.
- Importing `pkg/replace` from `pkg/scanner` creates an import cycle. This was verified impossible at `4149ac0` (`pkg/replace` depends only on `pkg/domain` and `pkg/pipeline`); if drift since then introduced one, stop and propose a tiny shared path-policy package instead of duplicating logic.
- Product intent requires metadata-based detection of `anvil.processed`; scanner does not currently probe media and that is out of scope.
- The fix starts deleting or mutating existing files/jobs.
- A legitimate required use case depends on processing filenames ending exactly `.anvil.<media-ext>`.

## Maintenance notes

- Reviewers should check false positives carefully. The guard must be suffix-based on the filename stem, not a broad substring match.
- If copy-output naming ever changes, update the centralized helper and its tests first.
- This plan prevents future recursive enqueueing; it intentionally does not clean historical recursive jobs.
