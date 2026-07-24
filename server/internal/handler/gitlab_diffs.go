package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	gl "github.com/multica-ai/multica/server/internal/integrations/gitlab"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Guard-branch sentinels for resolveGitLabDiffTarget. The values double as
// the machine-readable "error" field in the HTTP response body.
var (
	errProviderNotSupported   = errors.New("provider_not_supported")
	errMRConnectionUnresolved = errors.New("mr_connection_unresolved")
)

// resolveGitLabDiffTarget validates that a mirrored pull request row can
// serve GitLab merge request diffs and returns the GitLab connection ID to
// use. It is pure (no DB, no network) so the guard branches can be unit
// tested without a database: a non-GitLab provider is a client error, and a
// NULL connection_id means the mirror never resolved a single connection
// (e.g. the workspace had multiple GitLab connections at ingest time).
func resolveGitLabDiffTarget(row db.GithubPullRequest) (pgtype.UUID, error) {
	if row.Provider != "gitlab" {
		return pgtype.UUID{}, errProviderNotSupported
	}
	if !row.ConnectionID.Valid {
		return pgtype.UUID{}, errMRConnectionUnresolved
	}
	return row.ConnectionID, nil
}

// writeDiffTargetError maps a resolveGitLabDiffTarget sentinel onto its HTTP
// status and pinned error body.
func writeDiffTargetError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errProviderNotSupported):
		writeError(w, http.StatusBadRequest, "provider_not_supported")
	case errors.Is(err, errMRConnectionUnresolved):
		writeError(w, http.StatusConflict, "mr_connection_unresolved")
	default:
		writeError(w, http.StatusInternalServerError, "failed to resolve merge request diff target")
	}
}

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
// request mirrored onto an issue.
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
	if _, err := h.Queries.GetIssuePullRequestLink(ctx, db.GetIssuePullRequestLinkParams{
		IssueID:       issue.ID,
		PullRequestID: prID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "pull request not found for issue")
		return
	}

	row, err := h.Queries.GetPullRequestByID(ctx, db.GetPullRequestByIDParams{
		ID:          prID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "pull request not found")
		return
	}

	connectionID, err := resolveGitLabDiffTarget(row)
	if err != nil {
		writeDiffTargetError(w, err)
		return
	}

	// The connection can disappear after the mirror (user disconnected the
	// GitLab account) — surface that as 409, not a bare 404.
	conn, err := h.Queries.GetGitLabConnectionByID(ctx, db.GetGitLabConnectionByIDParams{
		ID:          connectionID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusConflict, "mr_connection_unresolved")
		return
	}

	if h.GitLabBox == nil {
		writeError(w, http.StatusInternalServerError, "gitlab token encryption is not configured")
		return
	}
	tokenBytes, err := h.GitLabBox.Open(conn.AccessTokenCiphertext)
	if err != nil {
		slog.Warn("gitlab: failed to decrypt access token for diff fetch",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to decrypt gitlab access token")
		return
	}
	instanceURL := ""
	if conn.InstanceUrl.Valid {
		instanceURL = conn.InstanceUrl.String
	}
	client, err := gl.NewClient(instanceURL, string(tokenBytes))
	if err != nil {
		slog.Warn("gitlab: failed to construct client for diff fetch",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to construct gitlab client")
		return
	}

	files, err := client.ListMergeRequestDiffs(ctx, row.RepoOwner+"/"+row.RepoName, int64(row.PrNumber))
	if err != nil {
		status, msg := diffFetchErrorStatus(err)
		if status == http.StatusInternalServerError {
			slog.Warn("gitlab: diff fetch failed",
				"connection_id", uuidToString(conn.ID), "err", err)
		}
		writeError(w, status, msg)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}
