package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
)

// FileStatus groups job states the way a library's file list shows them.
type FileStatus string

const (
	FileStatusNone    FileStatus = "none"    // seen by a scan, no job
	FileStatusQueued  FileStatus = "queued"  // pending, leased, retrying
	FileStatusRunning FileStatus = "running" // running, validating, replacing
	FileStatusDone    FileStatus = "done"    // complete
	FileStatusFailed  FileStatus = "failed"
	FileStatusSkipped FileStatus = "skipped" // skipped or canceled
)

// FileStatusOf maps a job state to its group. An empty state means no job.
func FileStatusOf(state domain.JobState) FileStatus {
	switch state {
	case "":
		return FileStatusNone
	case domain.JobStatePending, domain.JobStateLeased, domain.JobStateRetrying:
		return FileStatusQueued
	case domain.JobStateRunning, domain.JobStateValidating, domain.JobStateReplacing:
		return FileStatusRunning
	case domain.JobStateComplete:
		return FileStatusDone
	case domain.JobStateFailed:
		return FileStatusFailed
	default:
		return FileStatusSkipped
	}
}

// JobStatesOf lists the job states a status groups, the inverse of
// FileStatusOf. FileStatusNone and an unknown status have none.
func JobStatesOf(status FileStatus) []domain.JobState {
	var states []domain.JobState
	for _, state := range domain.JobStates() {
		if FileStatusOf(state) == status {
			states = append(states, state)
		}
	}
	return states
}

// ValidFileStatus reports whether a status names one of the groups.
func ValidFileStatus(status FileStatus) bool {
	switch status {
	case FileStatusNone, FileStatusQueued, FileStatusRunning, FileStatusDone, FileStatusFailed, FileStatusSkipped:
		return true
	default:
		return false
	}
}

// fileStatusSQL is FileStatusOf over the jobs alias j of a LEFT JOIN, and
// fileStatusRankSQL orders the groups by how much attention they need.
const (
	fileStatusSQL = `CASE
	WHEN j.state IS NULL THEN 'none'
	WHEN j.state IN ('pending', 'leased', 'retrying') THEN 'queued'
	WHEN j.state IN ('running', 'validating', 'replacing') THEN 'running'
	WHEN j.state = 'complete' THEN 'done'
	WHEN j.state = 'failed' THEN 'failed'
	ELSE 'skipped' END`
	fileStatusRankSQL = `CASE
	WHEN j.state IS NULL THEN 5
	WHEN j.state IN ('running', 'validating', 'replacing') THEN 0
	WHEN j.state IN ('pending', 'leased', 'retrying') THEN 1
	WHEN j.state = 'failed' THEN 2
	WHEN j.state = 'complete' THEN 3
	ELSE 4 END`
	// filePathSQL is the library-relative path of an asset: a package's asset
	// lives below its source directory, a file source is its own asset.
	filePathSQL = `CASE WHEN s.kind = 'package' THEN s.relative_path || '/' || a.relative_path ELSE s.relative_path END`
)

// LibraryFileSort names a sort key of the file list.
type LibraryFileSort string

const (
	LibraryFileSortPath    LibraryFileSort = "path"
	LibraryFileSortSize    LibraryFileSort = "size"
	LibraryFileSortStatus  LibraryFileSort = "status"
	LibraryFileSortUpdated LibraryFileSort = "updated_at"
)

// LibraryFileFilter selects one page of a library's files.
type LibraryFileFilter struct {
	LibraryName domain.LibraryName
	// Status keeps one group; empty keeps all.
	Status FileStatus
	// Query keeps files whose library-relative path contains it, ignoring
	// ASCII case.
	Query string
	// Sort defaults to the path.
	Sort       LibraryFileSort
	Descending bool
	// Limit of zero returns every match.
	Limit  int
	Offset int
}

// LibraryFile is one media file a scan currently knows, with the job of that
// exact occurrence when it has one.
type LibraryFile struct {
	SourceID domain.MediaSourceID
	AssetID  domain.MediaAssetID
	// Path is relative to the library root and slash-separated.
	Path      string
	SizeBytes int64
	Status    FileStatus
	// JobID is zero and the remaining job fields are empty without a job.
	JobID     domain.JobID
	JobState  domain.JobState
	LastError string
	// InputSizeBytes and OutputSizeBytes are what the job read and wrote,
	// recorded when it completed. SizeBytes is the file as it is now, which
	// after a replacement in place is the output.
	InputSizeBytes  int64
	OutputSizeBytes int64
	// Probe holds the facts of the job's last probe; it is zero until one
	// has run.
	Probe     ProbeFacts
	UpdatedAt time.Time
}

// ProbeFacts are the facts of a probe that a file list shows. They are kept
// in columns of their own beside the job's checkpoint, which holds the whole
// probe and is far too large to read once per listed file.
type ProbeFacts struct {
	// FormatName is ffprobe's demuxer name, such as "matroska,webm".
	FormatName string
	// VideoCodec, Width, and Height describe the main video stream: the
	// first one that is not an attached picture.
	VideoCodec    string
	Width, Height int
	// AudioCodec and AudioChannels describe the first audio stream.
	AudioCodec    string
	AudioChannels int
}

// probeFactsOf reduces a probe to what a list shows. A nil probe has none.
func probeFactsOf(probe *domain.ProbeResult) ProbeFacts {
	if probe == nil {
		return ProbeFacts{}
	}
	facts := ProbeFacts{FormatName: probe.FormatName}
	if video, found := domain.PrimaryVideoStream(probe.Streams); found {
		facts.VideoCodec, facts.Width, facts.Height = video.Codec, video.Width, video.Height
	}
	for _, stream := range probe.Streams {
		if stream.Type == "audio" {
			facts.AudioCodec, facts.AudioChannels = stream.Codec, stream.Channels
			break
		}
	}
	return facts
}

// ListLibraryFiles returns one page of a library's files and the number of
// files matching the filter across all pages.
//
// The UI polls this every few seconds, and the store has one connection, so
// the query stays narrow. It never selects pipeline_context_json: a
// checkpoint of several kilobytes per file made this the daemon's most
// expensive read. And it works in two steps, because filtering and sorting a
// library means going through all of its rows. The first step picks the page
// from what jobs_file_idx holds, the state and the last update of a job,
// without touching a job row; only the files of the page then have their job
// read.
func (s *SQLiteStore) ListLibraryFiles(ctx context.Context, filter LibraryFileFilter) (files []LibraryFile, total int64, err error) {
	if strings.TrimSpace(string(filter.LibraryName)) == "" {
		return nil, 0, fmt.Errorf("library name is required")
	}
	const (
		tables = `
FROM media_sources s
JOIN media_assets a ON a.source_id = s.id AND a.is_current = 1 AND a.status != 'missing'
`
		jobs = `LEFT JOIN jobs j ON j.source_id = s.id AND j.asset_id = a.id
`
	)
	where := `WHERE s.library_name = ? AND s.is_current = 1 AND s.status != 'missing'
`
	args := []any{string(filter.LibraryName)}
	// An occurrence has at most one job (jobs_target_idx), so the jobs only
	// change how many files match when the filter asks about them.
	counted := tables
	if filter.Status != "" {
		counted += jobs
		where += " AND (" + fileStatusSQL + ") = ?\n"
		args = append(args, string(filter.Status))
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		where += " AND (" + filePathSQL + `) LIKE ? ESCAPE '\'` + "\n"
		args = append(args, likeContains(query))
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*)"+counted+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count library files: %w", err)
	}

	direction := "ASC"
	if filter.Descending {
		direction = "DESC"
	}
	// The order is written in the column names of the page, which both
	// steps use: the first to pick the page, the second to keep its order.
	var order string
	switch filter.Sort {
	case LibraryFileSortSize:
		order = "file_size " + direction + ", file_path ASC"
	case LibraryFileSortStatus:
		order = "status_rank " + direction + ", file_path ASC"
	case LibraryFileSortUpdated:
		order = "file_updated " + direction + ", file_path ASC"
	case LibraryFileSortPath, "":
		order = "file_path " + direction
	default:
		return nil, 0, fmt.Errorf("unknown file sort %q", filter.Sort)
	}
	order += ", file_asset ASC\n"
	page := `
SELECT s.id AS file_source, a.id AS file_asset, ` + filePathSQL + ` AS file_path, a.size_bytes AS file_size,
	j.id AS file_job, (` + fileStatusRankSQL + `) AS status_rank,
	COALESCE(j.updated_at, a.updated_at) AS file_updated` + tables + jobs + where + "ORDER BY " + order
	if filter.Limit > 0 {
		page += "LIMIT ? OFFSET ?\n"
		args = append(args, filter.Limit, max(filter.Offset, 0))
	}
	query := `
SELECT file_source, file_asset, file_path, file_size,
	j.id, j.state, j.last_error, j.input_size_bytes, j.output_size_bytes,
	j.probe_format, j.probe_video_codec, j.probe_width, j.probe_height, j.probe_audio_codec, j.probe_audio_channels,
	file_updated
FROM (` + page + `)
LEFT JOIN jobs j ON j.id = file_job
ORDER BY ` + order
	files = []LibraryFile{}
	err = s.eachRow(ctx, "library files", query, args, func(rows *sql.Rows) error {
		var file LibraryFile
		var jobID sql.NullInt64
		var state, lastError, updated sql.NullString
		var inputSize, outputSize, width, height, audioChannels sql.NullInt64
		var format, videoCodec, audioCodec sql.NullString
		if err := rows.Scan(&file.SourceID, &file.AssetID, &file.Path, &file.SizeBytes,
			&jobID, &state, &lastError, &inputSize, &outputSize,
			&format, &videoCodec, &width, &height, &audioCodec, &audioChannels, &updated); err != nil {
			return err
		}
		file.JobID = domain.JobID(jobID.Int64)
		file.JobState = domain.JobState(state.String)
		file.Status = FileStatusOf(file.JobState)
		file.LastError = lastError.String
		file.InputSizeBytes = inputSize.Int64
		file.OutputSizeBytes = outputSize.Int64
		file.UpdatedAt = parseNullTime(updated)
		file.Probe = ProbeFacts{
			FormatName: format.String,
			VideoCodec: videoCodec.String, Width: int(width.Int64), Height: int(height.Int64),
			AudioCodec: audioCodec.String, AudioChannels: int(audioChannels.Int64),
		}
		files = append(files, file)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return files, total, nil
}

// likeContains builds a LIKE pattern that matches text containing the query
// literally; the pattern is used with ESCAPE '\'.
func likeContains(query string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query) + "%"
}
