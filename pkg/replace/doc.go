// Package replace owns journaled, no-overwrite publication and cleanup of
// replacement and download-handoff media after validation. Artifacts are
// written on the destination filesystem under the .anvil-part suffix (beside
// the destination for media libraries, under the handoff work directory for
// download libraries) and linked into place, so publication is a metadata
// operation, never a bulk copy.
package replace
