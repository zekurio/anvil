package domain

import "time"

type SourceKind string

const (
	SourceKindFile    SourceKind = "file"
	SourceKindPackage SourceKind = "package"
)

type MediaSourceStatus string

const (
	MediaSourceActive  MediaSourceStatus = "active"
	MediaSourceMissing MediaSourceStatus = "missing"
	MediaSourceIgnored MediaSourceStatus = "ignored"
)

type MediaSource struct {
	ID           MediaSourceID
	LibraryName  LibraryName
	Kind         SourceKind
	RelativePath string
	Status       MediaSourceStatus
	Fingerprint  FileFingerprint
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
}

type MediaAssetRole string

const (
	MediaAssetRolePrimaryVideo MediaAssetRole = "primary_video"
	MediaAssetRoleVideo        MediaAssetRole = "video"
	MediaAssetRoleSample       MediaAssetRole = "sample"
	MediaAssetRoleSubtitle     MediaAssetRole = "subtitle"
	MediaAssetRoleMetadata     MediaAssetRole = "metadata"
	MediaAssetRoleExtra        MediaAssetRole = "extra"
	MediaAssetRoleUnknown      MediaAssetRole = "unknown"
)

type MediaAssetStatus string

const (
	MediaAssetActive    MediaAssetStatus = "active"
	MediaAssetProcessed MediaAssetStatus = "processed"
	MediaAssetMissing   MediaAssetStatus = "missing"
	MediaAssetIgnored   MediaAssetStatus = "ignored"
)

type MediaAsset struct {
	ID           MediaAssetID
	SourceID     MediaSourceID
	RelativePath string
	Role         MediaAssetRole
	Status       MediaAssetStatus
	Fingerprint  FileFingerprint
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
}

type FileFingerprint struct {
	SizeBytes     int64
	ModTime       time.Time
	HashAlgorithm string
	HashValue     string
}
