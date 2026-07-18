package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	gl "github.com/multica-ai/multica/server/internal/integrations/gitlab"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// hookRecord is the canonical shape for a hook entry stored in the hooks
// JSONB column. It extends gitlab.go's hookEntry with URL, created_at, and
// last_error. The read-side (hookEntry in gitlab.go) only touches
// target_type/target_path/hook_id so this extended shape is backward-compatible
// — extra JSON keys are silently ignored by encoding/json on unmarshal.
type hookRecord struct {
	TargetType string  `json:"target_type"`
	TargetPath string  `json:"target_path"`
	HookID     int64   `json:"hook_id"`
	URL        string  `json:"url"`
	CreatedAt  string  `json:"created_at"`
	LastError  *string `json:"last_error"`
}

// ── AddGitLabHookTarget ───────────────────────────────────────────────────────

// AddGitLabHookTarget handles POST /api/workspaces/{id}/gitlab/connections/{connectionID}/hooks.
// It registers a new webhook on the GitLab target (project or group) and
// appends the hook metadata to the connection's hooks JSONB column.
//
// Caller must hold owner or admin role — the router enforces this.
//
// Error mapping from GitLab API:
//
//	403 → 422 (group: "group webhooks require Owner role and Premium tier";
//	              project: "token lacks Maintainer role")
//	404 → 422 "target not found or token lacks access"
//
// Concurrency note: hooks updates use a read-then-write pattern (Get →
// Update). Concurrent Add/Delete calls may race; this is acceptable because
// the hooks column is an append-only log of registrations, not a coordination
// table. A lost write simply means the hook exists on GitLab but is dropped
// from the local tracking, which self-heals on the next GC pass.
func (h *Handler) AddGitLabHookTarget(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	connectionID := chi.URLParam(r, "connectionID")
	connUUID, ok := parseUUIDOrBadRequest(w, connectionID, "connection id")
	if !ok {
		return
	}

	if !h.isGitLabConfigured() {
		writeError(w, http.StatusServiceUnavailable,
			"GitLab integration is not configured (MULTICA_GITLAB_SECRET_KEY is not set)")
		return
	}

	var req struct {
		TargetType string `json:"target_type"`
		TargetPath string `json:"target_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.TargetType) == "" {
		writeError(w, http.StatusBadRequest, "target_type is required")
		return
	}
	if strings.TrimSpace(req.TargetPath) == "" {
		writeError(w, http.StatusBadRequest, "target_path is required")
		return
	}
	if req.TargetType != "project" && req.TargetType != "group" {
		writeError(w, http.StatusBadRequest, "target_type must be 'project' or 'group'")
		return
	}

	publicURL := strings.TrimSpace(h.cfg.PublicURL)
	if publicURL == "" {
		writeError(w, http.StatusServiceUnavailable,
			"MULTICA_PUBLIC_URL is not configured; required to build webhook URLs")
		return
	}

	conn, err := h.Queries.GetGitLabConnectionByID(r.Context(), db.GetGitLabConnectionByIDParams{
		ID:          connUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}

	// Decrypt the webhook secret for use as the hook token.
	secretBytes, err := h.GitLabBox.Open(conn.WebhookSecretCiphertext)
	if err != nil {
		slog.Error("gitlab_hooks: failed to decrypt webhook secret",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to decrypt webhook secret")
		return
	}
	secretPlaintext := string(secretBytes)

	// Decrypt the access token to construct a GitLab API client.
	tokenBytes, err := h.GitLabBox.Open(conn.AccessTokenCiphertext)
	if err != nil {
		slog.Error("gitlab_hooks: failed to decrypt access token",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to decrypt access token")
		return
	}

	instanceURL := ""
	if conn.InstanceUrl.Valid {
		instanceURL = conn.InstanceUrl.String
	}
	gc, err := gl.NewClient(instanceURL, string(tokenBytes))
	if err != nil {
		slog.Error("gitlab_hooks: failed to construct GitLab client",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to construct GitLab client")
		return
	}

	hookURL := publicURL + "/api/webhooks/gitlab"
	// EnableSSLVerification governs GitLab→Multica certificate verification.
	// When Multica is served over HTTPS, GitLab should validate the TLS
	// certificate; when running locally over HTTP, verification is disabled.
	enableSSL := strings.HasPrefix(publicURL, "https://")

	params := gl.HookParams{
		URL:                   hookURL,
		Token:                 secretPlaintext,
		MergeRequestsEvents:   true,
		PipelineEvents:        true,
		EnableSSLVerification: enableSSL,
	}

	var hookID int64
	switch req.TargetType {
	case "project":
		hookID, err = gc.CreateProjectHook(r.Context(), req.TargetPath, params)
	case "group":
		hookID, err = gc.CreateGroupHook(r.Context(), req.TargetPath, params)
	}
	if err != nil {
		h.mapGitLabHookError(w, err, req.TargetType)
		return
	}

	// Read existing hooks, append the new entry, write back.
	// Concurrent updates may race — see the doc comment on AddGitLabHookTarget.
	hooks, err := decodeHooksJSON(conn.Hooks)
	if err != nil {
		slog.Warn("gitlab_hooks: failed to parse hooks jsonb, treating as empty",
			"connection_id", uuidToString(conn.ID), "err", err)
		hooks = nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	hooks = append(hooks, hookRecord{
		TargetType: req.TargetType,
		TargetPath: req.TargetPath,
		HookID:     hookID,
		URL:        hookURL,
		CreatedAt:  now,
		LastError:  nil,
	})

	raw, err := json.Marshal(hooks)
	if err != nil {
		slog.Error("gitlab_hooks: failed to marshal hooks json",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to persist hook entry")
		return
	}

	if err := h.Queries.UpdateGitLabConnectionHooks(r.Context(), db.UpdateGitLabConnectionHooksParams{
		ID:          connUUID,
		WorkspaceID: wsUUID,
		Hooks:       raw,
	}); err != nil {
		slog.Error("gitlab_hooks: UpdateGitLabConnectionHooks failed",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to persist hook entry")
		return
	}

	writeJSON(w, http.StatusCreated, sanitizeHookResponse(hooks))
}

// ── RemoveGitLabHookTarget ────────────────────────────────────────────────────

// RemoveGitLabHookTarget handles
// DELETE /api/workspaces/{id}/gitlab/connections/{connectionID}/hooks.
// It removes a previously registered webhook — first from the remote GitLab
// instance, then from the local hooks JSONB. A 404 from GitLab is tolerated
// (the hook may already have been deleted externally).
//
// Caller must hold owner or admin role — the router enforces this.
func (h *Handler) RemoveGitLabHookTarget(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	connectionID := chi.URLParam(r, "connectionID")
	connUUID, ok := parseUUIDOrBadRequest(w, connectionID, "connection id")
	if !ok {
		return
	}

	if !h.isGitLabConfigured() {
		writeError(w, http.StatusServiceUnavailable,
			"GitLab integration is not configured (MULTICA_GITLAB_SECRET_KEY is not set)")
		return
	}

	var req struct {
		TargetType string `json:"target_type"`
		TargetPath string `json:"target_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TargetType != "project" && req.TargetType != "group" {
		writeError(w, http.StatusBadRequest, "target_type must be 'project' or 'group'")
		return
	}
	if strings.TrimSpace(req.TargetPath) == "" {
		writeError(w, http.StatusBadRequest, "target_path is required")
		return
	}

	conn, err := h.Queries.GetGitLabConnectionByID(r.Context(), db.GetGitLabConnectionByIDParams{
		ID:          connUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}

	hooks, err := decodeHooksJSON(conn.Hooks)
	if err != nil {
		slog.Warn("gitlab_hooks: failed to parse hooks jsonb, treating as empty",
			"connection_id", uuidToString(conn.ID), "err", err)
		hooks = nil
	}

	// Find the matching hook entry.
	idx := -1
	for i, hk := range hooks {
		if hk.TargetType == req.TargetType && hk.TargetPath == req.TargetPath {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeError(w, http.StatusNotFound, "hook not found for this target")
		return
	}
	hookID := hooks[idx].HookID

	// Best-effort remote deletion.
	if hookID > 0 {
		tokenBytes, err := h.GitLabBox.Open(conn.AccessTokenCiphertext)
		if err != nil {
			slog.Error("gitlab_hooks: failed to decrypt access token for remote hook deletion",
				"connection_id", uuidToString(conn.ID), "err", err)
			writeError(w, http.StatusInternalServerError, "failed to decrypt access token")
			return
		}
		instanceURL := ""
		if conn.InstanceUrl.Valid {
			instanceURL = conn.InstanceUrl.String
		}
		gc, err := gl.NewClient(instanceURL, string(tokenBytes))
		if err != nil {
			slog.Error("gitlab_hooks: failed to construct GitLab client for deletion",
				"connection_id", uuidToString(conn.ID), "err", err)
			writeError(w, http.StatusInternalServerError, "failed to construct GitLab client")
			return
		}

		var delErr error
		switch req.TargetType {
		case "project":
			delErr = gc.DeleteProjectHook(r.Context(), req.TargetPath, hookID)
		case "group":
			delErr = gc.DeleteGroupHook(r.Context(), req.TargetPath, hookID)
		}
		// Tolerate 404: the hook may already have been deleted externally.
		if delErr != nil && !errors.Is(delErr, gl.ErrNotFound) {
			slog.Warn("gitlab_hooks: remote hook deletion failed",
				"connection_id", uuidToString(conn.ID),
				"target_type", req.TargetType,
				"target_path", req.TargetPath,
				"hook_id", hookID,
				"err", delErr)
			writeError(w, http.StatusInternalServerError, "failed to delete remote hook")
			return
		}
	}

	// Remove the entry from the local list.
	hooks = append(hooks[:idx], hooks[idx+1:]...)
	raw, err := json.Marshal(hooks)
	if err != nil {
		slog.Error("gitlab_hooks: failed to marshal hooks json",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to persist hook removal")
		return
	}

	if err := h.Queries.UpdateGitLabConnectionHooks(r.Context(), db.UpdateGitLabConnectionHooksParams{
		ID:          connUUID,
		WorkspaceID: wsUUID,
		Hooks:       raw,
	}); err != nil {
		slog.Error("gitlab_hooks: UpdateGitLabConnectionHooks failed during removal",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to persist hook removal")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// mapGitLabHookError translates GitHub API client errors into appropriate HTTP
// responses for the Add hook endpoint.
func (h *Handler) mapGitLabHookError(w http.ResponseWriter, err error, targetType string) {
	switch {
	case errors.Is(err, gl.ErrForbidden):
		if targetType == "group" {
			writeError(w, http.StatusUnprocessableEntity,
				"group webhooks require Owner role and Premium tier")
		} else {
			writeError(w, http.StatusUnprocessableEntity,
				"token lacks Maintainer role")
		}
	case errors.Is(err, gl.ErrNotFound):
		writeError(w, http.StatusUnprocessableEntity,
			"target not found or token lacks access")
	default:
		slog.Error("gitlab_hooks: failed to create hook", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create webhook on GitLab")
	}
}

// decodeHooksJSON parses the raw JSONB hooks column into a []hookRecord.
// On parse failure it returns an error so the caller can log and fall back.
func decodeHooksJSON(raw []byte) ([]hookRecord, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var hooks []hookRecord
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return nil, err
	}
	return hooks, nil
}

// sanitizeHookResponse returns a copy of the hooks list suitable for API
// responses. The URL field never contains the webhook secret (the secret is
// sent as a POST body token field, not a URL parameter). LastError is
// nil-safe in JSON output.
func sanitizeHookResponse(hooks []hookRecord) []hookRecord {
	out := make([]hookRecord, len(hooks))
	copy(out, hooks)
	return out
}
