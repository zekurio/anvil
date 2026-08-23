package ffmpeg

import (
	"slices"
	"testing"
)

func TestAnvilMetadataArgs(t *testing.T) {
	want := []string{"-metadata:s:v:0", "anvil.processed=true"}
	if got := anvilMetadataArgs(); !slices.Equal(got, want) {
		t.Fatalf("anvilMetadataArgs = %q, want %q", got, want)
	}
}
