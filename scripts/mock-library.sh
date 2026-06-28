#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DEFAULT_ROOT="$REPO_ROOT/tmp/mock-library"
DEFAULT_ADDR="127.0.0.1:18080"
DEFAULT_KEY="mock-secret"
EXPECTED_JOBS=3

usage() {
  cat <<'EOF'
Usage:
  scripts/mock-library.sh setup [root]
  scripts/mock-library.sh run [root]
  scripts/mock-library.sh serve-arrs [root]
  scripts/mock-library.sh reset [root]
  scripts/mock-library.sh paths [root]

Environment:
  ANVIL_MOCK_ROOT       Override default root, default: tmp/mock-library
  ANVIL_MOCK_ARR_ADDR   Mock Arr listen address, default: 127.0.0.1:18080
  ANVIL_MOCK_TIMEOUT    Smoke run timeout in seconds, default: 90
EOF
}

main() {
  local command="${1:-setup}"
  local root="${2:-${ANVIL_MOCK_ROOT:-$DEFAULT_ROOT}}"
  root="$(absolute_path "$root")"

  case "$command" in
    setup)
      setup_library "$root"
      print_paths "$root"
      ;;
    run)
      run_smoke "$root"
      ;;
    serve-arrs)
      setup_library "$root"
      serve_arrs "$root"
      ;;
    reset)
      rm -rf "$root"
      echo "Removed $root"
      ;;
    paths)
      print_paths "$root"
      ;;
    help|-h|--help)
      usage
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
}

absolute_path() {
  local path="$1"
  mkdir -p "$path"
  (cd "$path" && pwd -P)
}

setup_library() {
  local root="$1"
  require_tool ffmpeg

  mkdir -p \
    "$root/media/movies/Mock Movie (2026)" \
    "$root/media/tv/Mock Anime/Season 01" \
    "$root/downloads/complete/tv/Mock.Download.S01/Mock.Download.S01E01" \
    "$root/imports/tv" \
    "$root/logs" \
    "$root/secrets" \
    "$root/state" \
    "$root/tmp"

  printf '%s\n' "$DEFAULT_KEY" > "$root/secrets/radarr-api-key"
  printf '%s\n' "$DEFAULT_KEY" > "$root/secrets/sonarr-api-key"
  chmod 600 "$root/secrets/radarr-api-key" "$root/secrets/sonarr-api-key"

  create_sample \
    "$root/media/movies/Mock Movie (2026)/Mock Movie (2026).mkv" \
    "440" "eng" "English Main" \
    "660" "jpn" "Japanese Dub"

  create_sample \
    "$root/media/tv/Mock Anime/Season 01/Mock Anime S01E01.mkv" \
    "660" "jpn" "Japanese Main" \
    "440" "eng" "English Commentary"

  create_sample \
    "$root/downloads/complete/tv/Mock.Download.S01/Mock.Download.S01E01/Mock Download S01E01.mkv" \
    "660" "jpn" "Japanese Main" \
    "440" "eng" "English Commentary"

  printf 'mock nzb sidecar\n' > "$root/downloads/complete/tv/Mock.Download.S01/release.nzb"
  write_config "$root"
}

create_sample() {
  local output="$1"
  local freq_one="$2"
  local lang_one="$3"
  local title_one="$4"
  local freq_two="$5"
  local lang_two="$6"
  local title_two="$7"

  if [ -s "$output" ]; then
    return
  fi

  mkdir -p "$(dirname "$output")"
  local tmp_base tmp
  tmp_base="$(mktemp "${output}.tmp.XXXXXX")"
  rm -f "$tmp_base"
  tmp="${tmp_base}.mkv"

  ffmpeg -hide_banner -loglevel error -y \
    -f lavfi -i "testsrc2=size=320x180:rate=24" \
    -f lavfi -i "sine=frequency=${freq_one}:sample_rate=48000" \
    -f lavfi -i "sine=frequency=${freq_two}:sample_rate=48000" \
    -t 3 \
    -map 0:v:0 \
    -map 1:a:0 \
    -map 2:a:0 \
    -c:v libx264 \
    -preset ultrafast \
    -crf 35 \
    -pix_fmt yuv420p \
    -c:a aac \
    -shortest \
    -metadata:s:a:0 "language=${lang_one}" \
    -metadata:s:a:0 "title=${title_one}" \
    -metadata:s:a:1 "language=${lang_two}" \
    -metadata:s:a:1 "title=${title_two}" \
    "$tmp"

  mv "$tmp" "$output"
}

write_config() {
  local root="$1"
  local addr="${ANVIL_MOCK_ARR_ADDR:-$DEFAULT_ADDR}"

  cat > "$root/config.toml" <<EOF
[daemon]
temp_dir = "$root/tmp"
store_path = "$root/state/anvil.db"
worker_count = 1
total_threads = 2
max_attempts = 1
scan_interval = "1h"
scheduler_interval = "1s"
lease_duration = "5m"
shutdown_policy = "drain"
shutdown_timeout = "0s"
staging_cleanup_age = "0s"
log_level = "debug"

[flows.mock-sidecar]
steps = ["probe", "crop-detect", "audio-cleanup", "stage", "encode", "validate", "replace", "cleanup"]

[flows.mock-handoff]
steps = ["probe", "crop-detect", "audio-cleanup", "stage", "encode", "validate", "handoff", "cleanup"]

[profiles.mock-av1]
container = "mkv"

[profiles.mock-av1.video]
codec = "libsvtav1"
preset = "13"
pixel_format = "yuv420p10le"
crf_min = 45
crf_max = 45
target_vmaf = 0

[profiles.mock-av1.audio]
languages_to_keep = ["orig"]
fallback = "keep_all"
keep_commentary = false
unknown_as_original = true

[profiles.mock-av1.subtitles]
mode = "preserve"
fallback = "keep_all"
keep_forced = true
keep_external = true

[profiles.mock-av1.metadata]
mode = "preserve"

[profiles.mock-av1.attachments]
mode = "preserve"

[profiles.mock-av1.chapters]
mode = "preserve"

[arrs.mock-radarr]
type = "radarr"
base_url = "http://$addr/radarr"
api_key_file = "$root/secrets/radarr-api-key"

[arrs.mock-sonarr]
type = "sonarr"
base_url = "http://$addr/sonarr"
api_key_file = "$root/secrets/sonarr-api-key"

[libraries.mock-movies]
kind = "media"
path = "$root/media/movies"
arr = "mock-radarr"
flow = "mock-sidecar"
profile = "mock-av1"
priority = 10
include = ["*.mkv"]
exclude = ["**/*.anvil.*", "**/.staging/**"]

[libraries.mock-movies.media]
replacement_mode = "sidecar"

[libraries.mock-tv]
kind = "media"
path = "$root/media/tv"
arr = "mock-sonarr"
flow = "mock-sidecar"
profile = "mock-av1"
priority = 5
include = ["*.mkv"]
exclude = ["**/*.anvil.*", "**/.staging/**"]

[libraries.mock-tv.media]
replacement_mode = "sidecar"

[libraries.mock-download-tv]
kind = "download"
path = "$root/downloads/complete/tv"
arr = "mock-sonarr"
flow = "mock-handoff"
profile = "mock-av1"
priority = 20
include = ["*.mkv"]
exclude = ["**/sample*/**", "**/*sample*"]

[libraries.mock-download-tv.download]
handoff_path = "$root/imports/tv"
stable_for = "0s"
package_mode = "auto"
handoff_mode = "copy"
preserve_relative_path = true
cleanup_source_media = false
prune_empty_dirs = false
ignorable_globs = ["**/*.nzb", "**/*.nfo", "**/.DS_Store"]
EOF
}

serve_arrs() {
  local root="$1"
  local addr="${ANVIL_MOCK_ARR_ADDR:-$DEFAULT_ADDR}"
  cd "$REPO_ROOT"
  exec go run ./cmd/anvil-mockarr --root "$root" --addr "$addr" --key "$DEFAULT_KEY"
}

run_smoke() {
  local root="$1"
  local addr="${ANVIL_MOCK_ARR_ADDR:-$DEFAULT_ADDR}"
  local timeout_seconds="${ANVIL_MOCK_TIMEOUT:-90}"
  require_tool curl
  require_tool sqlite3

  setup_library "$root"
  clean_run_outputs "$root"

  cd "$REPO_ROOT"

  go run ./cmd/anvil-mockarr --root "$root" --addr "$addr" --key "$DEFAULT_KEY" \
    > "$root/logs/mockarr.log" 2>&1 &
  local arr_pid="$!"

  local daemon_pid=""

  cleanup_processes() {
    local daemon="${daemon_pid:-}"
    local arr="${arr_pid:-}"
    if [ -n "$daemon" ]; then
      kill "$daemon" 2>/dev/null || true
      wait "$daemon" 2>/dev/null || true
    fi
    if [ -n "$arr" ]; then
      kill "$arr" 2>/dev/null || true
      wait "$arr" 2>/dev/null || true
    fi
  }
  trap cleanup_processes EXIT INT TERM

  wait_for_arr "$addr"

  go run ./cmd/anvild --config "$root/config.toml" \
    > "$root/logs/anvild.log" 2>&1 &
  daemon_pid="$!"

  wait_for_jobs "$root" "$timeout_seconds"

  cleanup_processes
  trap - EXIT INT TERM

  echo "Mock smoke completed."
  print_paths "$root"
  sqlite3 "$root/state/anvil.db" "select id, library_name, state, last_error from jobs order by id;"
}

clean_run_outputs() {
  local root="$1"
  rm -f \
    "$root/state/anvil.db" \
    "$root/state/anvil.db-shm" \
    "$root/state/anvil.db-wal" \
    "$root/media/movies/Mock Movie (2026)/Mock Movie (2026).anvil.mkv" \
    "$root/media/tv/Mock Anime/Season 01/Mock Anime S01E01.anvil.mkv"
  rm -rf \
    "$root/imports/tv/Mock.Download.S01" \
    "$root/tmp/staging"
  mkdir -p "$root/logs"
  rm -f "$root/logs/mockarr.log" "$root/logs/anvild.log"
}

wait_for_arr() {
  local addr="$1"
  local deadline=$((SECONDS + 15))
  until curl -fsS "http://$addr/healthz" >/dev/null 2>&1; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "Mock Arr server did not become ready." >&2
      exit 1
    fi
    sleep 0.25
  done
}

wait_for_jobs() {
  local root="$1"
  local timeout_seconds="$2"
  local db="$root/state/anvil.db"
  local deadline=$((SECONDS + timeout_seconds))

  while true; do
    if [ -f "$db" ]; then
      local failed
      failed="$(sqlite3 "$db" "select count(*) from jobs where state = 'failed';")"
      if [ "$failed" != "0" ]; then
        echo "A mock-library job failed. Logs:" >&2
        echo "  $root/logs/anvild.log" >&2
        sqlite3 "$db" "select id, library_name, state, last_error from jobs order by id;" >&2
        exit 1
      fi

      local total incomplete
      total="$(sqlite3 "$db" "select count(*) from jobs;")"
      incomplete="$(sqlite3 "$db" "select count(*) from jobs where state != 'complete';")"
      if [ "$total" -ge "$EXPECTED_JOBS" ] && [ "$incomplete" = "0" ]; then
        return
      fi
    fi

    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "Timed out waiting for mock-library jobs. Logs:" >&2
      echo "  $root/logs/anvild.log" >&2
      if [ -f "$db" ]; then
        sqlite3 "$db" "select id, library_name, state, last_error from jobs order by id;" >&2
      fi
      exit 1
    fi

    sleep 1
  done
}

print_paths() {
  local root="$1"
  cat <<EOF
Mock library root: $root
Config:            $root/config.toml
Mock Arr URL:      http://${ANVIL_MOCK_ARR_ADDR:-$DEFAULT_ADDR}
Radarr movie:      $root/media/movies/Mock Movie (2026)/Mock Movie (2026).mkv
Sonarr episode:    $root/media/tv/Mock Anime/Season 01/Mock Anime S01E01.mkv
Download package:  $root/downloads/complete/tv/Mock.Download.S01
TV handoff path:   $root/imports/tv
Logs:              $root/logs
EOF
}

require_tool() {
  local tool="$1"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "Required tool not found on PATH: $tool" >&2
    echo "Enter the devenv shell first: devenv shell" >&2
    exit 1
  fi
}

main "$@"
