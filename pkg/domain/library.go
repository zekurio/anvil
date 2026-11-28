package domain

import (
	"encoding/json"
)

// Library is a library as the pipeline sees it. Every attempt keeps a JSON
// snapshot of it, and that snapshot is part of the fingerprint that guards
// resumable checkpoints, so the type holds only what processing a file
// depends on: pausing a library, changing its priority or concurrency limit,
// or editing its scan rules must not make the daemon redo work, and
// credentials have no place in a job's history. How the result is published
// is not a library setting: the flow decides it (Profile.Publish).
type Library struct {
	Name LibraryName
	Path string
	// FlowID is the flow assigned when the attempt started.
	FlowID   int64
	Metadata MetadataProviderPolicy
	// Reprocess queues a file a scan finds changed, or written again at a
	// path the library has seen before, as a download folder needs. Off,
	// such a file is recorded as processed and only a requeue runs it. An
	// unchanged file is never queued twice either way. A library that
	// reprocesses also skips every input that carries anvil's processed
	// marker, so its own output never loops; without Reprocess only a first
	// generation is skipped, and a forced requeue runs again.
	Reprocess bool
	// IgnorableGlobs are the residue patterns of the library: excluded from
	// discovery, and removable when a move prunes the emptied folders.
	IgnorableGlobs []string
}

type MetadataProviderPolicy struct {
	Provider MetadataProviderKind
	BaseURL  string
	// APIKey is used for the lookup and never written into a snapshot.
	APIKey     string `json:"-"`
	APIKeyFile string
}

type MetadataProviderKind string

const (
	MetadataProviderNone   MetadataProviderKind = ""
	MetadataProviderRadarr MetadataProviderKind = "radarr"
	MetadataProviderSonarr MetadataProviderKind = "sonarr"
)

// PackageMode decides how a library groups the files it finds into sources.
type PackageMode string

const (
	PackageModeAuto      PackageMode = "auto"
	PackageModeDirectory PackageMode = "directory"
	PackageModeFile      PackageMode = "file"
)

// FlowSnapshot is the flow an attempt ran, as it was when the attempt
// started. It is what attempts.resolved_flow_json holds. Graph is the flow's
// graph in its own JSON form; this package does not know its shape.
type FlowSnapshot struct {
	ID       int64           `json:"id"`
	Name     string          `json:"name"`
	Revision int             `json:"revision"`
	Graph    json.RawMessage `json:"graph"`
}

// Flow outcomes, as FlowResolution records them.
const (
	FlowOutcomeProcess = "process"
	FlowOutcomeSkip    = "skip"
)

// FlowResolution is what a flow decided for one file on one attempt. The
// flow step records it as the "flow-resolution" attempt artifact.
type FlowResolution struct {
	FlowID   int64  `json:"flow_id"`
	FlowName string `json:"flow_name"`
	Revision int    `json:"revision"`
	// Outcome is FlowOutcomeProcess or FlowOutcomeSkip.
	Outcome string `json:"outcome"`
	// Path lists the IDs of the nodes the file passed through, in order.
	Path []string `json:"path"`
	// Decisions holds one line per condition on the path.
	Decisions []string `json:"decisions"`
	// Plan states the result in words, one line per aspect.
	Plan []string `json:"plan"`
}
