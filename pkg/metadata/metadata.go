// Package metadata resolves per-job metadata from external library managers.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/language"
)

const defaultHTTPTimeout = 15 * time.Second

type Resolver struct {
	Client *http.Client
}

func (r Resolver) ResolveJobMetadata(ctx context.Context, library domain.Library, source domain.MediaSource, asset domain.MediaAsset, inputPath string) (domain.JobMetadata, error) {
	switch library.Metadata.Provider {
	case domain.MetadataProviderNone:
		return domain.JobMetadata{}, nil
	case domain.MetadataProviderRadarr:
		return r.resolveRadarr(ctx, library.Metadata, inputPath, source, asset)
	case domain.MetadataProviderSonarr:
		return r.resolveSonarr(ctx, library.Metadata, inputPath, source, asset)
	default:
		return domain.JobMetadata{}, fmt.Errorf("unsupported metadata provider %q", library.Metadata.Provider)
	}
}

func (r Resolver) resolveRadarr(ctx context.Context, policy domain.MetadataProviderPolicy, inputPath string, source domain.MediaSource, asset domain.MediaAsset) (domain.JobMetadata, error) {
	metadata, found, err := r.resolveArrList(ctx, policy, "/api/v3/movie", map[string]string{"includeMovieFile": "true"}, inputPath)
	if err != nil || found {
		return metadata, err
	}
	metadata, found, err = r.resolveArrParse(ctx, policy, domain.MetadataProviderRadarr, inputPath, releaseTitleCandidates(source, asset, inputPath))
	if err != nil || found {
		return metadata, err
	}
	return noArrMatchMetadata(domain.MetadataProviderRadarr), nil
}

func (r Resolver) resolveSonarr(ctx context.Context, policy domain.MetadataProviderPolicy, inputPath string, source domain.MediaSource, asset domain.MediaAsset) (domain.JobMetadata, error) {
	metadata, found, err := r.resolveArrList(ctx, policy, "/api/v3/series", nil, inputPath)
	if err != nil || found {
		return metadata, err
	}
	metadata, found, err = r.resolveArrParse(ctx, policy, domain.MetadataProviderSonarr, inputPath, releaseTitleCandidates(source, asset, inputPath))
	if err != nil || found {
		return metadata, err
	}
	return noArrMatchMetadata(domain.MetadataProviderSonarr), nil
}

func (r Resolver) resolveArrList(ctx context.Context, policy domain.MetadataProviderPolicy, apiPath string, query map[string]string, inputPath string) (domain.JobMetadata, bool, error) {
	var items []arrItem
	if err := r.getJSON(ctx, policy, apiPath, query, &items); err != nil {
		return domain.JobMetadata{}, false, err
	}
	item := bestMatch(items, inputPath)
	if item == nil {
		return domain.JobMetadata{}, false, nil
	}
	return domain.JobMetadata{
		OriginalLanguage: parseLanguage(item.OriginalLanguage),
	}, true, nil
}

func (r Resolver) resolveArrParse(ctx context.Context, policy domain.MetadataProviderPolicy, provider domain.MetadataProviderKind, inputPath string, titles []string) (domain.JobMetadata, bool, error) {
	for _, title := range titles {
		query := map[string]string{"title": title}
		if provider == domain.MetadataProviderSonarr && inputPath != "" {
			query["path"] = inputPath
		}
		var parsed arrParseResult
		if err := r.getJSON(ctx, policy, "/api/v3/parse", query, &parsed); err != nil {
			return domain.JobMetadata{}, false, err
		}
		item := parsed.Item(provider)
		if item == nil {
			continue
		}
		if original := parseLanguage(item.OriginalLanguage); original != "" {
			return domain.JobMetadata{OriginalLanguage: original}, true, nil
		}
	}
	return domain.JobMetadata{}, false, nil
}

func (r Resolver) getJSON(ctx context.Context, policy domain.MetadataProviderPolicy, apiPath string, query map[string]string, target any) error {
	if strings.TrimSpace(policy.BaseURL) == "" {
		return errors.New("metadata base URL is required")
	}
	apiKey, err := apiKey(policy)
	if err != nil {
		return err
	}
	endpoint, err := arrURL(policy.BaseURL, apiPath, query)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create metadata request: %w", err)
	}
	req.Header.Set("X-Api-Key", apiKey)

	resp, err := r.client().Do(req)
	if err != nil {
		return fmt.Errorf("fetch metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fetch metadata: unexpected status %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("decode metadata response: %w", err)
	}
	return nil
}

func (r Resolver) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: defaultHTTPTimeout}
}

func apiKey(policy domain.MetadataProviderPolicy) (string, error) {
	if strings.TrimSpace(policy.APIKeyFile) != "" {
		data, err := os.ReadFile(policy.APIKeyFile)
		if err != nil {
			return "", fmt.Errorf("read metadata API key file: %w", err)
		}
		key := strings.TrimSpace(string(data))
		if key == "" {
			return "", fmt.Errorf("metadata API key file %q is empty", policy.APIKeyFile)
		}
		return key, nil
	}
	key := strings.TrimSpace(policy.APIKey)
	if key == "" {
		return "", errors.New("metadata API key is required")
	}
	return key, nil
}

type arrItem struct {
	Path             string          `json:"path"`
	OriginalLanguage json.RawMessage `json:"originalLanguage"`
	MovieFile        *arrFile        `json:"movieFile"`
}

type arrFile struct {
	Path string `json:"path"`
}

type arrParseResult struct {
	Movie  *arrItem `json:"movie"`
	Series *arrItem `json:"series"`
}

func (r arrParseResult) Item(provider domain.MetadataProviderKind) *arrItem {
	switch provider {
	case domain.MetadataProviderRadarr:
		return r.Movie
	case domain.MetadataProviderSonarr:
		return r.Series
	default:
		return nil
	}
}

func noArrMatchMetadata(provider domain.MetadataProviderKind) domain.JobMetadata {
	return domain.JobMetadata{
		StreamCleanupDisabled:       true,
		StreamCleanupDisabledReason: fmt.Sprintf("%s metadata did not match input path or release title", provider),
	}
}

func arrURL(baseURL string, apiPath string, query map[string]string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse metadata base URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("metadata base URL %q must include scheme and host", baseURL)
	}
	parsed.Path = pathpkg.Join(parsed.Path, apiPath)
	values := parsed.Query()
	for key, value := range query {
		values.Set(key, value)
	}
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

func releaseTitleCandidates(source domain.MediaSource, asset domain.MediaAsset, inputPath string) []string {
	var candidates []string
	for _, value := range []string{
		source.RelativePath,
		asset.RelativePath,
		fileTitle(asset.RelativePath),
		fileTitle(source.RelativePath),
		fileTitle(inputPath),
		fileTitle(filepath.Dir(inputPath)),
	} {
		value = cleanReleaseTitle(value)
		if value != "" && !slices.Contains(candidates, value) {
			candidates = append(candidates, value)
		}
	}
	return candidates
}

func fileTitle(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	base := filepath.Base(filepath.Clean(value))
	ext := filepath.Ext(base)
	if ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}

func cleanReleaseTitle(value string) string {
	value = strings.TrimSpace(filepath.ToSlash(value))
	value = strings.Trim(value, "/")
	if value == "." || value == "" {
		return ""
	}
	return value
}

func bestMatch(items []arrItem, inputPath string) *arrItem {
	bestIndex := -1
	bestScore := -1
	for i := range items {
		for _, candidate := range itemPaths(items[i]) {
			score := pathScore(candidate, inputPath)
			if score > bestScore {
				bestIndex = i
				bestScore = score
			}
		}
	}
	if bestIndex < 0 {
		return nil
	}
	return &items[bestIndex]
}

func itemPaths(item arrItem) []string {
	paths := []string{item.Path}
	if item.MovieFile != nil {
		paths = append(paths, item.MovieFile.Path)
	}
	return paths
}

func pathScore(candidate string, inputPath string) int {
	candidate = cleanPath(candidate)
	inputPath = cleanPath(inputPath)
	if candidate == "" || inputPath == "" {
		return -1
	}
	if candidate == inputPath {
		return 1_000_000 + len(candidate)
	}
	if inside(candidate, inputPath) {
		return len(candidate)
	}
	return -1
}

func cleanPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func inside(root string, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

func parseLanguage(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return language.Normalize(text)
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	for _, key := range []string{"isoCode", "code", "name", "nameLower"} {
		value, _ := object[key].(string)
		if normalized := language.Normalize(value); normalized != "" {
			return normalized
		}
	}
	return ""
}
