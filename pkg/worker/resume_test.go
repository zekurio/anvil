package worker

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/flow"
	"github.com/zekurio/anvil/pkg/pipeline"
)

func TestResumeCropRevalidatesCachedCandidate(t *testing.T) {
	cached := domain.JobPipelineContext{
		Crop:   &domain.CropResult{Filter: "crop=176:64:996:64"},
		Search: &domain.SearchResult{CRF: 27},
	}
	persistence := &pipelineContextPersistence{
		cached:  &cached,
		current: cached,
	}
	job := &pipeline.JobContext{
		Probe: &domain.ProbeResult{Streams: []domain.MediaStream{{
			Type: "video", Width: 1920, Height: 1080,
		}}},
	}

	resumed, err := persistence.ResumeStep(context.Background(), "crop-detect", job)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed {
		t.Fatal("crop step was not resumed")
	}
	if job.Crop == nil {
		t.Fatal("crop result is nil")
	}
	if job.Crop.Filter != "" || job.Metadata.CropFilter != "" {
		t.Fatalf("cached crop was applied: %#v", job.Crop)
	}
	if job.Crop.CandidateFilter != "crop=176:64:996:64" {
		t.Fatalf("CandidateFilter = %q", job.Crop.CandidateFilter)
	}
	if !strings.Contains(job.Crop.RejectionReason, "retained area 0.54%") {
		t.Fatalf("RejectionReason = %q", job.Crop.RejectionReason)
	}
	if persistence.current.Crop == nil || persistence.current.Crop.RejectionReason == "" {
		t.Fatalf("revalidated crop was not retained in current context: %#v", persistence.current.Crop)
	}
	if persistence.current.Search != nil {
		t.Fatalf("cached search was retained after crop changed: %#v", persistence.current.Search)
	}
	if resumed, err := persistence.ResumeStep(context.Background(), "crf-search", job); err != nil || resumed {
		t.Fatalf("CRF search resume = %v, %v; want rerun", resumed, err)
	}
}

func TestResumeCropRejectsCachedFailedWindow(t *testing.T) {
	cached := domain.JobPipelineContext{
		Crop: &domain.CropResult{
			CandidateFilter: "crop=1920:800:0:140",
			Filter:          "crop=1920:800:0:140",
			Samples: []domain.CropSample{
				{Filter: "crop=1920:800:0:140"},
				{Filter: "crop=1920:1080:0:0", Error: "decode failed"},
				{Filter: "crop=1920:800:0:140"},
			},
		},
		Metadata: domain.JobMetadata{CropFilter: "crop=1920:800:0:140"},
		Search:   &domain.SearchResult{CRF: 27},
	}
	persistence := &pipelineContextPersistence{cached: &cached, current: cached}
	job := &pipeline.JobContext{
		Metadata: cached.Metadata,
		Probe: &domain.ProbeResult{Streams: []domain.MediaStream{{
			Type: "video", Width: 1920, Height: 1080,
		}}},
	}
	resumed, err := persistence.ResumeStep(context.Background(), "crop-detect", job)
	if err != nil || !resumed {
		t.Fatalf("crop resume = %v, %v", resumed, err)
	}
	if job.Crop.Filter != "" || job.Metadata.CropFilter != "" || job.Crop.RejectionReason != "crop sample failed" {
		t.Fatalf("unsafe cached crop retained: %#v", job.Crop)
	}
	if persistence.current.Crop.Filter != "" || persistence.current.Metadata.CropFilter != "" || persistence.current.Search != nil {
		t.Fatalf("unsafe checkpoint retained: %#v", persistence.current)
	}
	if resumed, err := persistence.ResumeStep(context.Background(), "crf-search", job); err != nil || resumed {
		t.Fatalf("CRF search resume = %v, %v; want rerun", resumed, err)
	}
}

func TestNativeSearchRejectsOldCheckpoint(t *testing.T) {
	base := domain.JobPipelineContext{Version: domain.JobPipelineContextVersion, InputPath: "movie.mkv"}
	cached := base
	cached.Version = 4
	cached.Search = &domain.SearchResult{CRF: 27}
	if pipelineContextMatches(base, cached) {
		t.Fatal("accepted pre-native-search checkpoint")
	}
}

func TestResumeDropsCachedCropOnceDisabled(t *testing.T) {
	cached := domain.JobPipelineContext{
		Crop: &domain.CropResult{
			CandidateFilter: "crop=1920:800:0:140",
			Filter:          "crop=1920:800:0:140",
			Samples:         []domain.CropSample{{Filter: "crop=1920:800:0:140"}, {Filter: "crop=1920:800:0:140"}},
		},
		Metadata: domain.JobMetadata{CropFilter: "crop=1920:800:0:140"},
		Search:   &domain.SearchResult{CRF: 27},
	}
	persistence := &pipelineContextPersistence{cached: &cached, current: cached}
	job := &pipeline.JobContext{
		Metadata: cached.Metadata,
		Profile:  domain.Profile{Crop: domain.CropPolicy{Disabled: true}},
		Probe:    &domain.ProbeResult{Streams: []domain.MediaStream{{Type: "video", Width: 1920, Height: 1080}}},
	}
	resumed, err := persistence.ResumeStep(context.Background(), "crop-detect", job)
	if err != nil || !resumed {
		t.Fatalf("crop resume = %v, %v", resumed, err)
	}
	if !job.Crop.Disabled || job.Crop.Filter != "" || job.Metadata.CropFilter != "" {
		t.Fatalf("cached crop survived a disabled policy: %#v", job.Crop)
	}
	if persistence.current.Metadata.CropFilter != "" || persistence.current.Search != nil {
		t.Fatalf("checkpoint kept the crop or the search measured with it: %#v", persistence.current)
	}
	if resumed, err := persistence.ResumeStep(context.Background(), "crf-search", job); err != nil || resumed {
		t.Fatalf("CRF search resume = %v, %v; want rerun", resumed, err)
	}
}

func TestResumeDetectsAgainOnceCropIsEnabled(t *testing.T) {
	cached := domain.JobPipelineContext{
		Crop:   &domain.CropResult{Disabled: true, SourceWidth: 1920, SourceHeight: 1080, OutputWidth: 1920, OutputHeight: 1080, RetainedAreaPercent: 100},
		Search: &domain.SearchResult{CRF: 27},
	}
	persistence := &pipelineContextPersistence{cached: &cached, current: cached}
	job := &pipeline.JobContext{Probe: &domain.ProbeResult{Streams: []domain.MediaStream{{Type: "video", Width: 1920, Height: 1080}}}}
	if resumed, err := persistence.ResumeStep(context.Background(), "crop-detect", job); err != nil || resumed {
		t.Fatalf("crop resume = %v, %v; want detection to run", resumed, err)
	}
	if job.Crop != nil {
		t.Fatalf("crop = %#v", job.Crop)
	}
	if resumed, err := persistence.ResumeStep(context.Background(), "crf-search", job); err != nil || resumed {
		t.Fatalf("CRF search resume = %v, %v; the cached search measured an uncropped frame", resumed, err)
	}
}

func TestResumeSearchAcrossQualityModes(t *testing.T) {
	searched := domain.SearchResult{CRF: 31, Metric: domain.QualityMetricVMAF, VMAF: 95.2}
	fixed := domain.SearchResult{QualityMode: domain.VideoQualityFixed, CRF: 24}
	tests := []struct {
		name   string
		cached domain.SearchResult
		mode   domain.VideoQualityMode
		want   bool
	}{
		{"search result resumes in search mode", searched, domain.VideoQualitySearch, true},
		{"search result resumes under the zero mode", searched, "", true},
		{"search result never decides a fixed encode", searched, domain.VideoQualityFixed, false},
		{"fixed result never stands in for a search", fixed, domain.VideoQualitySearch, false},
		{"fixed result is restated, not resumed", fixed, domain.VideoQualityFixed, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cached := domain.JobPipelineContext{Search: &tt.cached}
			persistence := &pipelineContextPersistence{cached: &cached, current: cached}
			job := &pipeline.JobContext{Profile: domain.Profile{Video: domain.VideoProfile{QualityMode: tt.mode, CRF: 24}}}
			resumed, err := persistence.ResumeStep(context.Background(), "crf-search", job)
			if err != nil || resumed != tt.want {
				t.Fatalf("resume = %v, %v; want %v", resumed, err, tt.want)
			}
			if (job.Search != nil) != tt.want {
				t.Fatalf("search = %+v", job.Search)
			}
		})
	}
}

// The first guard keys a checkpoint on what the flow means. Moving nodes
// around, relabelling them, or spelling out a default must keep it; anything
// that changes what the flow does to a file must drop it.
func TestCheckpointFollowsFlowMeaningNotLayout(t *testing.T) {
	original := flow.Default()
	fingerprint := func(graph flow.Graph) string {
		t.Helper()
		value, err := flowFingerprint(graph)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	job := &pipeline.JobContext{InputPath: "movie.mkv"}
	library := []byte(`{"Name":"movies"}`)
	cached := basePipelineContext(job, library, fingerprint(original), domain.JobMetadata{})
	matches := func(graph flow.Graph) bool {
		return pipelineContextMatches(basePipelineContext(job, library, fingerprint(graph), domain.JobMetadata{}), cached)
	}
	if !matches(original) {
		t.Fatal("an unchanged flow does not match its own checkpoint")
	}

	tidied := copyGraph(t, original)
	for i := range tidied.Nodes {
		tidied.Nodes[i].X += 160
		tidied.Nodes[i].Y += 48
		tidied.Nodes[i].Label = "renamed " + tidied.Nodes[i].ID
	}
	slices.Reverse(tidied.Nodes)
	slices.Reverse(tidied.Edges)
	if !matches(tidied) {
		t.Fatal("moving, relabelling, and reordering nodes invalidated the checkpoint")
	}

	video := slices.IndexFunc(original.Nodes, func(node flow.Node) bool { return node.Type == "video.encode" })
	if video < 0 {
		t.Fatal("the default flow has no video.encode node")
	}
	changes := map[string]func(*flow.Node){
		"quality target": func(node *flow.Node) { node.Options["target"] = 93.0 },
		"fixed CRF": func(node *flow.Node) {
			node.Options["quality_mode"] = "fixed"
			node.Options["crf"] = 24
		},
		"preset": func(node *flow.Node) { node.Options["preset"] = "4" },
	}
	for name, change := range changes {
		edited := copyGraph(t, original)
		if edited.Nodes[video].Options == nil {
			edited.Nodes[video].Options = map[string]any{}
		}
		change(&edited.Nodes[video])
		if matches(edited) {
			t.Errorf("checkpoint survived a changed %s", name)
		}
	}

	// An unreadable flow has no fingerprint, and a checkpoint is never
	// reused without one.
	unknown := basePipelineContext(job, library, "", domain.JobMetadata{})
	if pipelineContextMatches(unknown, unknown) {
		t.Fatal("a checkpoint matched without a flow fingerprint")
	}
	if _, err := flowFingerprint(flow.Graph{}); err == nil {
		t.Fatal("an invalid graph has a fingerprint")
	}
}

func copyGraph(t *testing.T, graph flow.Graph) flow.Graph {
	t.Helper()
	data, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	var copied flow.Graph
	if err := json.Unmarshal(data, &copied); err != nil {
		t.Fatal(err)
	}
	return copied
}

// The second guard sits behind the flow step: whatever the flow resolves now,
// results computed under a different effective profile are gone before a step
// can resume from them. Only the probe survives.
func TestProfileGuardDropsResultsOfAnotherProfile(t *testing.T) {
	base := domain.Profile{Name: "Default AV1", Video: domain.VideoProfile{CRFMin: 18, CRFMax: 40}}
	changes := map[string]func(*domain.Profile){
		"quality mode": func(p *domain.Profile) { p.Video.QualityMode = domain.VideoQualityFixed },
		"fixed CRF":    func(p *domain.Profile) { p.Video.CRF = 24 },
		"target":       func(p *domain.Profile) { p.Video.Target = 93 },
		"crop opt-out": func(p *domain.Profile) { p.Crop.Disabled = true },
		"audio encode": func(p *domain.Profile) { p.Audio.Encode = &domain.AudioEncode{Codec: "opus"} },
		"encode bitrate": func(p *domain.Profile) {
			p.Audio.Encode = &domain.AudioEncode{Codec: "opus", BitrateKbpsPerChannel: 64}
		},
		"audio downmix":   func(p *domain.Profile) { p.Audio.Downmix = &domain.AudioDownmix{MaxChannels: 2, Codec: "aac"} },
		"audio languages": func(p *domain.Profile) { p.Audio.LanguagesToKeep = []string{"eng"} },
		"remove subs":     func(p *domain.Profile) { p.Subtitles.RemoveAll = true },
		"drop bitmap":     func(p *domain.Profile) { p.Subtitles.DropBitmap = true },
	}
	checkpoint := func() *pipelineContextPersistence {
		cached := domain.JobPipelineContext{
			ResolvedProfileJSON: profileFingerprint(base),
			Probe:               &domain.ProbeResult{FormatName: "matroska"},
			Audio:               &domain.AudioSelection{},
			Crop:                &domain.CropResult{Filter: "crop=1920:800:0:140"},
			Search:              &domain.SearchResult{CRF: 27},
		}
		return &pipelineContextPersistence{cached: &cached, current: cached}
	}
	resumes := func(p *pipelineContextPersistence, profile domain.Profile) (audio, search bool) {
		t.Helper()
		job := &pipeline.JobContext{Profile: profile}
		if err := p.StepSucceeded(context.Background(), "flow", job); err != nil {
			t.Fatal(err)
		}
		audio, err := p.ResumeStep(context.Background(), "audio-cleanup", job)
		if err != nil {
			t.Fatal(err)
		}
		search, err = p.ResumeStep(context.Background(), "crf-search", job)
		if err != nil {
			t.Fatal(err)
		}
		return audio, search
	}

	p := checkpoint()
	if audio, search := resumes(p, base); !audio || !search {
		t.Fatalf("the same profile did not resume: audio %t, search %t", audio, search)
	}
	renamed := base
	renamed.Name = "Renamed flow"
	if audio, search := resumes(checkpoint(), renamed); !audio || !search {
		t.Fatalf("renaming the flow invalidated its results: audio %t, search %t", audio, search)
	}
	for name, change := range changes {
		profile := base
		change(&profile)
		p := checkpoint()
		if audio, search := resumes(p, profile); audio || search {
			t.Errorf("results survived a changed %s: audio %t, search %t", name, audio, search)
		}
		if p.current.Crop != nil || p.current.Audio != nil || p.current.Search != nil || p.current.Probe == nil {
			t.Errorf("%s: checkpoint after the guard = %+v", name, p.current)
		}
		if p.current.ResolvedProfileJSON != profileFingerprint(profile) {
			t.Errorf("%s: the checkpoint does not record the profile now in effect", name)
		}
	}
}

// A resumed probe hands back the probe and nothing else: the stream-cleanup
// decision a previous attempt made under its profile must not leak into this
// one, which is about to make its own.
func TestResumedProbeDoesNotRestoreDerivedMetadata(t *testing.T) {
	cached := domain.JobPipelineContext{
		Probe:    &domain.ProbeResult{FormatName: "matroska"},
		Metadata: domain.JobMetadata{StreamCleanupDisabled: true, StreamCleanupDisabledReason: "stale", CropFilter: "crop=1:1:0:0"},
	}
	p := &pipelineContextPersistence{cached: &cached, current: cached}
	job := &pipeline.JobContext{Metadata: domain.JobMetadata{OriginalLanguage: "eng"}}
	resumed, err := p.ResumeStep(context.Background(), "probe", job)
	if err != nil || !resumed || job.Probe == nil {
		t.Fatalf("probe resume = %t, %v", resumed, err)
	}
	if job.Metadata != (domain.JobMetadata{OriginalLanguage: "eng"}) {
		t.Fatalf("metadata after probe resume = %+v", job.Metadata)
	}
}
