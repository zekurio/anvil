// Package staging manages per-attempt scratch space: CRF-search samples and
// anything else that never publishes. The encode artifact itself is written
// on the destination filesystem (see replace.ArtifactPath); only scratch lives here.
package staging
