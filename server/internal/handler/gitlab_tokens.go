// Package handler — GitLab token update and webhook secret rotation.
//
// RotateGitLabConnectionSecret operational note:
//
//	Rotating the webhook secret creates a brief window where in-flight
//	webhooks may still arrive with the old token and receive 401.
//	This is harmless: GitLab's auto-disable logic only triggers after
//	4 consecutive failures (temporary) or 40 consecutive failures
//	(permanent), and self-managed instances have the feature flag
//	(auto_disabling_webhooks) disabled by default.
//	See https://docs.gitlab.com/user/project/integrations/webhooks/#auto-disabled-webhooks

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
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// UpdateGitLabConnectionToken (PUT /api/workspaces/{id}/gitlab/connections/{connectionID}/token)
// replaces the access token for an existing GitLab connection. The new token is
// validated against the GitLab API before the stored ciphertext is updated. On
// validation failure the old ciphertext is preserved and a 400 is returned with
// a sanitised message — the token value is never logged or echoed.
//
// Caller must hold owner or admin role — the router enforces this.
func (h *Handler) UpdateGitLabConnectionToken(w http.ResponseWriter, r *http.Request) {
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
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.AccessToken) == "" {
		writeError(w, http.StatusBadRequest, "access_token is required")
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

	// Validate the new token against the GitLab API before updating storage.
	// On failure return 400 with a sanitised message — never echo the token.
	instanceURL := ""
	if conn.InstanceUrl.Valid {
		instanceURL = conn.InstanceUrl.String
	}
	gc, err := gl.NewClient(instanceURL, req.AccessToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid instance URL")
		return
	}
	_, _, err = gc.ValidateToken(r.Context())
	if err != nil {
		if errors.Is(err, gl.ErrUnauthorized) {
			writeError(w, http.StatusBadRequest, "invalid access token")
			return
		}
		slog.Error("gitlab_tokens: ValidateToken failed", "err", err, "instance_url", instanceURL)
		writeError(w, http.StatusBadRequest, "failed to validate access token")
		return
	}

	// Encrypt the new token for at-rest storage.
	tokenCiphertext, err := h.GitLabBox.Seal([]byte(req.AccessToken))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt token")
		return
	}

	if err := h.Queries.UpdateGitLabConnectionToken(r.Context(), db.UpdateGitLabConnectionTokenParams{
		ID:                    connUUID,
		WorkspaceID:           wsUUID,
		AccessTokenCiphertext: tokenCiphertext,
	}); err != nil {
		slog.Error("gitlab_tokens: UpdateGitLabConnectionToken failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to update token")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// rotableTarget identifies a hook that was successfully patched during
// secret rotation and must be rolled back on failure.
type rotableTarget struct {
	TargetType string `json:"-"`
	TargetPath string `json:"-"`
	HookID     int64  `json:"hook_id"`
}

// RotateGitLabConnectionSecret (POST /api/workspaces/{id}/gitlab/connections/{connectionID}/rotate-secret)
// generates a new webhook secret, patches every registered hook on the remote
// GitLab instance to use it, and — only after every hook succeeds — atomically
// replaces the stored hash and ciphertext.
//
// If any hook patch fails the handler performs a best-effort rollback: hooks
// that were already updated are patched back to the old secret. The response
// (502) identifies which target failed. The stored hash and ciphertext are
// never updated on a partial failure, so subsequent webhooks continue to
// verify against the old secret.
//
// Caller must hold owner or admin role — the router enforces this.
func (h *Handler) RotateGitLabConnectionSecret(w http.ResponseWriter, r *http.Request) {
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

	conn, err := h.Queries.GetGitLabConnectionByID(r.Context(), db.GetGitLabConnectionByIDParams{
		ID:          connUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}

	// Decode hook records from the JSONB column.
	hooks, err := decodeHooksJSON(conn.Hooks)
	if err != nil {
		slog.Warn("gitlab_tokens: failed to parse hooks jsonb, treating as empty",
			"connection_id", uuidToString(conn.ID), "err", err)
		hooks = nil
	}

	// Decrypt the old webhook secret for rollback.
	oldSecretBytes, err := h.GitLabBox.Open(conn.WebhookSecretCiphertext)
	if err != nil {
		slog.Error("gitlab_tokens: failed to decrypt old webhook secret",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to decrypt webhook secret")
		return
	}
	oldSecret := string(oldSecretBytes)

	// Generate a new 32-byte webhook secret.
	newSecretBytes := make([]byte, 32)
	if _, err := rand.Read(newSecretBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate webhook secret")
		return
	}
	newSecret := hex.EncodeToString(newSecretBytes) // 64 hex chars

	// If there are no hooks, just swap the hash directly — no remote PATCH
	// calls needed.
	if len(hooks) == 0 {
		newHash := sha256.Sum256([]byte(newSecret))
		newCiphertext, err := h.GitLabBox.Seal([]byte(newSecret))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encrypt webhook secret")
			return
		}
		if err := h.Queries.UpdateGitLabConnectionSecret(r.Context(), db.UpdateGitLabConnectionSecretParams{
			ID:                      connUUID,
			WorkspaceID:             wsUUID,
			WebhookSecretHash:       newHash[:],
			WebhookSecretCiphertext: newCiphertext,
		}); err != nil {
			slog.Error("gitlab_tokens: UpdateGitLabConnectionSecret failed",
				"connection_id", uuidToString(conn.ID), "err", err)
			writeError(w, http.StatusInternalServerError, "failed to update secret")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"rotated": true})
		return
	}

	// Decrypt the access token to construct a GitLab API client for hook
	// patching.
	tokenBytes, err := h.GitLabBox.Open(conn.AccessTokenCiphertext)
	if err != nil {
		slog.Error("gitlab_tokens: failed to decrypt access token",
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
		slog.Error("gitlab_tokens: failed to construct GitLab client",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to construct GitLab client")
		return
	}

	// Patch every hook. Track successfully patched hooks so we can roll
	// them back on failure.
	patched := make([]rotableTarget, 0, len(hooks))

	for _, hook := range hooks {
		if hook.HookID <= 0 {
			continue
		}
		var targetType gl.TargetType
		switch hook.TargetType {
		case "project":
			targetType = gl.TargetProject
		case "group":
			targetType = gl.TargetGroup
		default:
			slog.Warn("gitlab_tokens: unknown hook target_type, skipping rotate",
				"connection_id", uuidToString(conn.ID),
				"target_type", hook.TargetType,
				"hook_id", hook.HookID)
			continue
		}

		if err := gc.PatchHookToken(r.Context(), targetType, hook.TargetPath, hook.HookID, newSecret); err != nil {
			// Failure — best-effort rollback every hook we already
			// patched.
			h.rollbackHookSecrets(r.Context(), gc, patched, oldSecret, conn.ID)
			slog.Warn("gitlab_tokens: secret rotation hook patch failed, rolled back",
				"connection_id", uuidToString(conn.ID),
				"failed_target_type", hook.TargetType,
				"failed_target_path", hook.TargetPath,
				"failed_hook_id", hook.HookID,
				"err", err)
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error":        "failed to patch hook on GitLab",
				"rotated":      false,
				"failed_target": hook.TargetPath,
			})
			return
		}

		patched = append(patched, rotableTarget{
			TargetType: hook.TargetType,
			TargetPath: hook.TargetPath,
			HookID:     hook.HookID,
		})
	}

	// All hooks patched successfully — update the stored hash and ciphertext.
	newHash := sha256.Sum256([]byte(newSecret))
	newCiphertext, err := h.GitLabBox.Seal([]byte(newSecret))
	if err != nil {
		// This is a catastrophic failure: hooks are already updated but we
		// can't store the new secret. Best-effort rollback to old secret.
		h.rollbackHookSecrets(r.Context(), gc, patched, oldSecret, conn.ID)
		writeError(w, http.StatusInternalServerError, "failed to encrypt webhook secret")
		return
	}

	if err := h.Queries.UpdateGitLabConnectionSecret(r.Context(), db.UpdateGitLabConnectionSecretParams{
		ID:                      connUUID,
		WorkspaceID:             wsUUID,
		WebhookSecretHash:       newHash[:],
		WebhookSecretCiphertext: newCiphertext,
	}); err != nil {
		// DB write failed after hooks were patched. Roll them back.
		h.rollbackHookSecrets(r.Context(), gc, patched, oldSecret, conn.ID)
		slog.Error("gitlab_tokens: UpdateGitLabConnectionSecret failed",
			"connection_id", uuidToString(conn.ID), "err", err)
		writeError(w, http.StatusInternalServerError, "failed to update secret")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"rotated": true})
}

// rollbackHookSecrets patches every given hook back to oldSecret on a
// best-effort basis. Failures are logged but never block the caller.
func (h *Handler) rollbackHookSecrets(ctx context.Context, gc *gl.Client, targets []rotableTarget, oldSecret string, connID pgtype.UUID) {
	for _, t := range targets {
		var targetType gl.TargetType
		switch t.TargetType {
		case "project":
			targetType = gl.TargetProject
		case "group":
			targetType = gl.TargetGroup
		default:
			continue
		}
		if err := gc.PatchHookToken(ctx, targetType, t.TargetPath, t.HookID, oldSecret); err != nil {
			slog.Warn("gitlab_tokens: rollback hook patch failed",
				"connection_id", uuidToString(connID),
				"target_type", t.TargetType,
				"target_path", t.TargetPath,
				"hook_id", t.HookID,
				"err", err)
		}
	}
}
