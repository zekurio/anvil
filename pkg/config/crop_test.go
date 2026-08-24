package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestLoadCropPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anvil.toml")
	data := []byte(`
[profiles.default-av1.crop]
seek_offsets = ["1s", "1m30s"]
frame_count = 42
limit = 20
round = 8
reset_count = 12
min_retained_area_percent = 65
min_width = 96
min_height = 80
required_alignment = 4
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := cfg.FindProfile(DefaultProfileName)
	if !ok {
		t.Fatal("default profile not found")
	}
	crop := profile.ToDomain().Crop
	if !slices.Equal(crop.SeekOffsets, []time.Duration{time.Second, 90 * time.Second}) {
		t.Fatalf("SeekOffsets = %v", crop.SeekOffsets)
	}
	if crop.FrameCount != 42 || crop.Limit != 20 || crop.Round != 8 || crop.ResetCount != 12 {
		t.Fatalf("sampling policy = %#v", crop)
	}
	if crop.MinRetainedAreaPercent != 65 || crop.MinWidth != 96 || crop.MinHeight != 80 || crop.RequiredAlignment != 4 {
		t.Fatalf("safety policy = %#v", crop)
	}
}

func TestLoadRejectsInvalidCropPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anvil.toml")
	data := []byte(`
[profiles.default-av1.crop]
seek_offsets = ["-1s"]
min_retained_area_percent = 101
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load succeeded with invalid crop policy")
	}
}

func TestLoadRejectsNonFiniteCropThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anvil.toml")
	data := []byte(`
[profiles.default-av1.crop]
min_retained_area_percent = nan
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load succeeded with non-finite crop threshold")
	}
}
