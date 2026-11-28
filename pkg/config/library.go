package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/zekurio/anvil/pkg/domain"
)

// FieldProblem is one thing wrong with a library or an Arr connection. Field
// names the setting at fault the way the web API spells it: the JSON path of
// the field, object keys and 0-based list indexes joined by dots, such as
// "ignorable_globs.0" or "rules.2.value". Message is a full sentence for
// the operator that does not repeat the field's name: the web UI shows it
// right under the field.
type FieldProblem struct {
	Field   string
	Message string
}

// WithDefaults fills the settings a library may leave unset: the rule
// fallback, the package mode, and the residue globs.
func (l LibraryConfig) WithDefaults() LibraryConfig {
	l.RuleFallback = NormalizeRuleFallback(l.RuleFallback)
	if l.Rules == nil {
		l.Rules = []Rule{}
	}
	l.PackageMode = orDefault(l.PackageMode, DefaultLibraryPackageMode)
	l.IgnorableGlobs = EffectiveIgnorableGlobs(l.IgnorableGlobs)
	return l
}

// Problems reports every rule a library's own settings break. It does not
// look at other libraries (see LibraryPathConflicts) or at whether the flow
// and Arr connection it names exist, which only the store can tell.
func (l LibraryConfig) Problems() []FieldProblem {
	var found []FieldProblem
	add := func(field, format string, args ...any) {
		found = append(found, FieldProblem{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	switch {
	case strings.TrimSpace(l.Path) == "":
		add("path", "Enter the folder to scan, as an absolute path such as /srv/media/movies.")
	case !filepath.IsAbs(l.Path):
		add("path", "%q is not an absolute path. Enter one that starts with /, such as /srv/media/movies.", l.Path)
	}
	if l.FlowID <= 0 {
		add("flow_id", "Choose a flow.")
	}
	if l.ConcurrencyLimit < 0 {
		add("concurrency_limit", "Enter 0 for no limit, or a whole number of jobs above 0.")
	}
	if l.ScanInterval.Duration < 0 {
		add("scan_interval", "Enter a duration above zero such as \"30m\", or leave it empty for the daemon's default.")
	}
	for _, problem := range ValidateRules(l.Rules, l.RuleFallback) {
		if problem.Index < 0 {
			add("rule_fallback", "%s", sentence(problem.Message))
			continue
		}
		add(fmt.Sprintf("rules.%d.%s", problem.Index, problem.Key), "%s", sentence(problem.Message))
	}
	if l.StableFor.Duration < 0 {
		add("stable_for", "Enter a duration of zero or more, such as \"45s\" or \"5m\".")
	}
	if !slices.Contains(packageModes, l.PackageMode) {
		add("package_mode", "The package mode must be \"auto\", \"directory\", or \"file\", not %q.", l.PackageMode)
	}
	for index, glob := range l.IgnorableGlobs {
		if strings.TrimSpace(glob) == "" || !doublestar.ValidatePattern(filepath.ToSlash(strings.TrimSpace(glob))) {
			add(fmt.Sprintf("ignorable_globs.%d", index), "%q is not a valid glob. Write a pattern such as \"**/*.nfo\" or \"**/sample*/**\".", glob)
		}
	}
	return found
}

// sentence makes a lowercase problem text read as a sentence.
func sentence(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return message
	}
	message = strings.ToUpper(message[:1]) + message[1:]
	if strings.HasSuffix(message, ".") {
		return message
	}
	return message + "."
}

// LibraryPathConflicts reports where a library's folder collides with the
// folders of the other libraries, with the folders flows move results to, or
// with the daemon's temp_dir. The engine assumes they are disjoint: a file
// below two library roots would be queued twice and replaced under the
// second job's feet, and a library around a move destination would scan the
// published files, part files included. temp_dir holds the daemon's own
// working files, search samples among them: a library around or inside it
// would have those scanned as media. An empty tempDir is not compared.
// moveDestinations are the folders of every stored flow's move outputs
// (flow.MoveDestinations); MoveDestinationConflicts checks the same pairs
// from the flow's side.
func LibraryPathConflicts(tempDir string, others map[string]LibraryConfig, moveDestinations []string, candidate LibraryConfig) []FieldProblem {
	var found []FieldProblem
	add := func(field, format string, args ...any) {
		found = append(found, FieldProblem{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	root := filepath.Clean(candidate.Path)
	if tempDir = absoluteTempDir(tempDir); tempDir != "" && pathsOverlap(root, tempDir) {
		add("path", "%s overlaps %s, where anvil keeps its working files (daemon.temp_dir). A library folder must be outside it.", root, tempDir)
	}
	for _, name := range sortedKeys(others) {
		other := others[name]
		if name == candidate.Name {
			continue
		}
		otherRoot := filepath.Clean(other.Path)
		if pathsOverlap(root, otherRoot) {
			add("path", "%s overlaps the library %q at %s. Library folders must not contain each other.", root, name, otherRoot)
		}
	}
	for _, destination := range moveDestinations {
		if destination = filepath.Clean(destination); pathsOverlap(root, destination) {
			add("path", "%s overlaps %s, a folder a flow moves results to. A library folder must be outside every move destination.", root, destination)
		}
	}
	return found
}

// MoveDestinationConflicts reports why a folder cannot be a move
// destination: it overlaps a library folder, whose scan would pick up the
// published files and part files, or the daemon's temp_dir, which the daemon
// cleans up after itself. An empty tempDir is not compared. The messages are
// full sentences for the operator.
func MoveDestinationConflicts(tempDir string, libraries map[string]LibraryConfig, destination string) []string {
	var found []string
	destination = filepath.Clean(destination)
	if tempDir = absoluteTempDir(tempDir); tempDir != "" && pathsOverlap(destination, tempDir) {
		found = append(found, fmt.Sprintf("%s overlaps %s, where anvil keeps its working files (daemon.temp_dir). A move destination must be outside it.", destination, tempDir))
	}
	for _, name := range sortedKeys(libraries) {
		if root := filepath.Clean(libraries[name].Path); pathsOverlap(destination, root) {
			found = append(found, fmt.Sprintf("%s overlaps the library %q at %s. A move destination must be outside every library folder.", destination, name, root))
		}
	}
	return found
}

// absoluteTempDir resolves a relative temp_dir against the working
// directory, as the daemon itself resolves it when it creates the directory.
// An empty one stays empty.
func absoluteTempDir(tempDir string) string {
	if strings.TrimSpace(tempDir) == "" {
		return ""
	}
	if absolute, err := filepath.Abs(tempDir); err == nil {
		tempDir = absolute
	}
	return filepath.Clean(tempDir)
}

// pathsOverlap reports whether two cleaned absolute paths are the same or
// one contains the other. The comparison is lexical, like every other path
// comparison between libraries.
func pathsOverlap(left, right string) bool {
	return pathWithin(left, right) || pathWithin(right, left)
}

func pathWithin(path, root string) bool {
	if path == root {
		return true
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Problems reports every rule an Arr connection breaks.
func (arr ArrConfig) Problems() []FieldProblem {
	var found []FieldProblem
	if !slices.Contains(arrProviders, arr.Type) {
		found = append(found, FieldProblem{Field: "type", Message: fmt.Sprintf("The type must be \"radarr\" or \"sonarr\", not %q.", arr.Type)})
	}
	switch parsed, err := url.Parse(strings.TrimSpace(arr.BaseURL)); {
	case strings.TrimSpace(arr.BaseURL) == "":
		found = append(found, FieldProblem{Field: "base_url", Message: "Enter the address of the server, such as http://localhost:7878."})
	case err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "":
		found = append(found, FieldProblem{Field: "base_url", Message: fmt.Sprintf("%q is not an http or https address. Enter one such as http://localhost:7878.", arr.BaseURL)})
	}
	if strings.TrimSpace(arr.APIKey) == "" && strings.TrimSpace(arr.APIKeyFile) == "" {
		found = append(found, FieldProblem{Field: "api_key", Message: "An API key is required. Enter the key of the Radarr or Sonarr server."})
	}
	if file := strings.TrimSpace(arr.APIKeyFile); file != "" && !filepath.IsAbs(file) {
		found = append(found, FieldProblem{Field: "api_key_file", Message: fmt.Sprintf("%q is not an absolute path. Enter one that starts with /, such as /run/secrets/radarr-api-key.", arr.APIKeyFile)})
	}
	return found
}

// GroupsPackages reports whether each top-level directory is one package
// source. In file mode, and in a zero value, every file is its own source.
func (l LibraryConfig) GroupsPackages() bool {
	return l.PackageMode == string(domain.PackageModeAuto) || l.PackageMode == string(domain.PackageModeDirectory)
}
