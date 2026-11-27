package flow

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// options are a node's settings after decoding: one entry per field of the
// node type, typed by FieldType as string, int, float64, bool, []string, or
// time.Duration. A field that is absent, hidden by VisibleWhen, or holds a
// value that failed to decode carries its default, so readers never see a
// missing or mistyped entry.
type options map[string]any

func (o options) text(key string) string {
	value, _ := o[key].(string)
	return value
}

func (o options) whole(key string) int {
	value, _ := o[key].(int)
	return value
}

func (o options) number(key string) float64 {
	value, _ := o[key].(float64)
	return value
}

func (o options) on(key string) bool {
	value, _ := o[key].(bool)
	return value
}

func (o options) list(key string) []string {
	value, _ := o[key].([]string)
	return value
}

func (o options) span(key string) time.Duration {
	value, _ := o[key].(time.Duration)
	return value
}

// optionProblem is one option of a node that breaks a rule.
type optionProblem struct {
	Field   string
	Message string
}

// decodeOptions reads raw against the fields of def. Fields are decoded in
// catalog order, which lets VisibleWhen look at the fields before it: a hidden
// field is not in effect, so its stored value is neither checked nor used.
func decodeOptions(def nodeDef, raw map[string]any) (options, []optionProblem) {
	decoded := options{}
	var problems []optionProblem
	flagged := map[string]bool{}
	for _, section := range def.Sections {
		for _, field := range section.Fields {
			decoded[field.Key], _ = decodeValue(field, field.Default)
			value, set := raw[field.Key]
			if !set || !visible(field, decoded) {
				continue
			}
			value, message := decodeValue(field, value)
			if message != "" {
				problems = append(problems, optionProblem{Field: field.Key, Message: message})
				flagged[field.Key] = true
				continue
			}
			decoded[field.Key] = value
		}
	}
	if def.check != nil {
		// A field that failed to decode already has its message, and the rule
		// would only be judging the default that stands in for it.
		for _, problem := range def.check(decoded) {
			if !flagged[problem.Field] {
				problems = append(problems, problem)
			}
		}
	}

	unknown := make([]string, 0, len(raw))
	for key := range raw {
		if _, known := decoded[key]; !known {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		problems = append(problems, optionProblem{
			Field:   key,
			Message: fmt.Sprintf("%q is not an option of this node. Remove it.", key),
		})
	}
	return decoded, problems
}

func visible(field Field, decoded options) bool {
	if field.VisibleWhen == nil {
		return true
	}
	return slices.Contains(field.VisibleWhen.In, decoded[field.VisibleWhen.Key])
}

// decodeValue converts one JSON-decoded option value to the Go type of its
// field. The message is empty on success and says what is valid otherwise.
func decodeValue(field Field, value any) (any, string) {
	switch field.Type {
	case FieldString:
		text, ok := value.(string)
		if !ok {
			return nil, "Must be text."
		}
		return text, ""
	case FieldBool:
		flag, ok := value.(bool)
		if !ok {
			return nil, "Must be true or false."
		}
		return flag, ""
	case FieldInt:
		number, ok := floatValue(value)
		if !ok || number != math.Trunc(number) || math.Abs(number) > 1<<53 {
			return nil, "Must be a whole number."
		}
		if message := rangeMessage(field, number); message != "" {
			return nil, message
		}
		return int(number), ""
	case FieldFloat:
		number, ok := floatValue(value)
		if !ok {
			return nil, "Must be a number."
		}
		if message := rangeMessage(field, number); message != "" {
			return nil, message
		}
		return number, ""
	case FieldDuration:
		text, ok := value.(string)
		if !ok {
			return nil, `Must be a duration such as "20s".`
		}
		duration, err := time.ParseDuration(strings.TrimSpace(text))
		if err != nil {
			return nil, `Must be a duration such as "20s".`
		}
		return duration, ""
	case FieldEnum:
		text, ok := value.(string)
		if !ok || !hasChoice(field, text) {
			return nil, "Must be one of: " + choiceValues(field) + "."
		}
		return text, ""
	case FieldEnumList:
		list, ok := stringList(value)
		if !ok || slices.ContainsFunc(list, func(entry string) bool { return !hasChoice(field, entry) }) {
			return nil, "Must be a list of: " + choiceValues(field) + "."
		}
		// The list is a set: catalog order and no repeats, so two ways of
		// ticking the same boxes decode alike.
		chosen := make([]string, 0, len(list))
		for _, choice := range field.Choices {
			if slices.Contains(list, choice.Value) {
				chosen = append(chosen, choice.Value)
			}
		}
		return chosen, ""
	case FieldStringList:
		list, ok := stringList(value)
		if !ok {
			return nil, "Must be a list of text values."
		}
		return list, ""
	default:
		return nil, "Must be a value this field supports."
	}
}

// floatValue accepts the float64 that encoding/json produces and the integer
// types Go code writes into a graph it builds itself.
func floatValue(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

// stringList accepts the []any that encoding/json produces and the []string
// Go code writes. The result is always a fresh, non-nil slice.
func stringList(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return append([]string{}, typed...), true
	case []any:
		list := make([]string, 0, len(typed))
		for _, entry := range typed {
			text, ok := entry.(string)
			if !ok {
				return nil, false
			}
			list = append(list, text)
		}
		return list, true
	default:
		return nil, false
	}
}

func rangeMessage(field Field, number float64) string {
	below := field.Min != nil && number < *field.Min
	above := field.Max != nil && number > *field.Max
	switch {
	case !below && !above:
		return ""
	case field.Min != nil && field.Max != nil:
		return fmt.Sprintf("Must be between %s and %s.", formatNumber(*field.Min), formatNumber(*field.Max))
	case field.Min != nil:
		return fmt.Sprintf("Must be at least %s.", formatNumber(*field.Min))
	default:
		return fmt.Sprintf("Must be at most %s.", formatNumber(*field.Max))
	}
}

func hasChoice(field Field, value string) bool {
	return slices.ContainsFunc(field.Choices, func(choice Choice) bool { return choice.Value == value })
}

func choiceValues(field Field) string {
	values := make([]string, 0, len(field.Choices))
	for _, choice := range field.Choices {
		values = append(values, choice.Value)
	}
	return strings.Join(values, ", ")
}

// formatNumber prints a number the way an operator typed it: 95, not 95.000000.
func formatNumber(number float64) string {
	return strconv.FormatFloat(number, 'f', -1, 64)
}
