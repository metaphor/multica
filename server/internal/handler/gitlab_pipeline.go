package handler

// T9 will fill the full Pipeline event handling logic.

import (
	"context"
	"log/slog"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// handleGitLabPipelineEvent processes a GitLab Pipeline webhook event.
// T9 implements the full pipeline CI status mirroring logic: parse pipeline
// status, correlate with MR via merge_request head/new SHA, and upsert
// check-suite rows.
func (h *Handler) handleGitLabPipelineEvent(ctx context.Context, conn db.GithubInstallation, body []byte) error {
	slog.Debug("gitlab webhook: pipeline event received (stub, T9 fills)")
	return nil
}
