// Package gitlab provides a redirect-safe REST client for the GitLab API v4.
//
// The client authenticates via PRIVATE-TOKEN header and intentionally
// refuses to follow HTTP redirects — this prevents the token from being
// leaked to a cross-host redirect target.
//
// API base URL is injectable via SetBaseURLForTesting so callers can
// point the client at an httptest server.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ── Enums ────────────────────────────────────────────────────────────────────

// TargetType identifies which GitLab resource a hook belongs to.
type TargetType string

const (
	TargetProject TargetType = "project"
	TargetGroup   TargetType = "group"
)

// ── Parameter types ──────────────────────────────────────────────────────────

// HookParams carries the configuration for a GitLab webhook.
//
// MergeRequestsEvents and PipelineEvents should be set to true by the
// caller; the client serialises whatever values it receives.
type HookParams struct {
	URL                    string `json:"url"`
	Token                  string `json:"token"`
	MergeRequestsEvents    bool   `json:"merge_requests_events"`
	PipelineEvents         bool   `json:"pipeline_events"`
	EnableSSLVerification  bool   `json:"enable_ssl_verification"`
}

// ── Client ───────────────────────────────────────────────────────────────────

// Client is a redirect-safe GitLab REST API v4 client.
//
// The zero value is not usable; construct via NewClient.
type Client struct {
	token      string
	httpClient *http.Client
	baseURL    string // API base; injectable for tests.
}

// NewClient validates an instance URL and token and returns a ready Client.
//
// Validation rules:
//   - scheme must be http or https
//   - URL must not contain userinfo (e.g. "http://user:pass@host")
//   - trailing "/" is stripped
func NewClient(instanceURL, token string) (*Client, error) {
	instanceURL = strings.TrimSpace(instanceURL)
	instanceURL = strings.TrimSuffix(instanceURL, "/")

	u, err := url.Parse(instanceURL)
	if err != nil {
		return nil, fmt.Errorf("gitlab: invalid instance URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("gitlab: unsupported scheme %q, must be http or https", u.Scheme)
	}
	if u.User != nil {
		return nil, errors.New("gitlab: instance URL must not contain userinfo")
	}

	return &Client{
		token: token,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			// Never follow redirects — the PRIVATE-TOKEN header would be
			// forwarded to the redirect target, potentially leaking the
			// token to an untrusted host.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		baseURL: instanceURL + "/api/v4",
	}, nil
}

// SetBaseURLForTesting overrides the API base URL computed from the
// instance URL. Intended for tests that point the client at an
// httptest server.
func (c *Client) SetBaseURLForTesting(u string) {
	c.baseURL = u
}

// ── API methods ──────────────────────────────────────────────────────────────

// ValidateToken checks that the configured token is valid by calling
// GET /api/v4/user. Returns the authenticated user's username and
// display name on success.
func (c *Client) ValidateToken(ctx context.Context) (username, name string, err error) {
	resp, err := c.do(ctx, http.MethodGet, "/user", nil)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", "", ErrUnauthorized
	}
	if err := c.mapError(resp); err != nil {
		return "", "", err
	}

	var body struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", fmt.Errorf("gitlab: decode user response: %w", err)
	}
	return body.Username, body.Name, nil
}

// CreateProjectHook registers a webhook on a GitLab project.
// projectFullPath is the URL-encoded full path (e.g. "org/sub/repo").
func (c *Client) CreateProjectHook(ctx context.Context, projectFullPath string, p HookParams) (int64, error) {
	return c.createHook(ctx, TargetProject, projectFullPath, p)
}

// CreateGroupHook registers a webhook on a GitLab group.
// groupFullPath is the URL-encoded full path (e.g. "org/subgroup").
func (c *Client) CreateGroupHook(ctx context.Context, groupFullPath string, p HookParams) (int64, error) {
	return c.createHook(ctx, TargetGroup, groupFullPath, p)
}

// createHook is the shared implementation for CreateProjectHook and
// CreateGroupHook.
func (c *Client) createHook(ctx context.Context, targetType TargetType, fullPath string, p HookParams) (int64, error) {
	escaped := url.PathEscape(fullPath)
	path := fmt.Sprintf("/%ss/%s/hooks", targetType, escaped)

	resp, err := c.do(ctx, http.MethodPost, path, p)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if err := c.mapError(resp); err != nil {
		return 0, err
	}

	var body struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("gitlab: decode hook response: %w", err)
	}
	return body.ID, nil
}

// DeleteProjectHook removes a webhook from a GitLab project.
func (c *Client) DeleteProjectHook(ctx context.Context, projectFullPath string, hookID int64) error {
	return c.deleteHook(ctx, TargetProject, projectFullPath, hookID)
}

// DeleteGroupHook removes a webhook from a GitLab group.
func (c *Client) DeleteGroupHook(ctx context.Context, groupFullPath string, hookID int64) error {
	return c.deleteHook(ctx, TargetGroup, groupFullPath, hookID)
}

// deleteHook is the shared implementation for DeleteProjectHook and
// DeleteGroupHook.
func (c *Client) deleteHook(ctx context.Context, targetType TargetType, fullPath string, hookID int64) error {
	escaped := url.PathEscape(fullPath)
	path := fmt.Sprintf("/%ss/%s/hooks/%d", targetType, escaped, hookID)

	resp, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return c.mapError(resp)
}

// PatchHookToken updates the secret token for an existing webhook.
//
// targetType determines whether the hook belongs to a project or group.
// targetPath is the full path (e.g. "org/sub/repo").
func (c *Client) PatchHookToken(ctx context.Context, targetType TargetType, targetPath string, hookID int64, newToken string) error {
	escaped := url.PathEscape(targetPath)
	path := fmt.Sprintf("/%ss/%s/hooks/%d", targetType, escaped, hookID)

	body := map[string]string{"token": newToken}
	resp, err := c.do(ctx, http.MethodPut, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return c.mapError(resp)
}

// GetUser fetches a GitLab user by numeric ID.
// Returns the username and avatar URL.
func (c *Client) GetUser(ctx context.Context, id int64) (username, avatarURL string, err error) {
	path := "/users/" + strconv.FormatInt(id, 10)
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", "", ErrNotFound
	}
	if err := c.mapError(resp); err != nil {
		return "", "", err
	}

	var body struct {
		Username  string `json:"username"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", fmt.Errorf("gitlab: decode user response: %w", err)
	}
	return body.Username, body.AvatarURL, nil
}

// ── Internal helpers ─────────────────────────────────────────────────────────

// do performs an HTTP request authenticated with PRIVATE-TOKEN and
// returns the raw response. The caller MUST close resp.Body.
func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	u := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("gitlab: marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("gitlab: build request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.httpClient.Do(req)
}

// mapError maps an HTTP response to a typed sentinel error. It reads at
// most 200 bytes of the response body to provide a debug message without
// exposing the private token. Call this after checking for status-code-
// specific sentinels (401, 404) that need custom handling.
func (c *Client) mapError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusUnprocessableEntity:
		body := readErrorBody(resp)
		return fmt.Errorf("%w: %s", ErrUnprocessable, body)
	default:
		if resp.StatusCode >= 500 {
			body := readErrorBody(resp)
			return &ErrServer{StatusCode: resp.StatusCode, Body: body}
		}
		body := readErrorBody(resp)
		return fmt.Errorf("gitlab: unexpected status %d: %s", resp.StatusCode, body)
	}
}

// readErrorBody reads at most 200 bytes from the response body for
// inclusion in an error message. The token is never present in the body
// (it is sent as a header), but we defensively strip it just in case.
func readErrorBody(resp *http.Response) string {
	if resp.Body == nil {
		return "(no body)"
	}
	// Read at most 200 bytes.
	buf := make([]byte, 200)
	n, err := resp.Body.Read(buf)
	if err != nil && err != io.EOF {
		return "(cannot read body)"
	}
	return string(buf[:n])
}

// ── Sentinel errors ──────────────────────────────────────────────────────────

var (
	ErrUnauthorized  = errors.New("gitlab: unauthorized (401)")
	ErrForbidden     = errors.New("gitlab: forbidden (403)")
	ErrNotFound      = errors.New("gitlab: not found (404)")
	ErrUnprocessable = errors.New("gitlab: unprocessable (422)")
)

// ErrServer is returned for 5xx responses.
type ErrServer struct {
	StatusCode int
	Body       string
}

func (e *ErrServer) Error() string {
	return fmt.Sprintf("gitlab: server error %d: %s", e.StatusCode, e.Body)
}
