-- Validate the provider CHECK constraints that were created NOT VALID in
-- migration 202. Since every existing row defaults to 'github', validation
-- completes without a table scan.

ALTER TABLE github_installation
    VALIDATE CONSTRAINT chk_github_installation_provider;

ALTER TABLE github_pull_request
    VALIDATE CONSTRAINT chk_github_pull_request_provider;

ALTER TABLE github_pending_check_suite
    VALIDATE CONSTRAINT chk_github_pending_check_suite_provider;
