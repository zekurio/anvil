package store

import (
	"database/sql"
	"errors"

	"github.com/zekurio/anvil/pkg/domain"
)

type scanner interface {
	Scan(dest ...any) error
}

func scanMediaSource(row scanner) (domain.MediaSource, error) {
	var source domain.MediaSource
	var modTime sql.NullString
	var firstSeenAt string
	var lastSeenAt string
	err := row.Scan(
		&source.ID,
		&source.LibraryName,
		&source.Kind,
		&source.RelativePath,
		&source.Generation,
		&source.Current,
		&source.Status,
		&source.Fingerprint.SizeBytes,
		&modTime,
		&source.Fingerprint.HashAlgorithm,
		&source.Fingerprint.HashValue,
		&firstSeenAt,
		&lastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MediaSource{}, ErrNotFound
	}
	if err != nil {
		return domain.MediaSource{}, err
	}
	source.Fingerprint.ModTime = parseNullTime(modTime)
	source.FirstSeenAt = parseTime(firstSeenAt)
	source.LastSeenAt = parseTime(lastSeenAt)
	return source, nil
}

func scanMediaAsset(row scanner) (domain.MediaAsset, error) {
	var asset domain.MediaAsset
	var modTime sql.NullString
	var firstSeenAt string
	var lastSeenAt string
	err := row.Scan(
		&asset.ID,
		&asset.SourceID,
		&asset.RelativePath,
		&asset.Generation,
		&asset.Current,
		&asset.Role,
		&asset.Status,
		&asset.Fingerprint.SizeBytes,
		&modTime,
		&asset.Fingerprint.HashAlgorithm,
		&asset.Fingerprint.HashValue,
		&firstSeenAt,
		&lastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MediaAsset{}, ErrNotFound
	}
	if err != nil {
		return domain.MediaAsset{}, err
	}
	asset.Fingerprint.ModTime = parseNullTime(modTime)
	asset.FirstSeenAt = parseTime(firstSeenAt)
	asset.LastSeenAt = parseTime(lastSeenAt)
	return asset, nil
}

func scanJob(row scanner) (domain.Job, error) {
	var job domain.Job
	var assetID sql.NullInt64
	var leaseDeadline sql.NullString
	var heartbeatAt sql.NullString
	var completedAt sql.NullString
	var createdAt string
	var updatedAt string
	err := row.Scan(
		&job.ID,
		&job.Slug,
		&job.SourceID,
		&assetID,
		&job.LibraryName,
		&job.Priority,
		&job.State,
		&job.LeaseOwner,
		&leaseDeadline,
		&heartbeatAt,
		&job.AttemptCount,
		&job.LastError,
		&job.InputSizeBytes,
		&job.OutputSizeBytes,
		&createdAt,
		&updatedAt,
		&completedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Job{}, ErrNotFound
	}
	if err != nil {
		return domain.Job{}, err
	}
	if assetID.Valid {
		job.AssetID = domain.MediaAssetID(assetID.Int64)
	}
	job.LeaseDeadline = parseNullTimePtr(leaseDeadline)
	job.HeartbeatAt = parseNullTimePtr(heartbeatAt)
	job.CreatedAt = parseTime(createdAt)
	job.UpdatedAt = parseTime(updatedAt)
	job.CompletedAt = parseNullTimePtr(completedAt)
	return job, nil
}

func scanJobSummary(row scanner) (JobSummary, error) {
	var summary JobSummary
	var assetID sql.NullInt64
	var leaseDeadline sql.NullString
	var heartbeatAt sql.NullString
	var completedAt sql.NullString
	var createdAt string
	var updatedAt string
	var assetPath sql.NullString
	var assetRole sql.NullString
	err := row.Scan(
		&summary.Job.ID,
		&summary.Job.Slug,
		&summary.Job.SourceID,
		&assetID,
		&summary.Job.LibraryName,
		&summary.Job.Priority,
		&summary.Job.State,
		&summary.Job.LeaseOwner,
		&leaseDeadline,
		&heartbeatAt,
		&summary.Job.AttemptCount,
		&summary.Job.LastError,
		&summary.Job.InputSizeBytes,
		&summary.Job.OutputSizeBytes,
		&createdAt,
		&updatedAt,
		&completedAt,
		&summary.SourceKind,
		&summary.SourcePath,
		&assetPath,
		&assetRole,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return JobSummary{}, ErrNotFound
	}
	if err != nil {
		return JobSummary{}, err
	}
	if assetID.Valid {
		summary.Job.AssetID = domain.MediaAssetID(assetID.Int64)
	}
	summary.Job.LeaseDeadline = parseNullTimePtr(leaseDeadline)
	summary.Job.HeartbeatAt = parseNullTimePtr(heartbeatAt)
	summary.Job.CreatedAt = parseTime(createdAt)
	summary.Job.UpdatedAt = parseTime(updatedAt)
	summary.Job.CompletedAt = parseNullTimePtr(completedAt)
	if assetPath.Valid {
		summary.AssetPath = assetPath.String
	}
	if assetRole.Valid {
		summary.AssetRole = domain.MediaAssetRole(assetRole.String)
	}
	return summary, nil
}

func scanAttempt(row scanner) (domain.Attempt, error) {
	var attempt domain.Attempt
	var finishedAt sql.NullString
	var startedAt string
	err := row.Scan(
		&attempt.ID,
		&attempt.JobID,
		&attempt.Number,
		&attempt.WorkerID,
		&attempt.State,
		&attempt.ResolvedLibrary,
		&attempt.ResolvedFlow,
		&attempt.ResolvedProfile,
		&startedAt,
		&finishedAt,
		&attempt.Error,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Attempt{}, ErrNotFound
	}
	if err != nil {
		return domain.Attempt{}, err
	}
	attempt.StartedAt = parseTime(startedAt)
	attempt.FinishedAt = parseNullTimePtr(finishedAt)
	return attempt, nil
}

func scanAttemptEvent(row scanner) (domain.AttemptEvent, error) {
	var event domain.AttemptEvent
	var createdAt string
	err := row.Scan(
		&event.ID,
		&event.AttemptID,
		&event.Type,
		&event.Name,
		&event.Message,
		&event.Payload,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AttemptEvent{}, ErrNotFound
	}
	if err != nil {
		return domain.AttemptEvent{}, err
	}
	event.CreatedAt = parseTime(createdAt)
	return event, nil
}
