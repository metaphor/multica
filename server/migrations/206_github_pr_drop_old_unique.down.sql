-- Restore the 4-column unique constraint on github_pull_request that
-- migration 206 dropped. This recreates the constraint originally installed
-- by migration 079.
ALTER TABLE github_pull_request
    ADD CONSTRAINT github_pull_request_workspace_id_repo_owner_repo_name_pr_nu_key
    UNIQUE (workspace_id, repo_owner, repo_name, pr_number);
