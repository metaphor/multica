/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { ApiClient } from "../api/client";
import { setApiInstance } from "../api";
import { githubKeys, useMergeRequestDiffs } from "./queries";

function createWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

describe("useMergeRequestDiffs", () => {
  let qc: QueryClient;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    setApiInstance(new ApiClient("https://api.example.test"));
  });

  afterEach(() => {
    qc.clear();
    vi.unstubAllGlobals();
  });

  it("uses the pinned query key and fetches the diffs endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ files: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    expect(githubKeys.mergeRequestDiffs("issue-1", "pr-1")).toEqual([
      "merge-request-diffs",
      "issue-1",
      "pr-1",
    ]);

    const { result } = renderHook(
      () => useMergeRequestDiffs("issue-1", "pr-1", true),
      { wrapper: createWrapper(qc) },
    );

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://api.example.test/api/issues/issue-1/pull-requests/pr-1/diffs",
    );
    expect(result.current.data).toEqual({ files: [] });
  });

  it("does not fetch when enabled is false", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(
      () => useMergeRequestDiffs("issue-1", "pr-1", false),
      { wrapper: createWrapper(qc) },
    );

    // A disabled query never leaves idle; give any would-be fetch a chance to
    // fire before asserting zero calls.
    await new Promise((resolve) => setTimeout(resolve, 25));

    expect(result.current.fetchStatus).toBe("idle");
    expect(result.current.data).toBeUndefined();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
