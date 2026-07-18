-- Revert provider columns and CHECK constraints added in migration 202.

ALTER TABLE github_installation
    DROP CONSTRAINT IF EXISTS chk_github_installation_provider,
    DROP COLUMN IF EXISTS hooks,
    DROP COLUMN IF EXISTS webhook_secret_ciphertext,
    DROP COLUMN IF EXISTS webhook_secret_hash,
    DROP COLUMN IF EXISTS access_token_ciphertext,
    DROP COLUMN IF EXISTS display_name,
    DROP COLUMN IF EXISTS instance_url,
    DROP COLUMN IF EXISTS provider;

ALTER TABLE github_pull_request
    DROP CONSTRAINT IF EXISTS chk_github_pull_request_provider,
    DROP COLUMN IF EXISTS provider;

ALTER TABLE github_pending_check_suite
    DROP CONSTRAINT IF EXISTS chk_github_pending_check_suite_provider,
    DROP COLUMN IF EXISTS provider;
