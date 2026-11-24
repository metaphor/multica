import { infiniteQueryOptions, queryOptions, useQuery } from "@tanstack/react-query";
import { api } from "../api";
import type { IssuePullRequestSummary } from "../types";

export const githubKeys = {
  all: (wsId: string) => ["github", wsId] as const,
  installations: (wsId: string) => [...githubKeys.all(wsId), "installations"] as const,
  repositories: (wsId: string, installationId: string) =>
    [...githubKeys.all(wsId), "installations", installationId, "repositories"] as const,
  pullRequests: (issueId: string) => ["github", "pull-requests", issueId] as const,
  pullRequestSummaries: (wsId: string) => ["github", "pull-requests", "summaries", wsId] as const,
  mergeRequestDiffs: (issueId: string, prId: string) =>
    ["merge-request-diffs", issueId, prId] as const,
};

export const githubInstallationsOptions = (wsId: string) =>
  queryOptions({
    queryKey: githubKeys.installations(wsId),
    queryFn: () => api.listGitHubInstallations(wsId),
    enabled: !!wsId,
  });

export const githubInstallationRepositoriesOptions = (
  wsId: string,
  installationId: string,
) =>
  infiniteQueryOptions({
    queryKey: githubKeys.repositories(wsId, installationId),
    queryFn: ({ pageParam }) =>
      api.listGitHubInstallationRepositories(wsId, installationId, {
        page: pageParam,
        per_page: 100,
      }),
    initialPageParam: 1,
    getNextPageParam: (lastPage) => lastPage.next_page ?? undefined,
    enabled: !!wsId && !!installationId,
  });

export type IssuePullRequestSummariesByIssue = Readonly<
  Record<string, readonly IssuePullRequestSummary[]>
>;

/**
 * Every linked PR in the workspace, grouped by issue id (newest first). One
 * workspace-wide read backs the PR chips on board cards, list rows and table
 * cells; enable it only while one of those surfaces shows PRs.
 */
export const issuePullRequestSummariesOptions = (wsId: string) =>
  queryOptions({
    queryKey: githubKeys.pullRequestSummaries(wsId),
    queryFn: async (): Promise<IssuePullRequestSummariesByIssue> => {
      const { pull_requests } = await api.listIssuePullRequestSummaries();
      const byIssue: Record<string, IssuePullRequestSummary[]> = {};
      for (const pr of pull_requests) {
        (byIssue[pr.issue_id] ??= []).push(pr);
      }
      return byIssue;
    },
    enabled: !!wsId,
  });

export const issuePullRequestsOptions = (issueId: string) =>
  queryOptions({
    queryKey: githubKeys.pullRequests(issueId),
    queryFn: () => api.listIssuePullRequests(issueId),
    enabled: !!issueId,
  });

// Full per-file diff payload for one linked MR/PR. Heavy response, so it is
// fetched on demand only — callers pass `enabled` tied to the diff modal's
// open state. Like the other github queries it inherits the default
// staleTime, so reopening the modal reuses the cache.
export function useMergeRequestDiffs(issueId: string, prId: string, enabled: boolean) {
  return useQuery({
    queryKey: githubKeys.mergeRequestDiffs(issueId, prId),
    queryFn: () => api.getMergeRequestDiffs(issueId, prId),
    enabled: enabled && !!issueId && !!prId,
  });
}
