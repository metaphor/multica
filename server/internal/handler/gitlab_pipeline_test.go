package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ── Helpers ─────────────────────────────────────────────────────────────────

// makePipelinePayload builds a GitLab Pipeline webhook payload as a
// map[string]any (simulating the raw JSON body from GitLab).
func makePipelinePayload(project string, pipelineID int64, sha, ref, status string, mrIID int64, extra ...any) []byte {
	payload := map[string]any{
		"object_kind": "pipeline",
		"project": map[string]any{
			"path_with_namespace": project,
		},
		"object_attributes": map[string]any{
			"id":         pipelineID,
			"sha":        sha,
			"ref":        ref,
			"status":     status,
			"created_at": "2024-01-01T12:00:00Z",
			"url":        "https://gitlab.example.com/org/sub/repo/-/pipelines/" + strconv.FormatInt(pipelineID, 10),
		},
	}
	if mrIID > 0 {
		payload["merge_request"] = map[string]any{"iid": mrIID}
	}
	for i := 0; i+1 < len(extra); i += 2 {
		key, ok := extra[i].(string)
		if !ok {
			continue
		}
		payload["object_attributes"].(map[string]any)[key] = extra[i+1]
	}

	b, _ := json.Marshal(payload)
	return b
}

// seedGitLabPipelineFixture creates a GitLab connection, optionally fires a
// MR webhook to mirror an MR row, and returns the connection and optional
// MR row. The caller is responsible for cleanup.
func seedGitLabPipelineFixture(t *testing.T, withMR bool, mrIID int64) (db.GithubInstallation, db.GithubPullRequest, bool) {
	t.Helper()
	ctx := context.Background()

	fakeGL := newFakeGitLabAPI(t, "gl-pl-user", "", http.StatusOK)
	t.Cleanup(func() { fakeGL.Close() })
	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-pl-token")

	if !withMR {
		t.Cleanup(func() {
			testPool.Exec(context.Background(),
				`DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
		})
		return conn, db.GithubPullRequest{}, false
	}

	// Mirror an MR so the pipeline has something to attribute to.
	body := makeMRPayload("org/sub/repo", "open", "opened", "mergeable",
		"abc123def456", 42, "iid", mrIID)
	if err := testHandler.handleGitLabMergeRequestEvent(ctx, conn, body); err != nil {
		t.Fatalf("seed mr: %v", err)
	}

	pr, err := testHandler.Queries.GetGitLabMergeRequest(ctx, db.GetGitLabMergeRequestParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		RepoOwner:   "org/sub",
		RepoName:    "repo",
		PrNumber:    int32(mrIID),
	})
	if err != nil {
		t.Fatalf("lookup seeded mr: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM github_pull_request_check_suite WHERE pr_id=$1`, uuidToString(pr.ID))
		testPool.Exec(context.Background(),
			`DELETE FROM github_pending_check_suite WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
		testPool.Exec(context.Background(),
			`DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})

	return conn, pr, true
}

// fireGitLabPipelineEvent calls handleGitLabPipelineEvent with the given
// payload and returns any error.
func fireGitLabPipelineEvent(t *testing.T, conn db.GithubInstallation, body []byte) error {
	t.Helper()
	ctx := context.Background()
	return testHandler.handleGitLabPipelineEvent(ctx, conn, body)
}

// getCheckSuiteRow queries the check_suite row by pr_id and suite_id.
func getCheckSuiteRow(t *testing.T, prID string, suiteID int64) (status string, conclusion pgtype.Text, appID int64) {
	t.Helper()
	ctx := context.Background()
	err := testPool.QueryRow(ctx, `
		SELECT status, conclusion, app_id
		FROM github_pull_request_check_suite
		WHERE pr_id = $1 AND suite_id = $2
	`, prID, suiteID).Scan(&status, &conclusion, &appID)
	if err != nil {
		t.Fatalf("query check_suite: pr_id=%s suite_id=%d: %v", prID, suiteID, err)
	}
	return
}

// confirmCheckSuitePending verifies a row exists in github_pending_check_suite
// with the given pr_number constraint.
func pendingStashCount(t *testing.T) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM github_pending_check_suite WHERE workspace_id=$1 AND provider='gitlab'`,
		testWorkspaceID).Scan(&n); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	return n
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestGitLabPipeline_HappyMRMatched(t *testing.T) {
	conn, pr, _ := seedGitLabPipelineFixture(t, true, 1)

	body := makePipelinePayload("org/sub/repo", 1001, pr.HeadSha, "main", "success", 1,
		"finished_at", "2024-01-01T12:10:00Z")
	if err := fireGitLabPipelineEvent(t, conn, body); err != nil {
		t.Fatalf("handleGitLabPipelineEvent: %v", err)
	}

	status, conclusion, appID := getCheckSuiteRow(t, uuidToString(pr.ID), 1001)
	if status != "completed" {
		t.Errorf("status = %q, want completed", status)
	}
	if conclusion.String != "success" {
		t.Errorf("conclusion = %q, want success", conclusion.String)
	}
	if appID != -1 {
		t.Errorf("app_id = %d, want -1", appID)
	}

	// Verify the MR shows checks_passed via ListPullRequestsByIssue (indirect
	// via the poll lookup — we query the check_suite row directly).
}

func TestGitLabPipeline_OutOfOrderStashDrain(t *testing.T) {
	// Pipeline arrives first — no MR mirrored yet → stash.
	fakeGL := newFakeGitLabAPI(t, "gl-pl-stash", "", http.StatusOK)
	defer fakeGL.Close()
	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-stash")

	body := makePipelinePayload("org/sub/repo", 2001, "abc987def654", "feature", "success", 5,
		"finished_at", "2024-01-01T12:05:00Z")
	if err := fireGitLabPipelineEvent(t, conn, body); err != nil {
		t.Fatalf("stash pipeline: %v", err)
	}

	if n := pendingStashCount(t); n != 1 {
		t.Fatalf("expected 1 pending stash, got %d", n)
	}

	// Now MR arrives → should drain + replay the pipeline.
	mrBody := makeMRPayload("org/sub/repo", "open", "opened", "mergeable",
		"abc987def654", 42, "iid", 5)
	if err := testHandler.handleGitLabMergeRequestEvent(context.Background(), conn, mrBody); err != nil {
		t.Fatalf("mr event: %v", err)
	}

	// Stash should be gone.
	if n := pendingStashCount(t); n != 0 {
		t.Errorf("expected 0 pending after drain, got %d", n)
	}

	// Check_suite row should exist.
	ctx := context.Background()
	pr, err := testHandler.Queries.GetGitLabMergeRequest(ctx, db.GetGitLabMergeRequestParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		RepoOwner:   "org/sub",
		RepoName:    "repo",
		PrNumber:    5,
	})
	if err != nil {
		t.Fatalf("lookup mr after drain: %v", err)
	}

	status, conclusion, appID := getCheckSuiteRow(t, uuidToString(pr.ID), 2001)
	if status != "completed" {
		t.Errorf("status = %q, want completed", status)
	}
	if conclusion.String != "success" {
		t.Errorf("conclusion = %q, want success", conclusion.String)
	}
	if appID != -1 {
		t.Errorf("app_id = %d, want -1", appID)
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM github_pull_request_check_suite WHERE pr_id=$1`, uuidToString(pr.ID))
		testPool.Exec(context.Background(),
			`DELETE FROM github_pending_check_suite WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
		testPool.Exec(context.Background(),
			`DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})
}

func TestGitLabPipeline_ShaOnlyAttribution(t *testing.T) {
	// Branch pipeline with no merge_request key — must match via head sha.
	fakeGL := newFakeGitLabAPI(t, "gl-pl-sha", "", http.StatusOK)
	defer fakeGL.Close()
	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-sha")

	// Pre-seed an open MR with the known head sha.
	mrBody := makeMRPayload("org/sub/repo", "open", "opened", "mergeable",
		"sha123abc456", 42, "iid", 10)
	if err := testHandler.handleGitLabMergeRequestEvent(context.Background(), conn, mrBody); err != nil {
		t.Fatalf("seed mr: %v", err)
	}

	// Pipeline payload has no merge_request key, but sha matches.
	body := makePipelinePayload("org/sub/repo", 3001, "sha123abc456", "feature",
		"success", 0, // no merge_request
		"finished_at", "2024-01-01T12:05:00Z")
	if err := fireGitLabPipelineEvent(t, conn, body); err != nil {
		t.Fatalf("sha pipeline: %v", err)
	}

	ctx := context.Background()
	pr, err := testHandler.Queries.GetGitLabMergeRequest(ctx, db.GetGitLabMergeRequestParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		RepoOwner:   "org/sub",
		RepoName:    "repo",
		PrNumber:    10,
	})
	if err != nil {
		t.Fatalf("lookup mr: %v", err)
	}

	status, conclusion, _ := getCheckSuiteRow(t, uuidToString(pr.ID), 3001)
	if status != "completed" || conclusion.String != "success" {
		t.Errorf("status=%q conclusion=%q, want completed/success", status, conclusion.String)
	}

	// Pipeline with no matching MR and no sha → stash with pr_number=0.
	body2 := makePipelinePayload("org/sub/repo", 3002, "unknown-sha", "feature",
		"running", 0,
		"created_at", "2024-01-01T12:00:00Z")
	if err := fireGitLabPipelineEvent(t, conn, body2); err != nil {
		t.Fatalf("no-match pipeline: %v", err)
	}

	if n := pendingStashCount(t); n != 1 {
		t.Errorf("expected 1 stash for pr_number=0, got %d", n)
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM github_pull_request_check_suite WHERE pr_id=$1`, uuidToString(pr.ID))
		testPool.Exec(context.Background(),
			`DELETE FROM github_pending_check_suite WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
		testPool.Exec(context.Background(),
			`DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})
}

func TestGitLabPipeline_OutOfOrderLateRunning(t *testing.T) {
	// success lands first, then a delayed running arrives → row keeps success.
	conn, pr, _ := seedGitLabPipelineFixture(t, true, 1)

	// First: success.
	b1 := makePipelinePayload("org/sub/repo", 4001, pr.HeadSha, "main", "success", 1,
		"finished_at", "2024-01-01T12:10:00Z")
	if err := fireGitLabPipelineEvent(t, conn, b1); err != nil {
		t.Fatalf("success: %v", err)
	}

	// Second: delayed running (same pipeline id, earlier timestamp).
	b2 := makePipelinePayload("org/sub/repo", 4001, pr.HeadSha, "main", "running", 1,
		"created_at", "2024-01-01T12:00:00Z")
	if err := fireGitLabPipelineEvent(t, conn, b2); err != nil {
		t.Fatalf("delayed running: %v", err)
	}

	// Row must still be success — rank guard prevents downgrade.
	status, conclusion, _ := getCheckSuiteRow(t, uuidToString(pr.ID), 4001)
	if status != "completed" {
		t.Errorf("status = %q, want completed (delayed running must not overwrite)", status)
	}
	if conclusion.String != "success" {
		t.Errorf("conclusion = %q, want success", conclusion.String)
	}
}

func TestGitLabPipeline_FailedAndCanceledMapping(t *testing.T) {
	conn, pr, _ := seedGitLabPipelineFixture(t, true, 1)

	// failed → ('completed', 'failure')
	b1 := makePipelinePayload("org/sub/repo", 5001, pr.HeadSha, "main", "failed", 1,
		"finished_at", "2024-01-01T12:10:00Z")
	if err := fireGitLabPipelineEvent(t, conn, b1); err != nil {
		t.Fatalf("failed: %v", err)
	}
	status, conclusion, _ := getCheckSuiteRow(t, uuidToString(pr.ID), 5001)
	if status != "completed" || conclusion.String != "failure" {
		t.Errorf("failed mapping: status=%q conclusion=%q, want completed/failure", status, conclusion.String)
	}

	// canceled → ('completed', 'cancelled')
	b2 := makePipelinePayload("org/sub/repo", 5002, pr.HeadSha, "main", "canceled", 1,
		"finished_at", "2024-01-01T12:20:00Z")
	if err := fireGitLabPipelineEvent(t, conn, b2); err != nil {
		t.Fatalf("canceled: %v", err)
	}
	status, conclusion, _ = getCheckSuiteRow(t, uuidToString(pr.ID), 5002)
	if status != "completed" || conclusion.String != "cancelled" {
		t.Errorf("canceled mapping: status=%q conclusion=%q, want completed/cancelled", status, conclusion.String)
	}
}

func TestGitLabPipeline_RetryCollapse(t *testing.T) {
	// Same sha retried twice: first failed, then success. Different pipeline ids.
	// With app_id=-1, DISTINCT ON (pr_id, app_id) collapses to latest (success).
	conn, pr, _ := seedGitLabPipelineFixture(t, true, 1)

	// First attempt: failed.
	b1 := makePipelinePayload("org/sub/repo", 6001, pr.HeadSha, "main", "failed", 1,
		"finished_at", "2024-01-01T12:10:00Z")
	if err := fireGitLabPipelineEvent(t, conn, b1); err != nil {
		t.Fatalf("failed: %v", err)
	}

	// Second attempt: success (different pipeline id).
	b2 := makePipelinePayload("org/sub/repo", 6002, pr.HeadSha, "main", "success", 1,
		"finished_at", "2024-01-01T12:20:00Z")
	if err := fireGitLabPipelineEvent(t, conn, b2); err != nil {
		t.Fatalf("success: %v", err)
	}

	// Both rows exist (different suite_id → different ON CONFLICT key).
	status1, _, _ := getCheckSuiteRow(t, uuidToString(pr.ID), 6001)
	status2, conclusion2, _ := getCheckSuiteRow(t, uuidToString(pr.ID), 6002)
	if status1 != "completed" {
		t.Errorf("suite 6001 status = %q, want completed", status1)
	}
	if status2 != "completed" || conclusion2.String != "success" {
		t.Errorf("suite 6002 status=%q conclusion=%q, want completed/success", status2, conclusion2.String)
	}

	// Verify DISTINCT ON (pr_id, app_id) collapses both to the latest.
	// Since both have app_id=-1, only the most recent (by updated_at desc) is kept.
	ctx := context.Background()
	var totalChecks int
	if err := testPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM (
			SELECT DISTINCT ON (pr_id, app_id) pr_id
			FROM github_pull_request_check_suite
			WHERE pr_id = $1 AND head_sha = $2
			ORDER BY pr_id, app_id, updated_at DESC
		) t
	`, uuidToString(pr.ID), pr.HeadSha).Scan(&totalChecks); err != nil {
		t.Fatalf("count distinct checks: %v", err)
	}
	if totalChecks != 1 {
		t.Errorf("DISTINCT ON (pr_id, app_id) returned %d rows, want 1 (both collapsed)", totalChecks)
	}
}

func TestGitLabPipeline_IdempotentDuplicate(t *testing.T) {
	// Same pipeline payload delivered twice → single row.
	conn, pr, _ := seedGitLabPipelineFixture(t, true, 1)

	body := makePipelinePayload("org/sub/repo", 7001, pr.HeadSha, "main", "success", 1,
		"finished_at", "2024-01-01T12:10:00Z")
	if err := fireGitLabPipelineEvent(t, conn, body); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := fireGitLabPipelineEvent(t, conn, body); err != nil {
		t.Fatalf("duplicate: %v", err)
	}

	// Only one row must exist.
	ctx := context.Background()
	var n int
	if err := testPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM github_pull_request_check_suite
		WHERE pr_id = $1 AND suite_id = 7001
	`, uuidToString(pr.ID)).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row, got %d", n)
	}
}

func TestSweeper_DeleteStalePendingCheckSuites(t *testing.T) {
	ctx := context.Background()

	// Insert a stale (8-day-old) pending row and a fresh (2-day-old) pending row.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO github_pending_check_suite
		(workspace_id, provider, installation_id, repo_owner, repo_name, pr_number,
		 suite_id, head_sha, app_id, conclusion, status, suite_updated_at, received_at)
		VALUES
		($1, 'gitlab', 0, 'o', 'r', 0, 8001, 'abc', -1, NULL, 'queued', now(), now() - interval '8 days')
	`, testWorkspaceID); err != nil {
		t.Fatalf("insert stale: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO github_pending_check_suite
		(workspace_id, provider, installation_id, repo_owner, repo_name, pr_number,
		 suite_id, head_sha, app_id, conclusion, status, suite_updated_at, received_at)
		VALUES
		($1, 'gitlab', 0, 'o', 'r', 0, 8002, 'def', -1, NULL, 'queued', now(), now() - interval '2 days')
	`, testWorkspaceID); err != nil {
		t.Fatalf("insert fresh: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM github_pending_check_suite WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})

	if err := testHandler.Queries.DeleteStalePendingCheckSuites(ctx); err != nil {
		t.Fatalf("sweeper: %v", err)
	}

	// Stale row (8 days) must be deleted.
	var staleCount int
	if err := testPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM github_pending_check_suite
		WHERE suite_id = 8001
	`).Scan(&staleCount); err != nil {
		t.Fatalf("count stale: %v", err)
	}
	if staleCount != 0 {
		t.Errorf("stale row not deleted: got %d", staleCount)
	}

	// Fresh row (2 days) must survive.
	var freshCount int
	if err := testPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM github_pending_check_suite
		WHERE suite_id = 8002
	`).Scan(&freshCount); err != nil {
		t.Fatalf("count fresh: %v", err)
	}
	if freshCount != 1 {
		t.Errorf("fresh row deleted: got %d", freshCount)
	}
}

func TestGitLabPipeline_BroadcastOnUpsert(t *testing.T) {
	conn, _, _ := seedGitLabPipelineFixture(t, true, 1)
	ctx := context.Background()

	pr, err := testHandler.Queries.GetGitLabMergeRequest(ctx, db.GetGitLabMergeRequestParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		RepoOwner:   "org/sub",
		RepoName:    "repo",
		PrNumber:    1,
	})
	if err != nil {
		t.Fatalf("lookup mr: %v", err)
	}

	// Capture broadcast.
	ch := make(chan events.Event, 1)
	testHandler.Bus.Subscribe(protocol.EventPullRequestUpdated, func(e events.Event) {
		select {
		case ch <- e:
		default:
		}
	})

	body := makePipelinePayload("org/sub/repo", 9001, pr.HeadSha, "main", "success", 1,
		"finished_at", "2024-01-01T12:10:00Z")
	if err := fireGitLabPipelineEvent(t, conn, body); err != nil {
		t.Fatalf("pipeline: %v", err)
	}

	select {
	case ev := <-ch:
		payload, ok := ev.Payload.(map[string]any)
		if !ok {
			t.Fatalf("broadcast payload type: %T", ev.Payload)
		}
		_, ok = payload["pull_request"]
		if !ok {
			t.Error("broadcast missing pull_request field")
		}
		_, ok = payload["linked_issue_ids"]
		if !ok {
			t.Error("broadcast missing linked_issue_ids field")
		}
	case <-time.After(time.Second):
		t.Fatal("no broadcast received")
	}

	t.Cleanup(func() {
		select {
		case <-ch:
		default:
		}
		testPool.Exec(context.Background(),
			`DELETE FROM github_pull_request_check_suite WHERE pr_id=$1`, uuidToString(pr.ID))
		testPool.Exec(context.Background(),
			`DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})
}

func TestGitLabPipeline_StatusMapping(t *testing.T) {
	cases := []struct {
		glStatus         string
		wantStatus       string
		wantConclusion   string
		wantConclusionOK bool
	}{
		{"created", "queued", "", false},
		{"waiting_for_resource", "queued", "", false},
		{"preparing", "queued", "", false},
		{"pending", "queued", "", false},
		{"scheduled", "queued", "", false},
		{"manual", "queued", "", false},
		{"running", "in_progress", "", false},
		{"success", "completed", "success", true},
		{"failed", "completed", "failure", true},
		{"canceled", "completed", "cancelled", true},
		{"skipped", "completed", "skipped", true},
		{"unknown_status", "queued", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.glStatus, func(t *testing.T) {
			status, conclusion := derivePipelineStatusConclusion(tc.glStatus)
			if status != tc.wantStatus {
				t.Errorf("status = %q, want %q", status, tc.wantStatus)
			}
			if tc.wantConclusionOK {
				if !conclusion.Valid || conclusion.String != tc.wantConclusion {
					t.Errorf("conclusion = %q (valid=%v), want %q (valid=true)",
						conclusion.String, conclusion.Valid, tc.wantConclusion)
				}
			} else {
				if conclusion.Valid {
					t.Errorf("conclusion should be NULL, got %q", conclusion.String)
				}
			}
		})
	}
}
