-- Partial unique index for GitHub installations: enforces
-- (workspace_id, installation_id) uniqueness only for GitHub rows.
-- Replaces the full-table UNIQUE constraint dropped in migration 207.
-- GitLab rows (installation_id = 0 sentinel) are excluded from this index.
-- Single statement: PostgreSQL rejects CREATE INDEX CONCURRENTLY inside a
-- transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS github_installation_ws_installation_github
    ON github_installation(workspace_id, installation_id)
    WHERE provider = 'github';
