import { infiniteQueryOptions, queryOptions, useQuery } from "@tanstack/react-query";
import { api } from "../api";

export const githubKeys = {
  all: (wsId: string) => ["github", wsId] as const,
  installations: (wsId: string) => [...githubKeys.all(wsId), "installations"] as const,
  repositories: (wsId: string, installationId: string) =>
    [...githubKeys.all(wsId), "installations", installationId, "repositories"] as const,
  pullRequests: (issueId: string) => ["github", "pull-requests", issueId] as const,
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
