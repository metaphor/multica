package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ── Tolerant payload types ──────────────────────────────────────────────────

type glPipelinePayload struct {
	Project          *glPipelineProject          `json:"project"`
	ObjectAttributes *glPipelineObjectAttributes `json:"object_attributes"`
	MergeRequest     *glPipelineMergeRequest     `json:"merge_request"`
}

type glPipelineProject struct {
	PathWithNamespace string `json:"path_with_namespace"`
}

type glPipelineObjectAttributes struct {
	ID         int64  `json:"id"`
	Sha        string `json:"sha"`
	Ref        string `json:"ref"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	FinishedAt string `json:"finished_at"`
	URL        string `json:"url"`
}

type glPipelineMergeRequest struct {
	IID int64 `json:"iid"`
}

// ── handleGitLabPipelineEvent ───────────────────────────────────────────────

func (h *Handler) handleGitLabPipelineEvent(ctx context.Context, conn db.GithubInstallation, body []byte) error {
	// 1. Tolerant parse — nil project / object_attributes is a format error.
	var p glPipelinePayload
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("gitlab: pipeline payload parse failed", "err", err)
		return nil
	}
	if p.Project == nil || p.ObjectAttributes == nil {
		slog.Warn("gitlab: pipeline payload missing project or object_attributes")
		return nil
	}
	oa := p.ObjectAttributes

	// 2. Split repo_owner / repo_name on the LAST "/".
	repoOwner, repoName := splitPathWithNamespace(p.Project.PathWithNamespace)

	// 3. Map GitLab pipeline status → canonical status/conclusion.
	status, conclusion := derivePipelineStatusConclusion(oa.Status)

	// 4. Compute updated_at: finished_at takes priority, fall back to
	//    created_at, then event receipt time.
	pipelineUpdatedAt := parseGLTime(oa.FinishedAt)
	if !pipelineUpdatedAt.Valid {
		pipelineUpdatedAt = parseGLTime(oa.CreatedAt)
	}
	if !pipelineUpdatedAt.Valid {
		pipelineUpdatedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}

	wsID := conn.WorkspaceID

	// 5. Attribution (three tiers):
	//    Tier 1: merge_request.iid → lookup by (workspace, owner, repo, iid).
	//    Tier 2: head sha → find latest open MR with matching sha.
	//    Tier 3: neither → stash as pending pipeline.
	var pr db.GithubPullRequest
	var found bool

	if p.MergeRequest != nil && p.MergeRequest.IID > 0 {
		mr, err := h.Queries.GetGitLabMergeRequest(ctx, db.GetGitLabMergeRequestParams{
			WorkspaceID: wsID,
			RepoOwner:   repoOwner,
			RepoName:    repoName,
			PrNumber:    int32(p.MergeRequest.IID),
		})
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("gitlab: lookup mr by iid failed",
					"err", err, "iid", p.MergeRequest.IID)
			}
		} else {
			pr = mr
			found = true
		}
	}

	if !found && oa.Sha != "" {
		mr, err := h.Queries.GetLatestOpenGitLabMRByHeadSha(ctx, db.GetLatestOpenGitLabMRByHeadShaParams{
			WorkspaceID: wsID,
			RepoOwner:   repoOwner,
			RepoName:    repoName,
			HeadSha:     oa.Sha,
		})
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("gitlab: lookup mr by head sha failed",
					"err", err, "sha", oa.Sha)
			}
		} else {
			pr = mr
			found = true
		}
	}

	if !found {
		// Stash: the MR row has not been mirrored yet. Store the pipeline
		// event pending the eventual MR upsert (see drain in gitlab_mr.go).
		iid := int32(0)
		if p.MergeRequest != nil {
			iid = int32(p.MergeRequest.IID)
		}
		if err := h.Queries.UpsertPendingGitLabPipeline(ctx, db.UpsertPendingGitLabPipelineParams{
			WorkspaceID:    wsID,
			RepoOwner:      repoOwner,
			RepoName:       repoName,
			PrNumber:       iid,
			SuiteID:        oa.ID,
			HeadSha:        oa.Sha,
			AppID:          -1,
			Status:         status,
			SuiteUpdatedAt: pipelineUpdatedAt,
			Conclusion:     conclusion,
		}); err != nil {
			slog.Warn("gitlab: stash pending pipeline failed",
				"err", err, "pipeline_id", oa.ID)
		}
		return nil
	}

	// 6. Upsert the pipeline (check-suite equivalent).
	//
	// Design note — app_id = -1:
	//   GitLab projects have a single CI pipeline system. There is no
	//   GitHub-style "multiple check-suite App" concept. Setting app_id
	//   to -1 (a sentinel value) causes the aggregation's DISTINCT ON
	//   (pr_id, app_id) to collapse all pipelines for this MR into
	//   "latest one" — which is exactly the desired semantic: re-running
	//   the pipeline on the same sha produces a new pipeline id and a
	//   new check_suite row (because suite_id = pipeline id serves as the
	//   ON CONFLICT key). If we were to use the pipeline id as app_id,
	//   each retry would create a separate aggregate row, accumulating
	//   stale failures together with the current success.
	if err := h.Queries.UpsertGitLabPipeline(ctx, db.UpsertGitLabPipelineParams{
		PrID:       pr.ID,
		SuiteID:    oa.ID,
		HeadSha:    oa.Sha,
		AppID:      -1,
		Status:     status,
		Conclusion: conclusion,
		UpdatedAt:  pipelineUpdatedAt,
	}); err != nil {
		slog.Warn("gitlab: upsert pipeline failed",
			"err", err, "pipeline_id", oa.ID, "mr_id", uuidToString(pr.ID))
		return err
	}

	// 7. Broadcast pull_request:updated to linked issues (mirrors GitHub
	//    check_suite broadcast shape).
	affectedIssues := map[string]struct{}{}
	issues, err := h.Queries.ListIssueIDsForPullRequest(ctx, pr.ID)
	if err == nil {
		for _, id := range issues {
			affectedIssues[uuidToString(id)] = struct{}{}
		}
	}
	linked := make([]string, 0, len(affectedIssues))
	for id := range affectedIssues {
		linked = append(linked, id)
	}
	resp := gitLabMRToResponse(pr)
	h.publish(protocol.EventPullRequestUpdated, uuidToString(wsID), "system", "", map[string]any{
		"pull_request":     resp,
		"linked_issue_ids": linked,
	})

	return nil
}

// ── Status mapping ───────────────────────────────────────────────────────────

// derivePipelineStatusConclusion maps GitLab pipeline status strings to the
// canonical (status, conclusion) pair used by the check_suite aggregation.
//
// Vocabulary alignment with github.sql aggregation CTE:
//   failed class  = ('failure','cancelled','timed_out','action_required','startup_failure','stale')
//   passed class  = ('success','neutral','skipped')
//   pending       = status != 'completed' OR conclusion IS NULL
func derivePipelineStatusConclusion(glStatus string) (status string, conclusion pgtype.Text) {
	switch strings.ToLower(glStatus) {
	case "created", "waiting_for_resource", "preparing", "pending", "scheduled", "manual":
		return "queued", pgtype.Text{}
	case "running":
		return "in_progress", pgtype.Text{}
	case "success":
		return "completed", strToText("success")
	case "failed":
		return "completed", strToText("failure")
	case "canceled":
		return "completed", strToText("cancelled")
	case "skipped":
		return "completed", strToText("skipped")
	default:
		return "queued", pgtype.Text{}
	}
}
