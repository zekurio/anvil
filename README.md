# Anvil

Anvil is a self-hosted Linux daemon that re-encodes media libraries with
FFmpeg. You add folders as libraries, draw a flow that says what happens to
each file, and Anvil works through the queue. The flow it ships with encodes
video to AV1 at a quality target. HEVC and H.264 are available too, and a flow
can copy the video and only clean up audio, subtitles, and metadata. In a media
library the result replaces the source in place. In a download library it goes
to the folder your importer watches. Both go through a journaled publish, so a
crash never leaves a half-written file under a real name.

Anvil is a smaller alternative to Tdarr and FileFlows, and it is small on
purpose. It is one daemon binary plus `ffmpeg` and `ffprobe`. Flows are built
from a fixed set of built-in nodes. There are no plugins, no script nodes, and
no worker nodes on other machines.

Linux is a hard requirement. Anvil watches the filesystem with inotify, holds a
`flock` so a second daemon can't open the same store, takes operator commands
over a Unix socket, and cancels work by killing whole process groups.

There are two binaries. `anvild` is the service. It runs the queue and serves
the web UI, where you manage libraries, flows, and Arr connections. `anvilctl`
is the terminal client, the way `systemctl` is for systemd. It never opens the
database or runs a media tool. It asks the running daemon instead, because a
second writer on a live store is how half-published files happen.

Anvil is early and changes often. Expect sharp edges.

## Quick start

You need `ffmpeg` and `ffprobe` on `PATH`, built with your video encoder and
with the `libvmaf` or `xpsnr` filter. The default flow uses `libsvtav1` and
`libvmaf`. Building takes Go 1.26.4 or newer and Node.js 22.12 or newer.

```sh
git clone https://github.com/zekurio/anvil
cd anvil
make all
```

`make all` builds the web UI and then `bin/anvild` and `bin/anvilctl`. Give the
daemon a directory for its state and a config that points into it:

```sh
mkdir -p ~/anvil/tmp
cat > ~/anvil/anvil.toml <<EOF
[daemon]
temp_dir = "$HOME/anvil/tmp"
store_path = "$HOME/anvil/anvil.db"
control_socket = "$HOME/anvil/anvild.sock"

[web]
listen = "127.0.0.1:8484"
EOF

bin/anvild check-config --config ~/anvil/anvil.toml
bin/anvild --config ~/anvil/anvil.toml
```

With Nix you can skip the build. The packaged daemon carries the web UI and
Jellyfin's ffmpeg:

```sh
nix run github:zekurio/anvil#anvild -- --config ~/anvil/anvil.toml
```

Open <http://127.0.0.1:8484>. Then:

1. Choose Add library in the sidebar. Give it a name, a kind, a folder, and
   the `Default AV1` flow. Leave "Start processing right away" off. The library
   is created paused, and its first scan queues the files without touching
   them.
2. Open Flows. `Default AV1` searches for an AV1 encode at VMAF 95, crops
   black bars, keeps every audio and subtitle track, and strips track titles.
   To change it, use Duplicate, edit the copy, and assign the copy to the
   library.
3. In the flow editor, choose Test on a file and pick a file from the
   library. Anvil probes it and shows the path it takes through the flow, each
   decision, the plan, and the ffmpeg command. Nothing is encoded or written.
4. On the library page, choose Resume library. The Queue page shows each
   running job with its step, percent, fps, speed, and time left.

`anvilctl` talks to the same daemon:

```sh
export ANVIL_CONTROL_SOCKET=~/anvil/anvild.sock
bin/anvilctl status
bin/anvilctl libraries
bin/anvilctl jobs
```

## Concepts

### Libraries

A library is a folder Anvil scans, with one flow assigned to it. Its name is 1
to 64 characters of `a-z`, `0-9`, `-`, and `_`. Name and kind cannot change
later.

A media library holds films or series. With replacement mode `replace`
the encode takes the source's place as `<name>.mkv`. With `copy` it is
published beside the source as `<name>.anvil.mkv` and the source stays.

A download library holds finished downloads. Each one is encoded and
handed off to the folder your importer watches. Its settings cover whether a
download is a single file or a directory, the handoff mode, whether the source
media is deleted after a successful handoff, and the quiet period that marks a
download as complete when Anvil saw no completion event.

Library folders must not contain each other, and a handoff folder must sit
outside every library folder. Anvil refuses a library that breaks this, because
a file under two libraries would be queued twice. Neither folder may overlap
`daemon.temp_dir`, where Anvil keeps its own working files. When
`daemon.media_roots` is set, both folders must also lie inside one of those
roots.

The scanner picks up `.mkv`, `.mp4`, `.m4v`, `.mov`, `.avi`, `.webm`, `.ts`,
and `.m2ts` files. It does not queue files with more than one hard link, files
with `sample` in the name or under a `sample*` directory, or its own
`.anvil.` copies and part files. A full scan runs every
`daemon.scan_interval`, and inotify events queue new files in between.

A paused library is still scanned and still queues files, but no new job
starts. Jobs that are already running finish. Libraries added in the web UI
start paused unless you say otherwise.

Each library also has a priority, an optional cap on simultaneous jobs, an
optional scan interval of its own, and an optional Arr connection. Anvil asks
that Radarr or Sonarr instance for a title's original language, which is what
the `orig` language token in a flow refers to. A new priority also applies to
the jobs the library already has in the queue.

Deleting a library removes it, its rules, and its job history from the store.
No media file is touched. Jobs that only wait in the queue are deleted with
it. Anvil refuses while a worker holds one of the library's jobs, or while a
publish that was interrupted part-way still has to be finished by the job's
next run. A publish that stopped in `conflict` does not block the delete.
Anvil then removes the job's own part file and leaves everything else the
conflict put on disk where it is.

### Rules

Rules decide at scan time which files of a library are queued. They run top to
bottom, the first rule that matches a file decides, and the library's fallback
decides when none matches.

| Field | Operator | Value |
| --- | --- | --- |
| `path` | `matches` | Doublestar glob on the path relative to the library, such as `**/Extras/**`. A glob without `/` also matches the file name alone, so `*.mkv` matches at any depth. |
| `path` | `regex` | Go regular expression on the same relative path. |
| `extension` | `is`, `is_not` | Comma-separated list without dots: `mkv, mp4`. |
| `size` | `gt`, `lt` | A number with a unit: `200 MB`, `1.5 GB`, `750 MiB`. `KB` to `TB` are powers of 1000, `KiB` to `TiB` powers of 1024. |

A deny rule on a path also covers everything inside a directory it matches.
Deny path rules that stand above every allow rule prune. The scanner does not
descend into what they match, ignores filesystem events below it, and leaves
it out of a download's size and stability checks. Every other denied file is
seen, counted as skipped, and not queued.

### Flows

A flow is a chart of nodes with exactly one input. Anvil evaluates it for each
file right after probing it, on every attempt:

- A condition node tests a fact from the probe and continues on its `true`
  or `false` output.
- An action node sets one part of the plan. When two nodes on a file's path
  set the same part, the last one wins. Encode video and Copy video share the
  video part.
- An output node ends the flow. Publish result runs the pipeline and
  publishes. Skip file marks the job skipped and leaves the file alone.

Whatever no node sets is left alone. A flow that is only Input file and
Publish result is a remux to MKV. It copies video, audio, and subtitles, and
keeps every track, title, attachment, and chapter.

Every output of every node on a path has to be connected, and a flow cannot
loop. A flow can have at most 500 nodes and 2000 edges. The editor will not
save a flow with errors. Saving changes what the
next attempt of every job in the assigned libraries does. Attempts that are
already running keep the flow they started with.

A flow cannot change what the engine does itself. Anvil always probes the
input, writes the output as MKV on the destination filesystem, validates it,
and publishes through the journal. It never overwrites an existing
destination. It tags the video stream of what it publishes with
`anvil.processed=true`, and a later job that finds the tag is skipped.
Requeueing a file in a media library runs it again anyway.

There is no per-file flow override, no scaling node, and no tone mapping.

### Node reference

Condition nodes read the main video stream, the audio tracks, or the file as a
whole. Each has a `true` and a `false` output.

| Node | True when | Options and defaults |
| --- | --- | --- |
| `check.video_codec` Check video codec | The video codec is one of the chosen ones. | `h264`, `hevc`, `av1`, `vp9`, `vp8`, `mpeg2`, `mpeg4`, `mpeg1`, `vc1`, `wmv3`, `theora`, `prores`. Default `av1`. |
| `check.resolution` Check resolution | The resolution class is at least, or at most, the chosen one. | `480p`, `720p`, `1080p`, `1440p`, `2160p`. Default at least `1080p`. A frame reaches a class by width or by height, so 1920x800 is `1080p`. |
| `check.hdr` Check HDR | The video is any of the chosen formats. | `hdr10`, `hlg`, `dolby_vision`. Default all three. Dolby Vision on an HDR10 or HLG base layer counts as both. |
| `check.bitrate` Check bitrate | The overall bitrate, from file size and duration, is above or below a value. | Default above 8000 kb/s. False when the duration is unknown. |
| `check.file_size` Check file size | The file is above or below a size. | Default above 1024 MiB. |
| `check.audio_language` Check audio language | Any audio track is in one of the languages. | Language tags, or `orig`. Default `eng`. An untagged track never matches. |
| `check.audio_codec` Check audio codec | Any audio track uses one of the codecs. | Codec names as ffprobe prints them. Default `truehd`, `dts`. |
| `check.audio_channels` Check audio channels | Any audio track has more channels than a layout. | Stereo or 5.1. Default stereo. |

Action nodes each have one output.

| Node | Sets | Options and defaults |
| --- | --- | --- |
| `video.encode` Encode video | Re-encodes the video. | Codec `av1`, `hevc`, or `h264`. Backend `software`, `qsv`, `vaapi`, or `amf`. Preset `6`. Bit depth 10 or 8. Quality is a target or a fixed CRF. Target: metric `vmaf` or `xpsnr`, score 95, CRF 18 to 40, smallest saving 20%, copy or encode when no CRF fits. Fixed CRF: 28. Advanced: samples per CRF, sample length `20s`, extra encoder arguments. |
| `video.copy` Copy video | Keeps the source video stream. | None. |
| `video.crop` Crop black bars | Turns black bar detection on. | Smallest picture to keep 70%. Advanced: sample windows, frames per window 300, offsets, black threshold 64, rounding 16 px, alignment 2 px, narrowest and shortest crop 128 px. |
| `audio.keep` Keep audio tracks | Which audio tracks stay. | Languages, empty for all. When no track matches: keep every track, keep the first, or fail the job. Keep commentary. Count untagged tracks as the original language. |
| `audio.encode` Encode audio | Re-encodes kept tracks to one codec. | Codec `opus`, `aac`, `ac3`, `eac3`, or `flac`. Default `eac3` at 96 kb/s per channel. Only these source codecs: default `truehd`, `dts`, empty for every kept track. |
| `audio.downmix` Downmix audio | Reduces tracks with more channels than a layout. | Largest layout 5.1 or stereo. Default 5.1, `eac3`, 96 kb/s per channel. |
| `subtitles.keep` Keep subtitles | Which subtitle tracks stay. | Languages, empty for all. The same fallback choices as audio. Keep forced, SDH, and commentary tracks. Count untagged tracks as the original language. |
| `subtitles.drop_bitmap` Remove image subtitles | Drops image-based tracks. | None. |
| `subtitles.remove_all` Remove all subtitles | Drops every subtitle track. | None. |
| `container.metadata` Clean metadata | Container metadata, track titles, attachments, chapters. | Each is preserve or strip. Track titles can also be standardized, and default to strip. The rest default to preserve. |

`input` Input file starts the flow. `output.publish` Publish result and
`output.skip` Skip file end it.

### Queue and jobs

Every queued file is a job. A job runs as one or more attempts, and an attempt
runs a fixed pipeline: `probe`, `flow`, `crop-detect`, `audio-cleanup`,
`subtitle-cleanup`, `stage`, `crf-search`, `encode`, `validate`, `publish`,
`cleanup`.

Job states are `pending`, `leased`, `running`, `validating`, `replacing`,
`complete`, `failed`, `retrying`, `skipped`, and `canceled`. The web UI groups
them as Queued, Running, Done, Failed, and Skipped. A failed attempt is retried
until the job has used `daemon.max_attempts` attempts, three by default. After
that the job stays failed until you retry it.

A retry does not start from nothing. Anvil checkpoints the probe, the audio
track selection, the crop, and a finished CRF search, and reuses them when the
input file and what the flow does to it are unchanged. Moving nodes around or
relabeling them does not count as a change.

While a job encodes, its percent, speed, and time left follow the count of
video frames written wherever that is ahead of FFmpeg's output timestamp. The
timestamp alone stalls when a sparse subtitle track ends early in the file.

The job view shows how the flow resolved, which tracks were kept and why, the
quality result, and every attempt with the commands it ran. `anvilctl show
JOB` prints the same record, with the publish journal.

`daemon.worker_count` is the number of jobs that run at once.
`daemon.total_threads` is the encoder thread budget they share.
`daemon.max_threads_per_job = 0` splits the budget across worker slots, so jobs
that arrive later can still start. A positive value sets a fixed per-job cap.
A running job keeps its allocation until it ends. The scheduler wakes when
work arrives or a worker finishes, and its timer is only a fallback.

## Video

### CRF search

With a quality target, Anvil looks for the highest integer CRF between Lowest
CRF and Highest CRF that still meets the target. It measures the upper
endpoint first, then the midpoint. After that it estimates the next CRF from
the last two scores. Estimates get a fixed budget, the number of bisections the
original range would need. A flat or reversed slope, or a spent budget, falls
back to plain bisection. Good estimates converge fast, and a bad score curve
still can't drag the search through every CRF.

The search assumes quality and size both drop as CRF rises, and it only
accepts CRFs it actually measured. The winner also has to meet the smallest
saving worth encoding. If nothing fits, Anvil copies the video stream. With
"Encode anyway" it also searches the size boundary, then picks the best
measured quality within the size limit, or the best measured quality overall
if no candidate fits the limit. A failed process or score always fails the
attempt, in that mode too.

Samples are evenly spaced. Zero samples per CRF takes one sample per 12
minutes of input, rounded up, and the sample length defaults to `20s`. When
the samples would cover 85% or more of the input, Anvil scores the whole video
once instead. It cuts clips at seekable keyframes, so a clip can include
preroll. Every candidate reuses the same clips and the same encoder settings
as the final encode. Anvil regenerates sample timestamps at the reported frame
rate, or 25 fps when that is unknown, so cutting reordered GOPs leaves no gaps.
Final encodes keep the source timeline and pass every source frame to the
encoder, so duplicate-timestamp and VFR sources come out full length. Each
encoded clip must decode to the same frame count as its reference.

VMAF uses ab-av1's default model and scaling. Sources smaller than 1728x972
get bicubic upscaling to 1080p. Sources larger than 2560x1440 use the
`vmaf_4k_v0.6.1` model, and those smaller than 3456x1944 get upscaled to 4K.
That way a VMAF target means the same thing at every resolution. XPSNR takes
the lowest of the Y, U, and V plane averages FFmpeg reports, then the
arithmetic mean across samples. Comparisons pair frames by index at fixed
analysis rates, 25 fps for VMAF and 60 fps for XPSNR. FFmpeg reports infinite
XPSNR for identical images, and Anvil records that as 100 so results stay valid
JSON. There is no HDR-specific metric model.

Savings compare the total bytes of the encoded clips with the reference clips.
Both are video-only Matroska clips, container overhead included. Audio,
subtitles, and attachments don't count. Sample estimates guarantee neither
whole-file quality nor final file size. The checkpoint stores every candidate
CRF, score, and size ratio, and each command writes a per-attempt process log.
An interrupted search starts over. A finished one resumes from its checkpoint.

### Fixed CRF

With a fixed CRF nothing is sampled or measured. There is no quality target,
no savings floor, and no "no fit" outcome, so the video is always encoded. The
editor checks the value against the encoder. `libsvtav1` takes 1 to 63,
`libx265` and `libx264` 0 to 51, QSV and VAAPI 1 to 51, and AMF 0 to 51. The
hardware AV1 encoders count to 255.

### Encoder arguments and crop

Extra encoder arguments are option and value pairs, one per entry, such as
`-svtav1-params` followed by `tune=0:film-grain=8`. They apply to both the
search and the final encode. Only an allow-list is accepted, and everything
else is rejected:

- The parameter bundles `-svtav1-params`, `-x265-params`, and `-x264-params`.
  Inside them, the keys `crf`, `qp`, `qpmin`, `qpmax`, `rc`, `bitrate`,
  `bit_rate`, `tbr`, `mbr`, and `preset` are refused, because Anvil sets
  quality, rate control, and speed itself.
- The tuning options `-tune`, `-profile`, `-level`, `-g`, `-keyint_min`,
  `-bf`, `-refs`, `-sc_threshold`, `-aq-mode`, `-aq-strength`,
  `-rc-lookahead`, `-b_strategy`, `-look_ahead`, `-look_ahead_depth`,
  `-extbrc`, `-low_power`, `-async_depth`, `-adaptive_i`, `-adaptive_b`, and
  `-quality`. A stream specifier such as `-profile:v` is allowed.

A value may not start with `-`. Nothing that names a file, a URL, a device, a
filter, a stream mapping, or timing gets through.

Cropping is opt-in per flow and happens in the encoder. A path that copies the
video crops nothing, and the editor warns about a crop node on such a path. A
detected crop that keeps less of the frame than the node allows is discarded,
and the file is encoded uncropped.

### HDR and Dolby Vision

HDR10 and HLG come through unchanged. For PQ and HLG sources, Anvil passes the
source's color range, matrix, transfer, and primaries to FFmpeg as explicit
options. FFmpeg carries HDR10 mastering display and content light level
metadata into the output. HDR10+ dynamic metadata is not carried, so those
sources play as HDR10. Validation records a finding when the output's color
tags differ from the source or the static metadata is gone.

The probe reads the Dolby Vision configuration record and the RPU on the first
decoded frames, so remuxes that dropped the record are still recognized. A
re-encode drops the RPU and keeps the base layer: profiles 7 and 8.1 become
HDR10, 8.4 becomes HLG, and 8.2 becomes SDR. Profiles 5 and 10.0 have no base
layer that shows correct colors without the RPU. For those, Anvil copies the
video stream whatever the flow says, and still applies the audio and subtitle
nodes. FFmpeg cannot stream-copy MP4 video tagged `dvh1` or `dvhe` into
Matroska, so those jobs fail and leave the source alone.

## Audio and subtitles

Without an audio node every audio track is kept and copied. Keep audio tracks
filters by language. Its commentary switch only applies once languages are
listed. With an empty list every track stays, commentary included.

Encode audio re-encodes the kept tracks its source codec list names. A track
that is already in the target codec is copied, and so is every track the list
does not name. Downmix audio re-encodes tracks that have more channels than
its layout and leaves the others alone. When both nodes claim a track, the
downmix uses the Encode audio codec and bitrate.

Bitrates are per channel. A 5.1 track at 96 kb/s per channel is encoded at
576 kb/s. Zero uses the encoder's default, and FLAC takes no bitrate. Anvil
keeps the result inside what each encoder accepts and records a note on the
track when it adjusts something:

| Codec | Encoder | Layouts | Bitrate limits |
| --- | --- | --- | --- |
| `opus` | `libopus` | Mono to 7.1 | At most 256 kb/s per channel. |
| `aac` | `aac` | Mono to 5.1, and 7.1. A 6.1 track becomes 5.1. | None. |
| `ac3` | `ac3` | Mono to 5.1. Wider tracks become 5.1. | At least 32 kb/s per channel, 640 kb/s in total. |
| `eac3` | `eac3` | Mono to 5.1. Wider tracks become 5.1. | At least 32 kb/s per channel, 4096 kb/s in total. |
| `flac` | `flac` | Mono to 7.1 | Lossless. |

A track with more than eight channels, or with an unknown channel count, is
copied. Validation checks every re-encoded track against the codec and channel
count that was planned.

Subtitles are copied, never converted. Keep subtitles filters by language,
and its forced, SDH, and commentary switches also apply only once languages
are listed. Remove image subtitles drops `hdmv_pgs_subtitle`, `dvd_subtitle`,
`dvb_subtitle`, and `xsub` tracks before the language rules run, so no
fallback brings one back. Remove all subtitles wins over a Keep subtitles
node on the same path.

When a flow keeps `orig` and Anvil cannot learn the original language, because
the library has no Arr connection or the lookup failed, the language filters
are turned off for that job and no track is dropped by language. Dropping
tracks on a guess is how the only useful audio track gets lost. Test on a file
never asks an Arr, so it shows this case for every flow that uses `orig`.

## Publishing

Anvil writes each encode on the destination filesystem as
`<name>.mkv.job-<id>.anvil-part`. After the validate step it links the file
into place under its final name. Publishing never copies across filesystems,
and the destination never appears half-written. Media libraries put the part
file next to its destination. Download libraries put it under
`<handoff_path>/.anvil-work` and create the package directory only at publish
time. An importer that sweeps a package directory after taking one episode
can't delete a sibling job's unpublished artifact that way. When a crashed
attempt leaves a part file behind, Anvil removes it once the job retries or is
canceled. Media scanners and Arrs ignore the suffix in the meantime.

Every publish is recorded in a journal before the filesystem changes. The
stages are `prepared`, `published`, `source_cleaned`, and `committed`, or
`conflict` when the destination already exists. After a crash, the job's next
attempt finishes the journaled publish from where it stopped before it looks
at anything else. Replacing a file in a media library keeps the source beside
it as `<file>.anvil.bak` until the publish is committed.

Validation is observational. It records findings such as duration drift or
changed color tags on the job and does not fail it. A missing output file is
the one hard failure. The CRF search is what decides whether an encode is
good enough.

`temp_dir` only holds scratch that never gets published, which means search
samples and process logs.

## Web UI access and security

The web UI can replace and delete media files, so who can reach it matters.

By default the daemon listens on `127.0.0.1:8484` and asks for no sign-in.
Anyone with a shell on the machine can use it. To listen on another address,
set a password:

```toml
[web]
listen = "0.0.0.0:8484"
password_file = "/run/secrets/anvil-web"
```

There is one shared password and no user accounts. `password_file` is
preferred over a literal `password`, and its trailing newline is ignored. The
daemon refuses to start with a password file it cannot read or that is empty.
It never falls back to an open UI. The password is read and the address bound
before the store is opened, so a start that fails on either leaves the
database unmigrated. It also refuses to start on an address
other machines can reach without a password, unless you set
`allow_unauthenticated = true`. With that set, everyone who can reach the port
controls Anvil, and the daemon logs a warning at every start.

A sign-in sets a session cookie that is `HttpOnly` and `SameSite=Strict` and
lasts 30 days. Sessions live in memory, so a restart signs everyone out.
Changing the password does too. From the fifth failed sign-in in a row from
one address, each failure makes that address wait 2 seconds. Attempts made
during the wait are refused without looking at the password and do not
lengthen it.

The API refuses requests a browser marks as coming from another site, and
every request that changes something must send
`Content-Type: application/json`. A daemon without a password also checks the
`Host` header. It only answers when addressed by a loopback name, by its
listen host, or by the IP address the connection arrived on, which stops DNS
rebinding. So `http://nas.local:8484` does not work without a password. Use
the IP address, or set one.

Anvil does not speak TLS. Put a reverse proxy in front for HTTPS:

- Pass the browser's `Host` header through, or set `X-Forwarded-Host` to it.
  Otherwise Anvil sees every change as coming from another origin and refuses
  it.
- Set `X-Forwarded-Proto: https` so the session cookie is marked `Secure`.
- `X-Forwarded-For` is not read. The sign-in throttle counts the proxy as one
  client.
- If the proxy does the authentication and Anvil has no password, the proxy
  must send a loopback `Host` such as `127.0.0.1:8484` together with
  `X-Forwarded-Host`.

Without `daemon.media_roots`, someone who is signed in can point a library at
any directory the service user can write, and Anvil will then replace files
there. The folder picker then lists directories and media file names anywhere
the service user can read. Set `media_roots` to confine both:

```toml
[daemon]
media_roots = ["/srv/media", "/srv/downloads"]
```

Library and handoff folders must then resolve, symlinks included, to a path
inside one of the roots, and the folder picker shows nothing outside them. The
NixOS module fills `media_roots` from `mediaPaths`.

Arr API keys are entered in the UI and stored in the database. The API never
returns a key, and it cannot point a connection at a key file. The daemon
calls whatever base URL an Arr connection names. It follows no redirects and
reads at most 4 MiB of a response. Treat the password like write access to
your media.

The daemon sets the database and its `-wal` and `-shm` files to mode `0600`
on every start, and creates a missing store directory with mode `0700`. A
directory that already exists keeps its mode. `anvilctl backup` writes its
snapshot with mode `0600` too.

The control socket is the other way in. The daemon creates it with mode
`0660`, and whoever may open it can run every `anvilctl` command.

## Configuration

`anvil.toml` holds the process settings and nothing else. `[daemon]` has the
paths, the media roots, worker and thread counts, intervals, shutdown
behavior, and log level.
`[web]` has `enabled`, `listen`, `password_file`, `password`, and
`allow_unauthenticated`. Libraries, flows, and Arr connections live in the
SQLite store and are edited in the web UI.

[`examples/anvil.toml`](examples/anvil.toml) is a minimal starting point.
[`examples/anvil-reference.toml`](examples/anvil-reference.toml) documents
every setting and its default. Without a config file the daemon keeps its
state under the system temporary directory, which does not survive a reboot.
Set `store_path` to persistent storage for anything you mean to keep.

Check a config before you start anything:

```sh
anvild check-config --config /etc/anvil/anvil.toml
anvild check-config --config /etc/anvil/anvil.toml --show
anvild preflight --config /etc/anvil/anvil.toml --library movies --limit 20
```

`--show` prints the effective config with defaults filled in and secrets
redacted. `preflight` lists what a scan of the stored libraries would queue
and where each result would be published. It opens the store read-only, so
the daemon must have started once. Both commands are local and read-only.
Anything that touches live state is in `anvilctl` or the web UI.

Changes made in the web UI apply at once. `SIGHUP` re-reads `anvil.toml` and
applies most daemon settings in place, and it re-reads the password file.
Changing `store_path`, `temp_dir`, `control_socket`, `web.enabled`, or
`web.listen` needs a restart. A reload that changes one of them, or that
names a password file the daemon cannot read, is rejected as a whole and the
old settings stay in force.

## Deployment

### NixOS

The NixOS module is the supported way to deploy. Add Anvil to your flake
inputs:

```nix
inputs.anvil.url = "github:zekurio/anvil";
```

Then import and configure the module:

```nix
{
  imports = [ inputs.anvil.nixosModules.anvil ];

  services.anvil = {
    enable = true;
    package = inputs.anvil.packages.${pkgs.stdenv.hostPlatform.system}.anvild;
    group = "anvil";
    createGroup = true;

    controlClient = {
      install = true;
      package = inputs.anvil.packages.${pkgs.stdenv.hostPlatform.system}.anvilctl;
    };

    web = {
      listenAddress = "0.0.0.0";
      passwordFile = "/run/secrets/anvil-web";
      openFirewall = true;
    };

    mediaPaths = [
      "/srv/media"
      "/srv/downloads"
    ];

    settings.daemon.worker_count = 2;
  };

  users.users.alice.extraGroups = [ "anvil" ];
}
```

The module renders `/etc/anvil/anvil.toml`, keeps the store in
`/var/lib/anvil/anvil.db` and scratch in `/var/lib/anvil/tmp`, puts the
control socket in `/run/anvil`, and adds Jellyfin's ffmpeg to the service
`PATH`. The socket is `0660` inside a `0750` directory owned by
`services.anvil.group`. Membership in that group is what grants `anvilctl`
access. Installing `anvilctl` grants nothing.

`services.anvil.web` has `enable`, `listenAddress`, `port`, `passwordFile`,
`allowUnauthenticated`, and `openFirewall`. The defaults are `127.0.0.1` and
`8484`. The service user must be able to read the password file. Keep it out
of `/tmp`, `/var/tmp`, and the Nix store. A non-loopback address without a
password fails at evaluation time, the same way the daemon would refuse it.
So does a port below 1024, which the unit cannot bind because it runs without
`CAP_NET_BIND_SERVICE`. Use a reverse proxy to serve the UI on port 80 or 443.

`services.anvil.mediaPaths` lists the directories the daemon may write. The
unit runs with `ProtectSystem=strict`, and the module cannot see what you add
in the web UI. List every library folder and every handoff folder, or a
parent of them. The module also renders the list as `daemon.media_roots`, so
the web UI refuses a library or handoff folder outside these paths and the
folder picker shows nothing else. Setting `settings.daemon.media_roots`
yourself replaces that list. With it set to `[]` a library outside
`mediaPaths` can be added and scanned, but its jobs fail with a read-only
file system error as soon as Anvil writes the encode. Each path must exist
when the service starts unless you prefix it with `-`. The service user also
needs ordinary write permission.

`services.anvil.settings` takes the same snake_case keys as the reference
TOML. Keys under `settings.web` override the `web` options one by one.

### Without Nix

Build with `make all`, which needs Go, Node.js, and npm, and install
`bin/anvild` and `bin/anvilctl` together. They share a private, versioned
protocol, and a client that speaks another protocol version is refused with
exit status 3. `make build` alone needs no Node.js. That daemon serves the
API and `anvilctl` but no web UI.

The service needs `ffmpeg` and `ffprobe` on its `PATH`, built with the video
encoders your flows use and with the `libvmaf` or `xpsnr` filter. Opus audio
needs `libopus`. Run it as a user that can write the library folders:

```sh
anvild --config /etc/anvil/anvil.toml
```

## Operating with anvilctl

```
anvilctl status                                  daemon state, workers, queue counts
anvilctl version                                 client, daemon, and protocol versions
anvilctl libraries                               libraries with flow, state, and job counts
anvilctl flows                                   flows and where they are assigned
anvilctl jobs [SELECTORS]                        list jobs
anvilctl show JOB                                show one job
anvilctl cancel [JOB...] [SELECTORS]             cancel jobs
anvilctl retry [JOB...] [--failed [--library N]] requeue failed jobs
anvilctl prune [--library N] [--state S,...] [--apply]
anvilctl recover
anvilctl scan [LIBRARY]
anvilctl stats [LIBRARY]
anvilctl requeue --library NAME PATH
anvilctl staging cleanup [--older-than D] [--dry-run] [--legacy-parts]
anvilctl backup DESTINATION
anvilctl help [COMMAND]
```

`libraries` and `flows` only list. Creating and editing them happens in the
web UI. The Check column of `flows` reports validation issues, and a flow with
errors fails every job that runs it.

Name a job by its numeric id or its slug. `--json`, or `-j`, works globally and
per command. Exit status is `0` on success, `1` when the command failed, `2`
for a usage error, `3` when the daemon is unreachable or speaks a different
protocol version, and `4` when something was not found. `--socket` or
`ANVIL_CONTROL_SOCKET` overrides the default `/run/anvil/anvild.sock`.

Human output is styled with Lip Gloss. You get color on a terminal and plain
text when the output is redirected or `NO_COLOR=1` is set. Job listings group
by library, with a count and one entry per job. Each entry shows the job's
numeric id, state, update time, input filename, and last error if it has one.
Long filenames and errors wrap to the terminal width. `show JOB` prints full
paths and slugs, how the flow resolved, and the attempt history. `--json` is
the format for scripts. JSON fields and exit codes don't change with the human
presentation. Listings print times in the client's local timezone, while JSON
keeps the daemon's timestamps.

```sh
anvilctl jobs --state pending,failed --json
anvilctl jobs --absolute-path '/mnt/media/converted/Release/Episode.mkv'
anvilctl cancel --library usenet-tv --state pending,running
anvilctl retry --failed --library movies
anvilctl prune --library movies --state complete,failed,canceled --apply
anvilctl staging cleanup --older-than 24h --dry-run
anvilctl backup /srv/backups/anvil-$(date +%F).db
```

`cancel` needs a selector that narrows the set. It refuses a job whose publish
is already journaled, because only the daemon can finish that destination
safely. Job pruning and staging cleanup skip anything active or holding an
unresolved publish journal too, and list it under `protected_jobs`.

Anvil only cleans up legacy output artifacts when you ask.
`anvilctl staging cleanup --older-than 24h --legacy-parts --dry-run` lists old
unscoped `.mkv.anvil-part` files in the libraries' output folders. Run it
again without `--dry-run` to delete the eligible ones. It keeps current job
parts and anything a publish journal still holds.

The database is real state now. It holds your libraries, flows, and Arr
connections next to the job history. `anvilctl backup` writes a consistent
snapshot while the daemon runs. Schedule it.

## Upgrading from a TOML-configured Anvil

Older versions read libraries, profiles, and Arr connections from
`anvil.toml`. This version keeps them in the store.

Back up the database with the old binaries first. The new daemon migrates the
store on its first start, and older binaries cannot open it afterwards. A
store from before schema version 5 is refused. Upgrade `anvild` and `anvilctl`
together.

On that first start the daemon imports the `[libraries]`, `[profiles]`, and
`[arrs]` tables once:

- Every profile a library uses becomes a flow that resolves to the same
  settings. The built-in default profile, left unmodified, maps onto
  `Default AV1`. Per-codec `video.overrides` become Check video codec
  branches. Profiles no library uses are not imported.
- `ignore_regex` and `exclude` become deny rules at the top of the library's
  rule list. `include` becomes allow rules below them with a deny fallback.
- Libraries keep their names, so their job history stays attached. They are
  not paused, because they were already running. There are two exceptions. A
  library whose imported flow does not validate is imported paused, and so is
  one whose folders overlap another library's or `temp_dir`. Fix the flow or
  the folders, then resume it. Relative paths are resolved to absolute ones.
- Arr connections keep their `api_key` or `api_key_file`. A connection that
  reads its key from a file keeps doing so until you enter a key in the UI.
- `validation.duration_tolerance_seconds` has no node, so flows use the
  default. Crop settings are dropped on paths that copy the video.

The import runs once, also when there was nothing to import. After it the
daemon ignores those tables and says so in its log at every start and reload.
`check-config` still validates them, so a table that used `ab_av1_args` or
`target_vmaf` has to be fixed or removed before the daemon starts.

Afterwards:

1. Run `anvilctl libraries` and `anvilctl flows`. A flow whose Check column is
   not `ok` fails its jobs until you fix it in the editor. The daemon log names
   the problem.
2. Open each library's Rules tab and each imported flow, and run Test on a
   file against a file you know.
3. Remove the three tables from `anvil.toml`, or from
   `services.anvil.settings`.
4. On NixOS, list your library and handoff folders in
   `services.anvil.mediaPaths`. The module still grants write access to the
   paths of legacy `settings.libraries` entries, but not to anything you add
   in the web UI. Those legacy paths are not part of `media_roots`, so an
   imported library outside `mediaPaths` cannot be saved again in the UI
   until its folder is listed.

## Development

Deployment needs Linux because the scanner depends on inotify. Everyday work
runs on macOS as well: building, linting, `gopls`, the web UI, and running
`anvilctl`. With [Nix](https://nixos.org/) and [direnv](https://direnv.net/),
the dev shell brings Go, `golangci-lint`, `gopls`, Node.js, ffmpeg, and
SQLite:

```sh
direnv allow
make all
```

Without direnv, `nix develop --no-pure-eval` enters the same shell. Without
Nix, install Go 1.26.4 or newer, Node.js 22.12 or newer, and the media tools
above, and use the same `make` targets. `make lint` runs `golangci-lint`
inside `nix develop`.

```sh
make all        # web UI, then bin/anvild and bin/anvilctl
make build      # the two binaries, without Node.js and without the UI
make web        # the web UI into web/dist
make web-check  # typecheck and build the web UI
make fmt        # go fmt ./...
make lint       # golangci-lint run ./...
make clean      # remove bin and the built UI
go test ./...
```

`ANVIL_MEDIA_TEST=1 go test ./pkg/ffmpeg ./pkg/search` also runs the tests
that encode synthetic media with real FFmpeg.

Two targets serve the UI with hot reload on <http://localhost:5173>:

```sh
make web-mock   # against an in-memory fake API, no daemon needed
make web-dev    # against a running daemon
```

`web-dev` proxies `/api` to `http://127.0.0.1:8484`. Set `ANVIL_API` to point
at another daemon. The proxy sends the browser's host on as
`X-Forwarded-Host`, so sign-in and changes work through it. Use `web-mock` to
work on the UI without a daemon.
[`web/README.md`](web/README.md) covers the frontend layout and conventions.

The flake exposes `packages.default` with both binaries, `packages.anvild`
wrapped with its media tools, `packages.anvilctl` standalone and deliberately
unwrapped, `packages.web` with the built UI, `apps.*`, and
`nixosModules.anvil`. Bump `vendorHash` in `flake.nix` when Go dependencies
change. JavaScript dependencies need no hash, because the build reads
`web/package-lock.json`.

Before opening a pull request, run `make fmt`, `make lint`, and
`go test ./...`, plus `make web-check` when you touched `web/`.
[`AGENTS.md`](AGENTS.md) covers branch, commit, and code conventions.

## Contributing

Found a bug or have an idea?
[Open an issue](https://github.com/zekurio/anvil/issues/new).

## License

[MIT](LICENSE)
