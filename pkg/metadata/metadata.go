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
	"strings"
	"time"

	"github.com/zekurio/anvil/pkg/domain"
	"github.com/zekurio/anvil/pkg/language"
)

const defaultHTTPTimeout = 15 * time.Second

type Resolver struct {
	Client *http.Client
}

func (r Resolver) ResolveJobMetadata(ctx context.Context, library domain.Library, _ domain.MediaSource, _ domain.MediaAsset, inputPath string) (domain.JobMetadata, error) {
	switch library.Metadata.Provider {
	case domain.MetadataProviderNone:
		return domain.JobMetadata{}, nil
	case domain.MetadataProviderRadarr:
		return r.resolveArr(ctx, library.Metadata, "/api/v3/movie", map[string]string{"includeMovieFile": "true"}, inputPath)
	case domain.MetadataProviderSonarr:
		return r.resolveArr(ctx, library.Metadata, "/api/v3/series", nil, inputPath)
	default:
		return domain.JobMetadata{}, fmt.Errorf("unsupported metadata provider %q", library.Metadata.Provider)
	}
}

func (r Resolver) resolveArr(ctx context.Context, policy domain.MetadataProviderPolicy, apiPath string, query map[string]string, inputPath string) (domain.JobMetadata, error) {
	if strings.TrimSpace(policy.BaseURL) == "" {
		return domain.JobMetadata{}, errors.New("metadata base URL is required")
	}
	apiKey, err := apiKey(policy)
	if err != nil {
		return domain.JobMetadata{}, err
	}
	endpoint, err := arrURL(policy.BaseURL, apiPath, query)
	if err != nil {
		return domain.JobMetadata{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return domain.JobMetadata{}, fmt.Errorf("create metadata request: %w", err)
	}
	req.Header.Set("X-Api-Key", apiKey)

	resp, err := r.client().Do(req)
	if err != nil {
		return domain.JobMetadata{}, fmt.Errorf("fetch metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domain.JobMetadata{}, fmt.Errorf("fetch metadata: unexpected status %s", resp.Status)
	}

	var items []arrItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return domain.JobMetadata{}, fmt.Errorf("decode metadata response: %w", err)
	}
	item := bestMatch(items, inputPath)
	if item == nil {
		return domain.JobMetadata{}, nil
	}
	return domain.JobMetadata{
		OriginalLanguage: parseLanguage(item.OriginalLanguage),
	}, nil
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
