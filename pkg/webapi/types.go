package webapi

import (
	"time"

	"github.com/zekurio/anvil/pkg/control"
	"github.com/zekurio/anvil/pkg/flow"
)

// The types in this file are the JSON contract between the daemon and the web
// UI, served under /api/v1. Field names are snake_case. Times are RFC 3339
// UTC. Lists are never null: an empty list encodes as []. Durations travel as
// Go duration strings ("30m", "20s"); an empty string means "use the default".
//
// Every non-2xx response carries ErrorResponse. Routes:
//
//	GET    /api/v1/session                    Session
//	POST   /api/v1/session                    LoginRequest -> Session
//	DELETE /api/v1/session                    -> 204
//
//	GET    /api/v1/status                     Status
//	GET    /api/v1/settings                   Settings
//	GET    /api/v1/fs?path=&files=            Directory
//
//	GET    /api/v1/node-types                 NodeTypes
//	GET    /api/v1/flows                      FlowList
//	POST   /api/v1/flows                      FlowWrite -> Flow (201)
//	POST   /api/v1/flows/check                FlowCheckRequest -> FlowCheck
//	POST   /api/v1/flows/test                 FlowTestRequest -> FlowTest
//	GET    /api/v1/flows/{id}                 Flow
//	PUT    /api/v1/flows/{id}                 FlowWrite -> Flow
//	DELETE /api/v1/flows/{id}                 -> 204
//	POST   /api/v1/flows/{id}/duplicate       -> Flow (201)
//
//	GET    /api/v1/libraries                  LibraryList
//	POST   /api/v1/libraries                  LibraryWrite -> Library (201)
//	GET    /api/v1/libraries/{name}           Library
//	PUT    /api/v1/libraries/{name}           LibraryWrite -> Library
//	DELETE /api/v1/libraries/{name}           -> 204
//	POST   /api/v1/libraries/{name}/scan      -> ScanResult
//	GET    /api/v1/libraries/{name}/files     ?status=&q=&sort=&dir=&limit=&offset= -> FileList (see FileList)
//	POST   /api/v1/libraries/{name}/requeue   RequeueRequest -> JobActionResult
//
//	GET    /api/v1/jobs                       ?library=&status=&state=&q=&limit=&offset= -> JobList (see JobList)
//	GET    /api/v1/jobs/{id}                  JobDetail
//	POST   /api/v1/jobs/cancel                JobActionRequest -> JobActionResult
//	POST   /api/v1/jobs/retry                 JobActionRequest -> JobActionResult
//
//	GET    /api/v1/arrs                       ArrList
//	POST   /api/v1/arrs                       ArrWrite -> Arr (201)
//	PUT    /api/v1/arrs/{name}                ArrWrite -> Arr
//	DELETE /api/v1/arrs/{name}                -> 204

// Error codes carried by ErrorResponse.
const (
	CodeInvalid         = "invalid"         // 400: malformed or failing validation
	CodeUnauthenticated = "unauthenticated" // 401: sign in first
	CodeNotFound        = "not_found"       // 404
	CodeConflict        = "conflict"        // 409: stale revision, name taken (Field is "name"), or still in use
	CodeInvalidFlow     = "invalid_flow"    // 422: the graph has error issues; see Issues
	CodeInternal        = "internal"        // 500
	CodeUnavailable     = "unavailable"     // 503: daemon is shutting down
)

// ErrorResponse is the body of every failed request.
type ErrorResponse struct {
	Error Error `json:"error"`
}

// Error says what went wrong in words fit to show the operator.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Field names the request field at fault, when there is exactly one. It
	// is the path of the field in the request body: object keys and 0-based
	// list indexes joined by dots, as in "name", "flow_id",
	// "media.replacement_mode", "download.handoff_path", "rules.2.value", or
	// "download.ignorable_globs.0". For a query parameter it is the
	// parameter's name. A name that is already taken answers CodeConflict
	// with Field "name"; every other conflict carries no field.
	Field string `json:"field,omitempty"`
	// Issues lists graph problems when Code is CodeInvalidFlow.
	Issues []flow.Issue `json:"issues,omitempty"`
}

// Session reports whether the browser may use the API.
type Session struct {
	// AuthRequired is false when the daemon runs without a password; the UI
	// then never shows a sign-in screen.
	AuthRequired  bool `json:"auth_required"`
	Authenticated bool `json:"authenticated"`
}

// LoginRequest signs in with the daemon's single shared password.
type LoginRequest struct {
	Password string `json:"password"`
}

// Status is the dashboard poll: daemon state, queue counts, and the live
// progress of every running job.
type Status struct {
	Version    string    `json:"version"`
	ServerTime time.Time `json:"server_time"`
	StartedAt  time.Time `json:"started_at"`
	Workers    Workers   `json:"workers"`
	// Queue counts jobs by state, keyed by domain.JobState. Absent states
	// are zero.
	Queue     map[string]int64 `json:"queue"`
	Running   []Progress       `json:"running"`
	Libraries int              `json:"libraries"`
	Flows     int              `json:"flows"`
}

// Workers reports worker slots.
type Workers struct {
	Configured int `json:"configured"`
	Active     int `json:"active"`
}

// Progress is the live state of one running job.
type Progress struct {
	JobID int64 `json:"job_id"`
	// Step is the pipeline step now running, e.g. "crf-search" or "encode".
	Step string `json:"step"`
	// Percent is 0-100 of the current step, or -1 when unknown.
	Percent float64 `json:"percent"`
	FPS     float64 `json:"fps"`
	// Speed is relative to realtime (1.9 means 1.9x); 0 when unknown.
	Speed float64 `json:"speed"`
	// ETASeconds is the estimated time left in the current step, or -1.
	ETASeconds float64   `json:"eta_seconds"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Settings is the read-only view of what the daemon was started with. The
// file on disk stays the place to change these.
type Settings struct {
	Version    string         `json:"version"`
	ConfigPath string         `json:"config_path"`
	Daemon     DaemonSettings `json:"daemon"`
	Web        WebSettings    `json:"web"`
}

// DaemonSettings mirrors the [daemon] table with defaults filled in.
type DaemonSettings struct {
	TempDir           string `json:"temp_dir"`
	StorePath         string `json:"store_path"`
	ControlSocket     string `json:"control_socket"`
	WorkerCount       int    `json:"worker_count"`
	TotalThreads      int    `json:"total_threads"`
	MaxThreadsPerJob  int    `json:"max_threads_per_job"`
	MaxAttempts       int    `json:"max_attempts"`
	ScanInterval      string `json:"scan_interval"`
	LeaseDuration     string `json:"lease_duration"`
	ShutdownPolicy    string `json:"shutdown_policy"`
	StagingCleanupAge string `json:"staging_cleanup_age"`
	LogLevel          string `json:"log_level"`
}

// WebSettings mirrors the [web] table.
type WebSettings struct {
	Listen       string `json:"listen"`
	AuthRequired bool   `json:"auth_required"`
}

// Directory lists one directory for the folder picker. Entries are sorted by
// name and hold directories only, plus media files when files=1 was asked.
type Directory struct {
	Path string `json:"path"`
	// Parent is empty at the filesystem root.
	Parent  string     `json:"parent"`
	Entries []DirEntry `json:"entries"`
}

// DirEntry is one child of a listed directory.
type DirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	// SizeBytes is set for files only.
	SizeBytes int64 `json:"size_bytes,omitempty"`
}

// NodeTypes is the node catalog, in palette order.
type NodeTypes struct {
	NodeTypes []flow.NodeType `json:"node_types"`
}

// FlowList lists every flow without its graph.
type FlowList struct {
	Flows []FlowSummary `json:"flows"`
}

// FlowSummary is one row of the flows page.
type FlowSummary struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Revision    int    `json:"revision"`
	NodeCount   int    `json:"node_count"`
	// Libraries names the libraries this flow is assigned to.
	Libraries []string  `json:"libraries"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Flow is a stored flow with its graph and everything derived from it.
type Flow struct {
	FlowSummary
	Graph flow.Graph `json:"graph"`
	// Check is the validation of the stored graph: a saved flow has no
	// error issues, but it may carry warnings.
	Check FlowCheck `json:"check"`
}

// FlowWrite creates or replaces a flow. Names are unique.
type FlowWrite struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Graph       flow.Graph `json:"graph"`
	// Revision must equal the stored revision on PUT, or the write is
	// rejected with CodeConflict. Ignored on POST.
	Revision int `json:"revision"`
}

// FlowCheckRequest validates a draft without saving it.
type FlowCheckRequest struct {
	Graph flow.Graph `json:"graph"`
}

// FlowCheck is everything the editor derives from a graph: whether it can be
// saved, what is wrong, and the one-line summary each node shows.
type FlowCheck struct {
	// Valid is true when no issue has error severity.
	Valid  bool         `json:"valid"`
	Issues []flow.Issue `json:"issues"`
	// Nodes is keyed by node ID and has an entry for every node in the graph.
	Nodes map[string]NodeCheck `json:"nodes"`
}

// NodeCheck is the derived state of one node.
type NodeCheck struct {
	// Summary is generated from the node's options, e.g.
	// "libsvtav1 · vmaf 95 · preset 6".
	Summary string `json:"summary"`
	// Invalid is true when the node has at least one error issue.
	Invalid bool `json:"invalid"`
}

// FlowTestRequest dry-runs a draft against one real file. Nothing is encoded
// or written; the file is only probed.
type FlowTestRequest struct {
	Graph flow.Graph `json:"graph"`
	// Path is the absolute path of a media file inside a configured library.
	Path string `json:"path"`
}

// FlowTest is the result of a dry run.
type FlowTest struct {
	Check   FlowCheck    `json:"check"`
	Outcome flow.Outcome `json:"outcome"`
	// Path lists the IDs of the nodes the file passed through, in order.
	Path []string `json:"path"`
	// Decisions explains each condition on the path in one line, e.g.
	// "Video is hevc? -> true (video codec is hevc)".
	Decisions []string `json:"decisions"`
	// File summarizes what the probe found.
	File FileFacts `json:"file"`
	// Plan states what would happen to the file, one line per aspect, e.g.
	// "Audio: keep eng, jpn; encode truehd to eac3 at 96 kb/s per channel".
	Plan []string `json:"plan"`
	// Command is the ffmpeg argument list the encode step would run, with
	// "{crf}" in place of a CRF the search has yet to pick and "{output}"
	// for the artifact path. Empty when Outcome is skip.
	Command []string `json:"command"`
}

// FileFacts is what a probe says about one file, in the words the UI shows.
type FileFacts struct {
	Path            string  `json:"path"`
	SizeBytes       int64   `json:"size_bytes"`
	DurationSeconds float64 `json:"duration_seconds"`
	Container       string  `json:"container"`
	// VideoCodec is the ffprobe codec name, e.g. "hevc".
	VideoCodec string `json:"video_codec"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	// HDR is "", "hdr10", "hlg", or "dolby_vision".
	HDR string `json:"hdr"`
	// BitrateKbps is the overall bitrate; 0 when unknown.
	BitrateKbps int64         `json:"bitrate_kbps"`
	Audio       []StreamFacts `json:"audio"`
	Subtitles   []StreamFacts `json:"subtitles"`
}

// StreamFacts describes one audio or subtitle track.
type StreamFacts struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Title    string `json:"title"`
	// Channels is set for audio tracks only.
	Channels int  `json:"channels,omitempty"`
	Default  bool `json:"default"`
	Forced   bool `json:"forced"`
}

// LibraryList lists every library with its stats, sorted by name.
type LibraryList struct {
	Libraries []Library `json:"libraries"`
}

// Library is a configured library plus what the store knows about it.
type Library struct {
	LibraryWrite
	// FlowName is the name of the assigned flow.
	FlowName   string       `json:"flow_name"`
	Stats      LibraryStats `json:"stats"`
	LastScanAt *time.Time   `json:"last_scan_at,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

// LibraryWrite creates or replaces a library. Name is the identity: it is
// set on POST, must match the URL on PUT, and never changes afterwards.
type LibraryWrite struct {
	// Name is 1-64 characters of a-z, 0-9, "-", and "_".
	Name string `json:"name"`
	// Kind is "media" (encode in place) or "download" (hand off to an
	// importer). It cannot change after creation.
	Kind string `json:"kind"`
	// Path is the absolute root that is scanned.
	Path string `json:"path"`
	// FlowID is the flow every file of this library runs through.
	FlowID int64 `json:"flow_id"`
	// Paused keeps scanning and queueing but starts no new job.
	Paused bool `json:"paused"`
	// Priority orders libraries when work competes; higher runs first.
	Priority int `json:"priority"`
	// ConcurrencyLimit caps simultaneously running jobs; 0 means no cap.
	ConcurrencyLimit int `json:"concurrency_limit"`
	// ScanInterval overrides the daemon's scan cadence; "" uses it.
	ScanInterval string `json:"scan_interval"`
	// Arr names the Arr connection used for original-language lookup; ""
	// disables it.
	Arr string `json:"arr"`
	// Rules run top to bottom and the first match decides whether a file is
	// queued; RuleFallback ("allow" or "deny") decides when none match.
	Rules        []Rule          `json:"rules"`
	RuleFallback string          `json:"rule_fallback"`
	Media        MediaOptions    `json:"media"`
	Download     DownloadOptions `json:"download"`
}

// Rule allows or denies files at scan time. Fields and operators:
//
//	path       matches (doublestar glob on the library-relative path), regex
//	extension  is, is_not (comma-separated list without dots: "mkv, mp4")
//	size       gt, lt (a size with unit: "200 MB", "1.5 GB")
type Rule struct {
	// Action is "allow" or "deny".
	Action string `json:"action"`
	Field  string `json:"field"`
	Op     string `json:"op"`
	Value  string `json:"value"`
}

// MediaOptions applies to libraries of kind "media".
type MediaOptions struct {
	// ReplacementMode is "replace" (swap the source for the encode) or
	// "copy" (publish the encode beside the source).
	ReplacementMode string `json:"replacement_mode"`
}

// DownloadOptions applies to libraries of kind "download".
type DownloadOptions struct {
	// HandoffPath is the absolute directory the importer watches.
	HandoffPath string `json:"handoff_path"`
	// StableFor is the quiet period before a download counts as complete.
	StableFor string `json:"stable_for"`
	// PackageMode is "auto", "directory", or "file".
	PackageMode string `json:"package_mode"`
	// HandoffMode is "copy" or "move".
	HandoffMode          string   `json:"handoff_mode"`
	PreserveRelativePath bool     `json:"preserve_relative_path"`
	CleanupSourceMedia   bool     `json:"cleanup_source_media"`
	PruneEmptyDirs       bool     `json:"prune_empty_dirs"`
	IgnorableGlobs       []string `json:"ignorable_globs"`
}

// LibraryStats summarizes a library for its stat bar.
type LibraryStats struct {
	Files      int64 `json:"files"`
	TotalBytes int64 `json:"total_bytes"`
	Pending    int64 `json:"pending"`
	Running    int64 `json:"running"`
	Done       int64 `json:"done"`
	Failed     int64 `json:"failed"`
	Skipped    int64 `json:"skipped"`
	// SavedBytes is input minus output size summed over completed jobs.
	SavedBytes int64 `json:"saved_bytes"`
}

// ScanResult reports one manual scan.
type ScanResult struct {
	Sources         int `json:"sources"`
	Assets          int `json:"assets"`
	EnqueuedJobs    int `json:"enqueued_jobs"`
	ExistingJobs    int `json:"existing_jobs"`
	SkippedIgnored  int `json:"skipped_ignored"`
	SkippedUnstable int `json:"skipped_unstable"`
}

// File statuses, as the UI groups job states.
const (
	FileStatusNone    = "none"    // seen by a scan, no job
	FileStatusQueued  = "queued"  // pending, leased, retrying
	FileStatusRunning = "running" // running, validating, replacing
	FileStatusDone    = "done"    // complete
	FileStatusFailed  = "failed"
	FileStatusSkipped = "skipped" // skipped or canceled
)

// FileList is one page of a library's files. The query parameters:
//
//	status  one FileStatus; empty lists every file
//	q       keeps files whose library-relative path contains it, ignoring case
//	sort    "path" (the default), "size", "status" (by how much attention the
//	        status needs: running, queued, failed, done, skipped, none), or
//	        "updated_at". Nothing else sorts: the probe columns (container,
//	        video codec, resolution, audio) are read from each job's
//	        checkpoint and have no order in the store.
//	dir     "asc" (the default) or "desc"
//	limit   page size, 100 by default and 500 at most
//	offset  rows to skip. An offset at or past Total answers an empty page
//	        with the real Total, so a client can step back to the last page.
type FileList struct {
	// Total counts every file matching the filter, across all pages.
	Total int64  `json:"total"`
	Files []File `json:"files"`
}

// File is one media file of a library and the state of its job, if any.
type File struct {
	SourceID int64 `json:"source_id"`
	AssetID  int64 `json:"asset_id,omitempty"`
	// Path is relative to the library root.
	Path         string `json:"path"`
	AbsolutePath string `json:"absolute_path"`
	SizeBytes    int64  `json:"size_bytes"`
	// Status is one of the FileStatus constants.
	Status string `json:"status"`
	JobID  int64  `json:"job_id,omitempty"`
	// JobState is the exact domain.JobState behind Status.
	JobState  string `json:"job_state,omitempty"`
	LastError string `json:"last_error,omitempty"`
	// Container, VideoCodec, Resolution, and Audio come from the job's last
	// probe and are empty until one has run. Resolution is a label such as
	// "1080p"; Audio describes the first track, e.g. "eac3 5.1".
	Container  string `json:"container"`
	VideoCodec string `json:"video_codec"`
	Resolution string `json:"resolution"`
	Audio      string `json:"audio"`
	// OutputSizeBytes is set once the job completed.
	OutputSizeBytes int64     `json:"output_size_bytes,omitempty"`
	Progress        *Progress `json:"progress,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// RequeueRequest forces fresh jobs for files of one library, including files
// that were already processed.
type RequeueRequest struct {
	// Paths are relative to the library root.
	Paths []string `json:"paths"`
}

// JobList is one page of the queue, newest activity first. The query
// parameters:
//
//	library  one library name; an unknown one answers CodeNotFound
//	status   FileStatus groups other than "none": queued, running, done,
//	         failed, skipped
//	state    exact domain.JobState values
//	q        keeps jobs whose path contains it, ignoring case
//	limit    page size, 100 by default and 500 at most
//	offset   rows to skip. An offset at or past Total answers an empty page
//	         with the real Total, so a client can step back to the last page.
//
// status and state may each be repeated or hold a comma-separated list. A job
// is listed when it matches any status or state named; with neither, every
// job is.
type JobList struct {
	Total int64 `json:"total"`
	Jobs  []Job `json:"jobs"`
}

// Job is one queue row.
type Job struct {
	control.JobResponse
	// Status is one of the FileStatus constants.
	Status          string    `json:"status"`
	InputSizeBytes  int64     `json:"input_size_bytes"`
	OutputSizeBytes int64     `json:"output_size_bytes"`
	Progress        *Progress `json:"progress,omitempty"`
}

// JobDetail is everything known about one job.
type JobDetail struct {
	control.JobShowResponse
	Status   string    `json:"status"`
	Progress *Progress `json:"progress,omitempty"`
	// Flow is how the flow resolved on the latest attempt that got far
	// enough to evaluate it.
	Flow *JobFlow `json:"flow,omitempty"`
	// File is the latest probe of the input, when one has run.
	File *FileFacts `json:"file,omitempty"`
}

// JobFlow records the flow a job ran and the path its file took.
type JobFlow struct {
	FlowID   int64        `json:"flow_id"`
	FlowName string       `json:"flow_name"`
	Revision int          `json:"revision"`
	Graph    flow.Graph   `json:"graph"`
	Outcome  flow.Outcome `json:"outcome"`
	// Path lists the IDs of the nodes the file passed through, in order.
	Path      []string `json:"path"`
	Decisions []string `json:"decisions"`
	Plan      []string `json:"plan"`
}

// JobActionRequest selects jobs to cancel or retry. IDs wins; otherwise
// Library and States select, and at least one of the three must be set.
type JobActionRequest struct {
	IDs     []int64  `json:"ids,omitempty"`
	Library string   `json:"library,omitempty"`
	States  []string `json:"states,omitempty"`
}

// JobActionResult reports a bulk action.
type JobActionResult struct {
	Matched int `json:"matched"`
	// Changed counts jobs the action applied to.
	Changed int `json:"changed"`
	// Skipped explains, per job, why the action did not apply.
	Skipped []JobActionSkip `json:"skipped"`
}

// JobActionSkip is one job an action left alone.
type JobActionSkip struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

// ArrList lists the configured Arr connections, sorted by name.
type ArrList struct {
	Arrs []Arr `json:"arrs"`
}

// Arr is one Radarr or Sonarr connection. The key itself is never returned.
type Arr struct {
	Name string `json:"name"`
	// Type is "radarr" or "sonarr".
	Type       string `json:"type"`
	BaseURL    string `json:"base_url"`
	APIKeyFile string `json:"api_key_file"`
	// APIKeySet reports whether a literal key is stored.
	APIKeySet bool `json:"api_key_set"`
	// Libraries names the libraries using this connection.
	Libraries []string `json:"libraries"`
}

// ArrWrite creates or replaces an Arr connection.
type ArrWrite struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	BaseURL    string `json:"base_url"`
	APIKeyFile string `json:"api_key_file"`
	// APIKey replaces the stored key when non-nil; nil keeps it and an
	// empty string clears it.
	APIKey *string `json:"api_key"`
}
