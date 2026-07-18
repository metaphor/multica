package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ── Test helpers ─────────────────────────────────────────────────────────────

func setupGitLabBox(t *testing.T) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	old := testHandler.GitLabBox
	testHandler.GitLabBox = box
	t.Cleanup(func() { testHandler.GitLabBox = old })
}

func seedMember(t *testing.T, role string) string {
	t.Helper()
	ctx := context.Background()
	var userID string
	err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ($1, 'gitlab-test-' || gen_random_uuid()::text || '@test.local')
		RETURNING id
	`, "GL Test "+role).Scan(&userID)
	if err != nil {
		t.Fatalf("seed user (%s): %v", role, err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, userID)
	})
	_, err = testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, $3)
	`, testWorkspaceID, userID, role)
	if err != nil {
		t.Fatalf("seed member (%s): %v", role, err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, userID)
	})
	return userID
}

func newGitLabRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	r.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
			r.Get("/gitlab/connections", h.ListGitLabConnections)
		})
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner", "admin"))
			r.Post("/gitlab/connections", h.CreateGitLabConnection)
			r.Delete("/gitlab/connections/{connectionID}", h.DeleteGitLabConnection)
		})
	})
	return r
}

// routeRequestWithBody creates an httptest request with the given string body
// and user/workspace headers. Use this for non-GET requests through the router.
func routeRequestWithBody(method, path, bodyStr, userID string) *http.Request {
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

// handlerRequest creates a request with chi URL params for direct handler testing.
func handlerRequest(method, path string, body io.Reader, params map[string]string) *http.Request {
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	if len(params) > 0 {
		rctx := chi.NewRouteContext()
		for k, v := range params {
			rctx.URLParams.Add(k, v)
		}
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	}
	return req
}

// ── Role matrix ──────────────────────────────────────────────────────────────

func TestGitLabConnection_MemberPOST_403(t *testing.T) {
	setupGitLabBox(t)
	memberID := seedMember(t, "member")
	router := newGitLabRouter(testHandler)

	req := routeRequestWithBody("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections",
		`{"instance_url":"https://gitlab.example.com","access_token":"test-token"}`,
		memberID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for member POST, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGitLabConnection_MemberDELETE_403(t *testing.T) {
	setupGitLabBox(t)
	memberID := seedMember(t, "member")
	router := newGitLabRouter(testHandler)

	req := routeRequestWithBody("DELETE",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/some-id",
		"", memberID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for member DELETE, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGitLabConnection_MemberGET_200(t *testing.T) {
	setupGitLabBox(t)
	memberID := seedMember(t, "member")
	router := newGitLabRouter(testHandler)

	req := routeRequestWithBody("GET",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections",
		"", memberID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		CanManage bool `json:"can_manage"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.CanManage {
		t.Error("member should have can_manage=false")
	}
}

func TestGitLabConnection_AdminPOST_201(t *testing.T) {
	setupGitLabBox(t)
	adminID := seedMember(t, "admin")
	router := newGitLabRouter(testHandler)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v4/user" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"username":"alice","name":"Alice Smith"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	body := `{"instance_url":"` + srv.URL + `","access_token":"test-token","display_name":"Test"}`
	req := routeRequestWithBody("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections",
		body, adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp GitLabConnectionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ID == "" {
		t.Error("expected non-empty id")
	}
	if resp.AccountLogin != "alice" {
		t.Errorf("account_login = %q, want alice", resp.AccountLogin)
	}
	if !resp.CanManage {
		t.Error("admin should have can_manage=true")
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM github_installation WHERE id=$1`, resp.ID)
	})
}

func TestGitLabConnection_OwnerDELETE_204(t *testing.T) {
	setupGitLabBox(t)
	ownerID := seedMember(t, "owner")
	ctx := context.Background()

	conn, err := testHandler.Queries.InsertGitLabConnection(ctx, db.InsertGitLabConnectionParams{
		WorkspaceID:             parseUUID(testWorkspaceID),
		AccountLogin:            "test-user",
		DisplayName:             pgtype.Text{String: "Test", Valid: true},
		InstanceUrl:             pgtype.Text{String: "https://gitlab.example.com", Valid: true},
		AccessTokenCiphertext:   []byte("enc"),
		WebhookSecretHash:       make([]byte, 32),
		WebhookSecretCiphertext: []byte("enc"),
		Hooks:                   []byte("[]"),
	})
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_installation WHERE id=$1`, uuidToString(conn.ID))
	})

	router := newGitLabRouter(testHandler)
	req := routeRequestWithBody("DELETE",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID),
		"", ownerID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	var count int
	err = testPool.QueryRow(ctx, `SELECT count(*) FROM github_installation WHERE id=$1`, uuidToString(conn.ID)).Scan(&count)
	if err != nil || count != 0 {
		t.Error("connection row should be deleted")
	}
}

// ── Configured ───────────────────────────────────────────────────────────────

func TestGitLabConnections_ConfiguredFalse(t *testing.T) {
	old := testHandler.GitLabBox
	testHandler.GitLabBox = nil
	t.Cleanup(func() { testHandler.GitLabBox = old })

	router := newGitLabRouter(testHandler)

	req := routeRequestWithBody("GET",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections",
		"", testUserID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Configured bool `json:"configured"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Configured {
		t.Error("expected configured=false when box is nil")
	}

	body := `{"instance_url":"https://gitlab.example.com","access_token":"tok"}`
	req = routeRequestWithBody("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections",
		body, testUserID)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("POST: expected 503, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "MULTICA_GITLAB_SECRET_KEY") {
		t.Error("503 body should mention MULTICA_GITLAB_SECRET_KEY")
	}
}

// ── Leak assertions ──────────────────────────────────────────────────────────

func TestGitLabConnections_LeakProof(t *testing.T) {
	setupGitLabBox(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v4/user" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"username":"alice","name":"Alice"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	body := `{"instance_url":"` + srv.URL + `","access_token":"test-token-leak"}`
	req := handlerRequest("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections",
		strings.NewReader(body),
		map[string]string{"id": testWorkspaceID})
	rr := httptest.NewRecorder()
	testHandler.CreateGitLabConnection(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var created GitLabConnectionResponse
	json.Unmarshal(rr.Body.Bytes(), &created)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM github_installation WHERE id=$1`, created.ID)
	})

	createBody := rr.Body.String()
	if strings.Contains(createBody, "test-token-leak") {
		t.Error("POST response should NOT contain the access token")
	}

	req = handlerRequest("GET",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections",
		nil,
		map[string]string{"id": testWorkspaceID})
	rr = httptest.NewRecorder()
	testHandler.ListGitLabConnections(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", rr.Code)
	}

	listBody := rr.Body.String()
	if strings.Contains(listBody, "test-token-leak") {
		t.Error("GET response should NOT contain the access token")
	}
	if strings.Contains(listBody, "test-token") {
		t.Error("GET response should NOT contain any token substring")
	}
}

// ── ValidateToken 401 → 400 ──────────────────────────────────────────────────

func TestGitLabConnections_ValidateTokenUnauthorized(t *testing.T) {
	setupGitLabBox(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	body := `{"instance_url":"` + srv.URL + `","access_token":"bad-token"}`
	req := handlerRequest("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections",
		strings.NewReader(body),
		map[string]string{"id": testWorkspaceID})
	rr := httptest.NewRecorder()
	testHandler.CreateGitLabConnection(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid token, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid access token") {
		t.Errorf("expected sanitised error, got: %s", rr.Body.String())
	}

	var count int
	testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM github_installation WHERE provider='gitlab' AND instance_url=$1`,
		srv.URL).Scan(&count)
	if count != 0 {
		t.Error("no DB row should be created for failed ValidateToken")
	}
}

// ── DELETE sends remote hook delete ──────────────────────────────────────────

func TestGitLabConnections_DeleteRemoteHook(t *testing.T) {
	setupGitLabBox(t)
	ctx := context.Background()

	var deletedHooks []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/hooks/") {
			deletedHooks = append(deletedHooks, r.Method+" "+r.URL.EscapedPath())
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	tokenCiphertext, err := testHandler.GitLabBox.Seal([]byte("fake-token"))
	if err != nil {
		t.Fatalf("Seal token: %v", err)
	}
	secretCiphertext, err := testHandler.GitLabBox.Seal([]byte("fake-secret"))
	if err != nil {
		t.Fatalf("Seal secret: %v", err)
	}

	hooksJSON := `[{"target_type":"project","target_path":"testorg/testrepo","hook_id":42,"url":"https://example.com/hook","created_at":"2024-01-01T00:00:00Z","last_error":null}]`
	conn, err := testHandler.Queries.InsertGitLabConnection(ctx, db.InsertGitLabConnectionParams{
		WorkspaceID:             parseUUID(testWorkspaceID),
		AccountLogin:            "test-user",
		DisplayName:             pgtype.Text{String: "Test", Valid: true},
		InstanceUrl:             pgtype.Text{String: srv.URL, Valid: true},
		AccessTokenCiphertext:   tokenCiphertext,
		WebhookSecretHash:       make([]byte, 32),
		WebhookSecretCiphertext: secretCiphertext,
		Hooks:                   []byte(hooksJSON),
	})
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_installation WHERE id=$1`, uuidToString(conn.ID))
	})

	req := handlerRequest("DELETE",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+uuidToString(conn.ID),
		nil,
		map[string]string{
			"id":           testWorkspaceID,
			"connectionID": uuidToString(conn.ID),
		})
	rr := httptest.NewRecorder()
	testHandler.DeleteGitLabConnection(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	if len(deletedHooks) == 0 {
		t.Fatal("expected at least one remote hook delete call")
	}
	found := false
	for _, h := range deletedHooks {
		if strings.Contains(h, "testorg%2Ftestrepo/hooks/42") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected DELETE to projects/testorg%%2Ftestrepo/hooks/42, got: %v", deletedHooks)
	}
}
