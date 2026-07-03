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

func TestDetectIgnoresCoverArtStreams(t *testing.T) {
	probed := domain.ProbeResult{Streams: []domain.MediaStream{
		{
			Index:       0,
			Type:        "video",
			Codec:       "png",
			Disposition: map[string]bool{"attached_pic": true},
			Tags: map[string]string{
				TagEncoded:    "true",
				TagProfile:    "default-av1",
				TagVideoCodec: "libsvtav1",
			},
		},
	}}
	match := Detect(probed, testProfile())
	if match.Compatible || len(match.Tags) > 0 {
		t.Fatalf("match = %+v, want no match for cover art stream", match)
	}
}

func TestDetectProcessedFindsCompatibleProcessedMarker(t *testing.T) {
	probed := domain.ProbeResult{Streams: []domain.MediaStream{
		{Index: 0, Type: "video", Tags: map[string]string{
			TagProcessed:   "true",
			TagProfile:     "default-av1",
			TagVideoAction: VideoActionCopy,
		}},
	}}
	match := DetectProcessed(probed, testProfile())
	if !match.Compatible {
		t.Fatalf("Compatible = false, want true")
	}
}

func TestOutputTagsAddsCurrentEncodeDetails(t *testing.T) {
	tags := OutputTags(domain.EncodePlan{
		ProfileName: domain.ProfileName("default-av1"),
		VideoCodec:  "libsvtav1",
		BitDepth:    10,
		PixelFormat: "yuv420p10le",
		CRF:         29,
		CropFilter:  "crop=1920:800:0:140",
	})
	if got, want := tags[TagEncoded], "true"; got != want {
		t.Fatalf("encoded tag = %q, want %q", got, want)
	}
	if got, want := tags[TagProcessed], "true"; got != want {
		t.Fatalf("processed tag = %q, want %q", got, want)
	}
	if got, want := tags[TagVideoAction], VideoActionEncode; got != want {
		t.Fatalf("video action = %q, want %q", got, want)
	}
	if got, want := tags[TagVideoCRF], "29"; got != want {
		t.Fatalf("crf tag = %q, want %q", got, want)
	}
	if got, want := tags[TagVersion], Version; got != want {
		t.Fatalf("version tag = %q, want %q", got, want)
	}
}

func TestOutputTagsMarksVideoCopyAsProcessedWithoutEncodedClaim(t *testing.T) {
	reason := "ab-av1 did not find a CRF satisfying VMAF/size constraints"
	tags := OutputTags(domain.EncodePlan{
		ProfileName:     domain.ProfileName("default-av1"),
		VideoCodec:      "libsvtav1",
		BitDepth:        10,
		PixelFormat:     "yuv420p10le",
		VideoCopy:       true,
		VideoCopyReason: reason,
		CRF:             18,
		CropFilter:      "crop=1920:800:0:140",
	})
	if got, want := tags[TagProcessed], "true"; got != want {
		t.Fatalf("processed tag = %q, want %q", got, want)
	}
	if got, want := tags[TagVideoAction], VideoActionCopy; got != want {
		t.Fatalf("video action = %q, want %q", got, want)
	}
	if got, want := tags[TagProcessReason], reason; got != want {
		t.Fatalf("process reason = %q, want %q", got, want)
	}
	if _, ok := tags[TagEncoded]; ok {
		t.Fatalf("encoded tag = %q, want absent", tags[TagEncoded])
	}
	if _, ok := tags[TagVideoCRF]; ok {
		t.Fatalf("crf tag = %q, want absent", tags[TagVideoCRF])
	}
	if _, ok := tags[TagCrop]; ok {
		t.Fatalf("crop tag = %q, want absent for copied video", tags[TagCrop])
	}
}

func TestOutputTagsPreservesExistingEncodedMarkerWhenCopyingVideo(t *testing.T) {
	tags := OutputTags(domain.EncodePlan{
		VideoCopy: true,
		AnvilTags: map[string]string{
			TagEncoded:  "true",
			TagVideoCRF: "29",
		},
	})
	if got, want := tags[TagEncoded], "true"; got != want {
		t.Fatalf("encoded tag = %q, want %q", got, want)
	}
	if got, want := tags[TagVideoCRF], "29"; got != want {
		t.Fatalf("crf tag = %q, want %q", got, want)
	}
	if got, want := tags[TagProcessed], "true"; got != want {
		t.Fatalf("processed tag = %q, want %q", got, want)
	}
	if got, want := tags[TagVideoAction], VideoActionCopy; got != want {
		t.Fatalf("video action = %q, want %q", got, want)
	}
}

func testProfile() domain.Profile {
	return domain.Profile{
		Name: domain.ProfileName("default-av1"),
		Video: domain.VideoProfile{
			Codec:       "av1",
			Accelerator: "software",
			BitDepth:    10,
		},
	}
}
