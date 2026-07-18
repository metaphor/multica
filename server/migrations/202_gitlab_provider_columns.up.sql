-- GitLab provider: extend github_* tables with a provider discriminator so the
-- GitLab integration can reuse the same tables. Adds provider columns to every
-- table, GitLab-specific connection columns on github_installation, and CHECK
-- constraints that pave the way for per-provider unique indexes.
--
-- Existing rows default to 'github', so the ALTER completes instantly on
-- existing datasets. The CHECK constraints are created NOT VALID and will be
-- validated in migration 203.

-- github_installation: provider + GitLab connection columns
ALTER TABLE github_installation
    ADD COLUMN provider TEXT NOT NULL DEFAULT 'github',
    ADD COLUMN instance_url TEXT,
    ADD COLUMN display_name TEXT,
    ADD COLUMN access_token_ciphertext BYTEA,
    ADD COLUMN webhook_secret_hash BYTEA,
    ADD COLUMN webhook_secret_ciphertext BYTEA,
    ADD COLUMN hooks JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE github_installation
    ADD CONSTRAINT chk_github_installation_provider
        CHECK (provider IN ('github', 'gitlab')) NOT VALID;

-- github_pull_request: provider discriminator
ALTER TABLE github_pull_request
    ADD COLUMN provider TEXT NOT NULL DEFAULT 'github';

ALTER TABLE github_pull_request
    ADD CONSTRAINT chk_github_pull_request_provider
        CHECK (provider IN ('github', 'gitlab')) NOT VALID;

-- github_pending_check_suite: provider discriminator
ALTER TABLE github_pending_check_suite
    ADD COLUMN provider TEXT NOT NULL DEFAULT 'github';

ALTER TABLE github_pending_check_suite
    ADD CONSTRAINT chk_github_pending_check_suite_provider
        CHECK (provider IN ('github', 'gitlab')) NOT VALID;
