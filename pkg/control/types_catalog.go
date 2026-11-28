package control

import "time"

// LibraryListResponse lists the libraries the daemon manages. Libraries are
// created and edited in the web UI; this is the read-only view of them.
type LibraryListResponse struct {
	APIVersion string         `json:"api_version"`
	ServerTime time.Time      `json:"server_time"`
	Libraries  []LibraryEntry `json:"libraries"`
}

// LibraryEntry is one library with what the store knows about it.
type LibraryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Reprocess queues a file a scan finds changed, or written again at a
	// path the library has seen before.
	Reprocess        bool   `json:"reprocess"`
	FlowID           int64  `json:"flow_id"`
	FlowName         string `json:"flow_name"`
	Paused           bool   `json:"paused"`
	Priority         int    `json:"priority"`
	ConcurrencyLimit int    `json:"concurrency_limit"`
	Arr              string `json:"arr,omitempty"`
	// Files and TotalBytes cover the media files the last scan found.
	Files      int64 `json:"files"`
	TotalBytes int64 `json:"total_bytes"`
	// Jobs counts the library's jobs by state. Absent states are zero.
	Jobs       map[string]int64 `json:"jobs"`
	SavedBytes int64            `json:"saved_bytes"`
	LastScanAt *time.Time       `json:"last_scan_at,omitempty"`
}

// FlowListResponse lists the stored flows without their graphs.
type FlowListResponse struct {
	APIVersion string      `json:"api_version"`
	ServerTime time.Time   `json:"server_time"`
	Flows      []FlowEntry `json:"flows"`
}

// FlowEntry is one flow.
type FlowEntry struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Revision    int    `json:"revision"`
	NodeCount   int    `json:"node_count"`
	// Libraries names the libraries the flow is assigned to.
	Libraries []string `json:"libraries"`
	// Errors and Warnings count the flow's validation issues. A flow with
	// errors fails every job that runs it; only one imported from a legacy
	// profile can have any.
	Errors    int       `json:"errors"`
	Warnings  int       `json:"warnings"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// JobFlowDetail is how the library's flow resolved on the latest attempt of
// a job that got far enough to evaluate it.
type JobFlowDetail struct {
	AttemptID     int64  `json:"attempt_id"`
	AttemptNumber int    `json:"attempt_number"`
	FlowID        int64  `json:"flow_id"`
	FlowName      string `json:"flow_name"`
	Revision      int    `json:"revision"`
	// Outcome is "process" or "skip".
	Outcome string `json:"outcome"`
	// Path lists the IDs of the nodes the file passed through, in order.
	Path      []string `json:"path"`
	Decisions []string `json:"decisions"`
	Plan      []string `json:"plan"`
}
