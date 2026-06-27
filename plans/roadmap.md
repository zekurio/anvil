# Roadmap

## Done

### Phase 0: Project Foundation

- Initialized Go module and `pkg/` package layout.
- Added daemon entrypoint.
- Added TOML config loading, defaults, validation, and example config.
- Added Nix/devenv development environment.
- Created GitHub repository.

### Phase 1: Domain And Store

- Added durable domain concepts for libraries, flows, profiles, media sources, media assets, jobs, attempts, and states.
- Added expandable profile sections for video, audio, subtitles, metadata, attachments, and chapters.
- Added media and download library concepts.
- Added SQLite store using `modernc.org/sqlite`.
- Added migrations and schema setup.
- Added source/asset upsert primitives.
- Added idempotent job enqueueing.
- Added job leases, heartbeats, attempts, state transitions, and stale lease recovery.
- Wired `anvild` to open the store and recover stale jobs on startup.

### Phase 2: Scanner Foundation

- Walk configured library paths.
- Apply include/exclude globs with `**` support.
- Apply download `ignorable_globs`.
- Filter likely media files.
- Skip samples.
- Fingerprint sources/assets with path, size, and mtime.
- Group nested download packages such as `SomeShowS01/SomeShowS01E01/episode_1.mkv`.
- Use all non-excluded package files for download stability checks.
- Insert/update media sources and assets.
- Enqueue source/asset jobs with library priority.
- Run one initial non-fatal scanner pass from `anvild`.

## Next

### Phase 3: Scheduler And Resources

Goal: lease work fairly and pass resource allocations into workers.

- Add scheduler loop.
- Respect daemon worker count.
- Respect library priority.
- Add per-library concurrency.
- Add `pkg/resources` thread allocator.
- Pass resource allocations into worker contexts.
- Decide how to split threads between `ab-av1 crf-search` and final `ffmpeg` encode.

### Phase 4: Worker Pipeline Skeleton

Goal: execute configured flows without real encode work first.

- Add `pkg/pipeline` block registry.
- Add `JobContext`.
- Resolve latest library, flow, and profile config when a job is leased.
- Start attempts with resolved config snapshots.
- Implement no-op or stub blocks for configured flow steps.
- Persist block start/finish/failure.
- Make failed blocks produce useful job errors.

### Phase 5: Probe And Search

Goal: produce real media metadata and a CRF result.

- Add ffprobe wrapper.
- Parse duration, streams, codec, pixel format, HDR-ish metadata, chapters, and attachments.
- Add `ab-av1 crf-search` backend.
- Parse search output into `SearchResult`.
- Persist probe/search results on attempts.

### Phase 6: Final Ffmpeg Encode

Goal: encode a candidate with an Anvil-owned ffmpeg command.

- Add `EncodePlan`.
- Render final ffmpeg args from `EncodePlan`.
- Use chosen CRF from search result.
- Add initial stream mapping policy.
- Add temp output path handling.
- Execute process with cancellation.
- Capture logs.

### Phase 7: Validation, Replacement, And Handoff

Goal: safely finish jobs only when output is acceptable.

- Validate output existence, probeability, duration, and required streams.
- Add minimum encode percentage / savings policy.
- For media libraries, replace originals with a backup workflow:

```text
movie.mkv -> movie.mkv.anvil-backup
movie.mkv.anvil-new -> movie.mkv
```

- For download libraries, build a staged handoff tree and copy or move it to the configured import path.
- Keep handoff separate from source cleanup.
- Delete source media only when `cleanup_source_media` is explicitly enabled.
- Prune source directories upward only while they are empty or contain only configured ignorable files.
- Preserve timestamps and permissions where practical.

### Phase 8: Operations

Goal: make the daemon comfortable to run.

- Add scanner and scheduler intervals.
- Add structured logs or a clear logging format.
- Add systemd unit example.
- Add graceful shutdown policy.
- Add config reload via `SIGHUP`.
- Add `nice` / `ionice` support.
- Add basic CLI commands if needed.

## Open Questions

- What exact meaning should "minimum encode percentage" have?
- Should output always preserve the original container extension, or follow profile container?
- Should all audio, subtitle, chapter, and attachment streams be preserved by default until explicit cleanup policies are implemented?
- How should file changes after a terminal job be detected and requeued?
- How aggressive should retries be for failed search/encode jobs, and should that be configurable per library/profile?
- Should Sonarr/Radarr context come from APIs, `.nfo` files, path conventions, or a normalized metadata provider interface first?
- Should the future management UI edit config files, write to SQLite, or manage a separate flow model?
