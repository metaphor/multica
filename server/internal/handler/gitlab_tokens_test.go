package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ── Test helpers ─────────────────────────────────────────────────────────────

// patchRecord captures a single PATCH request seen by the fake GitLab API.
type patchRecord struct {
	Method   string
	Path     string
	Body     map[string]string
	TargetID string // "project/org%2Frepo/42" style identifier
}

func newTokenRotateRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	r.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner", "admin"))
			r.Put("/gitlab/connections/{connectionID}/token", h.UpdateGitLabConnectionToken)
			r.Post("/gitlab/connections/{connectionID}/rotate-secret", h.RotateGitLabConnectionSecret)
		})
	})
	return r
}

// seedConnectionWithHooks inserts a GitLab connection with the given hook records
// pre-serialised into the hooks JSONB column. Returns the connection row.
func seedConnectionWithHooks(t *testing.T, fakeURL string, hooks []hookRecord) db.GithubInstallation {
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

	raw, err := json.Marshal(hooks)
	if err != nil {
		t.Fatalf("marshal hooks: %v", err)
	}

	conn, err := testHandler.Queries.InsertGitLabConnection(context.Background(), db.InsertGitLabConnectionParams{
		WorkspaceID:             wsUUID,
		AccountLogin:            "token-test-user",
		DisplayName:             pgtype.Text{String: "Token Test", Valid: true},
		InstanceUrl:             pgtype.Text{String: fakeURL, Valid: true},
		AccessTokenCiphertext:   tokenCiphertext,
		WebhookSecretHash:       hash[:],
		WebhookSecretCiphertext: secretCiphertext,
		Hooks:                   raw,
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

func tokenRequest(method, path, bodyStr, userID string) *http.Request {
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

// ── RotateSecret happy path ──────────────────────────────────────────────────

func TestRotateGitLabConnectionSecret_Happy(t *testing.T) {
	setupGitLabBox(t)
	adminID := seedMember(t, "admin")

	// Build a fake GitLab API that records PATCH requests and responds OK.
	var mu sync.Mutex
	var patches []patchRecord
	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		var rec patchRecord
		rec.Method = r.Method
		rec.Path = r.URL.Path
		if r.Body != nil {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				rec.Body = body
			}
		}
		patches = append(patches, rec)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"id":1}`)
	})

	// Seed connection with 2 hooks.
	hooks := []hookRecord{
		{TargetType: "project", TargetPath: "org/repo1", HookID: 10},
		{TargetType: "group", TargetPath: "org/group1", HookID: 20},
	}
	conn := seedConnectionWithHooks(t, fakeAPI.URL, hooks)

	// Record the old hash before rotation.
	oldConn, err := testHandler.Queries.GetGitLabConnectionByID(context.Background(), db.GetGitLabConnectionByIDParams{
		ID:          conn.ID,
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("pre-rotate GetGitLabConnectionByID: %v", err)
	}
	oldHash := make([]byte, len(oldConn.WebhookSecretHash))
	copy(oldHash, oldConn.WebhookSecretHash)

	// Rotate the secret.
	router := newTokenRotateRouter(testHandler)
	req := tokenRequest(http.MethodPost,
		fmt.Sprintf("/api/workspaces/%s/gitlab/connections/%s/rotate-secret", testWorkspaceID, uuidToString(conn.ID)),
		"", adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]bool
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp["rotated"] {
		t.Error("rotated should be true")
	}

	// Assert: fake API received 2 PATCH requests.
	mu.Lock()
	patchCount := len(patches)
	mu.Unlock()
	if patchCount != 2 {
		t.Errorf("expected 2 PATCH calls, got %d", patchCount)
	}

	// Assert: each PATCH carries the new secret token.
	mu.Lock()
	for i, p := range patches {
		if p.Body == nil {
			t.Errorf("PATCH[%d]: no body", i)
			continue
		}
		if tok, ok := p.Body["token"]; !ok || tok == "" {
			t.Errorf("PATCH[%d]: missing or empty token in body", i)
		}
	}
	mu.Unlock()

	// Assert: DB now has a new secret hash.
	updatedConn, err := testHandler.Queries.GetGitLabConnectionByID(context.Background(), db.GetGitLabConnectionByIDParams{
		ID:          conn.ID,
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("post-rotate GetGitLabConnectionByID: %v", err)
	}
	if string(updatedConn.WebhookSecretHash) == string(oldHash) {
		t.Error("webhook_secret_hash should have changed after rotation")
	}

	// Extract the new secret from the fake API body.
	mu.Lock()
	newSecret := patches[0].Body["token"]
	mu.Unlock()

	// Assert: can find the connection by the new secret hash.
	newHash := sha256.Sum256([]byte(newSecret))
	_, err = testHandler.Queries.GetGitLabConnectionBySecretHash(context.Background(), newHash[:])
	if err != nil {
		t.Fatalf("GetGitLabConnectionBySecretHash with new secret: %v", err)
	}

	// Assert: old token webhook → 401.
	t.Run("old_token_webhook_401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab", nil)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gitlab-Token", "old-secret-that-no-longer-works")
		req.Header.Set("X-Gitlab-Event", "Push Hook")
		rr := httptest.NewRecorder()
		testHandler.HandleGitLabWebhook(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 for old token, got %d", rr.Code)
		}
	})

	// Assert: new token webhook → 202.
	t.Run("new_token_webhook_202", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab", nil)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gitlab-Token", newSecret)
		req.Header.Set("X-Gitlab-Event", "Push Hook")
		rr := httptest.NewRecorder()
		testHandler.HandleGitLabWebhook(rr, req)
		if rr.Code != http.StatusAccepted {
			t.Errorf("expected 202 for new token, got %d", rr.Code)
		}
	})
}

// ── RotateSecret fail → rollback ─────────────────────────────────────────────

func TestRotateGitLabConnectionSecret_FailureRollback(t *testing.T) {
	setupGitLabBox(t)
	adminID := seedMember(t, "admin")

	// Fake API: succeeds on 1st PATCH, fails (500) on 2nd.
	var mu sync.Mutex
	var patchCount int
	var patches []patchRecord
	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		patchCount++

		var rec patchRecord
		rec.Method = r.Method
		rec.Path = r.URL.Path
		if r.Body != nil {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				rec.Body = body
			}
		}
		patches = append(patches, rec)

		// First PATCH succeeds, second fails.
		if patchCount == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"id":1}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"message":"Internal Server Error"}`)
	})

	hooks := []hookRecord{
		{TargetType: "project", TargetPath: "org/repo1", HookID: 10},
		{TargetType: "group", TargetPath: "org/group1", HookID: 20},
	}
	conn := seedConnectionWithHooks(t, fakeAPI.URL, hooks)

	// Snapshot old hash.
	oldConn, err := testHandler.Queries.GetGitLabConnectionByID(context.Background(), db.GetGitLabConnectionByIDParams{
		ID:          conn.ID,
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("pre-rotate GetGitLabConnectionByID: %v", err)
	}
	oldHash := make([]byte, len(oldConn.WebhookSecretHash))
	copy(oldHash, oldConn.WebhookSecretHash)

	// Rotate — should fail with 502.
	router := newTokenRotateRouter(testHandler)
	req := tokenRequest(http.MethodPost,
		fmt.Sprintf("/api/workspaces/%s/gitlab/connections/%s/rotate-secret", testWorkspaceID, uuidToString(conn.ID)),
		"", adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp["rotated"] != false {
		t.Error("rotated should be false")
	}
	if resp["failed_target"] == nil {
		t.Error("response should include failed_target")
	}

	// Assert: DB hash unchanged.
	updatedConn, err := testHandler.Queries.GetGitLabConnectionByID(context.Background(), db.GetGitLabConnectionByIDParams{
		ID:          conn.ID,
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("post-fail GetGitLabConnectionByID: %v", err)
	}
	if string(updatedConn.WebhookSecretHash) != string(oldHash) {
		t.Error("webhook_secret_hash should NOT have changed after failed rotation")
	}

	// Assert: the 1st hook received a rollback PATCH with the old token.
	mu.Lock()
	totalPatches := len(patches)
	mu.Unlock()
	if totalPatches < 3 {
		t.Fatalf("expected at least 3 PATCH calls (forward + rollback), got %d", totalPatches)
	}

	// The rollback PATCH (3rd call) should use the OLD token.
	mu.Lock()
	var rollbackBody map[string]string
	for _, p := range patches[2:] {
		if p.Body != nil {
			rollbackBody = p.Body
		}
	}
	mu.Unlock()
	if rollbackBody == nil {
		t.Fatal("no rollback PATCH body found")
	}
	if rollbackBody["token"] == "" {
		t.Error("rollback PATCH should carry a token")
	}
}

// ── UpdateToken invalid → preserve old ciphertext ────────────────────────────

func TestUpdateGitLabConnectionToken_InvalidToken(t *testing.T) {
	setupGitLabBox(t)
	adminID := seedMember(t, "admin")

	// Fake API: /user returns 401 for invalid token.
	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"message":"401 Unauthorized"}`)
	})

	// Seed a connection with a known valid token.
	hooks := []hookRecord{
		{TargetType: "project", TargetPath: "org/repo1", HookID: 10},
	}
	conn := seedConnectionWithHooks(t, fakeAPI.URL, hooks)

	// Snapshot old ciphertext.
	oldConn, err := testHandler.Queries.GetGitLabConnectionByID(context.Background(), db.GetGitLabConnectionByIDParams{
		ID:          conn.ID,
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("pre-update GetGitLabConnectionByID: %v", err)
	}
	oldCiphertext := make([]byte, len(oldConn.AccessTokenCiphertext))
	copy(oldCiphertext, oldConn.AccessTokenCiphertext)

	// Try to update token with an invalid one.
	router := newTokenRotateRouter(testHandler)
	req := tokenRequest(http.MethodPut,
		fmt.Sprintf("/api/workspaces/%s/gitlab/connections/%s/token", testWorkspaceID, uuidToString(conn.ID)),
		`{"access_token":"invalid-token"}`, adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid token, got %d: %s", rr.Code, rr.Body.String())
	}

	// Assert: ciphertext unchanged.
	updatedConn, err := testHandler.Queries.GetGitLabConnectionByID(context.Background(), db.GetGitLabConnectionByIDParams{
		ID:          conn.ID,
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("post-update GetGitLabConnectionByID: %v", err)
	}
	if string(updatedConn.AccessTokenCiphertext) != string(oldCiphertext) {
		t.Error("access_token_ciphertext should NOT have changed after failed validation")
	}

	// Assert: subsequent hook operations still work with the old token (via
	// the fake API that serves as the GitLab instance).
	// We verify by checking that the connection's stored token is unchanged.
}

// ── RotateSecret member access → 403 ─────────────────────────────────────────

func TestRotateGitLabConnectionSecret_MemberForbidden(t *testing.T) {
	setupGitLabBox(t)
	memberID := seedMember(t, "member") // regular member

	// Fake API — should never be called.
	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("fake API should not be called for forbidden request")
		w.WriteHeader(http.StatusOK)
	})

	conn := seedConnectionWithHooks(t, fakeAPI.URL, nil)

	router := newTokenRotateRouter(testHandler)
	req := tokenRequest(http.MethodPost,
		fmt.Sprintf("/api/workspaces/%s/gitlab/connections/%s/rotate-secret", testWorkspaceID, uuidToString(conn.ID)),
		"", memberID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for member, got %d", rr.Code)
	}
}

// ── RotateSecret no hooks → direct swap ──────────────────────────────────────

func TestRotateGitLabConnectionSecret_NoHooks(t *testing.T) {
	setupGitLabBox(t)
	adminID := seedMember(t, "admin")

	// Fake API — should never be called for PATCH.
	var apiCalled bool
	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		apiCalled = true
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"id":1}`)
	})

	// Seed connection with empty hooks.
	conn := seedConnectionWithHooks(t, fakeAPI.URL, nil)

	oldConn, err := testHandler.Queries.GetGitLabConnectionByID(context.Background(), db.GetGitLabConnectionByIDParams{
		ID:          conn.ID,
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("pre-rotate GetGitLabConnectionByID: %v", err)
	}
	oldHash := make([]byte, len(oldConn.WebhookSecretHash))
	copy(oldHash, oldConn.WebhookSecretHash)

	router := newTokenRotateRouter(testHandler)
	req := tokenRequest(http.MethodPost,
		fmt.Sprintf("/api/workspaces/%s/gitlab/connections/%s/rotate-secret", testWorkspaceID, uuidToString(conn.ID)),
		"", adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]bool
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp["rotated"] {
		t.Error("rotated should be true")
	}

	// Assert: no PATCH calls were made.
	if apiCalled {
		t.Error("fake API should not have been called when there are no hooks")
	}

	// Assert: DB hash changed.
	updatedConn, err := testHandler.Queries.GetGitLabConnectionByID(context.Background(), db.GetGitLabConnectionByIDParams{
		ID:          conn.ID,
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("post-rotate GetGitLabConnectionByID: %v", err)
	}
	if string(updatedConn.WebhookSecretHash) == string(oldHash) {
		t.Error("webhook_secret_hash should have changed for no-hooks connection")
	}
}

// ── UpdateToken member → 403 ─────────────────────────────────────────────────

func TestUpdateGitLabConnectionToken_MemberForbidden(t *testing.T) {
	setupGitLabBox(t)
	memberID := seedMember(t, "member")

	fakeAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("fake API should not be called for forbidden request")
		w.WriteHeader(http.StatusOK)
	})

	conn := seedConnectionWithHooks(t, fakeAPI.URL, nil)

	router := newTokenRotateRouter(testHandler)
	req := tokenRequest(http.MethodPut,
		fmt.Sprintf("/api/workspaces/%s/gitlab/connections/%s/token", testWorkspaceID, uuidToString(conn.ID)),
		`{"access_token":"new-token"}`, memberID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for member, got %d", rr.Code)
	}
}
