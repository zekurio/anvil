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
// "download.handoff_path" or "rules.2.value". Message is a full sentence for
// the operator that does not repeat the field's name: the web UI shows it
// right under the field.
type FieldProblem struct {
	Field   string
	Message string
}

// WithDefaults fills the settings a library may leave unset: the rule
// fallback, the replacement mode of a media library, and the modes, quiet
// period, and residue globs of a download library. Settings of the other
// kind are cleared, so a stored library never carries values that do not
// apply to it.
func (l LibraryConfig) WithDefaults() LibraryConfig {
	l.RuleFallback = NormalizeRuleFallback(l.RuleFallback)
	if l.Rules == nil {
		l.Rules = []Rule{}
	}
	if !l.IsDownload() {
		l.Media.ReplacementMode = orDefault(l.Media.ReplacementMode, DefaultReplacementMode)
		l.Download = DownloadLibraryConfig{}
		return l
	}
	l.Media = MediaLibraryConfig{}
	l.Download.PackageMode = orDefault(l.Download.PackageMode, DefaultPackageMode)
	l.Download.HandoffMode = orDefault(l.Download.HandoffMode, DefaultHandoffMode)
	l.Download.IgnorableGlobs = EffectiveIgnorableGlobs(l.Download.IgnorableGlobs)
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
	if !slices.Contains(libraryKinds, l.Kind) {
		add("kind", "The kind must be \"media\" or \"download\", not %q.", l.Kind)
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
	if !l.IsDownload() {
		if !slices.Contains(replacementModes, l.Media.ReplacementMode) {
			add("media.replacement_mode", "The replacement mode must be \"replace\" or \"copy\", not %q.", l.Media.ReplacementMode)
		}
		return found
	}

	download := l.Download
	switch {
	case strings.TrimSpace(download.HandoffPath) == "":
		add("download.handoff_path", "A download library needs a handoff folder, the folder its importer watches. Enter an absolute path such as /srv/imports/tv.")
	case !filepath.IsAbs(download.HandoffPath):
		add("download.handoff_path", "%q is not an absolute path. Enter one that starts with /, such as /srv/imports/tv.", download.HandoffPath)
	}
	if download.StableFor.Duration < 0 {
		add("download.stable_for", "Enter a duration of zero or more, such as \"45s\" or \"5m\".")
	}
	if !slices.Contains(packageModes, download.PackageMode) {
		add("download.package_mode", "The package mode must be \"auto\", \"directory\", or \"file\", not %q.", download.PackageMode)
	}
	if !slices.Contains(handoffModes, download.HandoffMode) {
		add("download.handoff_mode", "The handoff mode must be \"copy\" or \"move\", not %q.", download.HandoffMode)
	}
	for index, glob := range download.IgnorableGlobs {
		if strings.TrimSpace(glob) == "" || !doublestar.ValidatePattern(filepath.ToSlash(strings.TrimSpace(glob))) {
			add(fmt.Sprintf("download.ignorable_globs.%d", index), "%q is not a valid glob. Write a pattern such as \"**/*.nfo\" or \"**/sample*/**\".", glob)
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

// LibraryPathConflicts reports where a library's directories collide with
// those of the other libraries. The engine assumes they are disjoint: a file
// below two library roots would be queued twice and replaced under the
// second job's feet, and a handoff directory inside a library root would be
// scanned as that library's media, part files included.
func LibraryPathConflicts(others map[string]LibraryConfig, candidate LibraryConfig) []FieldProblem {
	var found []FieldProblem
	add := func(field, format string, args ...any) {
		found = append(found, FieldProblem{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	root := filepath.Clean(candidate.Path)
	handoff := ""
	if candidate.IsDownload() && strings.TrimSpace(candidate.Download.HandoffPath) != "" {
		handoff = filepath.Clean(candidate.Download.HandoffPath)
		if pathsOverlap(root, handoff) {
			add("download.handoff_path", "%s overlaps the library's own folder %s. The importer's folder must be outside the scanned one.", handoff, root)
		}
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
		if handoff != "" && pathsOverlap(handoff, otherRoot) {
			add("download.handoff_path", "%s overlaps the library %q at %s. A handoff folder must be outside every library folder.", handoff, name, otherRoot)
		}
		if other.IsDownload() && strings.TrimSpace(other.Download.HandoffPath) != "" {
			if otherHandoff := filepath.Clean(other.Download.HandoffPath); pathsOverlap(root, otherHandoff) {
				add("path", "%s overlaps %s, the handoff folder of the library %q. A library folder must be outside every handoff folder.", root, otherHandoff, name)
			}
		}
	}
	return found
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

// IsDownload reports whether the library processes downloader output and
// hands it off instead of replacing media in place.
func (l LibraryConfig) IsDownload() bool {
	return l.Kind == string(domain.LibraryKindDownload)
}

// GroupsPackages reports whether each top-level directory is one package
// source. Media libraries and file-mode downloads treat every file as its own
// source.
func (l LibraryConfig) GroupsPackages() bool {
	return l.IsDownload() && l.Download.PackageMode != string(domain.DownloadPackageModeFile)
}
