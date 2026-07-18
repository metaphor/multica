-- Drop the (workspace_id, installation_id) unique constraint on
-- github_installation that was installed by migration 133. Replaced by the
-- provider-scoped partial unique index in migration 208.
--
-- Constraint name verified from server/migrations/133_github_installation_multi_workspace.up.sql
ALTER TABLE github_installation
    DROP CONSTRAINT github_installation_workspace_id_installation_id_key;
