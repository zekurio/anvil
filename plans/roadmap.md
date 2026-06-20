# Roadmap

## Phase 0: Foundation

Status: mostly done.

- Initialize Git repo and Go module.
- Use `pkg/` package layout.
- Add Nix flake and devenv shell.
- Add daemon entrypoint.
- Add TOML config loading and validation.
- Add example config.
- Create GitHub repository.

## Phase 1: Domain And Store

Goal: define durable concepts before real encoding starts.

Tasks:

- Add domain models for libraries, flows, profiles, media files, jobs, attempts, and states.
- Choose SQLite driver.
- Add migrations.
- Implement store open/close and schema setup.
- Implement job creation and state transitions.
- Implement job lease and heartbeat primitives.
- Add recovery for stale running jobs.

## Phase 2: Scanner

Goal: turn configured libraries into queued candidate jobs.

Tasks:

- Walk configured library paths.
- Apply include/exclude globs.
- Filter likely media files.
- Fingerprint files enough to detect changes.
- Insert or update file records.
- Enqueue pending jobs.
- Add library priority to queued work.

Open detail:

- Decide whether fingerprinting starts with path/size/mtime or content hashing.

## Phase 3: Scheduler And Resources

Goal: select jobs fairly and allocate CPU sanely.

Tasks:

- Add scheduler loop.
- Respect library priority.
- Respect worker count.
- Add per-library concurrency.
- Add thread allocator in `pkg/resources`.
- Pass resource allocations into worker contexts.

Open detail:

- Decide how to split threads between `ab-av1 crf-search` and final `ffmpeg` encode.

## Phase 4: Worker Pipeline Skeleton

Goal: execute flows without doing real encode work yet.

Tasks:

- Add `pkg/pipeline` block registry.
- Add `JobContext`.
- Implement no-op or stub blocks for configured flow steps.
- Persist block start/finish/failure.
- Make failed blocks produce useful job errors.

## Phase 5: Probe And Search

Goal: produce a real CRF result.

Tasks:

- Add ffprobe wrapper.
- Parse duration, streams, codec, pixel format, HDR-ish metadata, chapters, and attachments.
- Add `ab-av1 crf-search` backend.
- Parse search output into `SearchResult`.
- Persist search result on job attempt.

## Phase 6: Final Ffmpeg Encode

Goal: encode a candidate file with an Anvil-owned ffmpeg command.

Tasks:

- Add `EncodePlan`.
- Render final ffmpeg args from `EncodePlan`.
- Use chosen CRF from search result.
- Add initial stream mapping policy.
- Add temp output path handling.
- Execute process with cancellation.
- Capture logs.

## Phase 7: Validation And Replacement

Goal: safely replace originals only when output is acceptable.

Tasks:

- Validate output existence, probeability, duration, and required streams.
- Add minimum encode percentage / savings policy.
- Copy final output beside original if temp dir is on another filesystem.
- Perform local replacement with backup:

```text
movie.mkv -> movie.mkv.anvil-backup
movie.mkv.anvil-new -> movie.mkv
```

- Clean up backup according to retention policy.
- Preserve timestamps and permissions where practical.

## Phase 8: Operations

Goal: make the daemon comfortable to run.

Tasks:

- Add structured logs or a clear logging format.
- Add systemd unit example.
- Add graceful shutdown policy.
- Add config reload via `SIGHUP`.
- Add `nice` / `ionice` support.
- Add basic CLI commands if needed.

## Open Questions

- Should replacement happen immediately, or should Anvil support a review/quarantine mode first?
- Should output always preserve the original container extension?
- What exact meaning should "minimum encode percentage" have?
- Should all audio, subtitle, chapter, and attachment streams be preserved by default?
- How aggressive should retries be for failed search/encode jobs?
- Should the future management UI edit config files, write to SQLite, or manage a separate flow model?
