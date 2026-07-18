package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ── Helpers ─────────────────────────────────────────────────────────────────

func setupGitLabAutoLinkFixture(t *testing.T, issueTitle string) (IssueResponse, db.GithubInstallation) {
	t.Helper()
	ctx := context.Background()

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":  issueTitle,
		"status": "in_progress",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: %d %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	json.NewDecoder(w.Body).Decode(&created)

	fakeGL := newFakeGitLabAPI(t, "gl-autolink-user", "", http.StatusOK)
	conn := seedGitLabConnectionMRTest(t, fakeGL.URL, "glpat-test-token")

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM issue_pull_request WHERE issue_id = $1`, created.ID)
		testPool.Exec(ctx, `DELETE FROM activity_log WHERE issue_id = $1`, created.ID)
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id = $1 AND provider = 'gitlab'`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, created.ID)
	})

	return created, conn
}

func fireGitLabMRWebhook(t *testing.T, conn db.GithubInstallation, iid int64,
	action, glState, title, description, branch string) {
	t.Helper()

	detailedMergeStatus := "mergeable"
	var mergedAt, closedAt any
	switch glState {
	case "merged":
		mergedAt = "2024-06-01T12:00:00Z"
		closedAt = "2024-06-01T12:00:00Z"
	case "closed":
		closedAt = "2024-06-01T12:00:00Z"
	}

	headLen := len(title)
	if headLen > 8 {
		headLen = 8
	}
	payload := makeMRPayload("org/sub/repo", action, glState, detailedMergeStatus,
		"sha-autolink-"+title[:headLen], 42,
		"title", title,
		"description", description,
		"source_branch", branch,
		"iid", iid,
		"merged_at", mergedAt,
		"closed_at", closedAt,
	)

	ctx := context.Background()
	if err := testHandler.handleGitLabMergeRequestEvent(ctx, conn, payload); err != nil {
		t.Fatalf("handleGitLabMergeRequestEvent: mr=%d action=%s state=%s: %v",
			iid, action, glState, err)
	}
}

func mrLinkCount(t *testing.T, issueID string) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM issue_pull_request WHERE issue_id = $1`,
		issueID).Scan(&n); err != nil {
		t.Fatalf("count links: %v", err)
	}
	return n
}

func mrCloseIntent(t *testing.T, issueID string) bool {
	t.Helper()
	ctx := context.Background()
	var ci bool
	if err := testPool.QueryRow(ctx,
		`SELECT close_intent FROM issue_pull_request WHERE issue_id = $1 LIMIT 1`,
		issueID).Scan(&ci); err != nil {
		t.Fatalf("read close_intent: %v", err)
	}
	return ci
}

func mrReferenceOnly(t *testing.T, issueID string) bool {
	t.Helper()
	ctx := context.Background()
	var ro bool
	if err := testPool.QueryRow(ctx,
		`SELECT reference_only FROM issue_pull_request WHERE issue_id = $1 LIMIT 1`,
		issueID).Scan(&ro); err != nil {
		t.Fatalf("read reference_only: %v", err)
	}
	return ro
}

func setWorkspaceGitLabAutoLinkSetting(t *testing.T, enabled *bool) {
	t.Helper()
	ctx := context.Background()

	var current []byte
	if err := testPool.QueryRow(ctx,
		`SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID,
	).Scan(&current); err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var s map[string]any
	if len(current) > 0 {
		if err := json.Unmarshal(current, &s); err != nil {
			s = map[string]any{}
		}
	} else {
		s = map[string]any{}
	}

	if enabled == nil {
		delete(s, "gitlab_auto_link_mrs_enabled")
	} else {
		s["gitlab_auto_link_mrs_enabled"] = *enabled
	}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if _, err := testPool.Exec(ctx,
		`UPDATE workspace SET settings = $1 WHERE id = $2`, b, testWorkspaceID,
	); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`UPDATE workspace SET settings = $1 WHERE id = $2`, current, testWorkspaceID)
	})
}

// ── Tests ──────────────────────────────────────────────────────────────────

func TestGitLabAutoLink_ClosingMR_AdvancesIssueToDone(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	issue, conn := setupGitLabAutoLinkFixture(t, "closing mr advances issue")

	title := "Closes " + issue.Identifier
	fireGitLabMRWebhook(t, conn, 1, "open", "opened", title, "", "feat/close")

	if n := mrLinkCount(t, issue.ID); n != 1 {
		t.Fatalf("after open: link count = %d, want 1", n)
	}
	if !mrCloseIntent(t, issue.ID) {
		t.Fatal("after open: close_intent = false, want true")
	}

	got, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("GetIssue after open: %v", err)
	}
	if got.Status != "in_progress" {
		t.Fatalf("after open: status = %q, want in_progress", got.Status)
	}

	fireGitLabMRWebhook(t, conn, 1, "merge", "merged", title, "", "feat/close")

	got, err = testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("GetIssue after merge: %v", err)
	}
	if got.Status != "done" {
		t.Fatalf("after merge: status = %q, want done", got.Status)
	}

	counts, err := testHandler.Queries.GetIssuePullRequestCloseAggregate(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("GetIssuePullRequestCloseAggregate: %v", err)
	}
	if counts.MergedWithCloseIntentCount < 1 {
		t.Errorf("merged_with_close_intent_count = %d, want >= 1", counts.MergedWithCloseIntentCount)
	}
	if counts.OpenCount != 0 {
		t.Errorf("open_count = %d, want 0", counts.OpenCount)
	}
}

func TestGitLabAutoLink_NonAdjacentKeyword_LinksWithoutCloseIntent(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	issue, conn := setupGitLabAutoLinkFixture(t, "non-adjacent keyword link")

	title := "Fix login " + issue.Identifier
	fireGitLabMRWebhook(t, conn, 1, "open", "opened", title, "", "feat/fix-login")

	if n := mrLinkCount(t, issue.ID); n != 1 {
		t.Fatalf("after open: link count = %d, want 1", n)
	}
	if mrCloseIntent(t, issue.ID) {
		t.Fatal("after open: close_intent = true, want false (keyword not adjacent)")
	}
	if mrReferenceOnly(t, issue.ID) {
		t.Fatal("after open: reference_only = true, want false (title mention)")
	}

	fireGitLabMRWebhook(t, conn, 1, "merge", "merged", title, "", "feat/fix-login")

	got, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("GetIssue after merge: %v", err)
	}
	if got.Status != "in_progress" {
		t.Fatalf("after merge: status = %q, want in_progress (no closing intent)", got.Status)
	}

	counts, err := testHandler.Queries.GetIssuePullRequestCloseAggregate(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("GetIssuePullRequestCloseAggregate: %v", err)
	}
	if counts.MergedWithCloseIntentCount != 0 {
		t.Errorf("merged_with_close_intent_count = %d, want 0", counts.MergedWithCloseIntentCount)
	}
}

func TestGitLabAutoLink_BodyOnlyMention_IsReferenceOnly(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	issue, conn := setupGitLabAutoLinkFixture(t, "body only mention")

	fireGitLabMRWebhook(t, conn, 1, "open", "opened",
		"Unrelated cleanup",
		"Related to "+issue.Identifier,
		"feat/cleanup")

	if n := mrLinkCount(t, issue.ID); n != 1 {
		t.Fatalf("after open: link count = %d, want 1", n)
	}
	if mrCloseIntent(t, issue.ID) {
		t.Fatal("after open: close_intent = true, want false")
	}
	if !mrReferenceOnly(t, issue.ID) {
		t.Fatal("after open: reference_only = false, want true (body-only mention)")
	}

	listed, err := testHandler.Queries.ListPullRequestsByIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("ListPullRequestsByIssue: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("body-only mention should be hidden from PR list, got %d rows", len(listed))
	}
}

func TestGitLabAutoLink_SiblingCloseAfterCloseKeywordMerge(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	issue, conn := setupGitLabAutoLinkFixture(t, "sibling close after keyword merge")

	fireGitLabMRWebhook(t, conn, 1, "open", "opened",
		"Closes "+issue.Identifier, "", "feat/primary")

	fireGitLabMRWebhook(t, conn, 2, "open", "opened",
		issue.Identifier+": follow-up cleanup", "", "feat/cleanup")

	got, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("GetIssue after both open: %v", err)
	}
	if got.Status != "in_progress" {
		t.Fatalf("after both open: status = %q, want in_progress", got.Status)
	}

	fireGitLabMRWebhook(t, conn, 1, "merge", "merged",
		"Closes "+issue.Identifier, "", "feat/primary")

	got, err = testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("GetIssue after A merge: %v", err)
	}
	if got.Status != "in_progress" {
		t.Fatalf("after A merge with B still open: status = %q, want in_progress", got.Status)
	}

	fireGitLabMRWebhook(t, conn, 2, "merge", "merged",
		issue.Identifier+": follow-up cleanup", "", "feat/cleanup")

	got, err = testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("GetIssue after B merge: %v", err)
	}
	if got.Status != "done" {
		t.Errorf("after both merged (A with close_intent, B link-only): status = %q, want done", got.Status)
	}
}

func TestGitLabAutoLink_CloseIntentPreservedAfterMerge(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	issue, conn := setupGitLabAutoLinkFixture(t, "close intent preservation")

	titleWithClose := "Closes " + issue.Identifier
	titleWithoutClose := "Updated: no longer closes " + issue.Identifier

	fireGitLabMRWebhook(t, conn, 1, "open", "opened", titleWithClose, "", "feat/preserve")
	if !mrCloseIntent(t, issue.ID) {
		t.Fatal("after open: close_intent = false, want true")
	}

	fireGitLabMRWebhook(t, conn, 1, "merge", "merged", titleWithClose, "", "feat/preserve")

	got, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("GetIssue after merge: %v", err)
	}
	if got.Status != "done" {
		t.Fatalf("after merge: status = %q, want done", got.Status)
	}

	fireGitLabMRWebhook(t, conn, 1, "update", "merged", titleWithoutClose, "", "feat/preserve")
	if !mrCloseIntent(t, issue.ID) {
		t.Fatal("after post-merge edit: close_intent = false, want true (preserved)")
	}

	got, err = testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("GetIssue after post-merge edit: %v", err)
	}
	if got.Status != "done" {
		t.Fatalf("after post-merge edit: status = %q, want done", got.Status)
	}
}

func TestGitLabAutoLink_DisabledByWorkspaceSetting(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	issue, conn := setupGitLabAutoLinkFixture(t, "auto-link disabled by setting")

	disabled := false
	setWorkspaceGitLabAutoLinkSetting(t, &disabled)

	title := "Closes " + issue.Identifier
	fireGitLabMRWebhook(t, conn, 1, "open", "opened", title, "", "feat/close")

	if n := mrLinkCount(t, issue.ID); n != 0 {
		t.Fatalf("with gitlab_auto_link_mrs_enabled=false: link count = %d, want 0", n)
	}
}

func TestGitLabAutoLink_DisabledByMasterGitLabSwitch(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	issue, conn := setupGitLabAutoLinkFixture(t, "auto-link disabled by master switch")

	ctx := context.Background()
	var current []byte
	if err := testPool.QueryRow(ctx,
		`SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID,
	).Scan(&current); err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var s map[string]any
	if len(current) > 0 {
		json.Unmarshal(current, &s)
	} else {
		s = map[string]any{}
	}
	s["gitlab_enabled"] = false
	b, _ := json.Marshal(s)
	if _, err := testPool.Exec(ctx, `UPDATE workspace SET settings = $1 WHERE id = $2`, b, testWorkspaceID); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`UPDATE workspace SET settings = $1 WHERE id = $2`, current, testWorkspaceID)
	})

	title := "Closes " + issue.Identifier
	fireGitLabMRWebhook(t, conn, 1, "open", "opened", title, "", "feat/close")

	if n := mrLinkCount(t, issue.ID); n != 0 {
		t.Fatalf("with gitlab_enabled=false: link count = %d, want 0", n)
	}
}

func TestGitLabAutoLink_DefaultsToEnabled(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	issue, conn := setupGitLabAutoLinkFixture(t, "default auto-link on")

	ctx := context.Background()
	var current []byte
	if err := testPool.QueryRow(ctx,
		`SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID,
	).Scan(&current); err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if _, err := testPool.Exec(ctx,
		`UPDATE workspace SET settings = '{}'::jsonb WHERE id = $1`, testWorkspaceID,
	); err != nil {
		t.Fatalf("reset settings: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`UPDATE workspace SET settings = $1 WHERE id = $2`, current, testWorkspaceID)
	})

	title := "Closes " + issue.Identifier
	fireGitLabMRWebhook(t, conn, 1, "open", "opened", title, "", "feat/close")

	if n := mrLinkCount(t, issue.ID); n != 1 {
		t.Fatalf("with empty settings: link count = %d, want 1", n)
	}
	if !mrCloseIntent(t, issue.ID) {
		t.Fatal("with empty settings: close_intent = false, want true")
	}
}

func TestGitLabAutoLink_BroadcastCarriesLinkedIssueIDs(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	issue, conn := setupGitLabAutoLinkFixture(t, "broadcast carries linked ids")

	ch := make(chan events.Event, 1)
	testHandler.Bus.Subscribe(protocol.EventPullRequestUpdated, func(e events.Event) {
		select {
		case ch <- e:
		default:
		}
	})

	title := "Closes " + issue.Identifier
	fireGitLabMRWebhook(t, conn, 1, "open", "opened", title, "", "feat/close")

	select {
	case ev := <-ch:
		payload, ok := ev.Payload.(map[string]any)
		if !ok {
			t.Fatalf("broadcast payload type: %T", ev.Payload)
		}
		raw, ok := payload["linked_issue_ids"].([]string)
		if ok {
			if len(raw) != 1 || raw[0] != issue.ID {
				t.Errorf("linked_issue_ids = %v, want [%s]", raw, issue.ID)
			}
			return
		}
	case <-time.After(time.Second):
		t.Fatal("broadcast not received within 1s")
	}
}
