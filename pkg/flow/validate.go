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

// MaxNodes and MaxEdges bound the size of a graph. A flow an operator draws
// has a few dozen nodes; the bounds are there so that a graph sent to the web
// API cannot make validation, which the editor asks for on every change,
// arbitrarily expensive. A node has at most a handful of outputs and every
// output takes one edge, so a graph within MaxNodes that needs more than
// MaxEdges does not exist.
const (
	MaxNodes = 500
	MaxEdges = 2000
)

func analyze(g Graph) analysis {
	// A graph over the bounds is not read at all: its only issue is its size.
	switch {
	case len(g.Nodes) > MaxNodes:
		a := analysis{}
		a.errorf("", "", "A flow can have at most %d nodes, and this one has %d.", MaxNodes, len(g.Nodes))
		return a
	case len(g.Edges) > MaxEdges:
		a := analysis{}
		a.errorf("", "", "A flow can have at most %d edges, and this one has %d.", MaxEdges, len(g.Edges))
		return a
	}
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
			a.errorf(node.ID, "", "Node ID %q is used more than once.", node.ID)
			continue
		}
		a.nodes[node.ID] = node
		def, known := nodeDefsByID[node.Type]
		if !known {
			a.errorf(node.ID, "", "%q is not a node type anvil knows. Replace this node.", node.Type)
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
			a.errorf(edge.To, "", "No edge can lead into the input node. Remove the edge from %s.", a.name(edge.From))
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
				a.errorf(target, "", "The flow loops back to %s. Every path must end at an output.", a.name(target))
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
// video: cropping needs a re-encode, so such a path crops nothing. The last
// crop node on a path is the one that counts.
//
// It only runs on a graph without errors, which has no loops. The work is
// linear in the size of the graph: one pass forward finds the video decisions
// a file can arrive at each node with, and one memoized pass backward finds
// from where a copy can still be published without meeting another crop node.
// Tracking the last crop node along every path instead costs the number of
// crop nodes times the size of the graph.
func (a *analysis) findUselessCrops(g Graph) {
	type state struct {
		node    string
		encodes bool
	}
	// after is the video decision once a node has been passed.
	after := func(s state) bool {
		switch a.nodes[s.node].Type {
		case typeVideoEncode:
			return true
		case typeVideoCopy:
			return false
		}
		return s.encodes
	}
	arrives := map[state]bool{{node: a.input}: true}
	stack := []state{{node: a.input}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, target := range a.targets[current.node] {
			next := state{node: target, encodes: after(current)}
			if !arrives[next] {
				arrives[next] = true
				stack = append(stack, next)
			}
		}
	}
	// publishesCopy reports whether a file arriving at a node with a video
	// decision can be published as a copy before another crop node takes
	// over. The recursion is as deep as the longest path, which maxNodes
	// bounds.
	known := make(map[state]bool)
	var publishesCopy func(s state) bool
	publishesCopy = func(s state) bool {
		if result, done := known[s]; done {
			return result
		}
		result := false
		switch a.nodes[s.node].Type {
		case typeVideoCrop:
		case typeOutputPublish:
			result = !s.encodes
		default:
			for _, target := range a.targets[s.node] {
				if publishesCopy(state{node: target, encodes: after(s)}) {
					result = true
					break
				}
			}
		}
		known[s] = result
		return result
	}
	reported := make(map[string]bool)
	for _, node := range g.Nodes {
		if a.nodes[node.ID].Type != typeVideoCrop || reported[node.ID] {
			continue
		}
		for _, encodes := range []bool{false, true} {
			if !arrives[state{node: node.ID, encodes: encodes}] || reported[node.ID] {
				continue
			}
			for _, target := range a.targets[node.ID] {
				if publishesCopy(state{node: target, encodes: encodes}) {
					reported[node.ID] = true
					a.warnf(node.ID, "A path through %s copies the video, so nothing is cropped there.", a.name(node.ID))
					break
				}
			}
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
