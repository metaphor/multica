-- Partial unique index on webhook_secret_hash for GitLab installations.
-- Ensures no two GitLab connections share the same webhook secret hash.
-- Single statement: PostgreSQL rejects CREATE INDEX CONCURRENTLY inside a
-- transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS github_installation_gitlab_secret_hash
    ON github_installation(webhook_secret_hash)
    WHERE provider = 'gitlab' AND webhook_secret_hash IS NOT NULL;
