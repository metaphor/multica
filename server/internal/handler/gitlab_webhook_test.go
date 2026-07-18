package handler

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/db/generated"
)

const testGitLabWebhookSecret = "test-secret-123"

// seedGitLabConnection inserts a GitLab webhook connection row into the test
// database. The caller is responsible for cleanup via the returned id.
func seedGitLabConnection(t *testing.T) db.GithubInstallation {
	t.Helper()
	hash := sha256.Sum256([]byte(testGitLabWebhookSecret))
	params := db.InsertGitLabConnectionParams{
		WorkspaceID:       parseUUID(testWorkspaceID),
		AccountLogin:      "test-gitlab-user",
		WebhookSecretHash: hash[:],
		Hooks:             []byte("[]"),
	}
	ctx := context.Background()
	conn, err := testHandler.Queries.InsertGitLabConnection(ctx, params)
	if err != nil {
		t.Fatalf("seedGitLabConnection: InsertGitLabConnection failed: %v", err)
	}
	return conn
}

func cleanupGitLabConnection(t *testing.T, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `DELETE FROM github_installation WHERE id = $1`, id); err != nil {
		t.Logf("cleanupGitLabConnection: DELETE failed (non-fatal): %v", err)
	}
}

func TestGitLabWebhookMissingToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab", nil)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	testHandler.HandleGitLabWebhook(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing token, got %d", rr.Code)
	}
}

func TestGitLabWebhookInvalidToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", "wrong-token")
	rr := httptest.NewRecorder()
	testHandler.HandleGitLabWebhook(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid token, got %d", rr.Code)
	}
}

func TestGitLabWebhookPushEventAcked(t *testing.T) {
	conn := seedGitLabConnection(t)
	t.Cleanup(func() { cleanupGitLabConnection(t, uuidToString(conn.ID)) })

	// Snapshot row count before the request.
	ctx := context.Background()
	var before int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM github_installation`).Scan(&before); err != nil {
		t.Fatalf("pre-count failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", testGitLabWebhookSecret)
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	rr := httptest.NewRecorder()
	testHandler.HandleGitLabWebhook(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("expected 202 for push event, got %d", rr.Code)
	}

	// Assert no DB side effects — the row count must be unchanged.
	var after int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM github_installation`).Scan(&after); err != nil {
		t.Fatalf("post-count failed: %v", err)
	}
	if after != before {
		t.Errorf("unexpected side effect: row count changed from %d to %d", before, after)
	}
}

func TestGitLabWebhookMergeRequestEventAcked(t *testing.T) {
	conn := seedGitLabConnection(t)
	t.Cleanup(func() { cleanupGitLabConnection(t, uuidToString(conn.ID)) })

	// Minimal valid GitLab Merge Request Hook payload.
	payload := `{"object_kind":"merge_request","event_type":"merge_request","user":{"id":1,"name":"Test"},"project":{"id":1},"object_attributes":{"id":1,"iid":1,"title":"Test MR","state":"opened","source_branch":"feature/test","target_branch":"main","action":"open"}}`

	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab",
		strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", testGitLabWebhookSecret)
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	rr := httptest.NewRecorder()
	testHandler.HandleGitLabWebhook(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Errorf("expected 202 for MR event, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGitLabWebhookBodyTooLarge(t *testing.T) {
	// Create a body that exceeds 10 MiB.
	largeBody := make([]byte, 11<<20)
	for i := range largeBody {
		largeBody[i] = 'x'
	}

	conn := seedGitLabConnection(t)
	t.Cleanup(func() { cleanupGitLabConnection(t, uuidToString(conn.ID)) })

	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab",
		strings.NewReader(string(largeBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", testGitLabWebhookSecret)
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	rr := httptest.NewRecorder()
	testHandler.HandleGitLabWebhook(rr, req)

	// MaxBytesReader returns an error after the limit; the handler returns a
	// 4xx (BadRequest) matching the same pattern as HandleGitHubWebhook.
	if rr.Code < 400 || rr.Code >= 500 {
		t.Errorf("expected 4xx for oversized body, got %d", rr.Code)
	}
}
