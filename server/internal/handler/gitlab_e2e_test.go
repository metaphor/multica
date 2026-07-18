package handler

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ── E2E: happy path ─────────────────────────────────────────────────────────

// TestGitLabE2E_HappyPath exercises the full GitLab integration lifecycle
// with a fake GitLab API server at the boundary and a real router + DB
// underneath. Every step includes DB row-level assertions.
func TestGitLabE2E_HappyPath(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()

	// ── Prepare ──────────────────────────────────────────────────────────

	setupGitLabBox(t)
	setPublicURL(t, "https://multica.example")

	adminID := seedMember(t, "admin")

	// Fake GitLab API records all hook-create requests for later assertion.
	type capturedHook struct {
		method string
		path   string
		body   json.RawMessage
	}
	var mu sync.Mutex
	var capturedHooks []capturedHook
	var nextHookID int64 = 42
	fakeGLUser := "gitlab-alice"
	fakeGLAvatar := "https://gitlab.example.com/avatar.png"

	fakeGLAPI := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		// ValidateToken: GET /api/v4/user
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/user":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"username":"%s","name":"Alice Smith"}`, fakeGLUser)
			return

		// Author backfill: GET /api/v4/users/:id
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v4/users/"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":99,"username":"%s","avatar_url":"%s"}`, fakeGLUser, fakeGLAvatar)
			return

		// Create project hook: POST /api/v4/projects/...
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/api/v4/projects/") && strings.Contains(r.URL.Path, "/hooks"):
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			capturedHooks = append(capturedHooks, capturedHook{
				method: r.Method,
				path:   r.URL.Path,
				body:   json.RawMessage(body),
			})
			hid := nextHookID
			nextHookID++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":%d}`, hid)
			return

		// Create group hook: POST /api/v4/groups/.../hooks
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/api/v4/groups/") && strings.Contains(r.URL.Path, "/hooks"):
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			capturedHooks = append(capturedHooks, capturedHook{
				method: r.Method,
				path:   r.URL.Path,
				body:   json.RawMessage(body),
			})
			hid := nextHookID
			nextHookID++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":%d}`, hid)
			return
		}
		http.NotFound(w, r)
	})

	// Cleanup after test completes.
	var connID string
	t.Cleanup(func() {
		if connID != "" {
			ctx := context.Background()
			testPool.Exec(ctx, `DELETE FROM issue_pull_request WHERE pull_request_id IN (SELECT id FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab')`, testWorkspaceID)
			testPool.Exec(ctx, `DELETE FROM github_pull_request_check_suite WHERE pr_id IN (SELECT id FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab')`, testWorkspaceID)
			testPool.Exec(ctx, `DELETE FROM github_pending_check_suite WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
			testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
			testPool.Exec(ctx, `DELETE FROM github_installation WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
			if connID != "" {
				testPool.Exec(ctx, `DELETE FROM github_installation WHERE id=$1`, connID)
			}
		}
	})

	// ── Step 1: Create connection ────────────────────────────────────────
	// POST /api/workspaces/{ws}/gitlab/connections
	// Fake API returns username; response must not contain token/secret.

	router := newGitLabFullRouter(testHandler)
	body := fmt.Sprintf(`{"instance_url":"%s","access_token":"glpat-step1-token","display_name":"E2E Test"}`, fakeGLAPI.URL)
	req := routeRequestWithBody("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections",
		body, adminID)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("Step 1: expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var connResp GitLabConnectionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &connResp); err != nil {
		t.Fatalf("Step 1: unmarshal response: %v", err)
	}
	if connResp.ID == "" {
		t.Fatal("Step 1: expected non-empty connection id")
	}
	if connResp.AccountLogin != fakeGLUser {
		t.Errorf("Step 1: account_login = %q, want %q", connResp.AccountLogin, fakeGLUser)
	}
	connID = connResp.ID

	// Leak check: response must not contain token or secret substrings.
	respBody := rr.Body.String()
	if strings.Contains(respBody, "glpat-step1-token") {
		t.Error("Step 1: response contains access token (leak!)")
	}
	if strings.Contains(respBody, "token") {
		t.Error("Step 1: response contains 'token' substring (leak!)")
	}
	if strings.Contains(respBody, "secret") {
		t.Error("Step 1: response contains 'secret' substring (leak!)")
	}

	// DB row exists.
	var dbCount int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM github_installation WHERE id=$1 AND provider='gitlab'`, connID).Scan(&dbCount); err != nil {
		t.Fatalf("Step 1: count DB rows: %v", err)
	}
	if dbCount != 1 {
		t.Errorf("Step 1: expected 1 DB row, got %d", dbCount)
	}

	t.Logf("Step 1 ✅: connection created id=%s account=%s", connID, fakeGLUser)

	// ── Step 2: Create webhook target ────────────────────────────────────
	// POST .../connections/{cid}/hooks {target_type:"project", target_path:"org/repo"}
	// Fake API records the request; returns hook_id=42.

	hookBody := `{"target_type":"project","target_path":"org/repo"}`
	req = routeRequestWithBody("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+connID+"/hooks",
		hookBody, adminID)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("Step 2: expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var hooks []hookRecord
	if err := json.Unmarshal(rr.Body.Bytes(), &hooks); err != nil {
		t.Fatalf("Step 2: unmarshal hooks: %v", err)
	}
	if len(hooks) != 1 {
		t.Fatalf("Step 2: expected 1 hook, got %d", len(hooks))
	}
	if hooks[0].HookID != 42 {
		t.Errorf("Step 2: hook_id = %d, want 42", hooks[0].HookID)
	}
	if hooks[0].TargetType != "project" {
		t.Errorf("Step 2: target_type = %q, want project", hooks[0].TargetType)
	}
	if hooks[0].TargetPath != "org/repo" {
		t.Errorf("Step 2: target_path = %q, want org/repo", hooks[0].TargetPath)
	}

	// Assert fake API captured the hook create request.
	mu.Lock()
	if len(capturedHooks) != 1 {
		t.Fatalf("Step 2: expected 1 captured hook POST, got %d", len(capturedHooks))
	}
	ch := capturedHooks[0]
	mu.Unlock()
	if ch.method != http.MethodPost {
		t.Errorf("Step 2: hook method = %s, want POST", ch.method)
	}
	if ch.path != "/api/v4/projects/org/repo/hooks" {
		t.Errorf("Step 2: hook path = %q, want /api/v4/projects/org/repo/hooks", ch.path)
	}
	var sentParams map[string]any
	if err := json.Unmarshal(ch.body, &sentParams); err != nil {
		t.Fatalf("Step 2: unmarshal captured hook body: %v", err)
	}
	if sentParams["url"] != "https://multica.example/api/webhooks/gitlab" {
		t.Errorf("Step 2: hook url = %q, want https://multica.example/api/webhooks/gitlab", sentParams["url"])
	}
	if token, ok := sentParams["token"].(string); !ok || token == "" {
		t.Error("Step 2: hook token is empty or missing")
	}
	if mergeEvents, ok := sentParams["merge_requests_events"].(bool); !ok || !mergeEvents {
		t.Error("Step 2: merge_requests_events should be true")
	}
	if pipelineEvents, ok := sentParams["pipeline_events"].(bool); !ok || !pipelineEvents {
		t.Error("Step 2: pipeline_events should be true")
	}
	if ssl, ok := sentParams["enable_ssl_verification"].(bool); !ok || !ssl {
		t.Error("Step 2: enable_ssl_verification should be true for https PublicURL")
	}

	// DB hooks jsonb has the entry.
	conn, err := testHandler.Queries.GetGitLabConnectionByID(ctx, db.GetGitLabConnectionByIDParams{
		ID:          util.MustParseUUID(connID),
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("Step 2: GetGitLabConnectionByID: %v", err)
	}
	dbHooks, err := decodeHooksJSON(conn.Hooks)
	if err != nil {
		t.Fatalf("Step 2: decodeHooksJSON: %v", err)
	}
	if len(dbHooks) != 1 {
		t.Fatalf("Step 2: expected 1 hook in DB, got %d", len(dbHooks))
	}
	if dbHooks[0].HookID != 42 {
		t.Errorf("Step 2: DB hook_id = %d, want 42", dbHooks[0].HookID)
	}
	if dbHooks[0].TargetPath != "org/repo" {
		t.Errorf("Step 2: DB target_path = %q, want org/repo", dbHooks[0].TargetPath)
	}

	t.Logf("Step 2 ✅: hook created id=42 target=%s/%s", hooks[0].TargetType, hooks[0].TargetPath)

	// ── Step 3: Decrypt webhook secret ───────────────────────────────────
	// Read connection row, decrypt webhook_secret_ciphertext to get plaintext.
	// Use the same secretbox key (wired via setupGitLabBox) for decryption.

	secretBytes, err := testHandler.GitLabBox.Open(conn.WebhookSecretCiphertext)
	if err != nil {
		t.Fatalf("Step 3: Open(webhook_secret_ciphertext): %v", err)
	}
	webhookSecret := string(secretBytes)
	if len(webhookSecret) != 64 { // 32 random bytes → 64 hex chars
		t.Errorf("Step 3: webhook secret length = %d, want 64 hex chars", len(webhookSecret))
	}

	// Verify the hash stored in DB matches sha256(secretHex).
	hash := sha256.Sum256([]byte(webhookSecret))
	if subtle.ConstantTimeCompare(hash[:], conn.WebhookSecretHash) != 1 {
		t.Error("Step 3: stored webhook_secret_hash does not match sha256(decrypted secret)")
	}

	t.Logf("Step 3 ✅: webhook secret decrypted, hash verified")

	// ── Step 4: MR open webhook → auto-link + broadcast ──────────────────
	// Seed an issue with the test workspace prefix (HAN).
	// POST /api/webhooks/gitlab with MR open fixture.
	// Title "Closes <PREFIX>-1" should trigger auto-link with close_intent=true.

	// Set workspace prefix to "HAN" (already the default from TestMain).
	// Seed the issue.
	w := httptest.NewRecorder()
	createReq := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":  "Closes HAN-1",
		"status": "in_progress",
	})
	testHandler.CreateIssue(w, createReq)
	if w.Code != http.StatusCreated {
		t.Fatalf("Step 4: CreateIssue: %d %s", w.Code, w.Body.String())
	}
	var createdIssue IssueResponse
	json.NewDecoder(w.Body).Decode(&createdIssue)
	issueID := createdIssue.ID

	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM activity_log WHERE issue_id=$1`, issueID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id=$1`, issueID)
	})

	// Capture broadcast events.
	mrBroadcastCh := make(chan events.Event, 2) // may get 2: pull_request:updated + MR-specific
	testHandler.Bus.Subscribe(protocol.EventPullRequestUpdated, func(e events.Event) {
		select {
		case mrBroadcastCh <- e:
		default:
		}
	})
	issueUpdatedCh := make(chan events.Event, 2)
	testHandler.Bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		select {
		case issueUpdatedCh <- e:
		default:
		}
	})

	// Build MR open payload.
	headSHA := "e2e-head-sha-abc123"
	mrPayload := map[string]any{
		"object_kind": "merge_request",
		"project": map[string]any{
			"path_with_namespace": "org/repo",
		},
		"object_attributes": map[string]any{
			"iid":                    1,
			"title":                  "Closes " + createdIssue.Identifier,
			"description":            "Fix for " + createdIssue.Identifier,
			"state":                  "opened",
			"draft":                  false,
			"detailed_merge_status": "mergeable",
			"source_branch":          "feature/e2e-test",
			"last_commit": map[string]any{
				"id": headSHA,
			},
			"merged_at":  nil,
			"closed_at":  nil,
			"url":        fakeGLAPI.URL + "/org/repo/-/merge_requests/1",
			"author_id":  99,
			"created_at": "2026-01-01T12:00:00Z",
			"updated_at": "2026-01-01T12:00:00Z",
			"action":     "open",
		},
	}
	mrBody, _ := json.Marshal(mrPayload)

	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab", strings.NewReader(string(mrBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", webhookSecret)
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	rr = httptest.NewRecorder()
	testHandler.HandleGitLabWebhook(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("Step 4: webhook: expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	// Assert github_pull_request row has provider='gitlab'.
	var gotPR struct {
		ID       string
		Provider string
		State    string
		HeadSha  string
		Title    string
	}
	err = testPool.QueryRow(ctx, `
		SELECT id::text, provider, state, head_sha, title
		FROM github_pull_request
		WHERE workspace_id=$1 AND provider='gitlab' AND repo_owner='org' AND repo_name='repo' AND pr_number=1
	`, testWorkspaceID).Scan(&gotPR.ID, &gotPR.Provider, &gotPR.State, &gotPR.HeadSha, &gotPR.Title)
	if err != nil {
		t.Fatalf("Step 4: query PR row: %v", err)
	}
	if gotPR.Provider != "gitlab" {
		t.Errorf("Step 4: provider = %q, want gitlab", gotPR.Provider)
	}
	if gotPR.State != "open" {
		t.Errorf("Step 4: state = %q, want open", gotPR.State)
	}
	if gotPR.HeadSha != headSHA {
		t.Errorf("Step 4: head_sha = %q, want %q", gotPR.HeadSha, headSHA)
	}

	// Assert issue_pull_request link with close_intent=true.
	var linkCloseIntent bool
	err = testPool.QueryRow(ctx, `
		SELECT close_intent FROM issue_pull_request
		WHERE issue_id=$1 AND pull_request_id=$2
	`, issueID, gotPR.ID).Scan(&linkCloseIntent)
	if err != nil {
		t.Fatalf("Step 4: query issue_pull_request link: %v", err)
	}
	if !linkCloseIntent {
		t.Error("Step 4: close_intent should be true (Closes keyword in title)")
	}

	// Assert pull_request:updated broadcast has provider='gitlab' and linked_issue_ids.
	select {
	case ev := <-mrBroadcastCh:
		payload, ok := ev.Payload.(map[string]any)
		if !ok {
			t.Fatalf("Step 4: broadcast payload type: %T", ev.Payload)
		}
		pr, ok := payload["pull_request"].(GitHubPullRequestResponse)
		if !ok {
			t.Fatalf("Step 4: pull_request payload type: %T", payload["pull_request"])
		}
		if pr.Provider != "gitlab" {
			t.Errorf("Step 4: broadcast provider = %q, want gitlab", pr.Provider)
		}
		linkedIDs, ok := payload["linked_issue_ids"].([]string)
		if !ok {
			t.Fatalf("Step 4: linked_issue_ids type: %T", payload["linked_issue_ids"])
		}
		if len(linkedIDs) != 1 || linkedIDs[0] != issueID {
			t.Errorf("Step 4: linked_issue_ids = %v, want [%s]", linkedIDs, issueID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Step 4: pull_request:updated broadcast not received within 2s")
	}

	t.Logf("Step 4 ✅: MR open linked issue %s, provider=gitlab, close_intent=true", createdIssue.Identifier)

	// ── Step 5: Pipeline running → checks_pending=1 ──────────────────────

	pipelineID := int64(70001)
	pipelineRunningBody := makePipelinePayload("org/repo", pipelineID, headSHA, "feature/e2e-test", "running", 1,
		"finished_at", "")
	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab", strings.NewReader(string(pipelineRunningBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", webhookSecret)
	req.Header.Set("X-Gitlab-Event", "Pipeline Hook")
	rr = httptest.NewRecorder()
	testHandler.HandleGitLabWebhook(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("Step 5: pipeline webhook: expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify check_suite row exists with status='in_progress'.
	var csStatus, csConclusion string
	err = testPool.QueryRow(ctx, `
		SELECT status, COALESCE(conclusion, '') FROM github_pull_request_check_suite
		WHERE pr_id=$1 AND suite_id=$2
	`, gotPR.ID, pipelineID).Scan(&csStatus, &csConclusion)
	if err != nil {
		t.Fatalf("Step 5: query check_suite: %v", err)
	}
	if csStatus != "in_progress" {
		t.Errorf("Step 5: status = %q, want in_progress", csStatus)
	}
	if csConclusion != "" {
		t.Errorf("Step 5: running pipeline should have no conclusion, got %q", csConclusion)
	}

	t.Logf("Step 5 ✅: pipeline running, checks_pending via status=%q", csStatus)

	// ── Step 6: Pipeline success → checks_passed=1, checks_failed=0 ─────

	pipelineID2 := int64(70002)
	pipelineSuccessBody := makePipelinePayload("org/repo", pipelineID2, headSHA, "feature/e2e-test", "success", 1,
		"finished_at", "2026-01-01T12:10:00Z")
	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab", strings.NewReader(string(pipelineSuccessBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", webhookSecret)
	req.Header.Set("X-Gitlab-Event", "Pipeline Hook")
	rr = httptest.NewRecorder()
	testHandler.HandleGitLabWebhook(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("Step 6: pipeline success webhook: expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	err = testPool.QueryRow(ctx, `
		SELECT status, COALESCE(conclusion, '') FROM github_pull_request_check_suite
		WHERE pr_id=$1 AND suite_id=$2
	`, gotPR.ID, pipelineID2).Scan(&csStatus, &csConclusion)
	if err != nil {
		t.Fatalf("Step 6: query check_suite: %v", err)
	}
	if csStatus != "completed" {
		t.Errorf("Step 6: status = %q, want completed", csStatus)
	}
	if csConclusion != "success" {
		t.Errorf("Step 6: conclusion = %q, want success", csConclusion)
	}

	// Aggregate: using the GitHub vocabulary, success pipeline counts as "passed" class.
	// Verify via direct count query.
	var checksPassed, checksFailed int
	err = testPool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN status='completed' AND conclusion IN ('success','neutral','skipped') THEN 1 ELSE 0 END), 0) AS passed,
			COALESCE(SUM(CASE WHEN status='completed' AND conclusion IN ('failure','cancelled','timed_out','action_required','startup_failure','stale') THEN 1 ELSE 0 END), 0) AS failed
		FROM github_pull_request_check_suite WHERE pr_id=$1
	`, gotPR.ID).Scan(&checksPassed, &checksFailed)
	if err != nil {
		t.Fatalf("Step 6: count check_suites: %v", err)
	}
	if checksPassed < 1 {
		t.Errorf("Step 6: checks_passed = %d, want >= 1", checksPassed)
	}
	if checksFailed != 0 {
		t.Errorf("Step 6: checks_failed = %d, want 0", checksFailed)
	}

	t.Logf("Step 6 ✅: pipeline success, checks_passed=%d, checks_failed=%d", checksPassed, checksFailed)

	// ── Step 7: MR merged → issue → done, issue:updated with gitlab_mr_merged ──

	mrMergedPayload := map[string]any{
		"object_kind": "merge_request",
		"project": map[string]any{
			"path_with_namespace": "org/repo",
		},
		"object_attributes": map[string]any{
			"iid":                    1,
			"title":                  "Closes " + createdIssue.Identifier,
			"description":            "Fix description",
			"state":                  "merged",
			"draft":                  false,
			"detailed_merge_status": "mergeable",
			"source_branch":          "feature/e2e-test",
			"last_commit": map[string]any{
				"id": headSHA,
			},
			"merged_at":  "2026-01-01T12:30:00Z",
			"closed_at":  nil,
			"url":        fakeGLAPI.URL + "/org/repo/-/merge_requests/1",
			"author_id":  99,
			"created_at": "2026-01-01T12:00:00Z",
			"updated_at": "2026-01-01T12:30:00Z",
			"action":     "merge",
		},
	}
	mrMergedBody, _ := json.Marshal(mrMergedPayload)

	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab", strings.NewReader(string(mrMergedBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", webhookSecret)
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	rr = httptest.NewRecorder()
	testHandler.HandleGitLabWebhook(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("Step 7: MR merged webhook: expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	// Issue should now be 'done'.
	updatedIssue, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatalf("Step 7: GetIssue: %v", err)
	}
	if updatedIssue.Status != "done" {
		t.Errorf("Step 7: issue status = %q, want done", updatedIssue.Status)
	}

	// Assert issue:updated broadcast with source='gitlab_mr_merged'.
	foundSource := false
	timeout := time.After(2 * time.Second)
loop7:
	for {
		select {
		case ev := <-issueUpdatedCh:
			payload, ok := ev.Payload.(map[string]any)
			if !ok {
				continue
			}
			source, _ := payload["source"].(string)
			if source == "gitlab_mr_merged" {
				foundSource = true
				// Also verify issue is in the payload.
				issueMap, ok := payload["issue"].(IssueResponse)
				if ok && issueMap.ID == issueID {
					if issueMap.Status != "done" {
						t.Errorf("Step 7: broadcast issue status = %q, want done", issueMap.Status)
					}
				}
				break loop7
			}
		case <-timeout:
			break loop7
		}
	}
	if !foundSource {
		t.Error("Step 7: issue:updated broadcast with source='gitlab_mr_merged' not found")
	}

	// Drain remaining broadcasts.
drain7:
	for {
		select {
		case <-issueUpdatedCh:
		default:
			break drain7
		}
	}

	t.Logf("Step 7 ✅: MR merged → issue %s advanced to done, source=gitlab_mr_merged broadcast", createdIssue.Identifier)

	// ── Step 8: reviewer-loop head_sha ───────────────────────────────────
	// GetIssueReviewHeadSha(ctx, issueID) returns the MR's head_sha.
	// This is provider-agnostic; the query joins via issue_pull_request.

	resolvedSHA := testHandler.TaskService.ResolveIssueReviewSHA(ctx, parseUUID(issueID))
	if resolvedSHA != headSHA {
		t.Errorf("Step 8: ResolveIssueReviewSHA = %q, want %q", resolvedSHA, headSHA)
	}

	t.Logf("Step 8 ✅: reviewer-loop head_sha resolved: %s", resolvedSHA)
}

// ── E2E: failure scenarios ───────────────────────────────────────────────────

// TestGitLabE2E_FailureScenarios tests error paths through the webhook ingress
// and connection management endpoints.
func TestGitLabE2E_FailureScenarios(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()

	// ── Test 1: Missing token → 401 ──────────────────────────────────────

	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	rr := httptest.NewRecorder()
	testHandler.HandleGitLabWebhook(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Failure 1: missing token: expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
	t.Log("Failure 1 ✅: missing token → 401")

	// ── Test 2: Wrong token → 401 ────────────────────────────────────────

	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitlab-Token", "wrong-token-no-match")
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	rr = httptest.NewRecorder()
	testHandler.HandleGitLabWebhook(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Failure 2: wrong token: expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
	t.Log("Failure 2 ✅: wrong token → 401")

	// ── Test 3: Locked MR → closed state ─────────────────────────────────
	// Seeded connection + locked MR webhook → row state should be 'closed'.

	setupGitLabBox(t)

	fakeGLLocked := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v4/user" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"username":"locked-user","name":"Locked"}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v4/users/") && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":1,"username":"locked-user","avatar_url":""}`))
			return
		}
		http.NotFound(w, r)
	})

	// Seed a connection with proper encrypted token/secret via seedGitLabHooksConnection.
	lockedConn := seedGitLabHooksConnection(t, fakeGLLocked.URL, nil)

	lockedPayload := map[string]any{
		"object_kind": "merge_request",
		"project": map[string]any{
			"path_with_namespace": "locked-org/repo",
		},
		"object_attributes": map[string]any{
			"iid":                    2,
			"title":                  "Locked MR test",
			"description":            "Should be closed",
			"state":                  "locked",
			"draft":                  false,
			"detailed_merge_status": "not_open",
			"source_branch":          "locked-branch",
			"last_commit": map[string]any{
				"id": "locked-sha",
			},
			"merged_at":  nil,
			"closed_at":  nil,
			"url":        fakeGLLocked.URL + "/locked-org/repo/-/merge_requests/2",
			"author_id":  1,
			"created_at": "2026-01-01T12:00:00Z",
			"updated_at": "2026-01-01T12:00:00Z",
			"action":     "update",
		},
	}
	lockedBody, _ := json.Marshal(lockedPayload)

	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab", strings.NewReader(string(lockedBody)))
	req.Header.Set("Content-Type", "application/json")
	// Decrypt the webhook secret so the token header matches the stored hash.
	lockedSecretBytes, _ := testHandler.GitLabBox.Open(lockedConn.WebhookSecretCiphertext)
	lockedSecret := string(lockedSecretBytes)
	req.Header.Set("X-Gitlab-Token", lockedSecret)
	req.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	rr = httptest.NewRecorder()
	testHandler.HandleGitLabWebhook(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("Failure 3: locked MR webhook: expected 202, got %d: %s", rr.Code, rr.Body.String())
	}

	// Locked MR should map to closed state (deriveGitLabMRState).
	var lockedState string
	err := testPool.QueryRow(ctx, `
		SELECT state FROM github_pull_request
		WHERE workspace_id=$1 AND provider='gitlab' AND repo_owner='locked-org' AND repo_name='repo' AND pr_number=2
	`, testWorkspaceID).Scan(&lockedState)
	if err != nil {
		t.Fatalf("Failure 3: query locked MR: %v", err)
	}
	if lockedState != "closed" {
		t.Errorf("Failure 3: locked MR state = %q, want closed", lockedState)
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab' AND repo_owner='locked-org'`, testWorkspaceID)
	})

	t.Log("Failure 3 ✅: locked MR → closed state")

	// ── Test 4: Group hook target returns 403 → 422 with Premium/Owner ───
	// Use a fake API that returns 403 for group hooks.

	setPublicURL(t, "https://multica.example")

	adminID := seedMember(t, "admin")
	fakeGLForbidden := fakeGitLabAPIServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v4/user" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"username":"forbidden-user","name":"Blocked"}`))
			return
		}
		if strings.Contains(r.URL.Path, "/groups/") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"message":"403 Forbidden"}`))
			return
		}
		http.NotFound(w, r)
	})

	// Create a connection pointed at this fake.
	body := fmt.Sprintf(`{"instance_url":"%s","access_token":"glpat-forbidden-token","display_name":"Forbidden"}`, fakeGLForbidden.URL)
	router := newGitLabFullRouter(testHandler)
	req = routeRequestWithBody("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections",
		body, adminID)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("Failure 4: create connection: expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var forbConn GitLabConnectionResponse
	json.Unmarshal(rr.Body.Bytes(), &forbConn)

	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM github_installation WHERE id=$1`, forbConn.ID)
	})

	req = routeRequestWithBody("POST",
		"/api/workspaces/"+testWorkspaceID+"/gitlab/connections/"+forbConn.ID+"/hooks",
		`{"target_type":"group","target_path":"my-group"}`, adminID)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("Failure 4: group 403: expected 422, got %d: %s", rr.Code, rr.Body.String())
	}
	respBody := rr.Body.String()
	if !strings.Contains(respBody, "Premium") || !strings.Contains(respBody, "Owner") {
		t.Errorf("Failure 4: 422 body must mention Premium and Owner, got: %q", respBody)
	}

	t.Log("Failure 4 ✅: group hook 403 → 422 with Premium/Owner hint")
}

// ── Router helpers ───────────────────────────────────────────────────────────

// newGitLabFullRouter builds a chi router with all GitLab routes wired via
// middleware (mirrors the production router). Used by E2E tests so every
// handler goes through its real middleware chain.
func newGitLabFullRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	r.Route("/api/workspaces/{id}", func(r chi.Router) {
		// Connections: member-read, admin-write.
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
			r.Get("/gitlab/connections", h.ListGitLabConnections)
		})
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner", "admin"))
			r.Post("/gitlab/connections", h.CreateGitLabConnection)
			r.Delete("/gitlab/connections/{connectionID}", h.DeleteGitLabConnection)
		})
		// Hooks: admin-only.
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner", "admin"))
			r.Post("/gitlab/connections/{connectionID}/hooks", h.AddGitLabHookTarget)
			r.Delete("/gitlab/connections/{connectionID}/hooks", h.RemoveGitLabHookTarget)
		})
	})
	return r
}


