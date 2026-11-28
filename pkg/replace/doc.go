// Package replace owns journaled, no-overwrite publication and cleanup after
// validation. The flow's output node decides between an in-place replacement
// and a move to another folder (a handoff, in the journal's words). Artifacts
// are written on the destination filesystem under the .anvil-part suffix
// (beside the destination for a replacement, under the handoff work directory
// of the destination folder for a move) and linked into place, so
// publication is a metadata operation, never a bulk copy.
package replace
