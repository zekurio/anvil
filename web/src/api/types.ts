// The JSON contract of /api/v1, mirrored 1:1 from the Go types. Field names
// are the wire names. Times are RFC 3339 UTC strings, lists are never null,
// and durations are Go duration strings ("30m", "20s"; "" means the default).
// Each block names the Go file it mirrors; change them together.

/* ---------- pkg/flow/graph.go ---------- */

export interface Graph {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

/** flow.Node. Options are keyed by Field.key; a missing key means the default. */
export interface GraphNode {
  id: string;
  /** A catalog NodeType id such as "video.encode". */
  type: string;
  /** Overrides the catalog label on the canvas; empty uses it. */
  label?: string;
  /** Canvas position in px, snapped to the 16px grid. */
  x: number;
  y: number;
  options?: Record<string, OptionValue>;
}

/** flow.Edge. */
export interface GraphEdge {
  from: string;
  /** Indexes the source node type's outputs. */
  port: number;
  to: string;
}

/** The JSON value of one node option; its shape follows the field's FieldType. */
export type OptionValue = string | number | boolean | string[] | null;

export type Severity = "error" | "warning";

/** flow.Issue. node_id is absent for problems of the graph as a whole. */
export interface Issue {
  severity: Severity;
  node_id?: string;
  field?: string;
  message: string;
}

export type Outcome = "process" | "skip";

/* ---------- pkg/flow/catalog.go ---------- */

export type NodeKind = "input" | "condition" | "video" | "audio" | "subtitles" | "container" | "output";

export interface NodeType {
  id: string;
  kind: NodeKind;
  /** Palette heading: "Input", "Inspect", "Video", "Audio", "Subtitles", "Container", or "Output". */
  group: string;
  label: string;
  description: string;
  /** Output port labels in order: [""] for one unlabeled port, ["true", "false"] for a condition, [] for a terminal node. */
  outputs: string[];
  sections: Section[];
}

export interface Section {
  title: string;
  collapsible?: boolean;
  /** Starts a collapsible section closed. */
  collapsed?: boolean;
  fields: Field[];
}

export type FieldType = "string" | "int" | "float" | "bool" | "enum" | "enum_list" | "string_list" | "duration";

export interface Field {
  key: string;
  label: string;
  type: FieldType;
  hint?: string;
  default: OptionValue;
  choices?: Choice[];
  /** Renders an enum as a segmented control instead of a select. */
  segmented?: boolean;
  min?: number;
  max?: number;
  /** Unit shown inside the input, e.g. "kb/s". */
  suffix?: string;
  mono?: boolean;
  placeholder?: string;
  /** Lets two short fields share a row. */
  half?: boolean;
  visible_when?: Visibility;
}

export interface Choice {
  value: string;
  label: string;
}

/** Shows a field only while the field named key holds one of in. */
export interface Visibility {
  key: string;
  in: OptionValue[];
}

/* ---------- pkg/domain/job.go ---------- */

export type JobState =
  | "pending"
  | "leased"
  | "running"
  | "validating"
  | "replacing"
  | "complete"
  | "failed"
  | "retrying"
  | "skipped"
  | "canceled";

export type AttemptState = "running" | "succeeded" | "failed" | "canceled";

export type AttemptEventType = "block_started" | "block_finished" | "block_failed" | "artifact";

/* ---------- pkg/domain/selection.go ---------- */

export type StreamKind = "audio" | "subtitle";

export type StreamSelectionRule =
  | "no_streams"
  | "cleanup_disabled"
  | "language_filter"
  | "fallback_keep_all"
  | "fallback_keep_first"
  | "fallback_fail_job";

export type StreamDecisionReason =
  | "language_match"
  | "original_language"
  | "unknown_as_original"
  | "cleanup_disabled"
  | "fallback_keep_all"
  | "fallback_keep_first"
  | "language_not_requested"
  | "commentary"
  | "forced"
  | "sdh";

export interface StreamDecision {
  index: number;
  codec?: string;
  language?: string;
  resolved_language?: string;
  title?: string;
  channels?: number;
  default?: boolean;
  forced?: boolean;
  kept: boolean;
  reason: StreamDecisionReason;
}

export interface StreamSelectionDecision {
  kind: StreamKind;
  rule: StreamSelectionRule;
  original_language?: string;
  requested_languages?: string[];
  resolved_languages?: string[];
  missing_languages?: string[];
  cleanup_disabled_reason?: string;
  streams?: StreamDecision[];
}

/* ---------- pkg/control/types.go ---------- */

export type PathMatchSide = "source" | "asset" | "destination" | "destination_directory";

export interface OccurrenceResponse {
  path: string;
  absolute_path?: string;
  generation: number;
  current: boolean;
  status: string;
}

export interface StreamSelectionResponse {
  attempt_id: number;
  recorded_at: string;
  /** Absent when the record could not be decoded; see decision_error. */
  decision?: StreamSelectionDecision;
  decision_error?: string;
}

export interface JobResponse {
  id: number;
  slug: string;
  library: string;
  state: JobState;
  source: OccurrenceResponse;
  asset?: OccurrenceResponse;
  attempt_count: number;
  created_at: string;
  updated_at: string;
  completed_at?: string;
  lease_owner?: string;
  lease_deadline?: string;
  heartbeat_at?: string;
  last_error?: string;
  destination_path?: string;
  publish_stage?: string;
  matched_on?: PathMatchSide[];
  stream_selection?: StreamSelectionResponse[];
}

/* ---------- pkg/control/types_job_detail.go ---------- */

export interface JobShowResponse {
  api_version: string;
  server_time: string;
  job: JobShowJob;
  pipeline_context?: PipelineContextDetail;
  publish_operation?: PublishOperationDetail;
  stream_selection?: AttemptStreamSelection[];
  attempts: AttemptDetail[];
}

/** control.JobDetail, renamed here because webapi.JobDetail takes the name. */
export interface JobShowJob {
  id: number;
  slug: string;
  state: JobState;
  library: string;
  attempt_count: number;
  updated_at: string;
  created_at: string;
  completed_at?: string;
  source_kind: string;
  source_path: string;
  asset_path: string;
  asset_role: string;
  path: string;
  last_error: string;
}

export interface AttemptStreamSelection {
  attempt_id: number;
  attempt_number: number;
  recorded_at: string;
  decision?: StreamSelectionDecision;
  decision_error?: string;
}

export interface PublishOperationDetail {
  kind: string;
  mode: string;
  stage: string;
  artifact_path: string;
  destination_path: string;
  cleanup_source_path?: string;
  cleanup_entries?: CleanupEntryDetail[];
  cleanup_directories?: string[];
  backup_path?: string;
  artifact_size_bytes: number;
  digest_algorithm?: string;
  conflict_description?: string;
  updated_at: string;
}

export interface CleanupEntryDetail {
  path: string;
  size_bytes: number;
}

export interface AttemptDetail {
  id: number;
  number: number;
  state: AttemptState;
  worker_id: string;
  started_at: string;
  finished_at?: string;
  error: string;
  events: AttemptEventDetail[];
}

export interface AttemptEventDetail {
  id: number;
  attempt_id: number;
  created_at: string;
  type: AttemptEventType;
  name: string;
  message: string;
  payload?: EventPayload;
  process_output?: ProcessOutputDetail;
  stream_selection?: StreamSelectionDecision;
  payload_error?: string;
}

export interface EventPayload {
  kind: string;
  size_bytes: number;
  json?: unknown;
  text?: string;
  bytes_base64?: string;
}

export interface ProcessOutputDetail {
  step: string;
  command: string[];
  exit_code: number;
  duration_ms: number;
  stdout_path: string;
  stderr_path: string;
  stdout_bytes: number;
  stderr_bytes: number;
  error: string;
}

export interface PipelineContextDetail {
  version: number;
  crop_evaluated?: boolean;
  crop_candidate_filter?: string;
  crop_filter?: string;
  crop_rejection_reason?: string;
  crop_no_op?: boolean;
  crop_source_width?: number;
  crop_source_height?: number;
  crop_output_width?: number;
  crop_output_height?: number;
  crop_retained_area_percent?: number;
  search_crf?: number;
  search_metric?: string;
  search_vmaf?: number;
  search_xpsnr?: number;
  search_skip_reason?: string;
}

/* ---------- pkg/webapi/types.go ---------- */

export type ErrorCode =
  | "invalid" // 400: malformed or failing validation
  | "unauthenticated" // 401: sign in first
  | "not_found" // 404
  | "conflict" // 409: stale revision, name taken, or still in use
  | "invalid_flow" // 422: the graph has error issues; see issues
  | "internal" // 500
  | "unavailable"; // 503: daemon is shutting down

/** The body of every failed request. */
export interface ErrorResponse {
  error: ErrorBody;
}

/** webapi.Error. */
export interface ErrorBody {
  code: ErrorCode;
  message: string;
  /** Names the request field at fault, when there is exactly one. */
  field?: string;
  /** Graph problems when code is "invalid_flow". */
  issues?: Issue[];
}

export interface Session {
  /** False when the daemon runs without a password; the UI then never shows a sign-in screen. */
  auth_required: boolean;
  authenticated: boolean;
}

export interface LoginRequest {
  password: string;
}

export interface Status {
  version: string;
  server_time: string;
  started_at: string;
  workers: Workers;
  /** Jobs by state. Absent states are zero. */
  queue: Partial<Record<JobState, number>>;
  running: Progress[];
  libraries: number;
  flows: number;
}

export interface Workers {
  configured: number;
  active: number;
}

/** The live state of one running job. */
export interface Progress {
  job_id: number;
  /** The pipeline step now running, e.g. "crf-search" or "encode". */
  step: string;
  /** 0-100 of the current step, or -1 when unknown. */
  percent: number;
  fps: number;
  /** Relative to realtime (1.9 means 1.9x); 0 when unknown. */
  speed: number;
  /** Estimated time left in the current step, or -1. */
  eta_seconds: number;
  updated_at: string;
}

/** Read-only view of what the daemon was started with. */
export interface Settings {
  version: string;
  config_path: string;
  daemon: DaemonSettings;
  web: WebSettings;
}

export interface DaemonSettings {
  temp_dir: string;
  store_path: string;
  control_socket: string;
  worker_count: number;
  total_threads: number;
  max_threads_per_job: number;
  max_attempts: number;
  scan_interval: string;
  lease_duration: string;
  shutdown_policy: string;
  staging_cleanup_age: string;
  log_level: string;
}

export interface WebSettings {
  listen: string;
  auth_required: boolean;
}

/** One directory for the folder picker. Entries are sorted by name. */
export interface Directory {
  path: string;
  /** Empty at the filesystem root. */
  parent: string;
  entries: DirEntry[];
}

export interface DirEntry {
  name: string;
  path: string;
  dir: boolean;
  /** Set for files only. */
  size_bytes?: number;
}

/** The node catalog, in palette order. */
export interface NodeTypes {
  node_types: NodeType[];
}

export interface FlowList {
  flows: FlowSummary[];
}

export interface FlowSummary {
  id: number;
  name: string;
  description: string;
  revision: number;
  node_count: number;
  /** Names of the libraries this flow is assigned to. */
  libraries: string[];
  created_at: string;
  updated_at: string;
}

export interface Flow extends FlowSummary {
  graph: Graph;
  /** Validation of the stored graph: no error issues, but possibly warnings. */
  check: FlowCheck;
}

export interface FlowWrite {
  name: string;
  description: string;
  graph: Graph;
  /** Must equal the stored revision on PUT, or the write fails with "conflict". Ignored on POST. */
  revision: number;
}

export interface FlowCheckRequest {
  graph: Graph;
}

export interface FlowCheck {
  /** True when no issue has error severity. */
  valid: boolean;
  issues: Issue[];
  /** Keyed by node id; has an entry for every node in the graph. */
  nodes: Record<string, NodeCheck>;
}

export interface NodeCheck {
  /** Generated from the node's options, e.g. "libsvtav1 · vmaf 95 · preset 6". */
  summary: string;
  invalid: boolean;
}

export interface FlowTestRequest {
  graph: Graph;
  /** Absolute path of a media file inside a configured library. */
  path: string;
}

export interface FlowTest {
  check: FlowCheck;
  outcome: Outcome;
  /** Ids of the nodes the file passed through, in order. */
  path: string[];
  decisions: string[];
  file: FileFacts;
  plan: string[];
  /** ffmpeg arguments with "{crf}" and "{output}" placeholders. Empty when outcome is "skip". */
  command: string[];
}

export type HDRKind = "" | "hdr10" | "hlg" | "dolby_vision";

export interface FileFacts {
  path: string;
  size_bytes: number;
  duration_seconds: number;
  container: string;
  video_codec: string;
  width: number;
  height: number;
  hdr: HDRKind;
  /** Overall bitrate; 0 when unknown. */
  bitrate_kbps: number;
  audio: StreamFacts[];
  subtitles: StreamFacts[];
}

export interface StreamFacts {
  index: number;
  codec: string;
  language: string;
  title: string;
  /** Audio tracks only. */
  channels?: number;
  default: boolean;
  forced: boolean;
}

export interface LibraryList {
  libraries: Library[];
}

export type LibraryKind = "media" | "download";
export type RuleAction = "allow" | "deny";

export interface LibraryWrite {
  /** 1-64 characters of a-z, 0-9, "-", and "_". The identity; never changes. */
  name: string;
  /** Cannot change after creation. */
  kind: LibraryKind;
  /** Absolute root that is scanned. */
  path: string;
  flow_id: number;
  /** Keeps scanning and queueing but starts no new job. */
  paused: boolean;
  /** Higher runs first when work competes. */
  priority: number;
  /** Caps simultaneously running jobs; 0 means no cap. */
  concurrency_limit: number;
  /** Overrides the daemon's scan cadence; "" uses it. */
  scan_interval: string;
  /** Arr connection used for original-language lookup; "" disables it. */
  arr: string;
  /** Run top to bottom; the first match decides. */
  rules: Rule[];
  rule_fallback: RuleAction;
  media: MediaOptions;
  download: DownloadOptions;
}

export interface Library extends LibraryWrite {
  flow_name: string;
  stats: LibraryStats;
  last_scan_at?: string;
  created_at: string;
  updated_at: string;
}

export type RuleField = "path" | "extension" | "size";
export type RuleOp = "matches" | "regex" | "is" | "is_not" | "gt" | "lt";

/**
 * Allows or denies files at scan time. Fields and operators:
 *   path       matches (doublestar glob on the library-relative path), regex
 *   extension  is, is_not (comma-separated list without dots: "mkv, mp4")
 *   size       gt, lt (a size with unit: "200 MB", "1.5 GB")
 */
export interface Rule {
  action: RuleAction;
  field: RuleField;
  op: RuleOp;
  value: string;
}

export interface MediaOptions {
  /** "replace" swaps the source for the encode; "copy" publishes it beside the source. */
  replacement_mode: "replace" | "copy";
}

export interface DownloadOptions {
  /** Absolute directory the importer watches. */
  handoff_path: string;
  /** Quiet period before a download counts as complete. */
  stable_for: string;
  package_mode: "auto" | "directory" | "file";
  handoff_mode: "copy" | "move";
  preserve_relative_path: boolean;
  cleanup_source_media: boolean;
  prune_empty_dirs: boolean;
  ignorable_globs: string[];
}

export interface LibraryStats {
  files: number;
  total_bytes: number;
  pending: number;
  running: number;
  done: number;
  failed: number;
  skipped: number;
  /** Input minus output size summed over completed jobs. */
  saved_bytes: number;
}

export interface ScanResult {
  sources: number;
  assets: number;
  enqueued_jobs: number;
  existing_jobs: number;
  skipped_ignored: number;
  skipped_unstable: number;
}

/**
 * How the UI groups job states:
 *   none     seen by a scan, no job
 *   queued   pending, leased, retrying
 *   running  running, validating, replacing
 *   done     complete
 *   failed
 *   skipped  skipped or canceled
 */
export type FileStatus = "none" | "queued" | "running" | "done" | "failed" | "skipped";

export interface FileList {
  /** Every file matching the filter, across all pages. */
  total: number;
  files: LibraryFile[];
}

/** webapi.File, renamed so it does not shadow the DOM File. */
export interface LibraryFile {
  source_id: number;
  asset_id?: number;
  /** Relative to the library root. */
  path: string;
  absolute_path: string;
  size_bytes: number;
  status: FileStatus;
  job_id?: number;
  /** The exact job state behind status. */
  job_state?: JobState;
  last_error?: string;
  /** From the job's last probe; empty until one has run. */
  container: string;
  video_codec: string;
  /** A label such as "1080p". */
  resolution: string;
  /** The first track, e.g. "eac3 5.1". */
  audio: string;
  /** Set once the job completed. */
  output_size_bytes?: number;
  progress?: Progress;
  updated_at: string;
}

export interface RequeueRequest {
  /** Relative to the library root. */
  paths: string[];
}

export interface JobList {
  total: number;
  jobs: Job[];
}

/** One queue row: control.JobResponse flattened with the web fields. */
export interface Job extends JobResponse {
  status: FileStatus;
  input_size_bytes: number;
  output_size_bytes: number;
  progress?: Progress;
}

/** Everything known about one job: control.JobShowResponse flattened with the web fields. */
export interface JobDetail extends JobShowResponse {
  status: FileStatus;
  progress?: Progress;
  /** How the flow resolved on the latest attempt that evaluated it. */
  flow?: JobFlow;
  /** The latest probe of the input, when one has run. */
  file?: FileFacts;
}

export interface JobFlow {
  flow_id: number;
  flow_name: string;
  revision: number;
  graph: Graph;
  outcome: Outcome;
  path: string[];
  decisions: string[];
  plan: string[];
}

/** ids wins; otherwise library and states select. At least one must be set. */
export interface JobActionRequest {
  ids?: number[];
  library?: string;
  states?: JobState[];
}

export interface JobActionResult {
  matched: number;
  changed: number;
  skipped: JobActionSkip[];
}

export interface JobActionSkip {
  id: number;
  reason: string;
}

export interface ArrList {
  arrs: Arr[];
}

/** One Radarr or Sonarr connection. The key itself is never returned. */
export interface Arr {
  name: string;
  type: "radarr" | "sonarr";
  base_url: string;
  api_key_file: string;
  /** Whether a literal key is stored. */
  api_key_set: boolean;
  /** Names of the libraries using this connection. */
  libraries: string[];
}

export interface ArrWrite {
  name: string;
  type: "radarr" | "sonarr";
  base_url: string;
  api_key_file: string;
  /** Replaces the stored key when a string; null keeps it and "" clears it. */
  api_key: string | null;
}

/* ---------- query parameters (route table in pkg/webapi/types.go) ---------- */

/** GET /libraries/{name}/files */
export interface FileQuery {
  status?: FileStatus;
  q?: string;
  sort?: string;
  dir?: "asc" | "desc";
  limit?: number;
  offset?: number;
}

/** GET /jobs */
export interface JobQuery {
  library?: string;
  state?: JobState | JobState[];
  q?: string;
  limit?: number;
  offset?: number;
}
