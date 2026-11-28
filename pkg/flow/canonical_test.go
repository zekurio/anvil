package flow

import (
	"bytes"
	"cmp"
	"encoding/json"
	"math/rand"
	"slices"
	"testing"
)

func canonical(t *testing.T, g Graph) string {
	t.Helper()
	data, err := Canonical(g)
	if err != nil {
		t.Fatalf("Canonical: %v\nissues: %s", err, printIssues(Validate(g)))
	}
	return string(data)
}

func canonicalSample() Graph {
	return Graph{
		Nodes: []Node{
			{ID: "in", Type: typeInput, X: 288, Y: 32},
			{ID: "check", Type: typeCheckHDR, X: 288, Y: 144, Options: map[string]any{"kinds": []any{"hlg", "hdr10"}}},
			{ID: "copy", Type: typeVideoCopy, X: 64, Y: 256},
			{ID: "encode", Type: typeVideoEncode, X: 512, Y: 256, Options: map[string]any{"preset": "4", "target": 93.5, "ffmpeg_args": []any{"-g", "240"}}},
			{ID: "audio", Type: typeAudioKeep, X: 288, Y: 368, Options: map[string]any{"languages": []any{"orig", "eng"}}},
			{ID: "out", Type: typeOutputReplace, X: 288, Y: 480},
		},
		Edges: []Edge{
			{From: "in", To: "check"},
			{From: "check", Port: 0, To: "copy"},
			{From: "check", Port: 1, To: "encode"},
			{From: "copy", To: "audio"},
			{From: "encode", To: "audio"},
			{From: "audio", To: "out"},
		},
	}
}

func TestCanonicalForm(t *testing.T) {
	g := chain(
		Node{ID: "in", Type: typeInput, Label: "Start", X: 16, Y: 16},
		Node{ID: "check", Type: typeCheckBitrate, Options: map[string]any{"kbps": 4000.0}},
		Node{ID: "out", Type: typeOutputReplace},
	)
	g.Edges = append(g.Edges, Edge{From: "check", Port: 1, To: "out"})
	want := `{"nodes":[` +
		`{"id":"check","type":"check.bitrate","options":{"kbps":4000,"op":"above"}},` +
		`{"id":"in","type":"input","options":{}},` +
		`{"id":"out","type":"output.replace","options":{}}],` +
		`"edges":[{"from":"check","port":0,"to":"out"},{"from":"check","port":1,"to":"out"},{"from":"in","port":0,"to":"check"}]}`
	if got := canonical(t, g); got != want {
		t.Errorf("Canonical() =\n%s\nwant\n%s", got, want)
	}
}

func TestCanonicalIgnoresLayoutLabelsAndOrder(t *testing.T) {
	base := canonicalSample()
	want := canonical(t, base)

	moved := canonicalSample()
	for i := range moved.Nodes {
		moved.Nodes[i].X += 160
		moved.Nodes[i].Y -= 16
		moved.Nodes[i].Label = "Renamed " + moved.Nodes[i].ID
	}
	if got := canonical(t, moved); got != want {
		t.Errorf("moving and relabeling changed the canonical form:\n%s\nwant\n%s", got, want)
	}

	random := rand.New(rand.NewSource(1))
	for range 20 {
		shuffled := canonicalSample()
		random.Shuffle(len(shuffled.Nodes), func(i, j int) {
			shuffled.Nodes[i], shuffled.Nodes[j] = shuffled.Nodes[j], shuffled.Nodes[i]
		})
		random.Shuffle(len(shuffled.Edges), func(i, j int) {
			shuffled.Edges[i], shuffled.Edges[j] = shuffled.Edges[j], shuffled.Edges[i]
		})
		if got := canonical(t, shuffled); got != want {
			t.Fatalf("reordering nodes and edges changed the canonical form:\n%s\nwant\n%s", got, want)
		}
	}

	// Option keys in another order, as a different client might send them.
	var reordered Graph
	source := `{"nodes":[
		{"id":"in","type":"input","x":0,"y":0},
		{"id":"check","type":"check.hdr","x":0,"y":0,"options":{"kinds":["hdr10","hlg","hlg"]}},
		{"id":"copy","type":"video.copy","x":0,"y":0},
		{"id":"encode","type":"video.encode","x":0,"y":0,"options":{"ffmpeg_args":["-g","240"],"target":93.5,"preset":"4"}},
		{"id":"audio","type":"audio.keep","x":0,"y":0,"options":{"languages":["orig","eng"]}},
		{"id":"out","type":"output.replace","x":0,"y":0}],
		"edges":[
		{"from":"in","port":0,"to":"check"},{"from":"check","port":0,"to":"copy"},{"from":"check","port":1,"to":"encode"},
		{"from":"copy","port":0,"to":"audio"},{"from":"encode","port":0,"to":"audio"},{"from":"audio","port":0,"to":"out"}]}`
	if err := json.Unmarshal([]byte(source), &reordered); err != nil {
		t.Fatal(err)
	}
	if got := canonical(t, reordered); got != want {
		t.Errorf("reordering option keys and ticking boxes in another order changed the canonical form:\n%s\nwant\n%s", got, want)
	}

	if got := canonical(t, viaJSON(t, base)); got != want {
		t.Errorf("a JSON round trip changed the canonical form:\n%s\nwant\n%s", got, want)
	}
}

func TestCanonicalFillsInDefaults(t *testing.T) {
	want := canonical(t, canonicalSample())

	explicit := canonicalSample()
	for i, node := range explicit.Nodes {
		def := nodeDefsByID[node.Type]
		values := map[string]any{}
		for _, section := range def.Sections {
			for _, field := range section.Fields {
				values[field.Key] = field.Default
			}
		}
		for key, value := range node.Options {
			values[key] = value
		}
		explicit.Nodes[i].Options = values
	}
	if got := canonical(t, viaJSON(t, explicit)); got != want {
		t.Errorf("spelling out every default changed the canonical form:\n%s\nwant\n%s", got, want)
	}

	// The same duration written another way is the same option.
	respelled := canonicalSample()
	respelled.Nodes[3].Options["sample_duration"] = "0m20s"
	if got := canonical(t, respelled); got != want {
		t.Errorf("respelling a default duration changed the canonical form:\n%s\nwant\n%s", got, want)
	}

	// An option that is hidden is not in effect, whatever it holds.
	hidden := canonicalSample()
	hidden.Nodes[3].Options["crf"] = 12
	if got := canonical(t, hidden); got != want {
		t.Errorf("a hidden option changed the canonical form:\n%s\nwant\n%s", got, want)
	}
}

func TestCanonicalChangesWithMeaning(t *testing.T) {
	base := canonical(t, canonicalSample())
	changes := map[string]func(g *Graph){
		"an option":       func(g *Graph) { g.Nodes[3].Options["preset"] = "5" },
		"a list's order":  func(g *Graph) { g.Nodes[4].Options["languages"] = []any{"eng", "orig"} },
		"a list's length": func(g *Graph) { g.Nodes[4].Options["languages"] = []any{"orig"} },
		"a default":       func(g *Graph) { g.Nodes[4].Options["keep_commentary"] = true },
		"a visible mode":  func(g *Graph) { g.Nodes[3].Options["quality_mode"] = "fixed" },
		"a node type":     func(g *Graph) { g.Nodes[5].Type = typeOutputSkip },
		"an edge": func(g *Graph) {
			g.Edges[1].To, g.Edges[2].To = "encode", "copy"
		},
		"an extra node": func(g *Graph) { g.Nodes = append(g.Nodes, Node{ID: "spare", Type: typeOutputSkip}) },
	}
	seen := map[string]string{"": base}
	for name, change := range changes {
		g := canonicalSample()
		change(&g)
		got := canonical(t, g)
		for other, form := range seen {
			if got == form {
				t.Errorf("changing %s gives the same canonical form as %q", name, other)
			}
		}
		seen[name] = got
	}
}

func TestCanonicalIsValidJSONWithSortedContent(t *testing.T) {
	data, err := Canonical(viaJSON(t, FromProfile(profileWithOverrides(t))))
	if err != nil {
		t.Fatal(err)
	}
	type node struct {
		ID      string         `json:"id"`
		Type    string         `json:"type"`
		Options map[string]any `json:"options"`
	}
	var decoded struct {
		Nodes []node `json:"nodes"`
		Edges []Edge `json:"edges"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if !slices.IsSortedFunc(decoded.Nodes, func(a, b node) int { return cmp.Compare(a.ID, b.ID) }) {
		t.Errorf("nodes are not sorted: %s", data)
	}
	if !slices.IsSortedFunc(decoded.Edges, func(a, b Edge) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.Port, b.Port), cmp.Compare(a.To, b.To))
	}) {
		t.Errorf("edges are not sorted: %s", data)
	}
	if len(decoded.Edges) < 8 {
		t.Errorf("expected the branching sample, got %d edges", len(decoded.Edges))
	}
	for _, node := range decoded.Nodes {
		fields := 0
		for _, section := range nodeDefsByID[node.Type].Sections {
			fields += len(section.Fields)
		}
		if node.Options == nil || len(node.Options) != fields {
			t.Errorf("%s has %d options in canonical form, want all %d", node.ID, len(node.Options), fields)
		}
	}
}
