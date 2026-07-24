-- Revert connection_id column added in migration 212.

ALTER TABLE github_pull_request
    DROP COLUMN IF EXISTS connection_id;
