package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	gl "github.com/multica-ai/multica/server/internal/integrations/gitlab"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ── Tolerant payload types ──────────────────────────────────────────────────
//
// GitLab's webhook payload differs across versions (CE ↔ EE, self-managed
// vs gitlab.com). Every field is either *json.RawMessage (defer decoding)
// or *T (check for nil before use). A missing field is a silent zero-value.

type glMRPayload struct {
	Project          *glMRProject          `json:"project"`
	ObjectAttributes *glMRObjectAttributes `json:"object_attributes"`
}

type glMRProject struct {
	PathWithNamespace string `json:"path_with_namespace"`
	// All other project fields ignored — we only need path_with_namespace
	// to derive repo_owner / repo_name.
}

type glMRObjectAttributes struct {
	IID                  int64              `json:"iid"`
	Title                string             `json:"title"`
	Description          *json.RawMessage   `json:"description"`   // string or null → tolerant
	State                string             `json:"state"`
	Draft                *bool              `json:"draft"`          // GitLab ≥14.0 may omit
	WorkInProgress       *bool              `json:"work_in_progress"` // legacy field
	DetailedMergeStatus  string             `json:"detailed_merge_status"`
	SourceBranch         string             `json:"source_branch"`
	LastCommit           *glMRCommitRef     `json:"last_commit"`
	MergedAt             *json.RawMessage   `json:"merged_at"`  // string or null → tolerant
	ClosedAt             *json.RawMessage   `json:"closed_at"`  // string or null → tolerant
	URL                  string             `json:"url"`
	AuthorID             int64              `json:"author_id"`
	CreatedAt            string             `json:"created_at"`
	UpdatedAt            string             `json:"updated_at"`
	Action               string             `json:"action"`
}

type glMRCommitRef struct {
	ID string `json:"id"`
}

// isDraft resolves the draft/WIP flag across GitLab versions.
// GitLab ≥14.0 uses draft; older versions use work_in_progress.
// When both are present (borderline upgrade window) draft wins.
func (a *glMRObjectAttributes) isDraft() bool {
	if a.Draft != nil {
		return *a.Draft
	}
	if a.WorkInProgress != nil {
		return *a.WorkInProgress
	}
	return false
}

// ── handleGitLabMergeRequestEvent ───────────────────────────────────────────

func (h *Handler) handleGitLabMergeRequestEvent(ctx context.Context, conn db.GithubInstallation, body []byte) error {
	// 1. Tolerant parse — nil project / object_attributes is a format error.
	var p glMRPayload
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("gitlab: mr payload parse failed", "err", err)
		return nil // don't block the 202 ACK
	}
	if p.Project == nil || p.ObjectAttributes == nil {
		slog.Warn("gitlab: mr payload missing project or object_attributes")
		return nil
	}
	oa := p.ObjectAttributes

	// 2. Split repo_owner / repo_name on the LAST "/".
	repoOwner, repoName := splitPathWithNamespace(p.Project.PathWithNamespace)

	// 3. Map GitLab state → canonical state.
	state := deriveGitLabMRState(oa.State)

	// 4. Derive mergeable_state.
	mergeable := deriveGitLabMergeableState(oa.DetailedMergeStatus)
	clearMergeable := deriveGitLabMRMergeableRefresh()

	// 5. Parse head_sha.
	headSHA := ""
	if oa.LastCommit != nil {
		headSHA = oa.LastCommit.ID
	}

	// 6. Parse timestamps (GitLab ISO 8601).
	mergedAt := parseGLTimeRaw(oa.MergedAt)
	closedAt := parseGLTimeRaw(oa.ClosedAt)

	// Fallback: if state is merged/closed but we have no timestamp,
	// use the event receipt time (now).
	if state == "merged" && !mergedAt.Valid {
		mergedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	if state == "closed" && !closedAt.Valid {
		closedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}

	// 7. Author backfill: decrypt access token, call GitLab API.
	authorLogin, authorAvatar := h.resolveGitLabMRAuthor(ctx, conn, oa.AuthorID)

	// 8. Upsert the MR row.
	wsID := conn.WorkspaceID
	prNumber := int32(oa.IID)
	pr, err := h.Queries.UpsertGitLabMergeRequest(ctx, db.UpsertGitLabMergeRequestParams{
		WorkspaceID:         wsID,
		RepoOwner:           repoOwner,
		RepoName:            repoName,
		PrNumber:            prNumber,
		Title:               oa.Title,
		State:               state,
		HtmlUrl:             oa.URL,
		PrCreatedAt:         parseGLTimeRequired(oa.CreatedAt),
		PrUpdatedAt:         parseGLTimeRequired(oa.UpdatedAt),
		HeadSha:             headSHA,
		Additions:           0, // webhook payload lacks diff stats
		Deletions:           0,
		ChangedFiles:        0,
		Branch:              strToText(oa.SourceBranch),
		AuthorLogin:         authorLogin,
		AuthorAvatarUrl:     authorAvatar,
		MergedAt:            mergedAt,
		ClosedAt:            closedAt,
		MergeableState:      mergeable,
		ClearMergeableState: clearMergeable,
	})
	if err != nil {
		slog.Warn("gitlab: upsert mr failed", "err", err, "repo", repoOwner+"/"+repoName, "mr", prNumber)
		return err
	}

	// 9. T9: drain pending pipelines here

	workspaceID := uuidToString(wsID)

	// 10. Auto-link: scan title/body/branch for issue identifiers, look them
	// up in this workspace, attach the link rows. Idempotent (ON CONFLICT
	// upserts the close_intent flag — see LinkIssueToPullRequest) so
	// re-firing the webhook doesn't duplicate.
	//
	// Mirrors the GitHub auto-link path (mirrorPullRequestForWorkspace) but
	// gated by gitlab_enabled / gitlab_auto_link_mrs_enabled workspace
	// settings, defaulting to ON to match GitHub's opt-out posture.
	linkedIssueIDs := make([]string, 0)
	if h.workspaceAutoLinkMRsEnabled(ctx, wsID) {
		description := extractDescription(oa.Description)
		idents := extractIdentifiers(oa.Title, description, oa.SourceBranch)

		// closingIdents is the subset of identifiers that this MR explicitly
		// declared via a closing keyword ("Closes/Fixes/Resolves HAN-X").
		// Linking still happens for every mention (idents above), but the
		// link row's close_intent column — and therefore whether the
		// auto-advance gate eventually fires — is only set for keyword-
		// declared identifiers.
		closingIdents := map[string]struct{}{}
		for _, c := range extractClosingIdentifiers(oa.Title, description) {
			closingIdents[c] = struct{}{}
		}

		// qualifyingIdents are the identifiers that genuinely tie this MR
		// to an issue: a title prefix, a branch-name reference, or a body
		// closing keyword. Any identifier linked but NOT in this set was
		// matched only by a bare mention in the MR body — flagged
		// reference_only and hidden from the issue's PR list.
		qualifyingIdents := map[string]struct{}{}
		for _, id := range extractIdentifiers(oa.Title, oa.SourceBranch) {
			qualifyingIdents[id] = struct{}{}
		}
		for c := range closingIdents {
			qualifyingIdents[c] = struct{}{}
		}

		// close_intent should follow the MR title/body while the MR is
		// still editable before its terminal close event. Once the MR has
		// reached a terminal state, later edit/update webhooks must not
		// rewrite the merge-time close decision.
		preserveCloseIntent := oa.Action != "close" && (state == "merged" || state == "closed")

		prefix := h.getIssuePrefix(ctx, wsID)

		// reevalIssues collects each issue whose link row we just touched so
		// we can re-run the auto-advance gate against the persisted aggregate
		// after every link upsert in this event.
		reevalIssues := make([]db.Issue, 0, len(idents))
		for _, id := range idents {
			issue, ok := h.lookupIssueByIdentifier(ctx, wsID, prefix, id)
			if !ok {
				continue
			}
			_, declared := closingIdents[id]
			closeIntent := declared && !preserveCloseIntent
			_, qualifies := qualifyingIdents[id]
			referenceOnly := !qualifies
			if err := h.Queries.LinkIssueToPullRequest(ctx, db.LinkIssueToPullRequestParams{
				IssueID:             issue.ID,
				PullRequestID:       pr.ID,
				CloseIntent:         closeIntent,
				ReferenceOnly:       referenceOnly,
				PreserveCloseIntent: preserveCloseIntent,
				LinkedByType:        strToText("system"),
				LinkedByID:          pgtype.UUID{},
			}); err != nil {
				slog.Warn("gitlab: link failed", "err", err)
				continue
			}
			linkedIssueIDs = append(linkedIssueIDs, uuidToString(issue.ID))
			reevalIssues = append(reevalIssues, issue)
		}

		// A terminal MR event (`merged` or `closed`) may be the moment the
		// last in-flight sibling resolves. We re-evaluate every issue we
		// just linked once both the MR row and the link row are persisted,
		// so the aggregate query sees the freshest state.
		if state == "merged" || state == "closed" {
			for _, issue := range reevalIssues {
				if issue.Status == "done" || issue.Status == "cancelled" {
					continue
				}
				counts, err := h.Queries.GetIssuePullRequestCloseAggregate(ctx, issue.ID)
				if err != nil {
					slog.Warn("gitlab: count linked pr states failed", "err", err, "issue_id", uuidToString(issue.ID))
					continue
				}
				if counts.OpenCount == 0 && counts.MergedWithCloseIntentCount > 0 {
					// advanceIssueToDone hardcodes "github_pr_merged" as
					// the event source. That source string has no runtime
					// code consumers (it only appears in issue timeline
					// copy), so mirroring its exact logic inline with
					// "gitlab_mr_merged" for timeline fidelity is safe
					// and self-contained. The underlying status transition
					// + parent notification + WS broadcast is identical.
					updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
						ID:          issue.ID,
						Status:      "done",
						WorkspaceID: issue.WorkspaceID,
					})
					if err != nil {
						slog.Warn("gitlab: advance issue to done failed", "err", err)
						continue
					}
					h.notifyParentOfChildDone(ctx, issue, updated)
					prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
					resp := issueToResponse(updated, prefix)
					h.publish(protocol.EventIssueUpdated, workspaceID, "system", "", map[string]any{
						"issue":          resp,
						"status_changed": true,
						"prev_status":    issue.Status,
						"creator_type":   issue.CreatorType,
						"creator_id":     uuidToString(issue.CreatorID),
						"source":         "gitlab_mr_merged",
					})
				}
			}
		}
	}

	// 11. Broadcast pull_request:updated.
	resp := gitLabMRToResponse(pr)
	h.publish(protocol.EventPullRequestUpdated, workspaceID, "system", "", map[string]any{
		"pull_request":     resp,
		"linked_issue_ids": linkedIssueIDs,
	})

	return nil
}

// workspaceAutoLinkMRsEnabled reports whether the workspace allows the
// GitLab webhook to create issue ↔ MR link rows. Defaults to true so that
// workspaces predating the feature keep the historical "auto-link on"
// behavior, and short-circuits to false whenever the master GitLab switch
// is explicitly off — mirroring the precedence used on the client side and
// the equivalent GitHub gate (workspaceAutoLinkPRsEnabled).
func (h *Handler) workspaceAutoLinkMRsEnabled(ctx context.Context, workspaceID pgtype.UUID) bool {
	ws, err := h.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil || len(ws.Settings) == 0 {
		return true
	}
	var s struct {
		GitLabEnabled           *bool `json:"gitlab_enabled"`
		GitLabAutoLinkMRsEnabled *bool `json:"gitlab_auto_link_mrs_enabled"`
	}
	if err := json.Unmarshal(ws.Settings, &s); err != nil {
		return true
	}
	if s.GitLabEnabled != nil && !*s.GitLabEnabled {
		return false
	}
	if s.GitLabAutoLinkMRsEnabled == nil {
		return true
	}
	return *s.GitLabAutoLinkMRsEnabled
}

// extractDescription extracts a plain string from a GitLab MR
// description field, which arrives as *json.RawMessage and may be
// a JSON string, null, or absent.
func extractDescription(raw *json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(*raw, &s); err != nil {
		return ""
	}
	return s
}

// ── State mapping ───────────────────────────────────────────────────────────

func deriveGitLabMRState(state string) string {
	switch state {
	case "opened":
		return "open"
	case "closed":
		return "closed"
	case "merged":
		return "merged"
	case "locked":
		return "closed"
	default:
		return "open"
	}
}

// ── Mergeable state derivation ──────────────────────────────────────────────

// deriveGitLabMergeableState maps GitLab's detailed_merge_status to our
// canonical mergeable_state. Unknown values → "unknown".
func deriveGitLabMergeableState(detailed string) pgtype.Text {
	if detailed == "" {
		return pgtype.Text{}
	}
	normalized := strings.ToLower(detailed)
	switch normalized {
	case "mergeable":
		return pgtype.Text{String: "clean", Valid: true}
	// Still-running checks → unknown (GitHub's "pending" equivalent).
	case "checking", "unchecked", "preparing", "approvals_syncing",
		"merge_time", "not_open", "ci_must_pass", "ci_still_running":
		return pgtype.Text{String: "unknown", Valid: true}
	// Draft → draft (same as GitHub).
	case "draft_status":
		return pgtype.Text{String: "draft", Valid: true}
	// Conflicts / need-rebase → dirty.
	case "conflict", "need_rebase",
		"locked_paths", "locked_lfs_files", "commits_status":
		return pgtype.Text{String: "dirty", Valid: true}
	// Blocked by reviews / policies / external checks.
	case "not_approved", "requested_changes",
		"discussions_not_resolved", "status_checks_must_pass",
		"merge_request_blocked", "security_policy_violations",
		"security_policy_pipeline_check", "jira_association_missing",
		"title_regex":
		return pgtype.Text{String: "blocked", Valid: true}
	default:
		return pgtype.Text{String: "unknown", Valid: true}
	}
}

// deriveGitLabMRMergeableRefresh returns whether the mergeable_state should
// be cleared (written as NULL). Unlike GitHub — where state-change events
// carry a stale or absent mergeable_state and therefore must NULL the old
// verdict — GitLab payloads ALWAYS include detailed_merge_status. Every
// event carries a value that can be mapped to a canonical state, and even
// metadata-only "update" events include the current detailed_merge_status.
//
// key difference from GitHub's derivePRMergeableState:
//   - GitHub: clear=true on open/reopen/synchronize because payload's
//     mergeable_state is stale → SQL CASE branch 1 writes NULL
//   - GitLab: always clear=false because the mapped value from
//     detailed_merge_status is always at least as fresh as the existing
//     row — CASE branch 2 writes the mapped value (which may equal the
//     existing value on metadata-only events, a no-op), and branch 3
//     preserves when detailed_merge_status is empty.
//
// T9 faces the same choice when processing pipeline events.
func deriveGitLabMRMergeableRefresh() pgtype.Bool {
	return pgtype.Bool{Bool: false, Valid: true}
}

// ── Author backfill ─────────────────────────────────────────────────────────

func (h *Handler) resolveGitLabMRAuthor(ctx context.Context, conn db.GithubInstallation, authorID int64) (pgtype.Text, pgtype.Text) {
	if authorID <= 0 || h.GitLabBox == nil {
		return strToText("unknown"), pgtype.Text{}
	}
	tokenBytes, err := h.GitLabBox.Open(conn.AccessTokenCiphertext)
	if err != nil {
		slog.Warn("gitlab: failed to decrypt access token for author lookup",
			"connection_id", uuidToString(conn.ID), "err", err)
		return strToText("unknown"), pgtype.Text{}
	}
	instanceURL := ""
	if conn.InstanceUrl.Valid {
		instanceURL = conn.InstanceUrl.String
	}
	gc, err := gl.NewClient(instanceURL, string(tokenBytes))
	if err != nil {
		slog.Warn("gitlab: failed to construct client for author lookup",
			"connection_id", uuidToString(conn.ID), "err", err)
		return strToText("unknown"), pgtype.Text{}
	}

	username, avatarURL, err := gc.GetUser(ctx, authorID)
	if err != nil {
		slog.Warn("gitlab: get user failed for author lookup",
			"connection_id", uuidToString(conn.ID), "author_id", authorID, "err", err)
		return strToText("unknown"), pgtype.Text{}
	}

	if username == "" {
		return strToText("unknown"), pgtype.Text{}
	}
	return strToText(username), strToText(avatarURL)
}

// ── Time parsing ────────────────────────────────────────────────────────────

func parseGLTime(s string) pgtype.Timestamptz {
	if s == "" {
		return pgtype.Timestamptz{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func parseGLTimeRequired(s string) pgtype.Timestamptz {
	t := parseGLTime(s)
	if !t.Valid {
		return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	return t
}

// parseGLTimeRaw handles json.RawMessage that may be null, a string, or absent.
func parseGLTimeRaw(raw *json.RawMessage) pgtype.Timestamptz {
	if raw == nil {
		return pgtype.Timestamptz{}
	}
	var s string
	if err := json.Unmarshal(*raw, &s); err != nil {
		// Not a string — maybe null or a number. Treat as absent.
		return pgtype.Timestamptz{}
	}
	return parseGLTime(s)
}

// ── Path splitting ──────────────────────────────────────────────────────────

// splitPathWithNamespace splits a GitLab path_with_namespace on the LAST "/".
// "org/sub/repo" → owner="org/sub", name="repo".
// "singleproject" → owner="", name="singleproject".
func splitPathWithNamespace(path string) (owner, name string) {
	if path == "" {
		return "", ""
	}
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return "", path
	}
	return path[:idx], path[idx+1:]
}

// ── Response conversion ─────────────────────────────────────────────────────

func gitLabMRToResponse(p db.GithubPullRequest) GitHubPullRequestResponse {
	return GitHubPullRequestResponse{
		ID:              uuidToString(p.ID),
		WorkspaceID:     uuidToString(p.WorkspaceID),
		RepoOwner:       p.RepoOwner,
		RepoName:        p.RepoName,
		Number:          p.PrNumber,
		Title:           p.Title,
		State:           p.State,
		HtmlURL:         p.HtmlUrl,
		Branch:          textToPtr(p.Branch),
		AuthorLogin:     textToPtr(p.AuthorLogin),
		AuthorAvatarURL: textToPtr(p.AuthorAvatarUrl),
		MergedAt:        timestampToPtr(p.MergedAt),
		ClosedAt:        timestampToPtr(p.ClosedAt),
		PRCreatedAt:     timestampToString(p.PrCreatedAt),
		PRUpdatedAt:     timestampToString(p.PrUpdatedAt),
		MergeableState:  textToPtr(p.MergeableState),
		ChecksConclusion: nil,
		Additions:        p.Additions,
		Deletions:        p.Deletions,
		ChangedFiles:     p.ChangedFiles,
		Provider:         p.Provider,
	}
}
