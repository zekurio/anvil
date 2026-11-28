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
		{"av1_qsv", 1, 255},
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
	// Every encoder Anvil can resolve has a range that qualityArgs can use.
	for accelerator, codecs := range encoders {
		for codec, encoder := range codecs {
			if lowest, highest := QualityRange(encoder); lowest < 0 || highest <= lowest {
				t.Errorf("%s/%s encoder %s has range %d-%d", accelerator, codec, encoder, lowest, highest)
			}
		}
	}
}
