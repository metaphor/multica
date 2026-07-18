package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ── Test helpers ─────────────────────────────────────────────────────────────

// seedGitLabConnectionMRTest creates a GitLab connection with an encrypted
// personal access token so author backfill can work. The returned connection
// row is cleaned up after the test.
func seedGitLabConnectionMRTest(t *testing.T, instanceURL, token string) db.GithubInstallation {
	t.Helper()
	setupGitLabBox(t)

	ctx := context.Background()

	// Encrypt the token.
	ciphertext, err := testHandler.GitLabBox.Seal([]byte(token))
	if err != nil {
		t.Fatalf("seal access token: %v", err)
	}

	// Webhook secret — not used in MR tests but required by Insert.
	webhookSecret := make([]byte, 32)
	webhookCipher, err := testHandler.GitLabBox.Seal(webhookSecret)
	if err != nil {
		t.Fatalf("seal webhook secret: %v", err)
	}

	conn, err := testHandler.Queries.InsertGitLabConnection(ctx, db.InsertGitLabConnectionParams{
		WorkspaceID:             parseUUID(testWorkspaceID),
		AccountLogin:            "gitlab-mr-test-user",
		DisplayName:             pgtype.Text{String: "MR Test", Valid: true},
		InstanceUrl:             pgtype.Text{String: instanceURL, Valid: true},
		AccessTokenCiphertext:   ciphertext,
		WebhookSecretHash:       make([]byte, 32), // sha256 of secret; unused here
		WebhookSecretCiphertext: webhookCipher,
		Hooks:                   []byte("[]"),
	})
	if err != nil {
		t.Fatalf("seed gitlab connection: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM github_installation WHERE id=$1`, uuidToString(conn.ID))
	})
	return conn
}

// makeMRPayload builds a GitLab Merge Request webhook payload as
// map[string]any (simulating the raw JSON body).
func makeMRPayload(project, action, state, detailedMergeStatus, headSHA string, authorID int64, extra ...any) []byte {
	payload := map[string]any{
		"object_kind": "merge_request",
		"project": map[string]any{
			"path_with_namespace": project,
		},
		"object_attributes": map[string]any{
			"iid":                    1,
			"title":                  "Test MR",
			"description":            "MR description",
			"state":                  state,
			"draft":                  false,
			"detailed_merge_status": detailedMergeStatus,
			"source_branch":          "feature-branch",
			"last_commit": map[string]any{
				"id": headSHA,
			},
			"merged_at":  nil,
			"closed_at":  nil,
			"url":        "http://gitlab.example.com/org/sub/repo/-/merge_requests/1",
			"author_id":  authorID,
			"created_at": "2024-01-01T12:00:00Z",
			"updated_at": "2024-01-01T12:00:00Z",
			"action":     action,
		},
	}

	// Process extra key-value pairs.
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

// initMRBroadcastCapture subscribes to pull_request:updated events before
// the MR handler fires and returns a channel that receives at most one event.
func initMRBroadcastCapture(t *testing.T) chan events.Event {
	t.Helper()
	ch := make(chan events.Event, 1)
	testHandler.Bus.Subscribe(protocol.EventPullRequestUpdated, func(e events.Event) {
		select {
		case ch <- e:
		default:
		}
	})
	t.Cleanup(func() {
		// Drain and close to prevent goroutine leak.
		select {
		case <-ch:
		default:
		}
	})
	return ch
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestGitLabMR_OpenMergeable(t *testing.T) {
	fakeGL := newFakeGitLabAPI(t, "gl-test-user", "https://example.com/avatar.png", http.StatusOK)
	defer fakeGL.Close()

	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-test-token")
	ch := initMRBroadcastCapture(t)

	body := makeMRPayload("org/sub/repo", "open", "opened", "mergeable",
		"abc123def456", 42)
	err := testHandler.handleGitLabMergeRequestEvent(context.Background(), conn, body)
	if err != nil {
		t.Fatalf("handleGitLabMergeRequestEvent: %v", err)
	}

	// Assert DB row.
	ctx := context.Background()

	// Direct query for the MR row.
	var got struct {
		RepoOwner      string
		RepoName       string
		PrNumber       int32
		State          string
		HeadSha        string
		MergeableState string
		Provider       string
		AuthorLogin    string
	}
	err = testPool.QueryRow(ctx, `
		SELECT repo_owner, repo_name, pr_number, state, head_sha,
		       mergeable_state, provider, author_login
		FROM github_pull_request
		WHERE workspace_id = $1 AND repo_owner = $2 AND repo_name = $3 AND pr_number = $4 AND provider = 'gitlab'
	`, testWorkspaceID, "org/sub", "repo", 1).Scan(
		&got.RepoOwner, &got.RepoName, &got.PrNumber, &got.State,
		&got.HeadSha, &got.MergeableState, &got.Provider, &got.AuthorLogin)
	if err != nil {
		t.Fatalf("query mr row: %v", err)
	}

	if got.RepoOwner != "org/sub" {
		t.Errorf("repo_owner = %q, want %q", got.RepoOwner, "org/sub")
	}
	if got.RepoName != "repo" {
		t.Errorf("repo_name = %q, want %q", got.RepoName, "repo")
	}
	if got.State != "open" {
		t.Errorf("state = %q, want open", got.State)
	}
	if got.HeadSha != "abc123def456" {
		t.Errorf("head_sha = %q, want abc123def456", got.HeadSha)
	}
	if got.MergeableState != "clean" {
		t.Errorf("mergeable_state = %q, want clean", got.MergeableState)
	}
	if got.Provider != "gitlab" {
		t.Errorf("provider = %q, want gitlab", got.Provider)
	}
	if got.AuthorLogin != "gl-test-user" {
		t.Errorf("author_login = %q, want gl-test-user (backfilled)", got.AuthorLogin)
	}

	// Assert broadcast payload.
	select {
	case ev := <-ch:
		payload, ok := ev.Payload.(map[string]any)
		if !ok {
			t.Fatalf("broadcast payload type: %T", ev.Payload)
		}
		pr, ok := payload["pull_request"].(GitHubPullRequestResponse)
		if !ok {
			t.Fatalf("pull_request payload type: %T", payload["pull_request"])
		}
		if pr.Provider != "gitlab" {
			t.Errorf("broadcast provider = %q, want gitlab", pr.Provider)
		}
		if pr.RepoOwner != "org/sub" {
			t.Errorf("broadcast repo_owner = %q, want org/sub", pr.RepoOwner)
		}
		if pr.State != "open" {
			t.Errorf("broadcast state = %q, want open", pr.State)
		}
	case <-time.After(time.Second):
		t.Fatal("broadcast not received within 1s")
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})
}

func TestGitLabMR_LegacyWorkInProgress(t *testing.T) {
	fakeGL := newFakeGitLabAPI(t, "gl-legacy-user", "", http.StatusOK)
	defer fakeGL.Close()

	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-test-token")
	ch := initMRBroadcastCapture(t)

	// Old-style: work_in_progress=true, no draft field, no merged_at.
	body := makeMrPayloadLegacy(t, "org/sub/repo", "open", "opened", "mergeable",
		"sha-wip", 99, true, nil)
	err := testHandler.handleGitLabMergeRequestEvent(context.Background(), conn, body)
	if err != nil {
		t.Fatalf("handleGitLabMergeRequestEvent: %v", err)
	}

	ctx := context.Background()
	var state, headSha string
	err = testPool.QueryRow(ctx, `
		SELECT state, head_sha FROM github_pull_request
		WHERE workspace_id=$1 AND provider='gitlab' AND repo_owner='org/sub' AND repo_name='repo' AND pr_number=1
	`, testWorkspaceID).Scan(&state, &headSha)
	if err != nil {
		t.Fatalf("query mr: %v", err)
	}
	if state != "open" {
		t.Errorf("state = %q, want open", state)
	}
	if headSha != "sha-wip" {
		t.Errorf("head_sha = %q, want sha-wip", headSha)
	}

	// Broadcast should have arrived.
	select {
	case <-ch:
		// OK
	case <-time.After(time.Second):
		t.Fatal("broadcast not received")
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})
}

// makeMrPayloadLegacy builds a payload where merged_at/closed_at can be set
// and draft is absent while work_in_progress is set.
func makeMrPayloadLegacy(t *testing.T, project, action, state, detailedMergeStatus, headSHA string,
	authorID int64, wip bool, mergedAtOrNil any) []byte {
	t.Helper()

	oa := map[string]any{
		"iid":                    1,
		"title":                  "Legacy MR",
		"description":            nil,
		"state":                  state,
		"work_in_progress":       wip,
		"detailed_merge_status": detailedMergeStatus,
		"source_branch":          "legacy-branch",
		"last_commit":            map[string]any{"id": headSHA},
		"url":                    "http://gitlab.example.com/" + project + "/-/merge_requests/1",
		"author_id":              authorID,
		"created_at":             "2024-01-01T12:00:00Z",
		"updated_at":             "2024-01-01T12:00:00Z",
		"action":                 action,
	}
	if mergedAtOrNil != nil {
		oa["merged_at"] = mergedAtOrNil
	}

	payload := map[string]any{
		"object_kind":       "merge_request",
		"project":           map[string]any{"path_with_namespace": project},
		"object_attributes": oa,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal legacy payload: %v", err)
	}
	return b
}

func TestGitLabMR_MergedNoTimestamp(t *testing.T) {
	fakeGL := newFakeGitLabAPI(t, "gl-merge-user", "", http.StatusOK)
	defer fakeGL.Close()

	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-test-token")

	before := time.Now().UTC()
	body := makeMrPayloadLegacy(t, "org/sub/repo", "merge", "merged", "mergeable",
		"sha-merged", 77, false, nil)
	err := testHandler.handleGitLabMergeRequestEvent(context.Background(), conn, body)
	if err != nil {
		t.Fatalf("handleGitLabMergeRequestEvent: %v", err)
	}
	after := time.Now().UTC()

	ctx := context.Background()
	var mergedAt time.Time
	var state string
	err = testPool.QueryRow(ctx, `
		SELECT merged_at, state FROM github_pull_request
		WHERE workspace_id=$1 AND provider='gitlab' AND repo_owner='org/sub' AND repo_name='repo' AND pr_number=1
	`, testWorkspaceID).Scan(&mergedAt, &state)
	if err != nil {
		t.Fatalf("query mr: %v", err)
	}
	if state != "merged" {
		t.Errorf("state = %q, want merged", state)
	}
	if mergedAt.Before(before.Add(-100*time.Millisecond)) || mergedAt.After(after.Add(100*time.Millisecond)) {
		t.Errorf("merged_at = %v, want between %v and %v (≈receipt time)", mergedAt, before, after)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})
}

func TestGitLabMR_LockedStateBecomesClosed(t *testing.T) {
	fakeGL := newFakeGitLabAPI(t, "gl-locked-user", "", http.StatusOK)
	defer fakeGL.Close()

	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-test-token")
	body := makeMRPayload("org/sub/repo", "close", "locked", "mergeable",
		"sha-locked", 11)
	err := testHandler.handleGitLabMergeRequestEvent(context.Background(), conn, body)
	if err != nil {
		t.Fatalf("handleGitLabMergeRequestEvent: %v", err)
	}

	ctx := context.Background()
	var state string
	err = testPool.QueryRow(ctx, `
		SELECT state FROM github_pull_request
		WHERE workspace_id=$1 AND provider='gitlab' AND repo_owner='org/sub' AND repo_name='repo' AND pr_number=1
	`, testWorkspaceID).Scan(&state)
	if err != nil {
		t.Fatalf("query mr: %v", err)
	}
	if state != "closed" {
		t.Errorf("state = %q, want closed (locked→closed)", state)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})
}

func TestGitLabMR_24MergeStates(t *testing.T) {
	// Table: GitLab detailed_merge_status → expected canonical state.
	cases := []struct {
		detailed string
		want     string
	}{
		// clean
		{"mergeable", "clean"},
		// unknown
		{"checking", "unknown"},
		{"unchecked", "unknown"},
		{"preparing", "unknown"},
		{"approvals_syncing", "unknown"},
		{"merge_time", "unknown"},
		{"not_open", "unknown"},
		{"ci_must_pass", "unknown"},
		{"ci_still_running", "unknown"},
		// draft
		{"draft_status", "draft"},
		// dirty
		{"conflict", "dirty"},
		{"need_rebase", "dirty"},
		{"locked_paths", "dirty"},
		{"locked_lfs_files", "dirty"},
		{"commits_status", "dirty"},
		// blocked
		{"not_approved", "blocked"},
		{"requested_changes", "blocked"},
		{"discussions_not_resolved", "blocked"},
		{"status_checks_must_pass", "blocked"},
		{"merge_request_blocked", "blocked"},
		{"security_policy_violations", "blocked"},
		{"security_policy_pipeline_check", "blocked"},
		{"jira_association_missing", "blocked"},
		{"title_regex", "blocked"},
		// unknown values
		{"some_future_status_xyz", "unknown"},
	}

	for _, tc := range cases {
		t.Run(tc.detailed, func(t *testing.T) {
			got := deriveGitLabMergeableState(tc.detailed)
			if !got.Valid {
				t.Errorf("expected Valid=true for %q, got Valid=false", tc.detailed)
				return
			}
			if got.String != tc.want {
				t.Errorf("deriveGitLabMergeableState(%q) = %q, want %q", tc.detailed, got.String, tc.want)
			}
			// Case insensitivity check.
			upper := deriveGitLabMergeableState(strings.ToUpper(tc.detailed))
			if !upper.Valid || upper.String != tc.want {
				t.Errorf("case-insensitive: deriveGitLabMergeableState(%q) = %q, want %q",
					strings.ToUpper(tc.detailed), upper.String, tc.want)
			}
		})
	}
}

func TestGitLabMR_EmptyMergeStatus(t *testing.T) {
	got := deriveGitLabMergeableState("")
	if got.Valid {
		t.Errorf("empty status should return Valid=false, got Valid=true with %q", got.String)
	}
}

func TestGitLabMR_AuthorBackfill500(t *testing.T) {
	fakeGL := newFakeGitLabAPI(t, "", "", http.StatusInternalServerError)
	defer fakeGL.Close()

	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-test-token")
	body := makeMRPayload("org/sub/repo", "open", "opened", "mergeable",
		"sha-500", 999)
	err := testHandler.handleGitLabMergeRequestEvent(context.Background(), conn, body)
	if err != nil {
		t.Fatalf("handleGitLabMergeRequestEvent should not fail even when author lookup 500s: %v", err)
	}

	ctx := context.Background()
	var authorLogin string
	err = testPool.QueryRow(ctx, `
		SELECT author_login FROM github_pull_request
		WHERE workspace_id=$1 AND provider='gitlab' AND repo_owner='org/sub' AND repo_name='repo' AND pr_number=1
	`, testWorkspaceID).Scan(&authorLogin)
	if err != nil {
		t.Fatalf("query mr: %v", err)
	}
	if authorLogin != "unknown" {
		t.Errorf("author_login = %q, want unknown (500 fallback)", authorLogin)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})
}

func TestGitLabMR_HybridCoexistence(t *testing.T) {
	ctx := context.Background()

	// Seed a GitHub PR #5, then receive a GitLab MR !5 in the same repo.
	_, err := testHandler.Queries.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		InstallationID: 1,
		RepoOwner:      "org/sub",
		RepoName:       "repo",
		PrNumber:       5,
		Title:          "GitHub PR",
		State:          "open",
		HtmlUrl:        "https://github.com/org/sub/repo/pull/5",
		HeadSha:        "gh-sha-5",
		PrCreatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
		PrUpdatedAt:    pgtype.Timestamptz{Time: time.Now(), Valid: true},
	})
	if err != nil {
		t.Fatalf("seed github pr: %v", err)
	}

	fakeGL := newFakeGitLabAPI(t, "gl-hybrid-user", "", http.StatusOK)
	defer fakeGL.Close()

	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-test-token")

	// Build payload with iid=5 to match the GitHub PR #5 above.
	payload := map[string]any{
		"object_kind": "merge_request",
		"project": map[string]any{
			"path_with_namespace": "org/sub/repo",
		},
		"object_attributes": map[string]any{
			"iid":                    5,
			"title":                  "GitLab MR",
			"description":            "GL MR desc",
			"state":                  "opened",
			"draft":                  false,
			"detailed_merge_status": "mergeable",
			"source_branch":          "gl-feature",
			"last_commit":            map[string]any{"id": "gl-sha-5"},
			"merged_at":              nil,
			"closed_at":              nil,
			"url":                    "http://gitlab.example.com/org/sub/repo/-/merge_requests/5",
			"author_id":              5,
			"created_at":             "2024-01-01T12:00:00Z",
			"updated_at":             "2024-01-01T12:00:00Z",
			"action":                 "open",
		},
	}
	body, _ := json.Marshal(payload)
	err = testHandler.handleGitLabMergeRequestEvent(context.Background(), conn, body)
	if err != nil {
		t.Fatalf("handleGitLabMergeRequestEvent: %v", err)
	}

	// Both rows should exist.
	var count int
	err = testPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM github_pull_request
		WHERE workspace_id=$1 AND repo_owner='org/sub' AND repo_name='repo' AND pr_number=5
	`, testWorkspaceID).Scan(&count)
	if err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows (github + gitlab), got %d", count)
	}

	// GetGitHubPullRequest should still find the GitHub one.
	ghPR, err := testHandler.Queries.GetGitHubPullRequest(ctx, db.GetGitHubPullRequestParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		RepoOwner:   "org/sub",
		RepoName:    "repo",
		PrNumber:    5,
	})
	if err != nil {
		t.Fatalf("GetGitHubPullRequest: %v", err)
	}
	if ghPR.Provider != "github" {
		t.Errorf("GetGitHubPullRequest provider = %q, want github", ghPR.Provider)
	}
	if ghPR.HeadSha != "gh-sha-5" {
		t.Errorf("GetGitHubPullRequest head_sha = %q, want gh-sha-5", ghPR.HeadSha)
	}
	if ghPR.State != "open" {
		t.Errorf("GetGitHubPullRequest state = %q, want open", ghPR.State)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id=$1 AND repo_owner='org/sub' AND repo_name='repo' AND pr_number=5`, testWorkspaceID)
	})
}

func TestGitLabMR_MalformedPayload(t *testing.T) {
	fakeGL := newFakeGitLabAPI(t, "", "", http.StatusOK)
	defer fakeGL.Close()

	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-test-token")

	// Missing project.
	body := []byte(`{"object_kind":"merge_request","object_attributes":{"iid":1}}`)
	err := testHandler.handleGitLabMergeRequestEvent(context.Background(), conn, body)
	if err != nil {
		t.Fatalf("malformed payload should not fail: %v", err)
	}

	// Missing object_attributes entirely.
	body2 := []byte(`{"object_kind":"merge_request","project":{"path_with_namespace":"a/b"}}`)
	err = testHandler.handleGitLabMergeRequestEvent(context.Background(), conn, body2)
	if err != nil {
		t.Fatalf("missing object_attributes should not fail: %v", err)
	}

	// Totally garbled JSON.
	err = testHandler.handleGitLabMergeRequestEvent(context.Background(), conn, []byte(`{not json`))
	if err != nil {
		t.Fatalf("garbled JSON should not fail: %v", err)
	}
}

func TestGitLabMR_BoxNilSkipsAuthor(t *testing.T) {
	fakeGL := newFakeGitLabAPI(t, "should-not-be-called", "", http.StatusOK)
	defer fakeGL.Close()

	// Seed connection WITHOUT box (raw ciphertext since we're not using encryption).
	ctx := context.Background()
	conn, err := testHandler.Queries.InsertGitLabConnection(ctx, db.InsertGitLabConnectionParams{
		WorkspaceID:             parseUUID(testWorkspaceID),
		AccountLogin:            "box-nil-user",
		DisplayName:             pgtype.Text{String: "Box Nil", Valid: true},
		InstanceUrl:             pgtype.Text{String: fakeGL.URL, Valid: true},
		AccessTokenCiphertext:   []byte("not-real-ciphertext"),
		WebhookSecretHash:       make([]byte, 32),
		WebhookSecretCiphertext: []byte("not-real"),
		Hooks:                   []byte("[]"),
	})
	if err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_installation WHERE id=$1`, uuidToString(conn.ID))
	})

	// Temporarily nil the box.
	old := testHandler.GitLabBox
	testHandler.GitLabBox = nil
	t.Cleanup(func() { testHandler.GitLabBox = old })

	body := makeMRPayload("org/sub/repo", "open", "opened", "mergeable",
		"sha-boxnil", 42)
	err = testHandler.handleGitLabMergeRequestEvent(context.Background(), conn, body)
	if err != nil {
		t.Fatalf("box=nil should not block mr: %v", err)
	}

	var authorLogin string
	err = testPool.QueryRow(ctx, `
		SELECT author_login FROM github_pull_request
		WHERE workspace_id=$1 AND provider='gitlab' AND repo_owner='org/sub' AND repo_name='repo' AND pr_number=1
	`, testWorkspaceID).Scan(&authorLogin)
	if err != nil {
		t.Fatalf("query mr: %v", err)
	}
	if authorLogin != "unknown" {
		t.Errorf("author_login = %q, want unknown (box nil fallback)", authorLogin)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})
}

// TestGitLabMR_MetaUpdate_PreservesMergeable verifies that a metadata-only
// "update" event (e.g. label change) with detailed_merge_status=mergeable
// does NOT clear the existing mergeable_state. Before the fix, clear=true
// caused the SQL CASE to write NULL, dropping "clean" → "unknown".
func TestGitLabMR_MetaUpdate_PreservesMergeable(t *testing.T) {
	fakeGL := newFakeGitLabAPI(t, "gl-meta-user", "", http.StatusOK)
	defer fakeGL.Close()
	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-test-token")
	ctx := context.Background()

	// Step 1: seed a row with mergeable_state = 'clean'.
	initialPayload := makeMRPayload("org/sub/repo", "open", "opened", "mergeable",
		"sha-meta-1", 42)
	err := testHandler.handleGitLabMergeRequestEvent(ctx, conn, initialPayload)
	if err != nil {
		t.Fatalf("step 1 (open): %v", err)
	}

	var ms string
	err = testPool.QueryRow(ctx, `
		SELECT mergeable_state FROM github_pull_request
		WHERE workspace_id=$1 AND provider='gitlab' AND repo_owner='org/sub' AND repo_name='repo' AND pr_number=1
	`, testWorkspaceID).Scan(&ms)
	if err != nil {
		t.Fatalf("step 1 query: %v", err)
	}
	if ms != "clean" {
		t.Fatalf("step 1 mergeable_state = %q, want clean", ms)
	}

	// Step 2: send a metadata update (same detailed_merge_status).
	updatePayload := makeMRPayload("org/sub/repo", "update", "opened", "mergeable",
		"sha-meta-1", 42)
	err = testHandler.handleGitLabMergeRequestEvent(ctx, conn, updatePayload)
	if err != nil {
		t.Fatalf("step 2 (update): %v", err)
	}

	err = testPool.QueryRow(ctx, `
		SELECT mergeable_state FROM github_pull_request
		WHERE workspace_id=$1 AND provider='gitlab' AND repo_owner='org/sub' AND repo_name='repo' AND pr_number=1
	`, testWorkspaceID).Scan(&ms)
	if err != nil {
		t.Fatalf("step 2 query: %v", err)
	}
	if ms != "clean" {
		t.Errorf("after metadata update: mergeable_state = %q, want clean (was NULL before fix)", ms)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})
}

// TestGitLabMR_OpenWithChecking_WritesUnknown verifies that an MR opened
// with detailed_merge_status=checking writes "unknown" (not NULL).
func TestGitLabMR_OpenWithChecking_WritesUnknown(t *testing.T) {
	fakeGL := newFakeGitLabAPI(t, "gl-check-user", "", http.StatusOK)
	defer fakeGL.Close()
	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-test-token")
	ctx := context.Background()

	body := makeMRPayload("org/sub/repo", "open", "opened", "checking",
		"sha-check-1", 43)
	err := testHandler.handleGitLabMergeRequestEvent(ctx, conn, body)
	if err != nil {
		t.Fatalf("open with checking: %v", err)
	}

	var ms string
	err = testPool.QueryRow(ctx, `
		SELECT mergeable_state FROM github_pull_request
		WHERE workspace_id=$1 AND provider='gitlab' AND repo_owner='org/sub' AND repo_name='repo' AND pr_number=1
	`, testWorkspaceID).Scan(&ms)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if ms != "unknown" {
		t.Errorf("mergeable_state = %q, want unknown (was NULL before fix)", ms)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})
}

// TestGitLabMR_EmptyDetailedStatus_PreservesExisting verifies that when
// detailed_merge_status is empty, the existing mergeable_state is preserved
// via the three-state CASE branch 3.
func TestGitLabMR_EmptyDetailedStatus_PreservesExisting(t *testing.T) {
	fakeGL := newFakeGitLabAPI(t, "gl-empty-user", "", http.StatusOK)
	defer fakeGL.Close()
	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-test-token")
	ctx := context.Background()

	// Step 1: seed with mergeable → "clean".
	initialPayload := makeMRPayload("org/sub/repo", "open", "opened", "mergeable",
		"sha-empty-1", 44)
	err := testHandler.handleGitLabMergeRequestEvent(ctx, conn, initialPayload)
	if err != nil {
		t.Fatalf("step 1 (open): %v", err)
	}

	// Step 2: update with empty detailed_merge_status.
	updatePayload := makeMRPayload("org/sub/repo", "update", "opened", "",
		"sha-empty-1", 44)
	err = testHandler.handleGitLabMergeRequestEvent(ctx, conn, updatePayload)
	if err != nil {
		t.Fatalf("step 2 (update with empty status): %v", err)
	}

	var ms string
	err = testPool.QueryRow(ctx, `
		SELECT mergeable_state FROM github_pull_request
		WHERE workspace_id=$1 AND provider='gitlab' AND repo_owner='org/sub' AND repo_name='repo' AND pr_number=1
	`, testWorkspaceID).Scan(&ms)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if ms != "clean" {
		t.Errorf("mergeable_state = %q, want clean (preserved via CASE branch 3)", ms)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id=$1 AND provider='gitlab'`, testWorkspaceID)
	})
}

// ── Fake GitLab API ──────────────────────────────────────────────────────────

// newFakeGitLabAPI starts an httptest server that:
// - GET /api/v4/users/:id → returns {username, avatar_url} or the given status.
func newFakeGitLabAPI(t *testing.T, username, avatarURL string, userStatus int) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GET /api/v4/users/:id
		if strings.HasPrefix(r.URL.Path, "/api/v4/users/") && r.Method == http.MethodGet {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v4/users/"), "/")
			if len(parts) != 1 {
				http.NotFound(w, r)
				return
			}
			id, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil || id <= 0 {
				http.NotFound(w, r)
				return
			}
			if userStatus == http.StatusInternalServerError {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if userStatus != http.StatusOK {
				http.Error(w, "not found", userStatus)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%d,"username":"%s","avatar_url":"%s"}`, id, username, avatarURL)
			return
		}
		http.NotFound(w, r)
	}))
	return srv
}
