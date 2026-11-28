package flow

import (
	"encoding/json"
	"math"
	"math/rand"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

// garbage is everything an option value could wrongly be: the types JSON
// decodes to, the types Go code might write, and values at the edges.
func garbage() []any {
	return []any{
		nil, true, false, "", " ", "x", "av1", "fixed", "search", "20s", "-5m", "999999h", "orig", "\x00", "∞",
		0.0, 1.0, -1.0, 0.5, 1e300, -1e300, float64(1 << 60), math.NaN(), math.Inf(1), math.Inf(-1),
		0, -1, 7, math.MaxInt, math.MinInt, int64(math.MaxInt64), int32(3), uint(3), float32(1.5),
		[]any{}, []any{""}, []any{"a", 1}, []any{nil}, []any{"av1", "av1"}, []any{[]any{"x"}}, []any{"-crf", "1"}, []any{"-x"},
		[]string{}, []string{"", ""}, []string{"hevc"}, []string{"-map", "0"}, []string(nil), []int{1},
		map[string]any{}, map[string]any{"a": 1}, struct{}{}, &struct{}{}, json.Number("5"), json.RawMessage(`"x"`),
	}
}

// exercise runs everything the package offers on a graph. None of it may
// panic, whatever the graph holds, and the results have to agree.
func exercise(t *testing.T, g Graph) {
	t.Helper()
	issues := Validate(g)
	if issues == nil {
		t.Fatal("Validate() returned nil")
	}
	for _, issue := range issues {
		if issue.Message == "" || (issue.Severity != SeverityError && issue.Severity != SeverityWarning) {
			t.Fatalf("malformed issue %+v", issue)
		}
	}
	summaries := Summaries(g)
	for _, node := range g.Nodes {
		if summaries[node.ID] == "" {
			t.Fatalf("node %q has no summary", node.ID)
		}
	}

	facts := probeOf(videoStream("hevc", 1920, 1080), audioStream("dts", "eng", 6))
	resolution, evaluateErr := Evaluate(g, facts)
	data, canonicalErr := Canonical(g)
	valid := len(errorIssues(issues)) == 0
	if valid != (evaluateErr == nil) || valid != (canonicalErr == nil) {
		t.Fatalf("valid = %t, Evaluate error = %v, Canonical error = %v\nissues:\n%s", valid, evaluateErr, canonicalErr, printIssues(issues))
	}
	if !valid {
		return
	}
	if resolution.Outcome != OutcomeProcess && resolution.Outcome != OutcomeSkip {
		t.Fatalf("Outcome = %q", resolution.Outcome)
	}
	if len(resolution.Path) < 2 || len(resolution.Plan) == 0 {
		t.Fatalf("Path = %v, Plan = %v", resolution.Path, resolution.Plan)
	}
	if !json.Valid(data) {
		t.Fatalf("Canonical() is not JSON: %s", data)
	}
	// A graph that validates resolves to a profile the engine can run.
	if profile := resolution.Profile; profile.Video.SkipEncode && !profile.Crop.Disabled {
		t.Fatalf("a copied video is cropped: %+v", profile)
	}
}

func TestGarbageOptionValuesNeverPanic(t *testing.T) {
	for _, def := range nodeDefs() {
		for _, section := range def.Sections {
			for _, field := range section.Fields {
				for _, value := range garbage() {
					node := newNode("n", def.ID, map[string]any{field.Key: value})
					g := chain(newNode("in", typeInput, nil), node, newNode("out", typeOutputReplace, nil))
					if def.Kind == KindCondition {
						g.Edges = append(g.Edges, Edge{From: "n", Port: 1, To: "out"})
					}
					exercise(t, g)

					// A value of the wrong type is a field-level issue.
					_, message := decodeValue(field, value)
					decoded, problems := decodeOptions(def, node.Options)
					if message != "" && visible(field, decoded) {
						if len(problems) == 0 || problems[0].Field != field.Key {
							t.Errorf("%s.%s = %#v: problems = %+v, want one for the field", def.ID, field.Key, value, problems)
						}
					}
				}
			}
		}
	}
}

func TestRandomGarbageGraphsNeverPanic(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	values := garbage()
	defs := nodeDefs()
	types := []string{"", "nope", "input.", "VIDEO.ENCODE"}
	for _, def := range defs {
		types = append(types, def.ID)
	}
	ids := []string{"", "a", "b", "c", "d", "e", "f", "in", "out"}

	for range 3000 {
		var g Graph
		for range random.Intn(8) {
			node := Node{ID: ids[random.Intn(len(ids))], Type: types[random.Intn(len(types))], Label: []string{"", "L"}[random.Intn(2)]}
			if def, known := nodeDefsByID[node.Type]; known && random.Intn(3) > 0 {
				node.Options = map[string]any{}
				for _, section := range def.Sections {
					for _, field := range section.Fields {
						if random.Intn(3) == 0 {
							node.Options[field.Key] = values[random.Intn(len(values))]
						}
					}
				}
				if random.Intn(10) == 0 {
					node.Options["unexpected"] = values[random.Intn(len(values))]
				}
			}
			g.Nodes = append(g.Nodes, node)
		}
		for range random.Intn(10) {
			g.Edges = append(g.Edges, Edge{
				From: ids[random.Intn(len(ids))],
				Port: random.Intn(5) - 1,
				To:   ids[random.Intn(len(ids))],
			})
		}
		exercise(t, g)
	}
}

// TestRandomValidGraphsEvaluate builds graphs that are valid by construction
// and checks they pass validation and resolve for every kind of file.
func TestRandomValidGraphsEvaluate(t *testing.T) {
	random := rand.New(rand.NewSource(11))
	var middle []nodeDef
	for _, def := range nodeDefs() {
		if def.Kind != KindInput && def.Kind != KindOutput {
			middle = append(middle, def)
		}
	}
	files := []Facts{
		{},
		probeOf(),
		probeOf(videoStream("av1", 3840, 2160)),
		probeOf(videoStream("h264", 640, 360), audioStream("aac", "", 2)),
		probeOf(audioStream("truehd", "jpn", 8), audioStream("ac3", "eng", 6)),
		{Probe: domain.ProbeResult{Streams: []domain.MediaStream{{Type: "subtitle", Codec: "subrip"}}}, Metadata: domain.JobMetadata{OriginalLanguage: "ja"}},
	}

	for range 300 {
		// Layered: every edge leads to a later node, so there is no cycle,
		// and every port is connected.
		count := 1 + random.Intn(12)
		g := Graph{Nodes: []Node{newNode("n0", typeInput, nil)}}
		for i := 1; i <= count; i++ {
			g.Nodes = append(g.Nodes, newNode("n"+string(rune('a'+i)), middle[random.Intn(len(middle))].ID, nil))
		}
		g.Nodes = append(g.Nodes, newNode("publish", typeOutputReplace, nil), newNode("skip", typeOutputSkip, nil))
		for i, node := range g.Nodes[:count+1] {
			for port := range nodeDefsByID[node.Type].Outputs {
				target := i + 1 + random.Intn(len(g.Nodes)-i-1)
				g.Edges = append(g.Edges, Edge{From: node.ID, Port: port, To: g.Nodes[target].ID})
			}
		}
		if errs := errorIssues(Validate(g)); len(errs) != 0 {
			t.Fatalf("a graph valid by construction has errors:\n%s", printIssues(errs))
		}
		for _, facts := range files {
			resolution := mustEvaluate(t, viaJSON(t, g), facts)
			last := resolution.Path[len(resolution.Path)-1]
			if resolution.Path[0] != "n0" || (last != "publish" && last != "skip") {
				t.Fatalf("Path = %v", resolution.Path)
			}
			if (resolution.Outcome == OutcomeProcess) != (last == "publish") {
				t.Fatalf("Outcome = %q at %s", resolution.Outcome, last)
			}
			conditions := 0
			for _, id := range resolution.Path {
				for _, node := range g.Nodes {
					if node.ID == id && nodeDefsByID[node.Type].Kind == KindCondition {
						conditions++
					}
				}
			}
			if len(resolution.Decisions) != conditions {
				t.Fatalf("%d decisions for %d conditions on %v", len(resolution.Decisions), conditions, resolution.Path)
			}
			if resolution.Profile.Video.SkipEncode && !resolution.Profile.Crop.Disabled {
				t.Fatalf("a copied video is cropped on %v", resolution.Path)
			}
		}
		exercise(t, g)
	}
}

func FuzzGraphJSON(f *testing.F) {
	for _, g := range []Graph{Default(), canonicalSample(), {}} {
		data, err := json.Marshal(g)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(`{"nodes":[{"id":"a","type":"input"},{"id":"b","type":"check.hdr","options":{"kinds":[1,null]}}],"edges":[{"from":"a","port":0,"to":"b"},{"from":"b","port":1,"to":"a"}]}`))
	f.Add([]byte(`{"nodes":[{"id":"a","type":"video.encode","options":{"crf":1e400,"preset":{"a":[]},"ffmpeg_args":["-x"]}}],"edges":[{"from":"a","port":-9,"to":"a"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var g Graph
		if err := json.Unmarshal(data, &g); err != nil {
			return
		}
		exercise(t, g)
	})
}
