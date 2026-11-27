package config

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Rule actions, fields, and operators. The strings are the ones the web UI
// sends and the store keeps.
const (
	RuleAllow = "allow"
	RuleDeny  = "deny"

	RuleFieldPath      = "path"
	RuleFieldExtension = "extension"
	RuleFieldSize      = "size"

	RuleOpMatches = "matches"
	RuleOpRegex   = "regex"
	RuleOpIs      = "is"
	RuleOpIsNot   = "is_not"
	RuleOpGreater = "gt"
	RuleOpLess    = "lt"
)

// Rule allows or denies files of a library at scan time. A library holds an
// ordered list: the first rule that matches a file decides, and the library's
// fallback decides when none does.
//
//	path       matches  doublestar glob on the library-relative path; a glob
//	                    without "/" also matches the file name alone
//	path       regex    Go regular expression on the library-relative path
//	extension  is       comma-separated list without dots: "mkv, mp4"
//	extension  is_not
//	size       gt, lt   a size with a unit: "200 MB", "1.5 GB", "750 MiB"
//
// Path rules treat the two actions differently, which is what the include,
// exclude, and ignore_regex settings they replace did. An allow rule looks at
// the file itself. A deny rule also covers everything inside a directory it
// matches, so "deny path matches Extras" denies Extras/a.mkv.
//
// A deny path rule that stands ahead of every allow rule prunes: the scanner
// never descends into a directory it matches, ignores filesystem events below
// it, and does not count what it matches towards the size or stability of a
// download package. That is the old exclude and ignore_regex behavior. Every
// other denial is decided per file: a denied media file is seen, counted as
// skipped, and not queued. That is the old include behavior.
type Rule struct {
	Action string `json:"action"`
	Field  string `json:"field"`
	Op     string `json:"op"`
	Value  string `json:"value"`
}

// String renders a rule the way an operator reads it in a log line.
func (r Rule) String() string {
	return fmt.Sprintf("%s %s %s %q", r.Action, r.Field, r.Op, r.Value)
}

// RuleProblem is one thing wrong with a library's rules.
type RuleProblem struct {
	// Index is the position of the rule at fault, or -1 for the fallback.
	Index int
	// Key names the part of the rule at fault: "action", "field", "op", or
	// "value". It is empty for the fallback.
	Key     string
	Message string
}

// RuleFile is what the rules know about one file.
type RuleFile struct {
	// Path is relative to the library root and slash-separated.
	Path      string
	SizeBytes int64
}

// RuleDecision is the outcome of judging one file.
type RuleDecision struct {
	Allowed bool
	// Rule is the index of the rule that decided, or -1 for the fallback.
	Rule int
	// Reason says why a denied file was denied.
	Reason string
}

// RuleSet is a compiled rule list. The zero value allows everything.
type RuleSet struct {
	rules        []compiledRule
	fallbackDeny bool
}

type compiledRule struct {
	source Rule
	deny   bool
	// Exactly one of the following is set, by field and operator.
	glob       string
	regex      *regexp.Regexp
	extensions []string
	negate     bool
	sizeBytes  int64
}

// NormalizeRuleFallback returns the fallback in effect: an empty value allows.
func NormalizeRuleFallback(fallback string) string {
	if strings.TrimSpace(fallback) == "" {
		return RuleAllow
	}
	return strings.TrimSpace(fallback)
}

// ValidateRules reports everything wrong with a rule list and its fallback.
func ValidateRules(rules []Rule, fallback string) []RuleProblem {
	var problems []RuleProblem
	for index, rule := range rules {
		if _, key, message := compileRule(rule); message != "" {
			problems = append(problems, RuleProblem{Index: index, Key: key, Message: message})
		}
	}
	if action := NormalizeRuleFallback(fallback); action != RuleAllow && action != RuleDeny {
		problems = append(problems, RuleProblem{Index: -1, Message: fmt.Sprintf("the fallback must be %q or %q, not %q", RuleAllow, RuleDeny, fallback)})
	}
	return problems
}

// CompileRules prepares a rule list for evaluation. It fails on the first
// problem ValidateRules would report.
func CompileRules(rules []Rule, fallback string) (RuleSet, error) {
	if problems := ValidateRules(rules, fallback); len(problems) > 0 {
		problem := problems[0]
		if problem.Index < 0 {
			return RuleSet{}, fmt.Errorf("rules: %s", problem.Message)
		}
		return RuleSet{}, fmt.Errorf("rule %d: %s", problem.Index+1, problem.Message)
	}
	set := RuleSet{
		rules:        make([]compiledRule, 0, len(rules)),
		fallbackDeny: NormalizeRuleFallback(fallback) == RuleDeny,
	}
	for _, rule := range rules {
		compiled, _, _ := compileRule(rule)
		set.rules = append(set.rules, compiled)
	}
	return set, nil
}

// compileRule returns the part of the rule at fault and a message when the
// rule is not valid.
func compileRule(rule Rule) (compiledRule, string, string) {
	compiled := compiledRule{source: rule}
	switch rule.Action {
	case RuleAllow:
	case RuleDeny:
		compiled.deny = true
	default:
		return compiled, "action", fmt.Sprintf("the action must be %q or %q, not %q", RuleAllow, RuleDeny, rule.Action)
	}
	value := strings.TrimSpace(rule.Value)
	switch rule.Field {
	case RuleFieldPath:
		switch rule.Op {
		case RuleOpMatches:
			pattern := filepath.ToSlash(value)
			if pattern == "" {
				return compiled, "value", "the glob must not be empty; write a pattern such as \"**/Extras/**\" or \"*.mkv\""
			}
			if !doublestar.ValidatePattern(pattern) {
				return compiled, "value", fmt.Sprintf("%q is not a valid glob; write a pattern such as \"**/Extras/**\" or \"*.mkv\"", value)
			}
			compiled.glob = pattern
		case RuleOpRegex:
			if value == "" {
				return compiled, "value", "the regular expression must not be empty"
			}
			regex, err := regexp.Compile(value)
			if err != nil {
				return compiled, "value", fmt.Sprintf("%q is not a valid regular expression: %v", value, err)
			}
			compiled.regex = regex
		default:
			return compiled, "op", fmt.Sprintf("a path rule uses %q or %q, not %q", RuleOpMatches, RuleOpRegex, rule.Op)
		}
	case RuleFieldExtension:
		switch rule.Op {
		case RuleOpIs:
		case RuleOpIsNot:
			compiled.negate = true
		default:
			return compiled, "op", fmt.Sprintf("an extension rule uses %q or %q, not %q", RuleOpIs, RuleOpIsNot, rule.Op)
		}
		extensions, message := parseExtensions(value)
		if message != "" {
			return compiled, "value", message
		}
		compiled.extensions = extensions
	case RuleFieldSize:
		switch rule.Op {
		case RuleOpGreater:
		case RuleOpLess:
			compiled.negate = true
		default:
			return compiled, "op", fmt.Sprintf("a size rule uses %q or %q, not %q", RuleOpGreater, RuleOpLess, rule.Op)
		}
		size, err := ParseSize(value)
		if err != nil {
			return compiled, "value", err.Error()
		}
		compiled.sizeBytes = size
	default:
		return compiled, "field", fmt.Sprintf("the field must be %q, %q, or %q, not %q", RuleFieldPath, RuleFieldExtension, RuleFieldSize, rule.Field)
	}
	return compiled, "", ""
}

func parseExtensions(value string) ([]string, string) {
	const valid = "list extensions without dots, separated by commas, such as \"mkv, mp4\""
	if value == "" {
		return nil, "the extension list must not be empty; " + valid
	}
	var extensions []string
	for _, entry := range strings.Split(value, ",") {
		entry = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(entry), "."))
		if entry == "" || strings.ContainsAny(entry, "./\\ \t") {
			return nil, fmt.Sprintf("%q is not a list of extensions; %s", value, valid)
		}
		extensions = append(extensions, entry)
	}
	return extensions, ""
}

type sizeUnit struct {
	name  string
	bytes float64
}

// sizeUnits are the units ParseSize accepts, matched without regard to case.
var sizeUnits = []sizeUnit{
	{"B", 1},
	{"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12},
	{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
}

// ParseSize reads a size with a unit, such as "200 MB", "1.5 GB", or
// "750 MiB". KB, MB, GB, and TB are powers of 1000; KiB, MiB, GiB, and TiB
// are powers of 1024. A bare number is refused, because guessing its unit
// would be off by a factor of a million one way or the other.
func ParseSize(text string) (int64, error) {
	invalid := fmt.Errorf("%q is not a size; write a number and a unit, such as \"200 MB\", \"1.5 GB\", or \"750 MiB\" (units: B, KB, MB, GB, TB, KiB, MiB, GiB, TiB)", text)
	trimmed := strings.TrimSpace(text)
	digits := strings.TrimRight(trimmed, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	unit := trimmed[len(digits):]
	digits = strings.TrimRight(digits, " ")
	if digits == "" || unit == "" || strings.Trim(digits, "0123456789.") != "" {
		return 0, invalid
	}
	number, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		return 0, invalid
	}
	index := slices.IndexFunc(sizeUnits, func(candidate sizeUnit) bool {
		return strings.EqualFold(candidate.name, unit)
	})
	if index < 0 {
		return 0, invalid
	}
	bytes := number * sizeUnits[index].bytes
	if bytes > 1<<62 {
		return 0, invalid
	}
	return int64(bytes), nil
}

// Prunes reports whether a path is out of the library altogether: a deny rule
// on the path matches it, or a directory above it, before any allow rule gets
// a say. Nothing below a pruned directory can be allowed, which is what lets
// the scanner skip the directory without looking inside.
func (s RuleSet) Prunes(rel string, dir bool) bool {
	for _, rule := range s.rules {
		if !rule.deny {
			return false
		}
		if rule.coversPath(rel, dir) {
			return true
		}
	}
	return false
}

// Decide judges one file: the first matching rule decides, and the fallback
// does when no rule matches.
func (s RuleSet) Decide(file RuleFile) RuleDecision {
	for index, rule := range s.rules {
		if !rule.matches(file) {
			continue
		}
		if rule.deny {
			return RuleDecision{Rule: index, Reason: fmt.Sprintf("%s%d (%s %s %q)", deniedByRulePrefix, index+1, rule.source.Field, rule.source.Op, rule.source.Value)}
		}
		return RuleDecision{Allowed: true, Rule: index}
	}
	if s.fallbackDeny {
		return RuleDecision{Rule: -1, Reason: deniedByFallback}
	}
	return RuleDecision{Allowed: true, Rule: -1}
}

const (
	deniedByRulePrefix = "denied by rule "
	deniedByFallback   = "no rule allows it"
)

// DeniedByRules reports whether reason is one Decide gives for a denial. A
// job skipped by a scan carries that reason as its last error, and the store
// uses this to tell such a job from one that was skipped for any other cause.
func DeniedByRules(reason string) bool {
	return reason == deniedByFallback || strings.HasPrefix(reason, deniedByRulePrefix)
}

func (r compiledRule) matches(file RuleFile) bool {
	switch {
	case r.glob != "" || r.regex != nil:
		if r.deny {
			return r.coversPath(file.Path, false)
		}
		return r.matchesPath(file.Path, false)
	case r.extensions != nil:
		extension := strings.ToLower(strings.TrimPrefix(path.Ext(file.Path), "."))
		return slices.Contains(r.extensions, extension) != r.negate
	case r.negate:
		return file.SizeBytes < r.sizeBytes
	default:
		return file.SizeBytes > r.sizeBytes
	}
}

// coversPath reports whether a path rule matches the path or any directory
// above it. Rules on other fields cover nothing.
func (r compiledRule) coversPath(rel string, dir bool) bool {
	if r.glob == "" && r.regex == nil {
		return false
	}
	rel = path.Clean(filepath.ToSlash(rel))
	for current := rel; current != "." && current != "/"; current = path.Dir(current) {
		if r.matchesPath(current, dir || current != rel) {
			return true
		}
	}
	return false
}

func (r compiledRule) matchesPath(rel string, dir bool) bool {
	if r.regex != nil {
		return r.regex.MatchString(rel) || dir && r.regex.MatchString(rel+"/")
	}
	return globMatches(r.glob, rel) || dir && globMatches(r.glob, rel+"/")
}

// GlobMatches reports whether a doublestar glob matches a slash-separated
// relative path. A pattern without "/" also matches the last path element
// alone, so "*.mkv" finds files at any depth. A trailing "/" on the path
// marks a directory.
func GlobMatches(pattern, rel string) (bool, error) {
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	if pattern == "" {
		return false, nil
	}
	if !doublestar.ValidatePattern(pattern) {
		return false, fmt.Errorf("invalid glob %q", pattern)
	}
	return globMatches(pattern, rel), nil
}

// globMatches is GlobMatches for a pattern already known to be valid.
func globMatches(pattern, rel string) bool {
	directory := strings.HasSuffix(filepath.ToSlash(rel), "/")
	rel = path.Clean(filepath.ToSlash(rel))
	base := path.Base(rel)
	if directory {
		rel += "/"
	}
	if doublestar.MatchUnvalidated(pattern, rel) {
		return true
	}
	return !strings.Contains(pattern, "/") && doublestar.MatchUnvalidated(pattern, base)
}

// LegacyRules converts the include, exclude, and ignore_regex settings of a
// TOML library into the equivalent rule list. ignore_regex and exclude become
// deny rules at the top, where they prune as they always did; include becomes
// allow rules below them, with a deny fallback so that nothing else is queued.
func LegacyRules(include, exclude, ignoreRegex []string) ([]Rule, string) {
	rules := make([]Rule, 0, len(include)+len(exclude)+len(ignoreRegex))
	for _, pattern := range ignoreRegex {
		rules = append(rules, Rule{Action: RuleDeny, Field: RuleFieldPath, Op: RuleOpRegex, Value: strings.TrimSpace(pattern)})
	}
	for _, pattern := range exclude {
		if strings.TrimSpace(pattern) != "" {
			rules = append(rules, Rule{Action: RuleDeny, Field: RuleFieldPath, Op: RuleOpMatches, Value: strings.TrimSpace(pattern)})
		}
	}
	// A list of only blank patterns matched nothing, so it still denies.
	fallback := RuleAllow
	if len(include) > 0 {
		fallback = RuleDeny
	}
	for _, pattern := range include {
		if strings.TrimSpace(pattern) != "" {
			rules = append(rules, Rule{Action: RuleAllow, Field: RuleFieldPath, Op: RuleOpMatches, Value: strings.TrimSpace(pattern)})
		}
	}
	return rules, fallback
}
