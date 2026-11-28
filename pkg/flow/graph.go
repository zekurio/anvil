package flow

// Graph is a flow as the editor draws it and the store persists it. Node
// positions are part of the graph but not of its meaning: two graphs that
// differ only in X, Y, or Label evaluate identically.
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Node is one step of a flow.
type Node struct {
	// ID is unique within the graph and chosen by the editor.
	ID string `json:"id"`
	// Type is a catalog NodeType ID such as "video.encode".
	Type string `json:"type"`
	// Label overrides the catalog label on the canvas; empty uses it.
	Label string `json:"label,omitempty"`
	// X and Y are the canvas position in px, snapped to the 16px grid.
	X int `json:"x"`
	Y int `json:"y"`
	// Options holds the node's settings keyed by Field.Key. A missing key
	// means the field's default.
	Options map[string]any `json:"options,omitempty"`
}

// Edge connects one output port of a node to the single input of another.
type Edge struct {
	From string `json:"from"`
	// Port indexes the source node type's Outputs.
	Port int    `json:"port"`
	To   string `json:"to"`
}

// Severity grades a validation issue. Errors block saving and assignment;
// warnings do not.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Issue is one problem found while validating a graph. NodeID is empty for
// problems of the graph as a whole, and Field is empty when the problem is
// not about one option.
type Issue struct {
	Severity Severity `json:"severity"`
	NodeID   string   `json:"node_id,omitempty"`
	Field    string   `json:"field,omitempty"`
	Message  string   `json:"message"`
}

// Outcome is what evaluating a flow for one file decided.
type Outcome string

const (
	// OutcomeProcess runs the pipeline with the resolved profile and
	// publishes the result.
	OutcomeProcess Outcome = "process"
	// OutcomeSkip leaves the file untouched and marks the job skipped.
	OutcomeSkip Outcome = "skip"
)
