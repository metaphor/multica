// Package handler — GitLab connection management.
//
// Security design notes:
//
//   - Webhook secrets are crypto/rand 32 bytes (256-bit entropy). A bare SHA-256
//     hash is sufficient for offline-guess resistance — the search space (2^256)
//     makes brute-force computationally infeasible without an HMAC deployment
//     binding. This mirrors the personal_access_token token_hash precedent.
//
//   - At-rest encryption uses AES-256-GCM via secretbox. The secretbox key is
//     decoupled from inbound webhook availability: when MULTICA_GITLAB_SECRET_KEY
//     is absent, webhook verification still works (it only needs the cleartext
//     secret hash), but connection management returns 503.

package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	gl "github.com/multica-ai/multica/server/internal/integrations/gitlab"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// GitLabConnectionResponse is the sanitised API surface for a GitLab connection.
// It never contains tokens, secrets, hashes, or ciphertext.
type GitLabConnectionResponse struct {
	ID           string          `json:"id"`
	InstanceURL  string          `json:"instance_url"`
	DisplayName  string          `json:"display_name"`
	AccountLogin string          `json:"account_login"`
	Hooks        json.RawMessage `json:"hooks"`
	CreatedAt    string          `json:"created_at"`
	Configured   bool            `json:"configured"`
	CanManage    bool            `json:"can_manage"`
}

func (h *Handler) isGitLabConfigured() bool { return h.GitLabBox != nil }

func gitLabConnectionToResponse(i db.GithubInstallation, canManage bool) GitLabConnectionResponse {
	return GitLabConnectionResponse{
		ID:           uuidToString(i.ID),
		InstanceURL:  textToStr(i.InstanceUrl),
		DisplayName:  textToStr(i.DisplayName),
		AccountLogin: i.AccountLogin,
		Hooks:        safeHooksJSON(i.Hooks),
		CreatedAt:    timestampToString(i.CreatedAt),
		Configured:   true,
		CanManage:    canManage,
	}
}

func textToStr(t pgtype.Text) string {
	if t.Valid {
		return t.String
	}
	return ""
}

func safeHooksJSON(raw []byte) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage("[]")
	}
	return raw
}

// gitLabConnectionToBroadcast returns a stripped payload containing only id and
// workspace_id. Realtime events fan out to every WS client subscribed to the
// workspace, so the payload must match the weakest-role view — admins re-query
// the list endpoint to recover management handles.
func gitLabConnectionToBroadcast(i db.GithubInstallation) map[string]any {
	return map[string]any{
		"id":           uuidToString(i.ID),
		"workspace_id": uuidToString(i.WorkspaceID),
	}
}

// CreateGitLabConnection (POST /api/workspaces/{id}/gitlab/connections)
// creates a new GitLab connection for the workspace. Only owner/admin callers
// are admitted — the router enforces this.
func (h *Handler) CreateGitLabConnection(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if !h.isGitLabConfigured() {
		writeError(w, http.StatusServiceUnavailable,
			"GitLab integration is not configured (MULTICA_GITLAB_SECRET_KEY is not set or is invalid — expected base64-encoded 32 bytes)")
		return
	}

	var req struct {
		InstanceURL string  `json:"instance_url"`
		AccessToken string  `json:"access_token"`
		DisplayName *string `json:"display_name,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.InstanceURL) == "" {
		writeError(w, http.StatusBadRequest, "instance_url is required")
		return
	}
	if strings.TrimSpace(req.AccessToken) == "" {
		writeError(w, http.StatusBadRequest, "access_token is required")
		return
	}

	// Validate the token against the GitLab API. On failure return 400
	// with a sanitised message — never echo the token.
	gc, err := gl.NewClient(req.InstanceURL, req.AccessToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid instance URL")
		return
	}
	username, _, err := gc.ValidateToken(r.Context())
	if err != nil {
		if errors.Is(err, gl.ErrUnauthorized) {
			writeError(w, http.StatusBadRequest, "invalid access token")
			return
		}
		slog.Error("gitlab: ValidateToken failed", "err", err, "instance_url", req.InstanceURL)
		writeError(w, http.StatusBadRequest, "failed to validate access token")
		return
	}

	// Generate a 32-byte webhook secret.
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate webhook secret")
		return
	}
	secretHex := hex.EncodeToString(secretBytes) // 64 hex chars

	// Hash the secret for lookup during webhook verification.
	hash := sha256.Sum256([]byte(secretHex))

	// Encrypt token and secret for at-rest storage.
	tokenCiphertext, err := h.GitLabBox.Seal([]byte(req.AccessToken))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt token")
		return
	}
	secretCiphertext, err := h.GitLabBox.Seal([]byte(secretHex))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt webhook secret")
		return
	}

	displayName := username
	if req.DisplayName != nil && strings.TrimSpace(*req.DisplayName) != "" {
		displayName = *req.DisplayName
	}

	member, _ := middleware.MemberFromContext(r.Context())
	canManage := roleAllowed(member.Role, "owner", "admin")

	conn, err := h.Queries.InsertGitLabConnection(r.Context(), db.InsertGitLabConnectionParams{
		WorkspaceID:             wsUUID,
		AccountLogin:            username,
		DisplayName:             pgtype.Text{String: displayName, Valid: true},
		InstanceUrl:             pgtype.Text{String: req.InstanceURL, Valid: true},
		AccessTokenCiphertext:   tokenCiphertext,
		WebhookSecretHash:       hash[:],
		WebhookSecretCiphertext: secretCiphertext,
		Hooks:                   []byte("[]"),
	})
	if err != nil {
		slog.Error("gitlab: InsertGitLabConnection failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to save connection")
		return
	}

	h.publish(protocol.EventGitLabConnectionCreated, workspaceID, "system", "",
		gitLabConnectionToBroadcast(conn))

	resp := gitLabConnectionToResponse(conn, canManage)
	resp.Configured = h.isGitLabConfigured()
	writeJSON(w, http.StatusCreated, resp)
}

// ListGitLabConnections (GET /api/workspaces/{id}/gitlab/connections)
// returns the workspace's connected GitLab instances to any workspace member.
// The response carries a can_manage hint and configured flag so the UI can
// gate connect/disconnect controls.
func (h *Handler) ListGitLabConnections(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	member, _ := middleware.MemberFromContext(r.Context())
	canManage := roleAllowed(member.Role, "owner", "admin")

	rows, err := h.Queries.ListGitLabConnectionsByWorkspace(r.Context(), wsUUID)
	if err != nil {
		slog.Error("gitlab: ListGitLabConnectionsByWorkspace failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list connections")
		return
	}
	out := make([]GitLabConnectionResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, gitLabConnectionToResponse(row, canManage))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connections": out,
		"configured":  h.isGitLabConfigured(),
		"can_manage":  canManage,
	})
}

type hookEntry struct {
	TargetType string `json:"target_type"`
	TargetPath string `json:"target_path"`
	HookID     int64  `json:"hook_id"`
}

// DeleteGitLabConnection (DELETE /api/workspaces/{id}/gitlab/connections/{connectionID})
// removes a GitLab connection. Before deleting the DB row it best-effort deletes
// every registered webhook from the remote GitLab instance — failures are logged
// but never block the local delete.
func (h *Handler) DeleteGitLabConnection(w http.ResponseWriter, r *http.Request) {
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

	conn, err := h.Queries.GetGitLabConnectionByID(r.Context(), db.GetGitLabConnectionByIDParams{
		ID:          connUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}

	// Best-effort remote hook deletion.
	if h.GitLabBox != nil {
		h.deleteRemoteHooks(r.Context(), conn)
	}

	if err := h.Queries.DeleteGitLabConnection(r.Context(), db.DeleteGitLabConnectionParams{
		ID:          connUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		slog.Error("gitlab: DeleteGitLabConnection failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to delete connection")
		return
	}

	h.publish(protocol.EventGitLabConnectionDeleted, workspaceID, "system", "",
		gitLabConnectionToBroadcast(conn))

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteRemoteHooks(ctx context.Context, conn db.GithubInstallation) {
	var hooks []hookEntry
	if err := json.Unmarshal(conn.Hooks, &hooks); err != nil {
		slog.Warn("gitlab: failed to parse hooks jsonb, skipping remote cleanup",
			"connection_id", uuidToString(conn.ID), "err", err)
		return
	}
	if len(hooks) == 0 {
		return
	}

	tokenBytes, err := h.GitLabBox.Open(conn.AccessTokenCiphertext)
	if err != nil {
		slog.Error("gitlab: failed to decrypt access token for remote hook cleanup",
			"connection_id", uuidToString(conn.ID), "err", err)
		return
	}
	instanceURL := ""
	if conn.InstanceUrl.Valid {
		instanceURL = conn.InstanceUrl.String
	}
	gc, err := gl.NewClient(instanceURL, string(tokenBytes))
	if err != nil {
		slog.Error("gitlab: failed to construct client for remote hook cleanup",
			"connection_id", uuidToString(conn.ID), "err", err)
		return
	}

	for _, hook := range hooks {
		if hook.HookID <= 0 {
			slog.Warn("gitlab: skipping hook entry with invalid id",
				"connection_id", uuidToString(conn.ID), "hook_id", hook.HookID)
			continue
		}
		var delErr error
		switch hook.TargetType {
		case "project":
			delErr = gc.DeleteProjectHook(ctx, hook.TargetPath, hook.HookID)
		case "group":
			delErr = gc.DeleteGroupHook(ctx, hook.TargetPath, hook.HookID)
		default:
			slog.Warn("gitlab: unknown hook target_type, skipping",
				"connection_id", uuidToString(conn.ID),
				"target_type", hook.TargetType,
				"hook_id", hook.HookID)
			continue
		}
		if delErr != nil && !errors.Is(delErr, gl.ErrNotFound) {
			slog.Warn("gitlab: best-effort remote hook delete failed",
				"connection_id", uuidToString(conn.ID),
				"target_type", hook.TargetType,
				"target_path", hook.TargetPath,
				"hook_id", hook.HookID,
				"err", delErr)
		}
	}
}
