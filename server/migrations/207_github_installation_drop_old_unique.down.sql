-- Restore the (workspace_id, installation_id) unique constraint as
-- originally created by migration 133.
ALTER TABLE github_installation
    ADD CONSTRAINT github_installation_workspace_id_installation_id_key
    UNIQUE (workspace_id, installation_id);
