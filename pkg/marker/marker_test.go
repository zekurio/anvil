package marker

import (
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func TestDetectFindsCompatibleAnvilVideoMarker(t *testing.T) {
	probed := domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 0, Type: "video", Tags: map[string]string{
			"AnViL.EnCoDeD":            "true",
			"anvil.profile":            "default-av1",
			"anvil.video.codec":        "libsvtav1",
			"anvil.video.pixel_format": "yuv420p10le",
			"anvil.crop":               "crop=1920:800:0:140",
		}},
	}}
	match := Detect(probed, testProfile())
	if !match.Compatible {
		t.Fatalf("Compatible = false, want true")
	}
	if got, want := match.CropFilter, "crop=1920:800:0:140"; got != want {
		t.Fatalf("CropFilter = %q, want %q", got, want)
	}
}

func TestDetectRejectsDifferentProfile(t *testing.T) {
	probed := domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 0, Type: "video", Tags: map[string]string{
			TagEncoded:    "true",
			TagProfile:    "other-profile",
			TagVideoCodec: "libsvtav1",
		}},
	}}
	if match := Detect(probed, testProfile()); match.Compatible {
		t.Fatal("Compatible = true, want false")
	}
}

func TestOutputTagsAddsCurrentEncodeDetails(t *testing.T) {
	tags := OutputTags(domain.EncodePlan{
		ProfileName: domain.ProfileName("default-av1"),
		VideoCodec:  "libsvtav1",
		PixelFormat: "yuv420p10le",
		CRF:         29,
		CropFilter:  "crop=1920:800:0:140",
	})
	if got, want := tags[TagEncoded], "true"; got != want {
		t.Fatalf("encoded tag = %q, want %q", got, want)
	}
	if got, want := tags[TagVideoCRF], "29"; got != want {
		t.Fatalf("crf tag = %q, want %q", got, want)
	}
	if got, want := tags[TagVersion], Version; got != want {
		t.Fatalf("version tag = %q, want %q", got, want)
	}
}

func testProfile() domain.Profile {
	return domain.Profile{
		Name: domain.ProfileName("default-av1"),
		Video: domain.VideoProfile{
			Codec:       "libsvtav1",
			PixelFormat: "yuv420p10le",
		},
	}
}
