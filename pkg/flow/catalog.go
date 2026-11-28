package flow

// Kind groups node types the way the canvas draws them; each kind owns one
// icon in the design system.
type Kind string

const (
	KindInput     Kind = "input"
	KindCondition Kind = "condition"
	KindVideo     Kind = "video"
	KindAudio     Kind = "audio"
	KindSubtitles Kind = "subtitles"
	KindContainer Kind = "container"
	KindOutput    Kind = "output"
)

// NodeType describes one kind of node well enough for the editor to list it
// in the palette and render its options form without knowing the type.
type NodeType struct {
	// ID is stable and stored in graphs, e.g. "video.encode".
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	// Group is the palette heading: "Input", "Inspect", "Video", "Audio",
	// "Subtitles", "Container", or "Output".
	Group       string `json:"group"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// Outputs labels the output ports in order. One unlabeled port is [""],
	// a condition is ["true", "false"], and a terminal node has none.
	Outputs  []string  `json:"outputs"`
	Sections []Section `json:"sections"`
}

// Section is one titled block of the options side sheet.
type Section struct {
	Title       string `json:"title"`
	Collapsible bool   `json:"collapsible,omitempty"`
	// Collapsed starts a collapsible section closed.
	Collapsed bool    `json:"collapsed,omitempty"`
	Fields    []Field `json:"fields"`
}

// FieldType selects the control that edits a field and the JSON type of its
// value in Node.Options.
type FieldType string

const (
	// FieldString is a JSON string edited in a text input.
	FieldString FieldType = "string"
	// FieldInt and FieldFloat are JSON numbers edited in a numeric input.
	FieldInt   FieldType = "int"
	FieldFloat FieldType = "float"
	// FieldBool is a JSON boolean edited with a switch.
	FieldBool FieldType = "bool"
	// FieldEnum is a JSON string, one of Choices, edited with a select or,
	// when Segmented is set, a segmented control.
	FieldEnum FieldType = "enum"
	// FieldEnumList is a JSON array of strings drawn from Choices.
	FieldEnumList FieldType = "enum_list"
	// FieldStringList is a JSON array of strings: free-form entries such as
	// extra ffmpeg arguments, or, with Field.Tokens, a set of simple tokens
	// such as language tags.
	FieldStringList FieldType = "string_list"
	// FieldDuration is a JSON string holding a Go duration such as "20s".
	FieldDuration FieldType = "duration"
	// FieldPath is a JSON string holding an absolute folder path, edited
	// with the daemon's folder picker. It is stored cleaned.
	FieldPath FieldType = "path"
)

// Field describes one option of a node type.
type Field struct {
	Key   string    `json:"key"`
	Label string    `json:"label"`
	Type  FieldType `json:"type"`
	// Hint is the one line shown under the control.
	Hint string `json:"hint,omitempty"`
	// Default is the value used when Node.Options has no entry for Key.
	Default any      `json:"default"`
	Choices []Choice `json:"choices,omitempty"`
	// Segmented renders an enum as a segmented control instead of a select.
	Segmented bool `json:"segmented,omitempty"`
	// Min and Max bound a number, both ends included unless MinExclusive
	// says otherwise.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// MinExclusive makes Min itself invalid: the value must be greater.
	MinExclusive bool `json:"min_exclusive,omitempty"`
	// Tokens marks a string_list whose entries are simple tokens, such as
	// language tags or codec names: none holds a comma or a space, order
	// does not matter, and a repeat means nothing. The editor may then end
	// an entry at a comma, split pasted text on commas and spaces, and drop
	// repeats. Without it entries are free-form and the list is kept as
	// typed: an entry ends at Enter only, pasted text splits on line
	// breaks only, and repeats stay, as ffmpeg arguments need.
	Tokens bool `json:"tokens,omitempty"`
	// Suffix is the unit shown inside the input, e.g. "kb/s".
	Suffix string `json:"suffix,omitempty"`
	// Mono renders the value in the monospace face: codec ids, globs,
	// arguments, numbers.
	Mono        bool   `json:"mono,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	// Half lets two short fields share a row.
	Half bool `json:"half,omitempty"`
	// VisibleWhen hides the field unless another field of the same node
	// currently has one of the listed values.
	VisibleWhen *Visibility `json:"visible_when,omitempty"`
	// Variants may depend on the effective values of earlier fields.
	Variants []FieldVariant `json:"variants,omitempty"`

	normalize    func(any) (any, bool)
	rangeContext string
}

// Choice is one allowed value of an enum field.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Visibility shows a field only while the field named Key holds one of In.
type Visibility struct {
	Key string `json:"key"`
	In  []any  `json:"in"`
}

// FieldVariant overrides a field when all its sibling conditions match.
// The first matching variant wins. Hidden affects presentation only.
type FieldVariant struct {
	When        []Visibility `json:"when"`
	Label       string       `json:"label,omitempty"`
	Hint        string       `json:"hint,omitempty"`
	Choices     []Choice     `json:"choices,omitzero"`
	Min         *float64     `json:"min,omitempty"`
	Max         *float64     `json:"max,omitempty"`
	Default     any          `json:"default,omitempty"`
	Placeholder string       `json:"placeholder,omitempty"`
	Hidden      bool         `json:"hidden,omitempty"`

	rangeContext string
}
