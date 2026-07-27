package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/integrations/vcs"
	gl "github.com/multica-ai/multica/server/internal/integrations/gitlab"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Guard-branch sentinel for the diff target provider check. The value doubles
// as the machine-readable "error" field in the HTTP response body.
var errProviderNotSupported = errors.New("provider_not_supported")

// diffFetchErrorStatus maps a ListMergeRequestDiffs client error onto the
// HTTP status and message served to the caller:
//
//	gitlab.ErrNotFound            -> 404 (the MR itself is gone upstream)
//	context.DeadlineExceeded      -> 504 (upstream timed out)
//	gitlab.ErrUnauthorized        -> 502 (stored token rejected upstream)
//	gitlab.ErrForbidden           -> 502 (stored token lost access upstream)
//	*gitlab.ErrServer (5xx)       -> 502 (upstream is broken, not us)
//	anything else                 -> 500
func diffFetchErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, gl.ErrNotFound):
		return http.StatusNotFound, "merge request not found"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "gitlab request timed out"
	case errors.Is(err, gl.ErrUnauthorized), errors.Is(err, gl.ErrForbidden):
		return http.StatusBadGateway, "gitlab rejected the stored access token"
	}
	var srvErr *gl.ErrServer
	if errors.As(err, &srvErr) {
		return http.StatusBadGateway, "gitlab upstream error"
	}
	return http.StatusInternalServerError, "failed to fetch merge request diffs"
}

// ListMergeRequestDiffs serves GET /api/issues/{id}/pull-requests/{prId}/diffs.
// It returns the provider-neutral changed-file list for a GitLab merge
// request mirrored onto an issue. Only self-hosted GitLab MRs are supported:
// the PR id addresses a vcs_pull_request row, so Forgejo/Gitea PRs are
// rejected with provider_not_supported.
func (h *Handler) ListMergeRequestDiffs(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	prID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "prId"), "pull request id")
	if !ok {
		return
	}

	ctx := r.Context()

	// The link row proves this PR is attached to the issue; without it the
	// (issue, pr) pair is unknown and the endpoint 404s rather than leaking
	// whether the PR exists in another context.
	if _, err := h.Queries.GetIssueVCSPullRequestLink(ctx, db.GetIssueVCSPullRequestLinkParams{
		IssueID:       issue.ID,
		PullRequestID: prID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "pull request not found for issue")
		return
	}

	row, err := h.Queries.GetVCSPullRequestByID(ctx, db.GetVCSPullRequestByIDParams{
		ID:          prID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "pull request not found")
		return
	}

	if row.Provider != string(vcs.KindGitLab) {
		writeError(w, http.StatusBadRequest, errProviderNotSupported.Error())
		return
	}

	// The connection can disappear after the mirror (user disconnected the
	// GitLab account) — surface that as 409, not a bare 404.
	conn, err := h.Queries.GetVCSConnectionByID(ctx, row.ConnectionID)
	if err != nil || conn.WorkspaceID != issue.WorkspaceID {
		writeError(w, http.StatusConflict, "mr_connection_unresolved")
		return
	}

	if h.VCSSecretBox == nil {
		writeError(w, http.StatusInternalServerError, "vcs token encryption is not configured")
		return
	}
	token, err := h.openVCSSecret(conn.AccessTokenEncrypted)
	if err != nil {
		slog.Warn("vcs: failed to decrypt access token for diff fetch",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to decrypt gitlab access token")
		return
	}
	client, err := gl.NewClient(conn.InstanceUrl, token)
	if err != nil {
		slog.Warn("vcs: failed to construct gitlab client for diff fetch",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to construct gitlab client")
		return
	}

	files, err := client.ListMergeRequestDiffs(ctx, row.RepoOwner+"/"+row.RepoName, int64(row.PrNumber))
	if err != nil {
		status, msg := diffFetchErrorStatus(err)
		if status == http.StatusInternalServerError {
			slog.Warn("vcs: gitlab diff fetch failed",
				"connection_id", uuidToString(conn.ID), "err", err)
		}
		writeError(w, status, msg)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}
