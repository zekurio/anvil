package pipeline

// ArtifactReport describes a durable block result that should be attached to
// every attempt, including attempts that resume the block from a checkpoint.
type ArtifactReport struct {
	Name    string
	Message string
	Payload any
}

// ArtifactReporter is implemented by blocks whose result should remain visible
// after source files and process logs are gone.
type ArtifactReporter interface {
	Artifact(job *JobContext) (ArtifactReport, bool)
}
