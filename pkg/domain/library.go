package domain

import "time"

type LibraryKind string

const (
	LibraryKindMedia    LibraryKind = "media"
	LibraryKindDownload LibraryKind = "download"
)

type Library struct {
	Name             LibraryName
	Kind             LibraryKind
	Path             string
	Priority         int
	FlowName         FlowName
	ProfileName      ProfileName
	IncludeGlobs     []string
	ExcludeGlobs     []string
	ConcurrencyLimit int
	Metadata         MetadataProviderPolicy
	Media            MediaLibraryPolicy
	Download         DownloadLibraryPolicy
}

type MetadataProviderPolicy struct {
	Provider   MetadataProviderKind
	BaseURL    string
	APIKey     string
	APIKeyFile string
}

type MetadataProviderKind string

const (
	MetadataProviderNone   MetadataProviderKind = ""
	MetadataProviderRadarr MetadataProviderKind = "radarr"
	MetadataProviderSonarr MetadataProviderKind = "sonarr"
)

type MediaLibraryPolicy struct {
	ReplacementMode ReplacementMode
}

type ReplacementMode string

const (
	ReplacementModeReplace ReplacementMode = "replace"
	ReplacementModeCopy    ReplacementMode = "copy"
)

type DownloadLibraryPolicy struct {
	HandoffPath          string
	StableFor            time.Duration
	PackageMode          DownloadPackageMode
	HandoffMode          HandoffMode
	PreserveRelativePath bool
	CleanupSourceMedia   bool
	PruneEmptyDirs       bool
	IgnorableGlobs       []string
}

type DownloadPackageMode string

const (
	DownloadPackageModeAuto      DownloadPackageMode = "auto"
	DownloadPackageModeDirectory DownloadPackageMode = "directory"
	DownloadPackageModeFile      DownloadPackageMode = "file"
)

type HandoffMode string

const (
	HandoffModeMove HandoffMode = "move"
	HandoffModeCopy HandoffMode = "copy"
)
