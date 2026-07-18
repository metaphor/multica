package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── Test helpers ─────────────────────────────────────────────────────────────

// newTestClient creates a Client pointed at the given httptest server.
func newTestClient(t *testing.T, token string) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, token)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, srv
}

// newFakeServer creates an httptest.Server with a custom mux, leaving
// the caller to register handlers.
func newFakeServer(t *testing.T) (*httptest.Server, *http.ServeMux) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, mux
}

// clientForFake builds a Client pointed at a fake server. Use this when
// you need to register specific handlers on the mux first.
func clientForFake(t *testing.T, srv *httptest.Server, token string) *Client {
	t.Helper()
	c, err := NewClient(srv.URL, token)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// recordHeaderHandler wraps an http.Handler and records every received
// request header name/value pair into a map. Token‑sensitive headers
// (PRIVATE-TOKEN) are captured verbatim for assertion.
type recordHeaderHandler struct {
	inner       http.Handler
	recorded    []headerSnapshot
	recordedSet map[string]struct{}
	mu          sync.Mutex
}

type headerSnapshot struct {
	Method  string
	Headers map[string]string
}

func newRecordedHandler(inner http.Handler) *recordHeaderHandler {
	return &recordHeaderHandler{
		inner:       inner,
		recordedSet: make(map[string]struct{}),
	}
}

func (h *recordHeaderHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	snap := headerSnapshot{Method: r.Method, Headers: make(map[string]string)}
	for k, vs := range r.Header {
		snap.Headers[k] = vs[0]
	}
	h.recorded = append(h.recorded, snap)
	h.recordedSet[r.Method+" "+r.URL.Path] = struct{}{}
	h.mu.Unlock()
	h.inner.ServeHTTP(w, r)
}

func (h *recordHeaderHandler) hasTokenHeader() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, snap := range h.recorded {
		if _, ok := snap.Headers["Private-Token"]; ok {
			return true
		}
	}
	return false
}

// ── NewClient validation ─────────────────────────────────────────────────────

func TestNewClient_NormalizesURL(t *testing.T) {
	tests := []struct {
		name        string
		rawURL      string
		wantBase    string
		wantErr     bool
		errContains string
	}{
		{
			name:     "https scheme",
			rawURL:   "https://gitlab.example.com",
			wantBase: "https://gitlab.example.com/api/v4",
		},
		{
			name:     "http scheme allowed",
			rawURL:   "http://gitlab.internal",
			wantBase: "http://gitlab.internal/api/v4",
		},
		{
			name:     "trailing slash stripped",
			rawURL:   "https://gitlab.example.com/",
			wantBase: "https://gitlab.example.com/api/v4",
		},
		{
			name:     "whitespace trimmed",
			rawURL:   "  https://gitlab.example.com  ",
			wantBase: "https://gitlab.example.com/api/v4",
		},
		{
			name:        "userinfo rejected",
			rawURL:      "http://user:pass@host/x",
			wantErr:     true,
			errContains: "userinfo",
		},
		{
			name:        "ftp scheme rejected",
			rawURL:      "ftp://host",
			wantErr:     true,
			errContains: "unsupported scheme",
		},
		{
			name:        "garbage URL",
			rawURL:      "not-a-url\x00",
			wantErr:     true,
			errContains: "invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClient(tt.rawURL, "tok")
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewClient: unexpected error: %v", err)
			}
			if c.baseURL != tt.wantBase {
				t.Errorf("baseURL = %q, want %q", c.baseURL, tt.wantBase)
			}
		})
	}
}

// ── HTTP behaviour ───────────────────────────────────────────────────────────

func TestClient_SendsPrivateTokenHeader(t *testing.T) {
	const token = "glpat-test-token-123"
	var gotToken atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken.Store(r.Header.Get("PRIVATE-TOKEN"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"username":"alice","name":"Alice"}`)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, token)
	_, _, err := c.ValidateToken(context.Background())
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}

	if got := gotToken.Load(); got != token {
		t.Errorf("PRIVATE-TOKEN = %v, want %q", got, token)
	}
}

func TestClient_ContentTypeJSON(t *testing.T) {
	var gotCT atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT.Store(r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":1}`)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	_, err := c.CreateProjectHook(context.Background(), "org/repo", HookParams{
		URL:                    "https://example.com/hook",
		Token:                  "hook-token",
		MergeRequestsEvents:    true,
		PipelineEvents:         true,
		EnableSSLVerification:  false,
	})
	if err != nil {
		t.Fatalf("CreateProjectHook: %v", err)
	}

	if got := gotCT.Load(); got != "application/json" {
		t.Errorf("Content-Type = %v, want application/json", got)
	}
}

func TestClient_RedirectNotFollowed_TokenNotLeaked(t *testing.T) {
	const token = "tok-redirect-test"

	// Server B — the redirect target. It records every header it sees
	// and must never observe PRIVATE-TOKEN.
	var bGotToken atomic.Value
	bGotToken.Store("") // initialise

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok := r.Header.Get("PRIVATE-TOKEN"); tok != "" {
			bGotToken.Store(tok)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"username":"bob","name":"Bob"}`)
	}))
	t.Cleanup(srvB.Close)

	// Server A — issues a 302 redirect to Server B.
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srvB.URL+"/api/v4/user", http.StatusFound)
	}))
	t.Cleanup(srvA.Close)

	c := clientForFake(t, srvA, token)
	_, _, err := c.ValidateToken(context.Background())
	if err == nil {
		t.Fatal("expected error due to redirect not followed, got nil")
	}

	// The client should have received the 302 and returned it as-is
	// (ErrUseLastResponse). Server B must NOT have seen the token.
	if got := bGotToken.Load().(string); got != "" {
		t.Errorf("Server B received PRIVATE-TOKEN=%q — token was leaked across redirect!", got)
	}
}

func TestClient_TimeoutOnSlowServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(15 * time.Second):
		case <-r.Context().Done():
			return
		}
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	start := time.Now()
	_, _, err := c.ValidateToken(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	// 10s ± 1s
	if elapsed < 9*time.Second || elapsed > 11*time.Second {
		t.Errorf("timeout elapsed %v, want ~10s (±1s)", elapsed)
	}
}

// ── ValidateToken ────────────────────────────────────────────────────────────

func TestValidateToken_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/v4/user" {
			t.Errorf("path = %s, want /api/v4/user", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"username":"alice","name":"Alice Smith"}`)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	username, name, err := c.ValidateToken(context.Background())
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if username != "alice" {
		t.Errorf("username = %q, want alice", username)
	}
	if name != "Alice Smith" {
		t.Errorf("name = %q, want Alice Smith", name)
	}
}

func TestValidateToken_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"401 Unauthorized"}`)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	_, _, err := c.ValidateToken(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("error = %v, want ErrUnauthorized", err)
	}
}

// ── CreateProjectHook ────────────────────────────────────────────────────────

func TestCreateProjectHook_HappyPath(t *testing.T) {
	var rawBody atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		// Path should contain URL-encoded full path.
		// Use EscapedPath() because r.URL.Path auto-decodes %2F to /.
		wantPath := "/api/v4/projects/org%2Fsub%2Frepo/hooks"
		if r.URL.EscapedPath() != wantPath {
			t.Errorf("path = %s, want %s", r.URL.EscapedPath(), wantPath)
		}
		if tok := r.Header.Get("PRIVATE-TOKEN"); tok != "test-token" {
			t.Errorf("PRIVATE-TOKEN = %q, want test-token", tok)
		}

		b, _ := io.ReadAll(r.Body)
		rawBody.Store(string(b))

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":42}`)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "test-token")
	id, err := c.CreateProjectHook(context.Background(), "org/sub/repo", HookParams{
		URL:                    "https://example.com/webhook",
		Token:                  "hook-secret",
		MergeRequestsEvents:    true,
		PipelineEvents:         true,
		EnableSSLVerification:  false,
	})
	if err != nil {
		t.Fatalf("CreateProjectHook: %v", err)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}

	// Assert JSON body content.
	gotBody := rawBody.Load().(string)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(gotBody), &parsed); err != nil {
		t.Fatalf("parse body JSON: %v", err)
	}
	if parsed["url"] != "https://example.com/webhook" {
		t.Errorf("body url = %v", parsed["url"])
	}
	if parsed["merge_requests_events"] != true {
		t.Errorf("body merge_requests_events = %v, want true", parsed["merge_requests_events"])
	}
	if parsed["pipeline_events"] != true {
		t.Errorf("body pipeline_events = %v, want true", parsed["pipeline_events"])
	}
	if parsed["enable_ssl_verification"] != false {
		t.Errorf("body enable_ssl_verification = %v, want false", parsed["enable_ssl_verification"])
	}
}

func TestCreateProjectHook_PathEscaping(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":1}`)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	_, err := c.CreateProjectHook(context.Background(), "org/sub/repo", HookParams{
		URL:  "https://example.com/hook",
		Token: "t",
	})
	if err != nil {
		t.Fatalf("CreateProjectHook: %v", err)
	}
	if gotPath != "/api/v4/projects/org%2Fsub%2Frepo/hooks" {
		t.Errorf("path = %s, want /api/v4/projects/org%%2Fsub%%2Frepo/hooks", gotPath)
	}
}

// ── CreateGroupHook ──────────────────────────────────────────────────────────

func TestCreateGroupHook_HappyPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":7}`)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	id, err := c.CreateGroupHook(context.Background(), "mygroup/subgroup", HookParams{
		URL:                   "https://example.com/hook",
		Token:                 "t",
		MergeRequestsEvents:   true,
		PipelineEvents:        true,
		EnableSSLVerification: true,
	})
	if err != nil {
		t.Fatalf("CreateGroupHook: %v", err)
	}
	if id != 7 {
		t.Errorf("id = %d, want 7", id)
	}
	wantPath := "/api/v4/groups/mygroup%2Fsubgroup/hooks"
	if gotPath != wantPath {
		t.Errorf("path = %s, want %s", gotPath, wantPath)
	}
}

// ── DeleteProjectHook / DeleteGroupHook ──────────────────────────────────────

func TestDeleteProjectHook_HappyPath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	err := c.DeleteProjectHook(context.Background(), "org/repo", 99)
	if err != nil {
		t.Fatalf("DeleteProjectHook: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", gotMethod)
	}
	if gotPath != "/api/v4/projects/org%2Frepo/hooks/99" {
		t.Errorf("path = %s", gotPath)
	}
}

func TestDeleteGroupHook_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not found"}`)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	err := c.DeleteGroupHook(context.Background(), "grp", 1)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// ── PatchHookToken ───────────────────────────────────────────────────────────

func TestPatchHookToken_Project(t *testing.T) {
	var gotMethod, gotPath string
	var rawBody atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		b, _ := io.ReadAll(r.Body)
		rawBody.Store(string(b))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	err := c.PatchHookToken(context.Background(), TargetProject, "org/repo", 123, "new-secret")
	if err != nil {
		t.Fatalf("PatchHookToken: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotPath != "/api/v4/projects/org%2Frepo/hooks/123" {
		t.Errorf("path = %s", gotPath)
	}

	body := rawBody.Load().(string)
	var parsed map[string]string
	json.Unmarshal([]byte(body), &parsed)
	if parsed["token"] != "new-secret" {
		t.Errorf("body token = %q, want new-secret", parsed["token"])
	}
}

func TestPatchHookToken_Group(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	err := c.PatchHookToken(context.Background(), TargetGroup, "mygroup", 7, "rotated")
	if err != nil {
		t.Fatalf("PatchHookToken: %v", err)
	}
	if gotPath != "/api/v4/groups/mygroup/hooks/7" {
		t.Errorf("path = %s", gotPath)
	}
}

// ── GetUser ──────────────────────────────────────────────────────────────────

func TestGetUser_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/users/5" {
			t.Errorf("path = %s, want /api/v4/users/5", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"username":"bob","avatar_url":"https://example.com/avatar.png"}`)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	username, avatarURL, err := c.GetUser(context.Background(), 5)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if username != "bob" {
		t.Errorf("username = %q, want bob", username)
	}
	if avatarURL != "https://example.com/avatar.png" {
		t.Errorf("avatarURL = %q", avatarURL)
	}
}

func TestGetUser_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	_, _, err := c.GetUser(context.Background(), 999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// ── Error mapping ────────────────────────────────────────────────────────────

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    error
		wantIs     error // errors.Is target
	}{
		{
			name:       "403 forbidden",
			statusCode: http.StatusForbidden,
			body:       `{"message":"Forbidden"}`,
			wantIs:     ErrForbidden,
		},
		{
			name:       "404 not found on create",
			statusCode: http.StatusNotFound,
			body:       `{"message":"Not found"}`,
			wantIs:     ErrNotFound,
		},
		{
			name:       "422 unprocessable",
			statusCode: http.StatusUnprocessableEntity,
			body:       `{"message":["url is blocked"]}`,
			wantIs:     ErrUnprocessable,
		},
		{
			name:       "502 bad gateway -> ErrServer",
			statusCode: http.StatusBadGateway,
			body:       `Internal Server Error`,
			wantIs:     new(ErrServer),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(srv.Close)

			c := clientForFake(t, srv, "tok")
			// Use CreateProjectHook to exercise mapError.
			_, err := c.CreateProjectHook(context.Background(), "a/b", HookParams{
				URL:    "https://example.com/hook",
				Token:  "t",
			})

			if err == nil {
				t.Fatal("expected error, got nil")
			}

			switch tt.wantIs.(type) {
			case *ErrServer:
				var srvErr *ErrServer
				if !errors.As(err, &srvErr) {
					t.Errorf("error %v is not *ErrServer", err)
				}
				if srvErr != nil && srvErr.StatusCode != tt.statusCode {
					t.Errorf("ErrServer.StatusCode = %d, want %d", srvErr.StatusCode, tt.statusCode)
				}
			default:
				if !errors.Is(err, tt.wantIs) {
					t.Errorf("error = %v, want errors.Is(…, %v)", err, tt.wantIs)
				}
			}

			// Ensure the error message does NOT contain the token.
			if contains(err.Error(), "tok") {
				t.Errorf("error message %q contains token!", err.Error())
			}
		})
	}
}

func TestErrorBody_Truncated200Bytes(t *testing.T) {
	longMsg := make([]byte, 300)
	for i := range longMsg {
		longMsg[i] = 'x'
	}
	body := fmt.Sprintf(`{"message":"%s"}`, longMsg)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	_, err := c.CreateProjectHook(context.Background(), "a/b", HookParams{
		URL:   "https://example.com/hook",
		Token: "t",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errMsg := err.Error()
	// err.Wrap text + body, total should be reasonable but body portion
	// should be at most ~200 bytes of the JSON.
	if len(errMsg) > 500 {
		t.Errorf("error message too long (%d bytes), body truncation may have failed", len(errMsg))
	}
}

// ── SetBaseURLForTesting ─────────────────────────────────────────────────────

func TestSetBaseURLForTesting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/user" {
			t.Errorf("path = %s, want /custom/user", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"username":"x","name":"X"}`)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	c.SetBaseURLForTesting(srv.URL + "/custom")
	_, _, err := c.ValidateToken(context.Background())
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
}

// ── Helper ───────────────────────────────────────────────────────────────────

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
