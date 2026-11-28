package video

import "testing"

func TestQualityRange(t *testing.T) {
	tests := []struct {
		encoder         string
		lowest, highest int
	}{
		{"libsvtav1", 1, 63},
		{"libx265", 0, 51},
		{"libx264", 0, 51},
		{"av1_qsv", 1, 51},
		{"hevc_qsv", 1, 51},
		{"h264_vaapi", 1, 51},
		{"av1_vaapi", 1, 255},
		{"av1_amf", 0, 255},
		{"hevc_amf", 0, 51},
	}
	for _, tt := range tests {
		if lowest, highest := QualityRange(tt.encoder); lowest != tt.lowest || highest != tt.highest {
			t.Errorf("QualityRange(%q) = %d-%d, want %d-%d", tt.encoder, lowest, highest, tt.lowest, tt.highest)
		}
	}
}

func TestEncoderRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Encoders() {
		byCodec, ok := LookupEncoder(e.Codec, e.Backend)
		if !ok || byCodec.Name != e.Name || seen[e.Name] {
			t.Fatalf("invalid or duplicate registry entry: %+v", e)
		}
		seen[e.Name] = true
		if e.Quality.Min > e.Quality.Default || e.Quality.Default > e.Quality.Max ||
			e.Quality.SearchMin < e.Quality.Min || e.Quality.SearchMax > e.Quality.Max ||
			e.Quality.SearchMin >= e.Quality.SearchMax || !e.Speed.Accepts(e.Speed.Default) {
			t.Fatalf("invalid defaults for %s", e.Name)
		}
	}
	if len(seen) != 12 {
		t.Fatalf("got %d encoders, want 12", len(seen))
	}
	e, ok := LookupEncoder("H.265", " QSV ")
	if !ok || e.Name != "hevc_qsv" || CanonicalCodec(e.Name) != "hevc" {
		t.Fatalf("alias lookup = %+v, %t", e, ok)
	}
	for _, name := range []string{"av1_nvenc", "hevc_videotoolbox"} {
		if _, ok := LookupEncoder(name, ""); ok {
			t.Errorf("unexpected encoder for %s", name)
		}
	}
}
