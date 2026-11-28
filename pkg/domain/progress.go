package domain

import "time"

// JobProgress is a live, in-memory view of what a running job is doing. It is
// never persisted: a restart loses it and the next progress line rebuilds it.
type JobProgress struct {
	JobID JobID
	// Step is the pipeline step that reported, such as "crf-search" or "encode".
	Step string
	// Percent is 0-100 of the current step, or -1 when it cannot be known.
	Percent float64
	// FPS is the encoder's current frame rate; 0 when unknown.
	FPS float64
	// Speed is the processing rate relative to realtime; 0 when unknown.
	Speed float64
	// ETASeconds estimates the time left in the current step, or -1.
	ETASeconds float64
	UpdatedAt  time.Time
}
