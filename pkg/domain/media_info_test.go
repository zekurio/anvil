package domain

import "testing"

func TestPrimaryVideoStreamSkipsAttachedPictures(t *testing.T) {
	cover := MediaStream{Index: 0, Type: "video", Codec: "png", Disposition: map[string]bool{"attached_pic": true}}
	episode := MediaStream{Index: 1, Type: "video", Codec: "h264"}
	audio := MediaStream{Index: 2, Type: "audio", Codec: "aac"}

	tests := map[string]struct {
		streams   []MediaStream
		wantIndex int
		wantFound bool
	}{
		"cover_before_primary": {streams: []MediaStream{cover, episode, audio}, wantIndex: 1, wantFound: true},
		"primary_before_cover": {streams: []MediaStream{episode, cover, audio}, wantIndex: 1, wantFound: true},
		"only_cover":           {streams: []MediaStream{cover, audio}, wantFound: false},
		"no_streams":           {wantFound: false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			stream, found := PrimaryVideoStream(tt.streams)
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if found && stream.Index != tt.wantIndex {
				t.Fatalf("stream index = %d, want %d", stream.Index, tt.wantIndex)
			}
		})
	}
}
