package probe

import "testing"

func TestParseFFProbeRate(t *testing.T) {
	for value, want := range map[string]float64{
		"24/1":       24,
		"24000/1001": 24000.0 / 1001,
		"25":         25,
		"0/0":        0,
		"30/0":       0,
		"-30/1":      0,
		"":           0,
		"N/A":        0,
	} {
		if got := parseFFProbeRate(value); got != want {
			t.Errorf("parseFFProbeRate(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestParseFFProbeReadsTheFrameRate(t *testing.T) {
	probed, err := parseFFProbe("/media/a.mkv", []byte(`{"streams":[
		{"index":0,"codec_type":"video","codec_name":"h264","avg_frame_rate":"24000/1001"},
		{"index":1,"codec_type":"audio","codec_name":"aac","avg_frame_rate":"0/0"}
	],"format":{"duration":"150.0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := probed.Streams[0].FrameRate; got != 24000.0/1001 {
		t.Errorf("video frame rate = %v", got)
	}
	if got := probed.Streams[1].FrameRate; got != 0 {
		t.Errorf("audio frame rate = %v", got)
	}
}
