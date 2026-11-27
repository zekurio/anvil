package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zekurio/anvil/pkg/config"
	"github.com/zekurio/anvil/pkg/domain"
)

func writeLibraryFile(t *testing.T, root, rel string, size int) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func discovered(t *testing.T, root string, library config.LibraryConfig) (allowed []string, ignored map[string]string) {
	t.Helper()
	candidates, _, _, err := discoverCandidates(context.Background(), root, library, nil)
	if err != nil {
		t.Fatal(err)
	}
	ignored = make(map[string]string)
	for _, candidate := range candidates {
		if candidate.ignored {
			ignored[candidate.libraryRelativePath] = candidate.ignoreReason
			continue
		}
		allowed = append(allowed, candidate.libraryRelativePath)
	}
	sort.Strings(allowed)
	return allowed, ignored
}

func TestDiscoveryAppliesRulesInOrder(t *testing.T) {
	root := t.TempDir()
	writeLibraryFile(t, root, "Film/film.mkv", 4000)
	writeLibraryFile(t, root, "Film/small.mkv", 100)
	writeLibraryFile(t, root, "Film/old.avi", 4000)
	writeLibraryFile(t, root, "Film/Extras/scene.mkv", 4000)
	writeLibraryFile(t, root, "Trailers/one.mkv", 4000)

	library := config.LibraryConfig{Kind: "media", Rules: []config.Rule{
		{Action: config.RuleDeny, Field: config.RuleFieldPath, Op: config.RuleOpMatches, Value: "Trailers"},
		{Action: config.RuleDeny, Field: config.RuleFieldPath, Op: config.RuleOpRegex, Value: `/Extras/`},
		{Action: config.RuleDeny, Field: config.RuleFieldSize, Op: config.RuleOpLess, Value: "1 KB"},
		{Action: config.RuleAllow, Field: config.RuleFieldExtension, Op: config.RuleOpIs, Value: "mkv"},
	}, RuleFallback: config.RuleDeny}

	allowed, ignored := discovered(t, root, library)
	if len(allowed) != 1 || allowed[0] != "Film/film.mkv" {
		t.Fatalf("allowed = %v", allowed)
	}
	// Pruned directories are never entered, so nothing below them is reported.
	for path := range ignored {
		if strings.HasPrefix(path, "Trailers/") || strings.Contains(path, "/Extras/") {
			t.Errorf("pruned file %q was visited", path)
		}
	}
	if reason := ignored["Film/small.mkv"]; !strings.Contains(reason, "rule 3") {
		t.Errorf("small.mkv reason = %q", reason)
	}
	if reason := ignored["Film/old.avi"]; reason != "no rule allows it" {
		t.Errorf("old.avi reason = %q", reason)
	}
}

// The rule list a TOML library's include, exclude, and ignore_regex convert to
// has to select exactly what those settings selected.
func TestLegacyRulesSelectLikeIncludeExcludeIgnore(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"Movies/a.mkv", "Movies/sub/b.mkv", "Movies/Extras/c.mkv", "Movies/d.tmp.mkv", "Shows/e.mkv", "f.mkv",
	} {
		writeLibraryFile(t, root, rel, 10)
	}
	rules, fallback := config.LegacyRules([]string{"Movies/*"}, []string{"**/Extras/**"}, []string{`\.tmp\.`})
	allowed, ignored := discovered(t, root, config.LibraryConfig{Kind: "media", Rules: rules, RuleFallback: fallback})
	if len(allowed) != 1 || allowed[0] != "Movies/a.mkv" {
		t.Fatalf("allowed = %v, ignored = %v", allowed, ignored)
	}
	// include never reached below Movies/*, and still does not.
	if _, seen := ignored["Movies/sub/b.mkv"]; !seen {
		t.Errorf("a file the include list missed was not reported as ignored: %v", ignored)
	}
	// An ignore_regex match on a file is still reported; an excluded
	// directory is still never entered.
	if _, seen := ignored["Movies/d.tmp.mkv"]; !seen {
		t.Errorf("ignore_regex file was not reported: %v", ignored)
	}
	if _, seen := ignored["Movies/Extras/c.mkv"]; seen {
		t.Errorf("excluded directory was entered: %v", ignored)
	}
}

// A plan says which ignored files the rules keep out, as opposed to the ones
// the scanner leaves alone on its own account: only the first kind takes a
// queued job off the queue.
func TestPlanMarksFilesTheRulesDeny(t *testing.T) {
	root := t.TempDir()
	writeLibraryFile(t, root, "film.mkv", 4000)
	writeLibraryFile(t, root, "small.mkv", 100)
	writeLibraryFile(t, root, "film-sample.mkv", 4000)
	library := config.LibraryConfig{Name: "movies", Kind: "media", Path: root, Rules: []config.Rule{
		{Action: config.RuleDeny, Field: config.RuleFieldSize, Op: config.RuleOpLess, Value: "1 KB"},
	}}
	plan, err := Scanner{}.PlanLibrary(context.Background(), library)
	if err != nil {
		t.Fatal(err)
	}
	denied := make(map[string]bool)
	for _, candidate := range plan.Candidates {
		if candidate.Denied && (!candidate.Ignored || candidate.IgnoreReason == "") {
			t.Errorf("%s is denied without being ignored with a reason: %+v", candidate.LibraryRelativePath, candidate)
		}
		denied[candidate.LibraryRelativePath] = candidate.Denied
	}
	if len(denied) != 3 || denied["film.mkv"] || !denied["small.mkv"] || denied["film-sample.mkv"] {
		t.Fatalf("denied = %v, want only small.mkv", denied)
	}
}

func TestDiscoveryRejectsBrokenRules(t *testing.T) {
	library := config.LibraryConfig{Kind: "media", Rules: []config.Rule{{Action: config.RuleDeny, Field: config.RuleFieldSize, Op: config.RuleOpLess, Value: "tiny"}}}
	if _, _, _, err := discoverCandidates(context.Background(), t.TempDir(), library, nil); err == nil || !strings.Contains(err.Error(), "rule 1") {
		t.Fatalf("err = %v", err)
	}
}

type recordingScanner struct {
	scans []string
}

func (s *recordingScanner) ScanLibrary(_ context.Context, library config.LibraryConfig) (ScanResult, error) {
	s.scans = append(s.scans, library.Name)
	return ScanResult{}, nil
}

func TestMonitorScansLibrariesAddedAndChangedAtRuntime(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := config.Default()
	cfg.Libraries = map[string]config.LibraryConfig{"movies": {Name: "movies", Kind: "media", Path: "/movies"}}
	scans := &recordingScanner{}
	monitor := &Monitor{
		Scanner:        scans,
		ConfigProvider: func() config.Config { return cfg },
		Debounce:       2 * time.Second,
		Now:            func() time.Time { return now },
	}
	monitor.pending = make(map[domain.LibraryName]ScanTrigger)
	monitor.fullDue = make(map[domain.LibraryName]time.Time)
	schedules := make(map[domain.LibraryName]time.Time)
	reasons := make(map[domain.LibraryName]string)

	monitor.reconcileSchedules(schedules, reasons)
	if due := schedules["movies"]; !due.Equal(now.Add(config.DefaultScanInterval)) {
		t.Fatalf("a library present at start was scheduled at %v", due)
	}

	// A new library, a rule change, and a pause arrive together.
	movies := cfg.Libraries["movies"]
	movies.Rules = []config.Rule{{Action: config.RuleDeny, Field: config.RuleFieldExtension, Op: config.RuleOpIs, Value: "avi"}}
	cfg.Libraries = map[string]config.LibraryConfig{
		"movies": movies,
		"shows":  {Name: "shows", Kind: "media", Path: "/shows", Paused: true},
	}
	monitor.reconcileSchedules(schedules, reasons)
	soon := now.Add(2 * time.Second)
	if !schedules["movies"].Equal(soon) || reasons["movies"] != "library-changed" {
		t.Fatalf("changed library: due %v reason %q", schedules["movies"], reasons["movies"])
	}
	if !schedules["shows"].Equal(soon) || reasons["shows"] != "library-added" {
		t.Fatalf("added library: due %v reason %q", schedules["shows"], reasons["shows"])
	}

	now = soon
	if err := monitor.scanDue(context.Background(), cfg, schedules, reasons); err != nil {
		t.Fatal(err)
	}
	if len(scans.scans) != 2 || scans.scans[0] != "movies" || scans.scans[1] != "shows" {
		t.Fatalf("scans = %v", scans.scans)
	}

	// Pausing alone changes nothing a scan would find.
	shows := cfg.Libraries["shows"]
	shows.Paused = false
	cfg.Libraries = map[string]config.LibraryConfig{"movies": movies, "shows": shows}
	monitor.reconcileSchedules(schedules, reasons)
	if reasons["shows"] != "" || !schedules["shows"].After(soon) {
		t.Fatalf("an unpaused library was rescanned: due %v reason %q", schedules["shows"], reasons["shows"])
	}

	// A deleted library drops out of every schedule.
	cfg.Libraries = map[string]config.LibraryConfig{"movies": movies}
	monitor.reconcileSchedules(schedules, reasons)
	if _, scheduled := schedules["shows"]; scheduled {
		t.Fatal("a deleted library is still scheduled")
	}
	if _, known := monitor.known["shows"]; known {
		t.Fatal("a deleted library is still known")
	}
}
