package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	gl "github.com/multica-ai/multica/server/internal/integrations/gitlab"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ── Test helpers ─────────────────────────────────────────────────────────────

func fakeGitLabAPIServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func captureHookBody(r *http.Request) []byte {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	return body
}

func seedGitLabHooksConnection(t *testing.T, fakeURL string, hooks []byte) db.GithubInstallation {
	t.Helper()
	if testHandler.GitLabBox == nil {
		t.Fatal("GitLabBox not set; call setupGitLabBox(t) first")
	}

	wsUUID := parseUUID(testWorkspaceID)

	secretHex := hex.EncodeToString(make([]byte, 32))
	secretCiphertext, err := testHandler.GitLabBox.Seal([]byte(secretHex))
	if err != nil {
		t.Fatalf("Seal(secret): %v", err)
	}
	hash := sha256.Sum256([]byte(secretHex))

	tokenCiphertext, err := testHandler.GitLabBox.Seal([]byte("glpat-fake-token"))
	if err != nil {
		t.Fatalf("Seal(token): %v", err)
	}

	if hooks == nil {
		hooks = []byte("[]")
	}

	conn, err := testHandler.Queries.InsertGitLabConnection(context.Background(), db.InsertGitLabConnectionParams{
		WorkspaceID:             wsUUID,
		AccountLogin:            "test-user",
		DisplayName:             pgtype.Text{String: "Test User", Valid: true},
		InstanceUrl:             pgtype.Text{String: fakeURL, Valid: true},
		AccessTokenCiphertext:   tokenCiphertext,
		WebhookSecretHash:       hash[:],
		WebhookSecretCiphertext: secretCiphertext,
		Hooks:                   hooks,
	})
	if err != nil {
		t.Fatalf("InsertGitLabConnection: %v", err)
	}
	t.Cleanup(func() {
		testHandler.Queries.DeleteGitLabConnection(context.Background(), db.DeleteGitLabConnectionParams{
			ID:          conn.ID,
			WorkspaceID: wsUUID,
		})
	})
	return conn
}

func newGitLabHooksRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	r.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner", "admin"))
			r.Post("/gitlab/connections/{connectionID}/hooks", h.AddGitLabHookTarget)
			r.Delete("/gitlab/connections/{connectionID}/hooks", h.RemoveGitLabHookTarget)
		})
	})
	return r
}

func hooksRouterRequest(method, path, bodyStr, userID string) *http.Request {
	var body io.Reader
	if bodyStr != "" {
		body = strings.NewReader(bodyStr)
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	return req
}

func setPublicURL(t *testing.T, url string) {
	t.Helper()
	prev := testHandler.cfg.PublicURL
	testHandler.cfg.PublicURL = url
	t.Cleanup(func() { testHandler.cfg.PublicURL = prev })
}

// ── Happy‑path tests ─────────────────────────────────────────────────────────

func TestAddGitLabHookTarget_Project_CreatesHookOnFakeAPI(t *testing.T) {
	setupGitLabBox(t)
	setPublicURL(t, "https://multica.example")
	adminID := seedMember(t, "admin")

	var capturedMethod, capturedPath string
	var capturedBody []byte
	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		capturedBody = captureHookBody(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id": 42}`)
	})

	conn := seedGitLabHooksConnection(t, fakeAPI.URL, nil)

	router := newGitLabHooksRouter(testHandler)
	req := hooksRouterRequest("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
		`{"target_type":"project","target_path":"org/repo"}`,
		adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	if capturedMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", capturedMethod)
	}
	wantPath := "/api/v4/projects/org/repo/hooks"
	if capturedPath != wantPath {
		t.Errorf("path = %s, want %s", capturedPath, wantPath)
	}

	var sentParams gl.HookParams
	if err := json.Unmarshal(capturedBody, &sentParams); err != nil {
		t.Fatalf("failed to decode sent params: %v", err)
	}
	if sentParams.URL != "https://multica.example/api/webhooks/gitlab" {
		t.Errorf("hook URL = %q, want %q", sentParams.URL, "https://multica.example/api/webhooks/gitlab")
	}
	if sentParams.Token == "" {
		t.Error("hook token is empty")
	}
	if !sentParams.MergeRequestsEvents {
		t.Error("merge_requests_events should be true")
	}
	if !sentParams.PipelineEvents {
		t.Error("pipeline_events should be true")
	}
	if !sentParams.EnableSSLVerification {
		t.Error("enable_ssl_verification should be true for https PublicURL")
	}

	var hooks []hookRecord
	if err := json.NewDecoder(rr.Body).Decode(&hooks); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook in response, got %d", len(hooks))
	}
	if hooks[0].HookID != 42 {
		t.Errorf("hook_id = %d, want 42", hooks[0].HookID)
	}
	if hooks[0].TargetType != "project" {
		t.Errorf("target_type = %q, want project", hooks[0].TargetType)
	}
	if hooks[0].TargetPath != "org/repo" {
		t.Errorf("target_path = %q, want org/repo", hooks[0].TargetPath)
	}

	updated, err := testHandler.Queries.GetGitLabConnectionByID(context.Background(), db.GetGitLabConnectionByIDParams{
		ID:          conn.ID,
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("GetGitLabConnectionByID after POST: %v", err)
	}
	dbHooks, err := decodeHooksJSON(updated.Hooks)
	if err != nil {
		t.Fatalf("decodeHooksJSON on DB row: %v", err)
	}
	if len(dbHooks) != 1 || dbHooks[0].HookID != 42 {
		t.Errorf("DB hooks = %+v, want 1 entry with hook_id=42", dbHooks)
	}
}

func TestAddGitLabHookTarget_HTTPPublicURL_DisablesSSLVerification(t *testing.T) {
	setupGitLabBox(t)
	setPublicURL(t, "http://localhost:8080")
	adminID := seedMember(t, "admin")

	var capturedBody []byte
	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		capturedBody = captureHookBody(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id": 99}`)
	})

	conn := seedGitLabHooksConnection(t, fakeAPI.URL, nil)

	router := newGitLabHooksRouter(testHandler)
	req := hooksRouterRequest("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
		`{"target_type":"project","target_path":"org/repo"}`,
		adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var sentParams gl.HookParams
	if err := json.Unmarshal(capturedBody, &sentParams); err != nil {
		t.Fatalf("failed to decode sent params: %v", err)
	}
	if sentParams.EnableSSLVerification {
		t.Error("enable_ssl_verification should be false for http PublicURL")
	}
}

func TestRemoveGitLabHookTarget_DeletesRemoteAndLocal(t *testing.T) {
	setupGitLabBox(t)
	setPublicURL(t, "https://multica.example")
	adminID := seedMember(t, "admin")

	var deletedMethod, deletedPath string
	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletedMethod = r.Method
			deletedPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id": 77}`)
	})

	existing := []hookRecord{{
		TargetType: "project",
		TargetPath: "org/repo",
		HookID:     77,
		URL:        "https://multica.example/api/webhooks/gitlab",
		CreatedAt:  "2025-01-01T00:00:00Z",
	}}
	raw, _ := json.Marshal(existing)
	conn := seedGitLabHooksConnection(t, fakeAPI.URL, raw)

	router := newGitLabHooksRouter(testHandler)
	req := hooksRouterRequest("DELETE",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
		`{"target_type":"project","target_path":"org/repo"}`,
		adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	if deletedMethod != http.MethodDelete {
		t.Errorf("delete method = %q, want DELETE", deletedMethod)
	}
	wantPath := "/api/v4/projects/org/repo/hooks/77"
	if deletedPath != wantPath {
		t.Errorf("delete path = %q, want %q", deletedPath, wantPath)
	}

	updated, err := testHandler.Queries.GetGitLabConnectionByID(context.Background(), db.GetGitLabConnectionByIDParams{
		ID:          conn.ID,
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("GetGitLabConnectionByID after DELETE: %v", err)
	}
	dbHooks, err := decodeHooksJSON(updated.Hooks)
	if err != nil {
		t.Fatalf("decodeHooksJSON on DB row: %v", err)
	}
	if len(dbHooks) != 0 {
		t.Errorf("DB hooks = %+v, want empty", dbHooks)
	}
}

func TestRemoveGitLabHookTarget_ToleratesRemote404(t *testing.T) {
	setupGitLabBox(t)
	setPublicURL(t, "https://multica.example")
	adminID := seedMember(t, "admin")

	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"404 Not found"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id": 77}`)
	})

	existing := []hookRecord{{
		TargetType: "project",
		TargetPath: "org/repo",
		HookID:     77,
		URL:        "https://multica.example/api/webhooks/gitlab",
		CreatedAt:  "2025-01-01T00:00:00Z",
	}}
	raw, _ := json.Marshal(existing)
	conn := seedGitLabHooksConnection(t, fakeAPI.URL, raw)

	router := newGitLabHooksRouter(testHandler)
	req := hooksRouterRequest("DELETE",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
		`{"target_type":"project","target_path":"org/repo"}`,
		adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204 (404 tolerated), got %d: %s", rr.Code, rr.Body.String())
	}

	updated, _ := testHandler.Queries.GetGitLabConnectionByID(context.Background(), db.GetGitLabConnectionByIDParams{
		ID:          conn.ID,
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	dbHooks, _ := decodeHooksJSON(updated.Hooks)
	if len(dbHooks) != 0 {
		t.Errorf("DB hooks should be empty after removal despite remote 404, got %+v", dbHooks)
	}
}

// ── Failure tests ────────────────────────────────────────────────────────────

func TestAddGitLabHookTarget_MissingPublicURL_Returns503(t *testing.T) {
	setupGitLabBox(t)
	setPublicURL(t, "")
	adminID := seedMember(t, "admin")

	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	conn := seedGitLabHooksConnection(t, fakeAPI.URL, nil)

	router := newGitLabHooksRouter(testHandler)
	req := hooksRouterRequest("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
		`{"target_type":"project","target_path":"org/repo"}`,
		adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "MULTICA_PUBLIC_URL") {
		t.Errorf("error body should mention MULTICA_PUBLIC_URL, got: %s", rr.Body.String())
	}
}

func TestAddGitLabHookTarget_Group_Forbidden_Returns422(t *testing.T) {
	setupGitLabBox(t)
	setPublicURL(t, "https://multica.example")
	adminID := seedMember(t, "admin")

	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"403 Forbidden"}`)
	})

	conn := seedGitLabHooksConnection(t, fakeAPI.URL, nil)

	router := newGitLabHooksRouter(testHandler)
	req := hooksRouterRequest("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
		`{"target_type":"group","target_path":"our-group"}`,
		adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d: %s", rr.Code, rr.Body.String())
	}
	errBody := rr.Body.String()
	if !strings.Contains(errBody, "Owner role") || !strings.Contains(errBody, "Premium tier") {
		t.Errorf("error body should mention Owner/Premium, got: %s", errBody)
	}
}

func TestAddGitLabHookTarget_Project_Forbidden_Returns422(t *testing.T) {
	setupGitLabBox(t)
	setPublicURL(t, "https://multica.example")
	adminID := seedMember(t, "admin")

	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"403 Forbidden"}`)
	})

	conn := seedGitLabHooksConnection(t, fakeAPI.URL, nil)

	router := newGitLabHooksRouter(testHandler)
	req := hooksRouterRequest("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
		`{"target_type":"project","target_path":"org/repo"}`,
		adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Maintainer role") {
		t.Errorf("error body should mention Maintainer role, got: %s", rr.Body.String())
	}
}

func TestAddGitLabHookTarget_NotFound_Returns422(t *testing.T) {
	setupGitLabBox(t)
	setPublicURL(t, "https://multica.example")
	adminID := seedMember(t, "admin")

	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"404 Project Not Found"}`)
	})

	conn := seedGitLabHooksConnection(t, fakeAPI.URL, nil)

	router := newGitLabHooksRouter(testHandler)
	req := hooksRouterRequest("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
		`{"target_type":"project","target_path":"org/nonexistent"}`,
		adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "target not found") {
		t.Errorf("error body should mention 'target not found', got: %s", rr.Body.String())
	}
}

func TestAddGitLabHookTarget_MemberRole_Returns403(t *testing.T) {
	setupGitLabBox(t)
	setPublicURL(t, "https://multica.example")
	memberID := seedMember(t, "member")

	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	conn := seedGitLabHooksConnection(t, fakeAPI.URL, nil)

	router := newGitLabHooksRouter(testHandler)
	req := hooksRouterRequest("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
		`{"target_type":"project","target_path":"org/repo"}`,
		memberID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for member, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAddGitLabHookTarget_DuplicateTarget_AppendsIdempotently(t *testing.T) {
	// Duplicate targets append without error. This avoids the complexity of a
	// pre-check race. A 409 semantic can be added later with a DB-level unique
	// constraint on (connection_id, target_type, target_path) in a future
	// JSONB indexing migration.
	setupGitLabBox(t)
	setPublicURL(t, "https://multica.example")
	adminID := seedMember(t, "admin")

	var callCount int
	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id": 99}`)
	})

	conn := seedGitLabHooksConnection(t, fakeAPI.URL, nil)
	router := newGitLabHooksRouter(testHandler)

	req1 := hooksRouterRequest("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
		`{"target_type":"project","target_path":"org/repo"}`,
		adminID)
	rr1 := httptest.NewRecorder()
	router.ServeHTTP(rr1, req1)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first POST: expected 201, got %d: %s", rr1.Code, rr1.Body.String())
	}

	req2 := hooksRouterRequest("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
		`{"target_type":"project","target_path":"org/repo"}`,
		adminID)
	rr2 := httptest.NewRecorder()
	router.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusCreated {
		t.Fatalf("second POST: expected 201, got %d: %s", rr2.Code, rr2.Body.String())
	}

	if callCount != 2 {
		t.Errorf("expected 2 remote hook creates, got %d", callCount)
	}

	updated, _ := testHandler.Queries.GetGitLabConnectionByID(context.Background(), db.GetGitLabConnectionByIDParams{
		ID:          conn.ID,
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	dbHooks, _ := decodeHooksJSON(updated.Hooks)
	if len(dbHooks) != 2 {
		t.Errorf("expected 2 hooks in DB, got %d: %+v", len(dbHooks), dbHooks)
	}
}

func TestAddGitLabHookTarget_InvalidBody_Returns400(t *testing.T) {
	setupGitLabBox(t)
	setPublicURL(t, "https://multica.example")
	adminID := seedMember(t, "admin")

	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	conn := seedGitLabHooksConnection(t, fakeAPI.URL, nil)
	router := newGitLabHooksRouter(testHandler)

	tests := []struct {
		name string
		body string
	}{
		{"empty body", `{}`},
		{"bad target_type", `{"target_type":"unknown","target_path":"x"}`},
		{"missing target_path", `{"target_type":"project"}`},
		{"not json", `not-json`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := hooksRouterRequest("POST",
				"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
				tt.body,
				adminID)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestRemoveGitLabHookTarget_NotFound_Returns404(t *testing.T) {
	setupGitLabBox(t)
	setPublicURL(t, "https://multica.example")
	adminID := seedMember(t, "admin")

	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	conn := seedGitLabHooksConnection(t, fakeAPI.URL, nil)

	router := newGitLabHooksRouter(testHandler)
	req := hooksRouterRequest("DELETE",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
		`{"target_type":"project","target_path":"org/nonexistent"}`,
		adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRemoveGitLabHookTarget_MemberRole_Returns403(t *testing.T) {
	setupGitLabBox(t)
	setPublicURL(t, "https://multica.example")
	memberID := seedMember(t, "member")

	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	conn := seedGitLabHooksConnection(t, fakeAPI.URL, nil)

	router := newGitLabHooksRouter(testHandler)
	req := hooksRouterRequest("DELETE",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID)+"/hooks",
		`{"target_type":"project","target_path":"org/repo"}`,
		memberID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for member, got %d: %s", rr.Code, rr.Body.String())
	}
}
