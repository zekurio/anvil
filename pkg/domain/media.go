package domain

import "time"

type SourceKind string

const (
	SourceKindFile    SourceKind = "file"
	SourceKindPackage SourceKind = "package"
)

type MediaSourceStatus string

const (
	MediaSourceActive    MediaSourceStatus = "active"
	MediaSourceProcessed MediaSourceStatus = "processed"
	MediaSourceMissing   MediaSourceStatus = "missing"
)

type MediaSource struct {
	ID           MediaSourceID
	LibraryName  LibraryName
	Kind         SourceKind
	RelativePath string
	Generation   int
	Current      bool
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
)

type MediaAsset struct {
	ID           MediaAssetID
	SourceID     MediaSourceID
	RelativePath string
	Generation   int
	Current      bool
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
