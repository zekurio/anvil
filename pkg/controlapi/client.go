package controlapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	httpClient *http.Client
}

func NewClient(socketPath string) (*Client, error) {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		return nil, errors.New("control socket path is required")
	}
	if !filepath.IsAbs(socketPath) {
		return nil, errors.New("control socket path must be absolute")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{httpClient: &http.Client{Transport: transport, Timeout: 15 * time.Second}}, nil
}

func (c *Client) Status(ctx context.Context) (StatusResponse, error) {
	var response StatusResponse
	if err := c.get(ctx, "/v1/status", nil, &response); err != nil {
		return StatusResponse{}, err
	}
	return response, nil
}

func (c *Client) ListJobs(ctx context.Context, query JobQuery) (JobListResponse, error) {
	if _, _, _, err := normalizeJobQuery(query); err != nil {
		return JobListResponse{}, err
	}
	values := make(url.Values)
	if query.Library != "" {
		values.Set("library", query.Library)
	}
	if query.Path != "" {
		values.Set("path", query.Path)
	}
	if query.AbsolutePath != "" {
		values.Set("absolute_path", query.AbsolutePath)
	}
	for _, state := range query.States {
		values.Add("state", state)
	}
	if query.CurrentOnly {
		values.Set("current_only", "true")
	}
	if query.WithSelection {
		values.Set("with_selection", "true")
	}
	if query.Limit > 0 {
		values.Set("limit", strconv.Itoa(query.Limit))
	}
	var response JobListResponse
	if err := c.get(ctx, "/v1/jobs", values, &response); err != nil {
		return JobListResponse{}, err
	}
	return response, nil
}

// CancelJobs requires an explicit selector; the daemon rejects an empty one so
// a mistyped command can never cancel the whole queue.
func (c *Client) CancelJobs(ctx context.Context, request JobCancelRequest) (JobCancelResponse, error) {
	if !request.hasSelector() {
		return JobCancelResponse{}, errors.New("cancel requires at least one selector")
	}
	if _, _, _, err := normalizeJobQuery(request.query()); err != nil {
		return JobCancelResponse{}, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return JobCancelResponse{}, fmt.Errorf("encode cancel request: %w", err)
	}
	var response JobCancelResponse
	if err := c.do(ctx, http.MethodPost, "/v1/jobs/cancel", nil, body, &response); err != nil {
		return JobCancelResponse{}, err
	}
	return response, nil
}

func (c *Client) get(ctx context.Context, requestPath string, query url.Values, target any) error {
	return c.do(ctx, http.MethodGet, requestPath, query, nil, target)
}

func (c *Client) do(ctx context.Context, method string, requestPath string, query url.Values, body []byte, target any) (err error) {
	if c == nil || c.httpClient == nil {
		return errors.New("control API client is required")
	}
	u := url.URL{Scheme: "http", Host: "anvild", Path: requestPath, RawQuery: query.Encode()}
	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), payload)
	if err != nil {
		return fmt.Errorf("build control API request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call control API: %w", err)
	}
	defer func() {
		err = errors.Join(err, response.Body.Close())
	}()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var apiError ErrorResponse
		if err := json.NewDecoder(response.Body).Decode(&apiError); err == nil && apiError.Error.Message != "" {
			return fmt.Errorf("control API %s: %s", apiError.Error.Code, apiError.Error.Message)
		}
		return fmt.Errorf("control API returned %s", response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode control API response: %w", err)
	}
	return nil
}
