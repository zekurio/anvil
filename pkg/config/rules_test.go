package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	valid := map[string]int64{
		"200 MB":   200_000_000,
		"200MB":    200_000_000,
		" 1.5 GB ": 1_500_000_000,
		"750 MiB":  750 << 20,
		"2 gib":    2 << 30,
		"0 B":      0,
		"12 kb":    12_000,
		"1 TiB":    1 << 40,
	}
	for text, want := range valid {
		got, err := ParseSize(text)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", text, got, err, want)
		}
	}
	for _, text := range []string{"", "200", "MB", "200 M", "-5 MB", "1e3 MB", "1 000 MB", "NaN MB", "Inf GB", "0x10 MB", "1.2.3 GB", "5 MBs", "99999999999999999999 TB"} {
		if _, err := ParseSize(text); err == nil {
			t.Errorf("ParseSize(%q) succeeded", text)
		} else if !strings.Contains(err.Error(), `such as "200 MB"`) {
			t.Errorf("ParseSize(%q) error does not say what is valid: %v", text, err)
		}
	}
}

func TestValidateRules(t *testing.T) {
	tests := []struct {
		name string
		rule Rule
		key  string
	}{
		{"glob", Rule{RuleDeny, RuleFieldPath, RuleOpMatches, "**/Extras/**"}, ""},
		{"regex", Rule{RuleAllow, RuleFieldPath, RuleOpRegex, `(?i)\.sample\.`}, ""},
		{"extension list", Rule{RuleDeny, RuleFieldExtension, RuleOpIsNot, "mkv, .MP4"}, ""},
		{"size", Rule{RuleDeny, RuleFieldSize, RuleOpLess, "200 MB"}, ""},
		{"unknown action", Rule{"block", RuleFieldPath, RuleOpMatches, "*"}, "action"},
		{"unknown field", Rule{RuleDeny, "name", RuleOpMatches, "*"}, "field"},
		{"operator of another field", Rule{RuleDeny, RuleFieldPath, RuleOpIs, "*"}, "op"},
		{"size with extension operator", Rule{RuleDeny, RuleFieldSize, RuleOpIs, "1 GB"}, "op"},
		{"empty glob", Rule{RuleDeny, RuleFieldPath, RuleOpMatches, " "}, "value"},
		{"broken glob", Rule{RuleDeny, RuleFieldPath, RuleOpMatches, "[a-"}, "value"},
		{"broken regex", Rule{RuleDeny, RuleFieldPath, RuleOpRegex, "("}, "value"},
		{"empty extension list", Rule{RuleDeny, RuleFieldExtension, RuleOpIs, ""}, "value"},
		{"blank extension entry", Rule{RuleDeny, RuleFieldExtension, RuleOpIs, "mkv,,mp4"}, "value"},
		{"extension with a path", Rule{RuleDeny, RuleFieldExtension, RuleOpIs, "a/b"}, "value"},
		{"size without unit", Rule{RuleDeny, RuleFieldSize, RuleOpGreater, "200"}, "value"},
	}
	for _, test := range tests {
		problems := ValidateRules([]Rule{test.rule}, RuleAllow)
		switch {
		case test.key == "" && len(problems) != 0:
			t.Errorf("%s: unexpected problems %+v", test.name, problems)
		case test.key != "" && (len(problems) != 1 || problems[0].Key != test.key || problems[0].Index != 0 || problems[0].Message == ""):
			t.Errorf("%s: problems = %+v, want one on %q", test.name, problems, test.key)
		}
	}
	if problems := ValidateRules(nil, "maybe"); len(problems) != 1 || problems[0].Index != -1 {
		t.Errorf("bad fallback: problems = %+v", problems)
	}
	if problems := ValidateRules(nil, ""); len(problems) != 0 {
		t.Errorf("empty fallback: problems = %+v", problems)
	}
	if _, err := CompileRules([]Rule{{RuleDeny, RuleFieldSize, RuleOpLess, "small"}}, ""); err == nil || !strings.HasPrefix(err.Error(), "rule 1: ") {
		t.Errorf("CompileRules error = %v", err)
	}
}

func TestRuleSetDecide(t *testing.T) {
	const mb = 1_000_000
	tests := []struct {
		name     string
		rules    []Rule
		fallback string
		file     RuleFile
		allowed  bool
		rule     int
	}{
		{name: "no rules allow", file: RuleFile{Path: "a.mkv"}, allowed: true, rule: -1},
		{name: "no rules, deny fallback", fallback: RuleDeny, file: RuleFile{Path: "a.mkv"}, rule: -1},
		{
			name:  "first match wins over a later allow",
			rules: []Rule{{RuleDeny, RuleFieldPath, RuleOpMatches, "Extras/**"}, {RuleAllow, RuleFieldPath, RuleOpMatches, "Extras/keep.mkv"}},
			file:  RuleFile{Path: "Extras/keep.mkv"},
			rule:  0,
		},
		{
			name:    "first match wins over a later deny",
			rules:   []Rule{{RuleAllow, RuleFieldPath, RuleOpMatches, "Extras/keep.mkv"}, {RuleDeny, RuleFieldPath, RuleOpMatches, "Extras/**"}},
			file:    RuleFile{Path: "Extras/keep.mkv"},
			allowed: true,
			rule:    0,
		},
		{
			name:  "deny covers the contents of a matching directory",
			rules: []Rule{{RuleDeny, RuleFieldPath, RuleOpMatches, "Extras"}},
			file:  RuleFile{Path: "Movies/Film/Extras/deleted scene.mkv"},
			rule:  0,
		},
		{
			name:     "allow looks at the file itself only",
			rules:    []Rule{{RuleAllow, RuleFieldPath, RuleOpMatches, "Movies/*"}},
			fallback: RuleDeny,
			file:     RuleFile{Path: "Movies/Film/film.mkv"},
			rule:     -1,
		},
		{
			name:     "allow matches a direct child",
			rules:    []Rule{{RuleAllow, RuleFieldPath, RuleOpMatches, "Movies/*"}},
			fallback: RuleDeny,
			file:     RuleFile{Path: "Movies/film.mkv"},
			allowed:  true,
			rule:     0,
		},
		{
			name:  "glob without slash matches the file name at any depth",
			rules: []Rule{{RuleDeny, RuleFieldPath, RuleOpMatches, "*.sample.mkv"}},
			file:  RuleFile{Path: "a/b/c.sample.mkv"},
			rule:  0,
		},
		{
			name:  "regex on the relative path",
			rules: []Rule{{RuleDeny, RuleFieldPath, RuleOpRegex, `(?i)^trailers/`}},
			file:  RuleFile{Path: "Trailers/one.mkv"},
			rule:  0,
		},
		{
			name:  "regex with a trailing slash covers a directory",
			rules: []Rule{{RuleDeny, RuleFieldPath, RuleOpRegex, `/featurettes/$`}},
			file:  RuleFile{Path: "Film/featurettes/one.mkv"},
			rule:  0,
		},
		{
			name:  "extension is",
			rules: []Rule{{RuleDeny, RuleFieldExtension, RuleOpIs, "avi, ts"}},
			file:  RuleFile{Path: "a/Old.AVI"},
			rule:  0,
		},
		{
			name:    "extension is leaves others alone",
			rules:   []Rule{{RuleDeny, RuleFieldExtension, RuleOpIs, "avi, ts"}},
			file:    RuleFile{Path: "a/new.mkv"},
			allowed: true,
			rule:    -1,
		},
		{
			name:  "extension is_not",
			rules: []Rule{{RuleDeny, RuleFieldExtension, RuleOpIsNot, "mkv,mp4"}},
			file:  RuleFile{Path: "a/old.avi"},
			rule:  0,
		},
		{
			name:  "size lt",
			rules: []Rule{{RuleDeny, RuleFieldSize, RuleOpLess, "200 MB"}},
			file:  RuleFile{Path: "a.mkv", SizeBytes: 199 * mb},
			rule:  0,
		},
		{
			name:    "size lt is strict",
			rules:   []Rule{{RuleDeny, RuleFieldSize, RuleOpLess, "200 MB"}},
			file:    RuleFile{Path: "a.mkv", SizeBytes: 200 * mb},
			allowed: true,
			rule:    -1,
		},
		{
			name:     "size gt allows under a deny fallback",
			rules:    []Rule{{RuleAllow, RuleFieldSize, RuleOpGreater, "1 GB"}},
			fallback: RuleDeny,
			file:     RuleFile{Path: "a.mkv", SizeBytes: 2000 * mb},
			allowed:  true,
			rule:     0,
		},
	}
	for _, test := range tests {
		set, err := CompileRules(test.rules, test.fallback)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		decision := set.Decide(test.file)
		if decision.Allowed != test.allowed || decision.Rule != test.rule {
			t.Errorf("%s: decision = %+v, want allowed=%t rule=%d", test.name, decision, test.allowed, test.rule)
		}
		if !decision.Allowed && decision.Reason == "" {
			t.Errorf("%s: denial has no reason", test.name)
		}
	}
}

func TestRuleSetPrunes(t *testing.T) {
	denyExtras := Rule{RuleDeny, RuleFieldPath, RuleOpMatches, "**/Extras/**"}
	tests := []struct {
		name  string
		rules []Rule
		path  string
		dir   bool
		want  bool
	}{
		{"matching directory", []Rule{denyExtras}, "Film/Extras", true, true},
		{"file below a matching directory", []Rule{denyExtras}, "Film/Extras/a.mkv", false, true},
		{"unrelated directory", []Rule{denyExtras}, "Film/Featurettes", true, false},
		{"basename glob on a directory", []Rule{{RuleDeny, RuleFieldPath, RuleOpMatches, "Extras"}}, "Film/Extras", true, true},
		{"trailing-slash glob on a directory", []Rule{{RuleDeny, RuleFieldPath, RuleOpMatches, "ignored/"}}, "ignored", true, true},
		{"regex on a directory", []Rule{{RuleDeny, RuleFieldPath, RuleOpRegex, `^tmp/$`}}, "tmp", true, true},
		{"regex on a file", []Rule{{RuleDeny, RuleFieldPath, RuleOpRegex, `\.part\.mkv$`}}, "a.part.mkv", false, true},
		{"second deny in a row still prunes", []Rule{{RuleDeny, RuleFieldSize, RuleOpLess, "1 MB"}, denyExtras}, "Extras", true, true},
		{"an earlier allow could rescue a file inside", []Rule{{RuleAllow, RuleFieldPath, RuleOpMatches, "Extras/keep.mkv"}, denyExtras}, "Extras", true, false},
		{"an earlier allow on another field also stops pruning", []Rule{{RuleAllow, RuleFieldExtension, RuleOpIs, "mkv"}, denyExtras}, "Extras", true, false},
		{"size and extension rules never prune", []Rule{{RuleDeny, RuleFieldExtension, RuleOpIsNot, "mkv"}}, "notes.txt", false, false},
	}
	for _, test := range tests {
		set, err := CompileRules(test.rules, "")
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if got := set.Prunes(test.path, test.dir); got != test.want {
			t.Errorf("%s: Prunes(%q, %t) = %t, want %t", test.name, test.path, test.dir, got, test.want)
		}
	}
	if (RuleSet{}).Prunes("anything", true) {
		t.Error("the zero rule set prunes")
	}
}

// Everything a pruned directory holds must also be denied file by file, or
// skipping the directory would change what the rules mean.
func TestPruningAgreesWithDecide(t *testing.T) {
	set, err := CompileRules([]Rule{
		{RuleDeny, RuleFieldPath, RuleOpRegex, `^private/`},
		{RuleDeny, RuleFieldPath, RuleOpMatches, "Extras"},
		{RuleAllow, RuleFieldExtension, RuleOpIs, "mkv"},
	}, RuleDeny)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"private/a.mkv", "Film/Extras/a.mkv", "Film/Extras/deep/b.mkv"} {
		if !set.Prunes(file, false) {
			t.Errorf("Prunes(%q) = false", file)
		}
		if set.Decide(RuleFile{Path: file}).Allowed {
			t.Errorf("Decide(%q) allowed a pruned file", file)
		}
	}
}

func TestLegacyRulesKeepOrderAndFallback(t *testing.T) {
	rules, fallback := LegacyRules([]string{"Movies/**", " "}, []string{"**/Extras/**"}, []string{`\.tmp$`})
	want := []Rule{
		{RuleDeny, RuleFieldPath, RuleOpRegex, `\.tmp$`},
		{RuleDeny, RuleFieldPath, RuleOpMatches, "**/Extras/**"},
		{RuleAllow, RuleFieldPath, RuleOpMatches, "Movies/**"},
	}
	if len(rules) != len(want) || fallback != RuleDeny {
		t.Fatalf("rules = %+v, fallback = %q", rules, fallback)
	}
	for i := range want {
		if rules[i] != want[i] {
			t.Errorf("rule %d = %+v, want %+v", i, rules[i], want[i])
		}
	}
	if rules, fallback := LegacyRules(nil, []string{"x"}, nil); len(rules) != 1 || fallback != RuleAllow {
		t.Errorf("exclude only: rules = %+v, fallback = %q", rules, fallback)
	}
}

func TestWebValidation(t *testing.T) {
	tests := []struct {
		name string
		web  WebConfig
		want string
	}{
		{name: "loopback without password", web: WebConfig{Enabled: true, Listen: "127.0.0.1:8484"}},
		{name: "localhost without password", web: WebConfig{Enabled: true, Listen: "localhost:8484"}},
		{name: "ipv6 loopback", web: WebConfig{Enabled: true, Listen: "[::1]:8484"}},
		{name: "every interface without password", web: WebConfig{Enabled: true, Listen: ":8484"}, want: "web.allow_unauthenticated = true"},
		{name: "lan address without password", web: WebConfig{Enabled: true, Listen: "192.168.1.10:8484"}, want: "set web.password_file"},
		{name: "lan address with password file", web: WebConfig{Enabled: true, Listen: "0.0.0.0:8484", PasswordFile: "/run/secrets/anvil"}},
		{name: "lan address, explicitly open", web: WebConfig{Enabled: true, Listen: "0.0.0.0:8484", AllowUnauthenticated: true}},
		{name: "both password sources", web: WebConfig{Enabled: true, Listen: "127.0.0.1:1", Password: "a", PasswordFile: "/b"}, want: "keep only one"},
		{name: "no port", web: WebConfig{Enabled: true, Listen: "127.0.0.1"}, want: "write host:port"},
		{name: "bad port", web: WebConfig{Enabled: true, Listen: "127.0.0.1:http"}, want: "invalid port"},
		{name: "disabled is not checked", web: WebConfig{Listen: "nonsense"}},
	}
	for _, test := range tests {
		var p problems
		test.web.validate(&p)
		joined := strings.Join(p, "\n")
		switch {
		case test.want == "" && len(p) != 0:
			t.Errorf("%s: unexpected problems: %s", test.name, joined)
		case test.want != "" && !strings.Contains(joined, test.want):
			t.Errorf("%s: problems %q do not mention %q", test.name, joined, test.want)
		}
	}
	if cfg := Default(); !cfg.Web.Enabled || cfg.Web.Listen != DefaultWebListen || cfg.Web.AuthRequired() {
		t.Errorf("default web config = %+v", cfg.Web)
	}
}

// TestWebListenTable runs the addresses of testdata/web_listen.json, which
// the NixOS module's mirror of this rule (nix/modules/web-listen.nix) is
// checked against as well. The two must agree: the module asserts at
// evaluation time what the daemon refuses at start.
func TestWebListenTable(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "web_listen.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Listen   string `json:"listen"`
		Valid    bool   `json:"valid"`
		Loopback bool   `json:"loopback"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 40 {
		t.Fatalf("only %d cases", len(cases))
	}
	for _, c := range cases {
		var p problems
		WebConfig{Enabled: true, Listen: c.Listen}.validate(&p)
		joined := strings.Join(p, "\n")
		valid := !strings.Contains(joined, "is invalid") && !strings.Contains(joined, "invalid port")
		// Without a password, the one remaining problem of a valid address
		// is that other machines can reach it.
		loopback := valid && len(p) == 0
		if valid != c.Valid || loopback != c.Loopback {
			t.Errorf("%q: valid %v, loopback %v, want valid %v, loopback %v (%s)", c.Listen, valid, loopback, c.Valid, c.Loopback, joined)
		}
	}
}
