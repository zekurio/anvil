// Package staging manages per-attempt scratch space: CRF-search samples and
// anything else that never publishes. The encode artifact itself is written
// next to its destination (see replace.PartPath); only scratch lives here.
package staging
