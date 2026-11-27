package flow

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"
)

// Canonical encodes what a graph means as stable JSON: positions and labels
// are left out, every option of every node is present with defaults filled
// in, and nodes, edges, and option keys are sorted. Two graphs that differ
// only in layout, labels, ordering, or in spelling out a default encode to
// the same bytes, which makes the result fit for comparing and fingerprinting
// flows. An option hidden by another option's value is not in effect and
// encodes as its default.
//
// It returns an error when the graph has error issues; see Validate.
func Canonical(g Graph) ([]byte, error) {
	a := analyze(g)
	if err := a.err(); err != nil {
		return nil, err
	}

	type node struct {
		ID      string         `json:"id"`
		Type    string         `json:"type"`
		Options map[string]any `json:"options"`
	}
	canonical := struct {
		Nodes []node `json:"nodes"`
		Edges []Edge `json:"edges"`
	}{
		Nodes: make([]node, 0, len(g.Nodes)),
		Edges: append([]Edge{}, g.Edges...),
	}
	for _, source := range g.Nodes {
		// encoding/json writes map keys in sorted order.
		values := make(map[string]any, len(a.options[source.ID]))
		for key, value := range a.options[source.ID] {
			if duration, ok := value.(time.Duration); ok {
				value = duration.String()
			}
			values[key] = value
		}
		canonical.Nodes = append(canonical.Nodes, node{ID: source.ID, Type: source.Type, Options: values})
	}
	slices.SortFunc(canonical.Nodes, func(left, right node) int { return cmp.Compare(left.ID, right.ID) })
	slices.SortFunc(canonical.Edges, func(left, right Edge) int {
		return cmp.Or(
			cmp.Compare(left.From, right.From),
			cmp.Compare(left.Port, right.Port),
			cmp.Compare(left.To, right.To),
		)
	})
	return json.Marshal(canonical)
}
