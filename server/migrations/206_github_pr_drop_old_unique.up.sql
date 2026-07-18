-- Drop the original 4-column unique constraint on github_pull_request,
-- replaced by the provider-aware unique index installed in migration 205.
-- The old constraint was created in migration 079 as:
--   UNIQUE (workspace_id, repo_owner, repo_name, pr_number)
ALTER TABLE github_pull_request
    DROP CONSTRAINT github_pull_request_workspace_id_repo_owner_repo_name_pr_nu_key;
