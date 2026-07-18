import type { Workspace } from "../types";

export interface GitLabSettings {
  /** Master switch. When false, every UI affordance and side-effect is gated off. */
  enabled: boolean;
  /** Issue-detail MR sidebar visibility. Implies `enabled`. */
  mrSidebar: boolean;
  /** Auto-link issues ↔ MRs from webhook payloads. Implies `enabled`. */
  autoLinkMRs: boolean;
}

/**
 * Pure derivation from a workspace's settings JSONB. Defaults every flag to
 * true so workspaces predating the GitLab integration keep the historical
 * "all on" behavior. Mirrors deriveGitHubSettings; the shared
 * `co_authored_by_enabled` key intentionally stays in the GitHub module.
 */
export function deriveGitLabSettings(
  workspace: Pick<Workspace, "settings"> | null | undefined,
): GitLabSettings {
  const s = (workspace?.settings ?? {}) as Record<string, unknown>;
  const enabled = s.gitlab_enabled !== false;
  return {
    enabled,
    mrSidebar: enabled && s.gitlab_mr_sidebar_enabled !== false,
    autoLinkMRs: enabled && s.gitlab_auto_link_mrs_enabled !== false,
  };
}
