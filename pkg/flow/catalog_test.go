package flow

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCatalogListsEveryNodeTypeInPaletteOrder(t *testing.T) {
	want := []struct {
		id      string
		kind    Kind
		group   string
		label   string
		outputs []string
	}{
		{"input", KindInput, "Input", "Input file", []string{""}},
		{"check.video_codec", KindCondition, "Inspect", "Check video codec", []string{"true", "false"}},
		{"check.resolution", KindCondition, "Inspect", "Check resolution", []string{"true", "false"}},
		{"check.hdr", KindCondition, "Inspect", "Check HDR", []string{"true", "false"}},
		{"check.bitrate", KindCondition, "Inspect", "Check bitrate", []string{"true", "false"}},
		{"check.file_size", KindCondition, "Inspect", "Check file size", []string{"true", "false"}},
		{"check.audio_language", KindCondition, "Inspect", "Check audio language", []string{"true", "false"}},
		{"check.audio_codec", KindCondition, "Inspect", "Check audio codec", []string{"true", "false"}},
		{"check.audio_channels", KindCondition, "Inspect", "Check audio channels", []string{"true", "false"}},
		{"video.encode", KindVideo, "Video", "Encode video", []string{""}},
		{"video.copy", KindVideo, "Video", "Copy video", []string{""}},
		{"video.crop", KindVideo, "Video", "Crop black bars", []string{""}},
		{"audio.keep", KindAudio, "Audio", "Keep audio tracks", []string{""}},
		{"audio.encode", KindAudio, "Audio", "Encode audio", []string{""}},
		{"audio.downmix", KindAudio, "Audio", "Downmix audio", []string{""}},
		{"subtitles.keep", KindSubtitles, "Subtitles", "Keep subtitles", []string{""}},
		{"subtitles.drop_bitmap", KindSubtitles, "Subtitles", "Remove image subtitles", []string{""}},
		{"subtitles.remove_all", KindSubtitles, "Subtitles", "Remove all subtitles", []string{""}},
		{"container.metadata", KindContainer, "Container", "Clean metadata", []string{""}},
		{"output.replace", KindOutput, "Output", "Replace file", []string{}},
		{"output.move", KindOutput, "Output", "Move file", []string{}},
		{"output.skip", KindOutput, "Output", "Skip file", []string{}},
	}
	catalog := Catalog()
	if len(catalog) != len(want) {
		t.Fatalf("Catalog() has %d node types, want %d", len(catalog), len(want))
	}
	for i, nodeType := range catalog {
		w := want[i]
		if nodeType.ID != w.id || nodeType.Kind != w.kind || nodeType.Group != w.group || nodeType.Label != w.label {
			t.Errorf("Catalog()[%d] = %s/%s/%s/%s, want %s/%s/%s/%s",
				i, nodeType.ID, nodeType.Kind, nodeType.Group, nodeType.Label, w.id, w.kind, w.group, w.label)
		}
		if nodeType.Outputs == nil || !slices.Equal(nodeType.Outputs, w.outputs) {
			t.Errorf("%s outputs = %#v, want %#v", nodeType.ID, nodeType.Outputs, w.outputs)
		}
		if nodeType.Description == "" {
			t.Errorf("%s has no description", nodeType.ID)
		}
		if nodeType.Sections == nil {
			t.Errorf("%s sections are nil, want an empty list", nodeType.ID)
		}
	}
}

func TestCatalogIsAFreshCopy(t *testing.T) {
	first := Catalog()
	first[0].Label = "changed"
	encode := first[9]
	encode.Sections[0].Fields[0].Default = "changed"
	encode.Sections[0].Fields[0].Choices[0].Value = "changed"
	encode.Sections[1].Fields[1].VisibleWhen.Key = "changed"

	second := Catalog()
	if second[0].Label != "Input file" {
		t.Errorf("label leaked: %q", second[0].Label)
	}
	field := second[9].Sections[0].Fields[0]
	if field.Default != "av1" || field.Choices[0].Value != "av1" {
		t.Errorf("field leaked: %+v", field)
	}
	if key := second[9].Sections[1].Fields[1].VisibleWhen.Key; key != "quality_mode" {
		t.Errorf("visibility leaked: %q", key)
	}
	if def := nodeDefsByID[typeVideoEncode]; def.Sections[0].Fields[0].Default != "av1" {
		t.Errorf("registry changed: %+v", def.Sections[0].Fields[0])
	}
}

func TestCatalogEncodesWithoutNulls(t *testing.T) {
	data, err := json.Marshal(Catalog())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "null") {
		t.Errorf("catalog JSON contains null: %s", data)
	}
}

func TestCatalogFieldsAreSelfConsistent(t *testing.T) {
	seenTypes := map[string]bool{}
	for _, def := range nodeDefs() {
		if def.ID == "" || seenTypes[def.ID] {
			t.Errorf("node type ID %q is empty or repeated", def.ID)
		}
		seenTypes[def.ID] = true

		behaviors := 0
		if def.test != nil {
			behaviors++
		}
		if def.apply != nil {
			behaviors++
		}
		if def.outcome != "" {
			behaviors++
		}
		wantBehaviors := 1
		switch {
		case def.ID == typeInput:
			wantBehaviors = 0
		case def.outcome == OutcomeProcess:
			// A publishing output also writes Profile.Publish.
			wantBehaviors = 2
		}
		if behaviors != wantBehaviors {
			t.Errorf("%s has %d behaviors, want %d", def.ID, behaviors, wantBehaviors)
		}
		if (def.test != nil) != (def.Kind == KindCondition) || (def.outcome != "") != (def.Kind == KindOutput) {
			t.Errorf("%s behavior does not match kind %s", def.ID, def.Kind)
		}

		fields := map[string]Field{}
		for i, section := range def.Sections {
			if section.Title == "" || len(section.Fields) == 0 {
				t.Errorf("%s section %d needs a title and fields", def.ID, i)
			}
			if section.Title == "Advanced" && (!section.Collapsible || !section.Collapsed || i != len(def.Sections)-1) {
				t.Errorf("%s Advanced section must be last, collapsible, and collapsed", def.ID)
			}
			if section.Collapsed && !section.Collapsible {
				t.Errorf("%s section %q is collapsed but not collapsible", def.ID, section.Title)
			}
			for _, field := range section.Fields {
				name := def.ID + "." + field.Key
				if field.Key == "" || field.Label == "" {
					t.Errorf("%s needs a key and a label", name)
				}
				if _, repeated := fields[field.Key]; repeated {
					t.Errorf("%s is defined twice", name)
				}
				checkFieldDefault(t, name, field)
				checkFieldShape(t, name, field)
				checkFieldVisibility(t, name, field, fields)
				fields[field.Key] = field
			}
		}

		// A node is valid at its defaults, so dropping it on the canvas never
		// starts with an error, except where no default can be right: only
		// the operator knows where a move goes.
		decoded, problems := decodeOptions(def, nil)
		wantProblems := 0
		if def.ID == typeOutputMove {
			wantProblems = 1
		}
		if len(problems) != wantProblems {
			t.Errorf("%s defaults have problems: %+v", def.ID, problems)
		}
		if len(decoded) != len(fields) {
			t.Errorf("%s decodes %d options, want %d", def.ID, len(decoded), len(fields))
		}
		if def.summary == nil || def.summary(decoded) == "" {
			t.Errorf("%s has no summary at its defaults", def.ID)
		}
	}
}

// checkFieldDefault makes sure the default has the JSON type the field's
// control edits, and that it is a value the field itself accepts.
func checkFieldDefault(t *testing.T, name string, field Field) {
	t.Helper()
	data, err := json.Marshal(field.Default)
	if err != nil {
		t.Fatalf("%s default: %v", name, err)
	}
	var wire any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("%s default: %v", name, err)
	}
	ok := false
	switch field.Type {
	case FieldString, FieldEnum, FieldDuration, FieldPath:
		_, ok = wire.(string)
	case FieldInt:
		number, isNumber := wire.(float64)
		ok = isNumber && number == math.Trunc(number)
	case FieldFloat:
		_, ok = wire.(float64)
	case FieldBool:
		_, ok = wire.(bool)
	case FieldEnumList, FieldStringList:
		var entries []any
		entries, ok = wire.([]any)
		for _, entry := range entries {
			if _, isString := entry.(string); !isString {
				ok = false
			}
		}
	}
	if !ok {
		t.Errorf("%s default %s is not a JSON %s", name, data, field.Type)
	}
	if _, message := decodeValue(field, wire); message != "" {
		t.Errorf("%s rejects its own default %s: %s", name, data, message)
	}
	if _, message := decodeValue(field, field.Default); message != "" {
		t.Errorf("%s rejects its own Go default %#v: %s", name, field.Default, message)
	}
}

func checkFieldShape(t *testing.T, name string, field Field) {
	t.Helper()
	hasChoices := field.Type == FieldEnum || field.Type == FieldEnumList
	if hasChoices != (len(field.Choices) > 0) {
		t.Errorf("%s of type %s has %d choices", name, field.Type, len(field.Choices))
	}
	values := map[string]bool{}
	for _, choice := range field.Choices {
		if choice.Value == "" || choice.Label == "" || values[choice.Value] {
			t.Errorf("%s choice %+v is empty or repeated", name, choice)
		}
		values[choice.Value] = true
	}
	if field.Segmented && field.Type != FieldEnum {
		t.Errorf("%s is segmented but not an enum", name)
	}
	numeric := field.Type == FieldInt || field.Type == FieldFloat
	if !numeric && (field.Min != nil || field.Max != nil) {
		t.Errorf("%s of type %s has a range", name, field.Type)
	}
	if field.Min != nil && field.Max != nil && *field.Min > *field.Max {
		t.Errorf("%s has min %v above max %v", name, *field.Min, *field.Max)
	}
	if field.MinExclusive && field.Min == nil {
		t.Errorf("%s excludes a minimum it does not have", name)
	}
	if field.Tokens && field.Type != FieldStringList {
		t.Errorf("%s holds tokens but is not a string list", name)
	}
	// Half the options sheet is about 180px, which a segmented control with
	// two short choices fills. A third choice does not fit beside another
	// field, and the editor would have to fall back to a select.
	if field.Segmented && field.Half {
		width := 0
		for _, choice := range field.Choices {
			width += len(choice.Label)
		}
		if len(field.Choices) > 2 || width > 16 {
			t.Errorf("%s is segmented and half a row wide, but its %d choices (%d characters) do not fit there", name, len(field.Choices), width)
		}
	}
	if field.Type == FieldDuration {
		if duration, ok := mustDecode(t, name, field).(time.Duration); !ok || duration <= 0 {
			t.Errorf("%s default duration = %v", name, duration)
		}
	}
}

func checkFieldVisibility(t *testing.T, name string, field Field, earlier map[string]Field) {
	t.Helper()
	if field.VisibleWhen == nil {
		return
	}
	// Decoding goes through the fields in order, so a field can only depend
	// on one that comes before it.
	controller, ok := earlier[field.VisibleWhen.Key]
	if !ok {
		t.Errorf("%s is visible by %q, which is not an earlier field", name, field.VisibleWhen.Key)
		return
	}
	if len(field.VisibleWhen.In) == 0 {
		t.Errorf("%s is never visible", name)
	}
	if controller.VisibleWhen != nil {
		t.Errorf("%s depends on %q, which is itself conditional", name, controller.Key)
	}
	for _, value := range field.VisibleWhen.In {
		switch typed := value.(type) {
		case string:
			if controller.Type != FieldEnum || !hasChoice(controller, typed) {
				t.Errorf("%s is visible for %q, which %q cannot hold", name, typed, controller.Key)
			}
		case bool:
			if controller.Type != FieldBool {
				t.Errorf("%s is visible for a boolean, but %q is %s", name, controller.Key, controller.Type)
			}
		default:
			t.Errorf("%s visibility value %#v must be a string or a boolean", name, value)
		}
	}
}

func mustDecode(t *testing.T, name string, field Field) any {
	t.Helper()
	value, message := decodeValue(field, field.Default)
	if message != "" {
		t.Fatalf("%s default: %s", name, message)
	}
	return value
}
