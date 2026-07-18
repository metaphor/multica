package handler

import (
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/logger"
)

// HandleGitLabWebhook is the public webhook ingress for GitLab integrations.
// It mirrors the structure of HandleGitHubWebhook: cap body at 10 MiB, verify
// the secret via X-Gitlab-Token header against a stored sha256 hash, and route
// on X-Gitlab-Event. Only token-missing or token-mismatch returns 401; every
// other failure is logged and acked with 202 to avoid GitLab auto-disabling
// the webhook endpoint (4 consecutive failures → temporarily disabled, 40 →
// permanently disabled).
//
// See: https://docs.gitlab.com/user/project/integrations/webhooks/#auto-disabled-webhooks
func (h *Handler) HandleGitLabWebhook(w http.ResponseWriter, r *http.Request) {
	// Cap request body at 10 MiB to prevent memory exhaustion.
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body failed")
		return
	}

	// Verify the webhook token via sha256 hash. GitLab sends the secret as
	// a plain-text header; we never store or log the raw token.
	token := r.Header.Get("X-Gitlab-Token")
	if token == "" {
		// Rate-limit log: WARN so operators can see the misconfiguration,
		// but do not log the missing token value (there is none) to avoid
		// leaking anything.
		slog.Warn("gitlab webhook: missing X-Gitlab-Token header", logger.RequestAttrs(r)...)
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	hash := sha256.Sum256([]byte(token))
	ctx := r.Context()
	conn, err := h.Queries.GetGitLabConnectionBySecretHash(ctx, hash[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		slog.Warn("gitlab webhook: lookup connection by secret hash failed",
			append([]any{"error", err}, logger.RequestAttrs(r)...)...)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	event := r.Header.Get("X-Gitlab-Event")
	var handleErr error
	switch event {
	case "Merge Request Hook":
		handleErr = h.handleGitLabMergeRequestEvent(ctx, conn, body)
	case "Pipeline Hook":
		handleErr = h.handleGitLabPipelineEvent(ctx, conn, body)
	default:
		// Acknowledge every event so GitLab doesn't mark the endpoint
		// as failing, but ignore types we don't model (same semantics
		// as HandleGitHubWebhook's default branch).
	}

	if handleErr != nil {
		// Log the failure but still return 202. Returning a non-2xx
		// status would cause GitLab to increment its internal failure
		// counter, potentially leading to the webhook being
		// auto-disabled after 4 consecutive failures (temporary) or
		// 40 failures (permanent). See docs referenced above.
		slog.Warn("gitlab webhook: event handler failed",
			append([]any{"event", event, "error", handleErr}, logger.RequestAttrs(r)...)...)
	}

	w.WriteHeader(http.StatusAccepted)
}
