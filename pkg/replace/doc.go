// Package replace owns journaled, no-overwrite publication and cleanup of
// replacement and download-handoff media after validation. Artifacts are
// written next to their destination under the .anvil-part suffix and linked
// into place, so publication is a same-directory metadata operation, never a
// bulk copy.
package replace
