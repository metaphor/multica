-- Rebuild github_pending_check_suite primary key to include provider,
-- so the same (repo_owner, repo_name, pr_number) tuple can exist under
-- both 'github' and 'gitlab' providers without collision.
--
-- AccessExclusive lock is acceptable on this table: it is a transient stash
-- written by webhook handlers while waiting for the corresponding PR row,
-- and row counts are typically zero or small. The lock duration is negligible.
ALTER TABLE github_pending_check_suite
    DROP CONSTRAINT github_pending_check_suite_pkey;

ALTER TABLE github_pending_check_suite
    ADD PRIMARY KEY (workspace_id, provider, repo_owner, repo_name, pr_number, suite_id);
