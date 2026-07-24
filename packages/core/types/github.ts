export type GitHubPullRequestState = "open" | "closed" | "merged" | "draft";

/** Aggregated CI status for a PR's current head SHA, computed server-side from
 * the latest check_suite per app. `null` when no completed suite has been seen
 * yet (e.g. PR just opened, or repository has no CI configured). */
export type GitHubPullRequestChecksConclusion = "passed" | "failed" | "pending";

/** Raw mirror of GitHub's `mergeable_state`. The UI only surfaces `clean` and
 * `dirty`; the other values (`blocked`, `behind`, `unstable`, `unknown`,
 * `has_hooks`, `draft`) round-trip but render as unknown to avoid asserting
 * "conflicts" for blocking reasons that aren't actual conflicts. */
export type GitHubMergeableState = string;

export interface GitHubInstallation {
  id: string;
  workspace_id: string;
  /** GitHub's numeric installation id — the management handle used by the
   * connect / disconnect flows. Omitted when the caller cannot manage
   * integrations (see `ListGitHubInstallationsResponse.can_manage`). */
  installation_id?: number;
  account_login: string;
  account_type: "User" | "Organization";
  account_avatar_url: string | null;
  created_at: string;
  /** Display name of the workspace member who connected this installation.
   * Optional because older backends and minimum-visibility deployments may
   * omit it; the UI renders the "connected by" line only when present. */
  connected_by?: string;
}

export interface GitHubPullRequest {
  id: string;
  workspace_id: string;
  /** Which forge produced this row. Present on every backend response since
   * the GitLab integration landed (both GitHub PRs and GitLab MRs share this
   * shape and this table). */
  provider: "github" | "gitlab";
  repo_owner: string;
  repo_name: string;
  number: number;
  title: string;
  state: GitHubPullRequestState;
  html_url: string;
  branch: string | null;
  author_login: string | null;
  author_avatar_url: string | null;
  merged_at: string | null;
  closed_at: string | null;
  pr_created_at: string;
  pr_updated_at: string;
  /** Optional; older backends omit this field. */
  mergeable_state?: GitHubMergeableState | null;
  /** Optional; older backends omit this field. */
  checks_conclusion?: GitHubPullRequestChecksConclusion | null;
  /** Per-suite counts that feed the segmented progress bar. Older backends
   * omit these; treat absence as 0 (the card renders only when sum > 0). */
  checks_passed?: number;
  checks_failed?: number;
  checks_pending?: number;
  /** Diff stats from GitHub's `pull_request` payload. Older backends omit
   * these fields; we treat 0/0/0 as "unknown" and hide the stats row. */
  additions?: number;
  deletions?: number;
  changed_files?: number;
}

export interface ListGitHubInstallationsResponse {
  installations: GitHubInstallation[];
  /** Whether the deployment has GitHub App credentials configured. When false, the Connect button is hidden / disabled. */
  configured: boolean;
  /** Whether the caller can connect / disconnect installations. Non-admin
   * members get `false` along with installations that omit `installation_id`.
   * Older backends predating MUL-2413 omit the field; treat absence as
   * `false` for read-only safety. */
  can_manage?: boolean;
}

export interface GitHubConnectResponse {
  /** The GitHub App install URL the browser should open. Empty when `configured` is false. */
  url?: string;
  configured: boolean;
}

/** One registered webhook target on a GitLab connection, mirrored from the
 * connection's `hooks` JSONB column (server `hookRecord`). */
export interface GitLabHookTarget {
  target_type: "project" | "group";
  /** Full GitLab path of the project or group (e.g. "acme/backend"). */
  target_path: string;
  hook_id: number;
  url: string;
  created_at: string;
  last_error: string | null;
}

export interface GitLabConnection {
  id: string;
  instance_url: string;
  display_name: string;
  account_login: string;
  hooks: GitLabHookTarget[];
  created_at: string;
}

export interface GitLabConnectionListResponse {
  connections: GitLabConnection[];
  /** Whether the deployment has GitLab credentials configured
   * (MULTICA_GITLAB_SECRET_KEY set). When false, the Connect button is
   * hidden / disabled. */
  configured: boolean;
  /** Whether the caller can connect / disconnect / manage connections.
   * Non-admin members get `false`. Older backends predating the GitLab
   * integration omit the field; treat absence as `false` for read-only
   * safety. */
  can_manage?: boolean;
}

/** One file's diff inside a merge request, mirroring the server's
 * `ChangedFile` JSON from `GET /api/issues/{id}/pull-requests/{prId}/diffs`.
 * `patch` is the unified-diff hunk text (empty for binary or too-large
 * files); the rendering layer decides how to display `collapsed` /
 * `tooLarge` / `isGenerated` files. */
export interface MergeRequestDiffFile {
  oldPath: string;
  newPath: string;
  status: "added" | "modified" | "deleted" | "renamed";
  patch: string;
  isGenerated: boolean;
  collapsed: boolean;
  tooLarge: boolean;
}

export interface MergeRequestDiffsResponse {
  files: MergeRequestDiffFile[];
}
