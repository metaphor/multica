-- Restore the original 5-column primary key (without provider) on
-- github_pending_check_suite, as created by migration 096.
ALTER TABLE github_pending_check_suite
    DROP CONSTRAINT github_pending_check_suite_pkey;

ALTER TABLE github_pending_check_suite
    ADD PRIMARY KEY (workspace_id, repo_owner, repo_name, pr_number, suite_id);
