package handler

// T7 will fill the full Merge Request event mirroring logic.

import (
	"context"
	"log/slog"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// handleGitLabMergeRequestEvent processes a GitLab Merge Request webhook event.
// T7 implements the full MR mirroring logic: upsert MR rows, auto-link to
// issues via identifier extraction, and enqueue CI pipeline retrieval.
func (h *Handler) handleGitLabMergeRequestEvent(ctx context.Context, conn db.GithubInstallation, body []byte) error {
	slog.Debug("gitlab webhook: merge request event received (stub, T7 fills)")
	return nil
}
