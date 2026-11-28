package flow

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
)

// loadProfile resolves a TOML profile named "p" the way the daemon does, so
// the tests compare against the real legacy path.
func loadProfile(t *testing.T, data string) domain.Profile {
	t.Helper()
	path := filepath.Join(t.TempDir(), "anvil.toml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := cfg.FindProfile("p")
	if !ok {
		t.Fatal(`the test config has no profile "p"`)
	}
	return profile.ToDomain()
}

const overridesTOML = `
[profiles.p.video]
preset = "5"
ffmpeg_args = ["-g", "240"]

[profiles.p.video.overrides.hevc]
target = 96.0
min_savings_percent = 40.0
preset = "4"
ffmpeg_args = ["-svtav1-params", "tune=0"]

[profiles.p.video.overrides.av1]
skip_encode = true

[profiles.p.video.overrides.h264]
codec = "h265"
accelerator = "qsv"
bit_depth = 8
crf_min = 20
crf_max = 30
metric = "xpsnr"
target = 42.0
force_encode_on_no_fit = true

[profiles.p.video.overrides.mpeg2video]
preset = "8"

[profiles.p.audio]
languages_to_keep = ["orig", "eng"]
`

func profileWithOverrides(t *testing.T) domain.Profile {
	t.Helper()
	return loadProfile(t, overridesTOML)
}

func representativeProfiles(t *testing.T) map[string]domain.Profile {
	t.Helper()
	return map[string]domain.Profile{
		"defaults": loadProfile(t, "[profiles.p]\n"),
		"everything set": loadProfile(t, `
[profiles.p.video]
codec = "hevc"
accelerator = "vaapi"
preset = "slow"
bit_depth = 8
crf_min = 1
crf_max = 51
samples = 3
sample_duration = "1m30s"
metric = "xpsnr"
target = 41.5
min_savings_percent = 0.0
force_encode_on_no_fit = true
ffmpeg_args = ["-g", "240", "-bf", "4"]

[profiles.p.crop]
seek_offsets = ["5m", "1h"]
samples = 6
frame_count = 100
limit = 24
round = 2
reset_count = 30
min_retained_area_percent = 55.5
min_width = 320
min_height = 200
required_alignment = 8

[profiles.p.audio]
languages_to_keep = ["orig", "eng", "jpn"]
keep_commentary = true
fallback = "keep_first"
unknown_as_original = true

[profiles.p.subtitles]
languages_to_keep = ["eng"]
keep_forced = true
keep_sdh = true
keep_commentary = true
fallback = "fail_job"
unknown_as_original = true

[profiles.p.metadata]
mode = "strip"
track_titles = "standardize"

[profiles.p.attachments]
mode = "strip"

[profiles.p.chapters]
mode = "strip"
`),
		"overrides": profileWithOverrides(t),
		"one override": loadProfile(t, `
[profiles.p.video.overrides.hevc]
target = 96.0
`),
		"copy by default, encode one codec": loadProfile(t, `
[profiles.p.video]
skip_encode = true

[profiles.p.video.overrides.h264]
skip_encode = false
preset = "4"
`),
		"copy everything": loadProfile(t, `
[profiles.p.video]
skip_encode = true

[profiles.p.metadata]
track_titles = "preserve"
`),
	}
}

func sourceFacts() map[string]Facts {
	withOriginal := probeOf(videoStream("hevc", 3840, 2160), audioStream("truehd", "jpn", 8))
	withOriginal.Metadata.OriginalLanguage = "ja"
	return map[string]Facts{
		"hevc":          probeOf(videoStream("hevc", 1920, 1080), audioStream("ac3", "eng", 6)),
		"hevc and orig": withOriginal,
		"h264":          probeOf(videoStream("h264", 1280, 720)),
		"av1":           probeOf(videoStream("av1", 1920, 1080)),
		"mpeg2video":    probeOf(videoStream("mpeg2video", 720, 576)),
		"vp9":           probeOf(videoStream("vp9", 1920, 1080)),
		"no video":      probeOf(audioStream("flac", "eng", 2)),
	}
}

// legacyProfile is what the fixed pipeline used for a file before flows: the
// profile with domain.EffectiveVideoProfile applied for the source codec.
func legacyProfile(p domain.Profile, facts Facts) domain.Profile {
	source, _ := domain.PrimaryVideoStream(facts.Probe.Streams)
	p.Video = domain.EffectiveVideoProfile(p, facts.Metadata, source.Codec)
	return p
}

// equivalent compares what a flow resolved with what the legacy path used,
// field by field. Name and the override map are the flow's own business.
// Where the video is copied, the encoder settings are inert and a flow never
// crops, so those are compared as "copied, not cropped" only.
func equivalent(t *testing.T, got, want domain.Profile) {
	t.Helper()
	got.Name, want.Name = "", ""
	want.Video.Overrides = nil
	if got.Video.Overrides != nil {
		t.Errorf("Video.Overrides = %v, want none", got.Video.Overrides)
	}
	if want.Video.SkipEncode {
		if !got.Video.SkipEncode || !got.Crop.Disabled {
			t.Errorf("SkipEncode, Crop.Disabled = %t, %t; want a copy without crop", got.Video.SkipEncode, got.Crop.Disabled)
		}
		got.Video, want.Video = domain.VideoProfile{}, domain.VideoProfile{}
		got.Crop, want.Crop = domain.CropPolicy{}, domain.CropPolicy{}
	}
	// The TOML path leaves an empty offset list non-nil.
	if len(got.Crop.SeekOffsets) == 0 {
		got.Crop.SeekOffsets = nil
	}
	if len(want.Crop.SeekOffsets) == 0 {
		want.Crop.SeekOffsets = nil
	}
	for _, part := range []struct {
		name      string
		got, want any
	}{
		{"Container", got.Container, want.Container},
		{"Video", got.Video, want.Video},
		{"Crop", got.Crop, want.Crop},
		{"Audio", got.Audio, want.Audio},
		{"Subtitles", got.Subtitles, want.Subtitles},
		{"Validation", got.Validation, want.Validation},
		{"Metadata", got.Metadata, want.Metadata},
		{"Attachments", got.Attachments, want.Attachments},
		{"Chapters", got.Chapters, want.Chapters},
	} {
		if !reflect.DeepEqual(part.got, part.want) {
			t.Errorf("%s =\n%+v\nwant\n%+v", part.name, part.got, part.want)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Profile =\n%+v\nwant\n%+v", got, want)
	}
}

func TestFromProfileIsEquivalentToTheLegacyPath(t *testing.T) {
	for name, profile := range representativeProfiles(t) {
		for _, g := range []Graph{FromProfile(profile), viaJSON(t, FromProfile(profile))} {
			if issues := Validate(g); len(issues) != 0 {
				t.Errorf("%s: FromProfile() has issues:\n%s", name, printIssues(issues))
				continue
			}
			for source, facts := range sourceFacts() {
				t.Run(name+"/"+source, func(t *testing.T) {
					resolution := mustEvaluate(t, g, facts)
					if resolution.Outcome != OutcomeProcess {
						t.Errorf("Outcome = %q", resolution.Outcome)
					}
					equivalent(t, resolution.Profile, legacyProfile(profile, facts))
				})
			}
		}
	}
}

func TestFromProfileBranchesOnOverrides(t *testing.T) {
	profile := profileWithOverrides(t)
	g := FromProfile(profile)

	tests := []struct {
		codec     string
		path      []string
		decisions []string
	}{
		{"av1", []string{"input", "is-av1", "video-av1", "audio", "subtitles", "metadata", "publish"}, []string{
			"Video is av1? → true (video codec is av1)",
		}},
		{"h264", []string{"input", "is-av1", "is-h264", "video-h264", "crop-h264", "audio", "subtitles", "metadata", "publish"}, []string{
			"Video is av1? → false (video codec is h264)",
			"Video is h264? → true (video codec is h264)",
		}},
		{"vp9", []string{"input", "is-av1", "is-h264", "is-hevc", "is-mpeg2", "video", "crop", "audio", "subtitles", "metadata", "publish"}, []string{
			"Video is av1? → false (video codec is vp9)",
			"Video is h264? → false (video codec is vp9)",
			"Video is hevc? → false (video codec is vp9)",
			"Video is mpeg2? → false (video codec is vp9)",
		}},
	}
	for _, test := range tests {
		resolution := mustEvaluate(t, g, probeOf(videoStream(test.codec, 1920, 1080)))
		if !slices.Equal(resolution.Path, test.path) {
			t.Errorf("%s: Path = %v, want %v", test.codec, resolution.Path, test.path)
		}
		if !slices.Equal(resolution.Decisions, test.decisions) {
			t.Errorf("%s: Decisions = %q, want %q", test.codec, resolution.Decisions, test.decisions)
		}
	}

	// The override is applied over the base: arguments append, the rest
	// replaces.
	hevc := mustEvaluate(t, g, probeOf(videoStream("hevc", 1920, 1080))).Profile.Video
	if hevc.Target != 96 || hevc.MinSavingsPercent != 40 || hevc.Preset != "4" {
		t.Errorf("hevc video = %+v", hevc)
	}
	if want := []string{"-g", "240", "-svtav1-params", "tune=0"}; !slices.Equal(hevc.FFmpegArgs, want) {
		t.Errorf("hevc FFmpegArgs = %v, want %v", hevc.FFmpegArgs, want)
	}
}

// An override on a codec the old fixed list of check.video_codec did not
// offer used to be dropped, and a replace-mode library then re-encoded
// sources the profile said to copy.
func TestFromProfileConvertsOverridesOnAnyCodec(t *testing.T) {
	profile := loadProfile(t, `
[profiles.p.video.overrides.h265]
target = 96.0

[profiles.p.video.overrides.ffv1]
skip_encode = true

[profiles.p.video.overrides.vvc]
skip_encode = true

[profiles.p.video.overrides.wmv2]
preset = "8"
`)
	g := viaJSON(t, FromProfile(profile))
	if issues := Validate(g); len(issues) != 0 {
		t.Fatalf("FromProfile() has issues:\n%s", printIssues(issues))
	}
	for _, key := range []string{"hevc", "ffv1", "vvc", "wmv2"} {
		if !slices.ContainsFunc(g.Nodes, func(node Node) bool { return node.ID == "is-"+key }) {
			t.Errorf("the override on %s has no condition node", key)
		}
	}

	copied := map[string]bool{"ffv1": true, "vvc": true}
	for _, codec := range []string{"hevc", "ffv1", "vvc", "wmv2", "h264", "mjpeg"} {
		t.Run(codec, func(t *testing.T) {
			facts := probeOf(videoStream(codec, 1920, 1080))
			resolution := mustEvaluate(t, g, facts)
			want := legacyProfile(profile, facts)
			if want.Video.SkipEncode != copied[codec] {
				t.Fatalf("the legacy path has SkipEncode = %t for %s", want.Video.SkipEncode, codec)
			}
			equivalent(t, resolution.Profile, want)
		})
	}
}

// "mpeg2" and "mpeg2video" are one family to check.video_codec. The legacy
// path only ever matched the name ffprobe prints, so that key wins.
func TestFromProfileKeepsTheOverrideFFprobeNames(t *testing.T) {
	profile := loadProfile(t, `
[profiles.p.video.overrides.mpeg2]
preset = "2"

[profiles.p.video.overrides.mpeg2video]
skip_encode = true
`)
	g := FromProfile(profile)
	if issues := Validate(g); len(issues) != 0 {
		t.Fatalf("FromProfile() has issues:\n%s", printIssues(issues))
	}
	facts := probeOf(videoStream("mpeg2video", 720, 576))
	equivalent(t, mustEvaluate(t, g, facts).Profile, legacyProfile(profile, facts))
}

func TestFromProfileRoundTripsWhatAFlowResolves(t *testing.T) {
	profile := baseProfile()
	profile.Video = domain.VideoProfile{
		Codec: "hevc", Accelerator: "qsv", Preset: "", BitDepth: 8,
		QualityMode: domain.VideoQualityFixed, CRF: 22,
		// Search settings are not in effect in fixed mode and read as defaults.
		CRFMin: 18, CRFMax: 40, SampleDuration: 20 * time.Second, Metric: domain.QualityMetricVMAF, Target: 95, MinSavingsPercent: 20,
	}
	profile.Crop.Disabled = false
	profile.Crop.SeekOffsets = []time.Duration{90 * time.Second}
	profile.Audio = domain.AudioProfile{
		LanguagesToKeep: []string{"eng"}, Fallback: domain.StreamFallbackFailJob,
		Encode:  &domain.AudioEncode{Codec: "flac", SourceCodecs: []string{"pcm_s24le"}},
		Downmix: &domain.AudioDownmix{MaxChannels: 2, Codec: "aac", BitrateKbpsPerChannel: 0},
	}
	profile.Subtitles = domain.SubtitleProfile{Fallback: domain.StreamFallbackKeepAll, RemoveAll: true, DropBitmap: true}
	profile.Metadata.TrackTitles = domain.TrackTitleModeStandardize

	g := viaJSON(t, FromProfile(profile))
	if issues := Validate(g); len(issues) != 0 {
		t.Fatalf("FromProfile() has issues:\n%s", printIssues(issues))
	}
	ids := make([]string, 0, len(g.Nodes))
	for _, node := range g.Nodes {
		ids = append(ids, node.ID)
	}
	want := []string{"input", "video", "crop", "audio", "audio-encode", "audio-downmix", "subtitles", "subtitles-images", "subtitles-remove", "metadata", "publish"}
	if !slices.Equal(ids, want) {
		t.Errorf("nodes = %v, want %v", ids, want)
	}
	if got := mustEvaluate(t, g, probeOf()).Profile; !reflect.DeepEqual(got, profile) {
		t.Errorf("Profile =\n%+v\nwant\n%+v", got, profile)
	}

	// Disabled cropping means no crop node.
	profile.Crop.Disabled = true
	if slices.ContainsFunc(FromProfile(profile).Nodes, func(node Node) bool { return node.Type == typeVideoCrop }) {
		t.Error("a profile without cropping got a crop node")
	}
}

func TestDefaultReproducesTheDefaultProfile(t *testing.T) {
	defaults, ok := config.Default().FindProfile(config.DefaultProfileName)
	if !ok {
		t.Fatal("no default profile")
	}
	g := Default()
	if issues := Validate(g); len(issues) != 0 {
		t.Fatalf("Default() has issues:\n%s", printIssues(issues))
	}
	want := []Node{
		{ID: "input", Type: typeInput, X: 288, Y: 32},
		{ID: "video", Type: typeVideoEncode, X: 288, Y: 144},
		{ID: "crop", Type: typeVideoCrop, X: 288, Y: 256},
		{ID: "audio", Type: typeAudioKeep, X: 288, Y: 368},
		{ID: "subtitles", Type: typeSubtitlesKeep, X: 288, Y: 480},
		{ID: "metadata", Type: typeContainerMetadata, X: 288, Y: 592},
		{ID: "publish", Type: typeOutputReplace, X: 288, Y: 704},
	}
	if len(g.Nodes) != len(want) {
		t.Fatalf("Default() has %d nodes, want %d", len(g.Nodes), len(want))
	}
	for i, node := range g.Nodes {
		if node.ID != want[i].ID || node.Type != want[i].Type || node.X != want[i].X || node.Y != want[i].Y || node.Label != "" {
			t.Errorf("node %d = %s %s at (%d,%d), want %s %s at (%d,%d)",
				i, node.ID, node.Type, node.X, node.Y, want[i].ID, want[i].Type, want[i].X, want[i].Y)
		}
		if i > 0 && !slices.Contains(g.Edges, Edge{From: g.Nodes[i-1].ID, Port: 0, To: node.ID}) {
			t.Errorf("no edge from %s to %s", g.Nodes[i-1].ID, node.ID)
		}
	}
	if len(g.Edges) != len(want)-1 {
		t.Errorf("Default() has %d edges, want %d", len(g.Edges), len(want)-1)
	}

	for source, facts := range sourceFacts() {
		resolution := mustEvaluate(t, viaJSON(t, g), facts)
		if resolution.Outcome != OutcomeProcess || len(resolution.Decisions) != 0 {
			t.Errorf("%s: Outcome = %q, Decisions = %q", source, resolution.Outcome, resolution.Decisions)
		}
		equivalent(t, resolution.Profile, defaults.ToDomain())
	}

	// Nodes left at their catalog defaults do the same as the default flow,
	// which spells every option out.
	bare := Default()
	for i := range bare.Nodes {
		bare.Nodes[i].Options = nil
	}
	spelledOut, err := Canonical(g)
	if err != nil {
		t.Fatal(err)
	}
	atDefaults, err := Canonical(bare)
	if err != nil {
		t.Fatal(err)
	}
	if string(spelledOut) != string(atDefaults) {
		t.Errorf("the catalog defaults differ from the default profile:\n%s\nwant\n%s", atDefaults, spelledOut)
	}

	wantPlan := []string{
		"Video: encode to av1 with libsvtav1, preset 6, 10-bit, VMAF 95 target (CRF 18–40); crop black bars",
		"Audio: keep every track; copy",
		"Subtitles: keep every track",
		"Container: mkv; strip track titles",
		"Output: replace the source in place",
	}
	if got := mustEvaluate(t, g, probeOf()).Plan; !slices.Equal(got, wantPlan) {
		t.Errorf("Plan = %q, want %q", got, wantPlan)
	}
}

func TestDefaultIsAFreshGraph(t *testing.T) {
	first := Default()
	first.Nodes[1].Options["preset"] = "0"
	first.Nodes[0].ID = "changed"
	first.Edges[0].To = "changed"
	if second := Default(); second.Nodes[1].Options["preset"] != "6" || second.Nodes[0].ID != "input" || second.Edges[0].To != "video" {
		t.Errorf("Default() shares state between calls: %+v", second)
	}
}

func TestGeneratedLayoutMatchesTheReference(t *testing.T) {
	g := FromProfile(loadProfile(t, `
[profiles.p.video.overrides.hevc]
skip_encode = true
`))
	want := map[string][2]int{
		"input":      {288, 32},
		"is-hevc":    {288, 144},
		"video-hevc": {64, 256},
		"video":      {512, 256},
		"crop":       {512, 368},
		"audio":      {288, 480},
		"subtitles":  {288, 592},
		"metadata":   {288, 704},
		"publish":    {288, 816},
	}
	if len(g.Nodes) != len(want) {
		t.Errorf("FromProfile() has %d nodes, want %d", len(g.Nodes), len(want))
	}
	for _, node := range g.Nodes {
		if position := [2]int{node.X, node.Y}; position != want[node.ID] {
			t.Errorf("%s at %v, want %v", node.ID, position, want[node.ID])
		}
	}
	if label := g.Nodes[1].Label; label != "Video is hevc?" {
		t.Errorf("condition label = %q", label)
	}
}

func TestGeneratedLayoutRules(t *testing.T) {
	const nodeWidth, nodeHeight = 240, 64
	for name, profile := range representativeProfiles(t) {
		g := FromProfile(profile)
		positions := map[string]Node{}
		for i, node := range g.Nodes {
			positions[node.ID] = node
			if node.X%16 != 0 || node.Y%16 != 0 || node.X < 0 || node.Y < 0 {
				t.Errorf("%s: %s at (%d,%d) is off the 16 px grid", name, node.ID, node.X, node.Y)
			}
			if (node.Y-32)%112 != 0 {
				t.Errorf("%s: %s at y=%d is not on a row", name, node.ID, node.Y)
			}
			for _, other := range g.Nodes[:i] {
				if abs(node.X-other.X) < nodeWidth && abs(node.Y-other.Y) < nodeHeight {
					t.Errorf("%s: %s and %s overlap", name, node.ID, other.ID)
				}
			}
		}
		if first, last := g.Nodes[0], g.Nodes[len(g.Nodes)-1]; first.Type != typeInput || last.Type != typeOutputReplace || first.X != last.X {
			t.Errorf("%s: the flow does not run from an input to a publish node in one column", name)
		}
		for _, edge := range g.Edges {
			from, to := positions[edge.From], positions[edge.To]
			if to.Y <= from.Y {
				t.Errorf("%s: edge %s -> %s does not go down", name, edge.From, edge.To)
			}
			if from.Type != typeCheckVideoCodec {
				continue
			}
			// True goes left and false goes right, a node's width and more apart.
			switch {
			case edge.Port == 0 && to.X != from.X-224:
				t.Errorf("%s: true branch of %s is at x=%d, want %d", name, edge.From, to.X, from.X-224)
			case edge.Port == 1 && to.X < from.X+224:
				t.Errorf("%s: false branch of %s is at x=%d, want at least %d", name, edge.From, to.X, from.X+224)
			}
		}
	}
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
