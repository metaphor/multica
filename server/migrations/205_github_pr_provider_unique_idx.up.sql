-- Provider-aware unique index on github_pull_request. Ensures
-- (workspace_id, provider, repo_owner, repo_name, pr_number) uniqueness
-- across both GitHub and GitLab PRs. This replaces the old 4-column constraint
-- dropped in migration 206.
-- Single statement: PostgreSQL rejects CREATE INDEX CONCURRENTLY inside a
-- transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS github_pull_request_ws_provider_repo_pr
    ON github_pull_request(workspace_id, provider, repo_owner, repo_name, pr_number);
