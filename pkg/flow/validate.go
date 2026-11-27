package flow

import (
	"fmt"
	"strconv"
	"strings"
)

// analysis is a graph read once: its issues, and the structure Validate,
// Evaluate, Summaries, and Canonical all need.
type analysis struct {
	issues []Issue
	// optionWarnings are held back until the errors are listed.
	optionWarnings []Issue
	// nodes holds the first node of every non-empty ID.
	nodes map[string]Node
	// options holds the decoded options of every node of a known type.
	options map[string]options
	// targets maps a node to the node each of its output ports leads to; ""
	// marks an unconnected port. A node of an unknown type has no ports to
	// index, so it lists the targets of its edges instead.
	targets map[string][]string
	// input is the ID of the input node, or "" when the graph has none.
	input string
}

// Validate reports what is wrong with a graph. The order depends only on the
// graph: nodes and their options as the graph lists them, then edges, loops,
// and unconnected outputs, and warnings last. A graph can be saved and
// evaluated when no issue has error severity.
func Validate(g Graph) []Issue {
	issues := analyze(g).issues
	if issues == nil {
		return []Issue{}
	}
	return issues
}

// Summaries returns the one-line summary every node shows on the canvas,
// keyed by node ID. An option that fails validation reads as its default.
func Summaries(g Graph) map[string]string {
	summaries := make(map[string]string, len(g.Nodes))
	for _, node := range g.Nodes {
		def, known := nodeDefsByID[node.Type]
		if !known {
			summaries[node.ID] = "unknown node type"
			continue
		}
		decoded, _ := decodeOptions(def, node.Options)
		summaries[node.ID] = def.summary(decoded)
	}
	return summaries
}

func analyze(g Graph) analysis {
	a := analysis{
		nodes:   make(map[string]Node, len(g.Nodes)),
		options: make(map[string]options, len(g.Nodes)),
		targets: make(map[string][]string, len(g.Nodes)),
	}
	a.readNodes(g)
	a.readEdges(g)
	a.findCycles(g)
	reached := a.findDanglingPorts()
	a.issues = append(a.issues, a.optionWarnings...)
	if a.input != "" {
		for _, node := range g.Nodes {
			if _, unique := a.nodes[node.ID]; unique && !reached[node.ID] {
				a.warnf(node.ID, "%s is not connected to the input, so no file reaches it.", a.name(node.ID))
				// A duplicate ID is reported once, for its first node.
				reached[node.ID] = true
			}
		}
	}
	if a.err() == nil {
		a.findUselessCrops(g)
	}
	return a
}

func (a *analysis) readNodes(g Graph) {
	var inputs []string
	for _, node := range g.Nodes {
		if node.ID == "" {
			a.errorf("", "", "Every node needs an ID.")
			continue
		}
		if _, duplicate := a.nodes[node.ID]; duplicate {
			a.errorf(node.ID, "", "Node ID %q is used more than once. Every node needs its own ID.", node.ID)
			continue
		}
		a.nodes[node.ID] = node
		def, known := nodeDefsByID[node.Type]
		if !known {
			a.errorf(node.ID, "", "%q is not a node type Anvil knows. Replace this node.", node.Type)
			continue
		}
		decoded, problems, warnings := decodeOptionsWithWarnings(def, node.Options)
		a.options[node.ID] = decoded
		a.targets[node.ID] = make([]string, len(def.Outputs))
		for _, problem := range problems {
			a.errorf(node.ID, problem.Field, "%s", problem.Message)
		}
		for _, warning := range warnings {
			a.optionWarnings = append(a.optionWarnings, Issue{
				Severity: SeverityWarning, NodeID: node.ID, Field: warning.Field, Message: warning.Message,
			})
		}
		if node.Type == typeInput {
			inputs = append(inputs, node.ID)
		}
	}
	if len(inputs) == 0 {
		a.errorf("", "", "A flow needs an input node. Add one.")
		return
	}
	// With several inputs the first still anchors the reachability checks,
	// so the rest of the report stays useful.
	a.input = inputs[0]
	for _, extra := range inputs[1:] {
		a.errorf(extra, "", "A flow has exactly one input node. Remove this one.")
	}
}

func (a *analysis) readEdges(g Graph) {
	for _, edge := range g.Edges {
		if _, exists := a.nodes[edge.From]; !exists {
			a.errorf("", "", "An edge starts at %q, which is not a node of this flow. Remove the edge.", edge.From)
			continue
		}
		target, exists := a.nodes[edge.To]
		if !exists {
			a.errorf(edge.From, "", "An edge from %s leads to %q, which is not a node of this flow. Remove the edge.", a.name(edge.From), edge.To)
			continue
		}
		if _, known := nodeDefsByID[a.nodes[edge.From].Type]; !known {
			// The unknown type is already reported and its ports cannot be
			// checked. Its edges still count, so that the nodes behind it are
			// not reported as cut off from the input.
			a.targets[edge.From] = append(a.targets[edge.From], edge.To)
			continue
		}
		ports := a.targets[edge.From]
		switch {
		case len(ports) == 0:
			a.errorf(edge.From, "", "%s ends the flow and has no outputs. Remove the edge that leaves it.", a.name(edge.From))
		case edge.Port < 0 || edge.Port >= len(ports):
			a.errorf(edge.From, "", "%s has outputs 0 to %d, and an edge uses output %d. Remove the edge.", a.name(edge.From), len(ports)-1, edge.Port)
		case target.Type == typeInput:
			a.errorf(edge.To, "", "The input node starts the flow, so no edge can lead into it. Remove the edge from %s.", a.name(edge.From))
		case ports[edge.Port] != "":
			a.errorf(edge.From, "", "%s has more than one edge. An output leads to exactly one node.", a.port(edge.From, edge.Port))
		default:
			ports[edge.Port] = edge.To
		}
	}
}

// findCycles reports every node an edge loops back to. A file has to reach
// an output node, which a loop would keep it from.
func (a *analysis) findCycles(g Graph) {
	const (
		visiting = 1
		done     = 2
	)
	state := make(map[string]int, len(a.nodes))
	reported := make(map[string]bool)
	var visit func(id string)
	visit = func(id string) {
		state[id] = visiting
		for _, target := range a.targets[id] {
			switch {
			case target == "":
			case state[target] == visiting && !reported[target]:
				reported[target] = true
				a.errorf(target, "", "The flow loops back to %s. Every path has to end at an output without repeating a node.", a.name(target))
			case state[target] == 0:
				visit(target)
			}
		}
		state[id] = done
	}
	for _, node := range g.Nodes {
		if _, exists := a.nodes[node.ID]; exists && state[node.ID] == 0 {
			visit(node.ID)
		}
	}
}

// findDanglingPorts walks the graph from the input and reports every output
// port on the way that leads nowhere: a file taking it would fall off the
// flow. It returns the nodes it reached.
func (a *analysis) findDanglingPorts() map[string]bool {
	reached := make(map[string]bool, len(a.nodes))
	if a.input == "" {
		return reached
	}
	reached[a.input] = true
	queue := []string{a.input}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for port, target := range a.targets[id] {
			switch {
			case target == "":
				a.errorf(id, "", "%s is not connected.", a.port(id, port))
			case !reached[target]:
				reached[target] = true
				queue = append(queue, target)
			}
		}
	}
	return reached
}

// findUselessCrops warns about crop nodes on a path that ends up copying the
// video: cropping needs a re-encode, so such a path crops nothing. It walks
// every combination of (node, video decision, last crop node) once, which
// stays small even when branches multiply the number of paths.
func (a *analysis) findUselessCrops(g Graph) {
	type state struct {
		node    string
		encodes bool
		crop    string
	}
	useless := make(map[string]bool)
	seen := make(map[state]bool)
	stack := []state{{node: a.input}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[current] {
			continue
		}
		seen[current] = true
		switch a.nodes[current.node].Type {
		case typeVideoEncode:
			current.encodes = true
		case typeVideoCopy:
			current.encodes = false
		case typeVideoCrop:
			current.crop = current.node
		case typeOutputPublish:
			if !current.encodes && current.crop != "" {
				useless[current.crop] = true
			}
		}
		for _, target := range a.targets[current.node] {
			stack = append(stack, state{node: target, encodes: current.encodes, crop: current.crop})
		}
	}
	for _, node := range g.Nodes {
		if useless[node.ID] {
			useless[node.ID] = false
			a.warnf(node.ID, "Cropping needs a re-encode, but a path through %s copies the video, so nothing is cropped there.", a.name(node.ID))
		}
	}
}

func (a *analysis) errorf(nodeID, field, format string, args ...any) {
	a.issues = append(a.issues, Issue{
		Severity: SeverityError,
		NodeID:   nodeID,
		Field:    field,
		Message:  fmt.Sprintf(format, args...),
	})
}

func (a *analysis) warnf(nodeID, format string, args ...any) {
	a.issues = append(a.issues, Issue{
		Severity: SeverityWarning,
		NodeID:   nodeID,
		Message:  fmt.Sprintf(format, args...),
	})
}

// err summarizes the error issues, or returns nil when the graph can be
// evaluated.
func (a *analysis) err() error {
	count := 0
	first := ""
	for _, issue := range a.issues {
		if issue.Severity != SeverityError {
			continue
		}
		if count == 0 {
			first = issue.Message
		}
		count++
	}
	switch count {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("flow is not valid: %s", strings.TrimSuffix(first, "."))
	default:
		return fmt.Errorf("flow is not valid: %s (%d problems in total)", strings.TrimSuffix(first, "."), count)
	}
}

// label is what the canvas calls a node: its own label, or its type's.
func (a *analysis) label(id string) string {
	node := a.nodes[id]
	if node.Label != "" {
		return node.Label
	}
	if def, known := nodeDefsByID[node.Type]; known {
		return def.Label
	}
	return id
}

// name is the node's label in quotes, for use inside a message.
func (a *analysis) name(id string) string {
	return strconv.Quote(a.label(id))
}

// port names one output of a node for a message: `"false" output of "Video
// is hevc?"`, or `Output of "Encode video"` for a node with a single,
// unlabeled port.
func (a *analysis) port(id string, port int) string {
	label := nodeDefsByID[a.nodes[id].Type].Outputs[port]
	if label == "" {
		return "Output of " + a.name(id)
	}
	return strconv.Quote(label) + " output of " + a.name(id)
}
