package flow

import (
	"encoding/json"
	"testing"

	"github.com/zekurio/anvil/pkg/domain"
)

func newNode(id, nodeType string, values map[string]any) Node {
	return Node{ID: id, Type: nodeType, Options: values}
}

// chain connects the nodes in order through output port 0.
func chain(nodes ...Node) Graph {
	g := Graph{Nodes: nodes}
	for i := 1; i < len(nodes); i++ {
		g.Edges = append(g.Edges, Edge{From: nodes[i-1].ID, To: nodes[i].ID})
	}
	return g
}

// branching sends a file through one condition: true publishes, false skips.
func branching(condition Node) Graph {
	return Graph{
		Nodes: []Node{
			newNode("in", typeInput, nil),
			condition,
			newNode("publish", typeOutputReplace, nil),
			newNode("skip", typeOutputSkip, nil),
		},
		Edges: []Edge{
			{From: "in", To: condition.ID},
			{From: condition.ID, Port: 0, To: "publish"},
			{From: condition.ID, Port: 1, To: "skip"},
		},
	}
}

// viaJSON returns the graph as the API delivers it: numbers are float64 and
// lists are []any.
func viaJSON(t *testing.T, g Graph) Graph {
	t.Helper()
	data, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Graph
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func mustEvaluate(t *testing.T, g Graph, facts Facts) Resolution {
	t.Helper()
	resolution, err := Evaluate(g, facts)
	if err != nil {
		t.Fatalf("Evaluate: %v\nissues: %+v", err, Validate(g))
	}
	return resolution
}

func errorIssues(issues []Issue) []Issue {
	var errs []Issue
	for _, issue := range issues {
		if issue.Severity == SeverityError {
			errs = append(errs, issue)
		}
	}
	return errs
}

func videoStream(codec string, width, height int) domain.MediaStream {
	return domain.MediaStream{Index: 0, Type: "video", Codec: codec, Width: width, Height: height}
}

func audioStream(codec, language string, channels int) domain.MediaStream {
	return domain.MediaStream{Type: "audio", Codec: codec, Language: language, Channels: channels}
}

func probeOf(streams ...domain.MediaStream) Facts {
	return Facts{Probe: domain.ProbeResult{
		Path:            "/media/movie.mkv",
		DurationSeconds: 7200,
		SizeBytes:       8 << 30,
		Streams:         streams,
	}}
}
