package control

import "time"

// JobRetryRequest requeues jobs. References name individual jobs by id or slug;
// Failed is the bulk form and is deliberately a separate field so a bulk retry
// can never be the accidental result of an empty reference list.
type JobRetryRequest struct {
	References []string `json:"references,omitempty"`
	Failed     bool     `json:"failed,omitempty"`
	Library    string   `json:"library,omitempty"`
}

type JobRetryResponse struct {
	APIVersion string    `json:"api_version"`
	ServerTime time.Time `json:"server_time"`
	// RetriedFailed counts jobs requeued by the bulk --failed form only.
	RetriedFailed int64            `json:"retried_failed"`
	Jobs          []JobRetryResult `json:"jobs"`
}

type JobRetryResult struct {
	ID      int64  `json:"id"`
	Slug    string `json:"slug"`
	Library string `json:"library"`
	State   string `json:"state"`
}

// JobPruneRequest deletes terminal jobs whose source occurrence is already
// missing. Apply is opt-in: without it the command reports what it would do.
type JobPruneRequest struct {
	Library string   `json:"library,omitempty"`
	States  []string `json:"state,omitempty"`
	Apply   bool     `json:"apply,omitempty"`
}

type JobPruneResponse struct {
	APIVersion      string           `json:"api_version"`
	ServerTime      time.Time        `json:"server_time"`
	DryRun          bool             `json:"dry_run"`
	MatchedJobs     int64            `json:"matched_jobs"`
	AffectedSources int64            `json:"affected_sources"`
	DeletedJobs     int64            `json:"deleted_jobs"`
	ByState         map[string]int64 `json:"by_state,omitempty"`
	// ProtectedJobs lists jobs excluded from the prune because they still own
	// an unresolved publish journal. Deleting those rows would cascade the
	// journal away and strand a staged artifact, a half-published destination,
	// or an .anvil-backup with nothing left that knows how to resolve them.
	ProtectedJobs []ProtectedJob `json:"protected_jobs,omitempty"`
}

// ProtectedJob explains one refusal to delete or clean something up.
type ProtectedJob struct {
	ID     int64  `json:"id"`
	Slug   string `json:"slug"`
	Reason string `json:"reason"`
}

// Reasons a job or staging directory is protected from maintenance.
const (
	ProtectedReasonPublishJournal = "unresolved_publish_journal"
	ProtectedReasonActiveJob      = "job_not_terminal"
)

type JobRecoverResponse struct {
	APIVersion    string    `json:"api_version"`
	ServerTime    time.Time `json:"server_time"`
	RecoveredJobs int64     `json:"recovered_jobs"`
}

// LibraryScanRequest scans one configured library, or every library when
// Library is empty.
type LibraryScanRequest struct {
	Library string `json:"library,omitempty"`
}

type LibraryScanResponse struct {
	APIVersion      string     `json:"api_version"`
	ServerTime      time.Time  `json:"server_time"`
	Libraries       int        `json:"libraries"`
	Sources         int        `json:"sources"`
	Assets          int        `json:"assets"`
	EnqueuedJobs    int        `json:"enqueued_jobs"`
	ExistingJobs    int        `json:"existing_jobs"`
	SkippedIgnored  int        `json:"skipped_ignored"`
	SkippedUnstable int        `json:"skipped_unstable"`
	NextStableAt    *time.Time `json:"next_stable_at,omitempty"`
}

type LibraryStatsRequest struct {
	Library string `json:"library,omitempty"`
}

type LibraryStatsResponse struct {
	APIVersion string              `json:"api_version"`
	ServerTime time.Time           `json:"server_time"`
	Libraries  []LibraryStatsEntry `json:"libraries"`
}

type LibraryStatsEntry struct {
	Library         string  `json:"library"`
	Jobs            int64   `json:"jobs"`
	InputSizeBytes  int64   `json:"input_size_bytes"`
	OutputSizeBytes int64   `json:"output_size_bytes"`
	SavedBytes      int64   `json:"saved_bytes"`
	SavedPercent    float64 `json:"saved_percent"`
}

// ForceOccurrenceRequest explicitly creates the next occurrence of one exact
// library-relative media path and enqueues it.
type ForceOccurrenceRequest struct {
	Library string `json:"library"`
	Path    string `json:"path"`
}

type ForceOccurrenceResponse struct {
	APIVersion       string    `json:"api_version"`
	ServerTime       time.Time `json:"server_time"`
	Library          string    `json:"library"`
	Path             string    `json:"path"`
	SourceID         int64     `json:"source_id"`
	SourceGeneration int       `json:"source_generation"`
	AssetID          int64     `json:"asset_id"`
	AssetGeneration  int       `json:"asset_generation"`
	JobID            int64     `json:"job_id"`
	JobSlug          string    `json:"job_slug"`
	JobState         string    `json:"job_state"`
}

// StagingCleanupRequest removes stale staging directories. OlderThan is a Go
// duration string; empty uses daemon.staging_cleanup_age.
type StagingCleanupRequest struct {
	OlderThan   string `json:"older_than,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`
	LegacyParts bool   `json:"legacy_parts,omitempty"`
}

type StagingCleanupResponse struct {
	APIVersion string    `json:"api_version"`
	ServerTime time.Time `json:"server_time"`
	DryRun     bool      `json:"dry_run"`
	Root       string    `json:"root"`
	OlderThan  string    `json:"older_than"`
	Candidates int       `json:"candidates"`
	Removed    int       `json:"removed"`
	Skipped    int       `json:"skipped"`
	// Protected counts staging directories that were old enough to remove but
	// still belong to a live attempt or an unresolved publish journal.
	Protected     int                       `json:"protected"`
	ProtectedJobs []ProtectedJob            `json:"protected_jobs,omitempty"`
	Errors        []string                  `json:"errors,omitempty"`
	LegacyParts   *LegacyPartCleanupSummary `json:"legacy_parts,omitempty"`
}

type LegacyPartCleanupSummary struct {
	Candidates int `json:"candidates"`
	Removed    int `json:"removed"`
	Protected  int `json:"protected"`
}

// StoreBackupRequest writes a consistent SQLite snapshot. The destination is
// resolved by the daemon, because the daemon is the process that owns the
// database and the only one that can name its live path safely.
type StoreBackupRequest struct {
	Destination string `json:"destination"`
}

type StoreBackupResponse struct {
	APIVersion string    `json:"api_version"`
	ServerTime time.Time `json:"server_time"`
	Path       string    `json:"path"`
	SizeBytes  int64     `json:"size_bytes"`
	Integrity  string    `json:"integrity"`
}
