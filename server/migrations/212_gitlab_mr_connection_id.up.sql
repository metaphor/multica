-- GitLab MR → connection resolution: add connection_id to github_pull_request
-- so a mirrored GitLab merge request row can be traced back to the
-- github_installation (GitLab connection) that produced it. No foreign key
-- and no index — relationships are enforced in the application layer.
--
-- Backfill: only GitLab rows are backfilled, and only when the workspace has
-- exactly ONE GitLab connection — in that case the attribution is unambiguous.
-- Workspaces with zero or multiple GitLab connections keep NULL (unknown /
-- ambiguous), matching the endpoint's resolution semantics.

ALTER TABLE github_pull_request
    ADD COLUMN connection_id UUID;

UPDATE github_pull_request gpr
SET connection_id = gi.id
FROM github_installation gi
WHERE gpr.provider = 'gitlab'
  AND gi.provider = 'gitlab'
  AND gi.workspace_id = gpr.workspace_id
  AND (SELECT count(*) FROM github_installation x
       WHERE x.workspace_id = gpr.workspace_id
         AND x.provider = 'gitlab') = 1;
