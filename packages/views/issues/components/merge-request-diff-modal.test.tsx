import { describe, expect, it, vi, beforeEach } from "vitest";
import { screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";
import { ApiError } from "@multica/core/api";
import type { MergeRequestDiffsResponse } from "@multica/core/types";

const mockState = {
  data: undefined as MergeRequestDiffsResponse | undefined,
  isLoading: false,
  error: null as Error | null,
  refetch: vi.fn(),
};

vi.mock("@multica/core/github/queries", () => ({
  useMergeRequestDiffs: (_issueId: string, _prId: string, _enabled: boolean) => mockState,
}));

vi.mock("@multica/ui/components/ui/dialog", () => ({
  Dialog: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  DialogContent: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  DialogHeader: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: React.ReactNode }) => <h2>{children}</h2>,
  DialogDescription: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

vi.mock("@multica/ui/components/ui/diff-viewer/diff-file-list", () => ({
  DiffFileList: ({ files }: { files: Array<{ newPath: string }> }) => (
    <div data-testid="diff-file-list">
      {files.map((file, index) => (
        <span key={index} data-testid="diff-file-name">{file.newPath}</span>
      ))}
    </div>
  ),
}));

vi.mock("@multica/ui/components/ui/spinner", () => ({
  Spinner: () => <span data-testid="spinner" />,
}));

vi.mock("@multica/ui/components/ui/skeleton", () => ({
  Skeleton: ({ className }: { className?: string }) => <div data-testid="skeleton" className={className} />,
}));

vi.mock("@multica/ui/components/ui/button", () => ({
  Button: ({ children, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button {...props}>{children}</button>
  ),
}));

import { MergeRequestDiffModal } from "./merge-request-diff-modal";

function renderModal() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <MergeRequestDiffModal
        onClose={vi.fn()}
        data={{ issueId: "issue-1", prId: "pr-1", title: "Add feature" }}
      />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mockState.data = undefined;
  mockState.isLoading = false;
  mockState.error = null;
  mockState.refetch.mockClear();
});

describe("MergeRequestDiffModal", () => {
  it("renders both filenames when the diff has two files", () => {
    mockState.data = {
      files: [
        {
          oldPath: "src/old.ts",
          newPath: "src/new.ts",
          status: "modified",
          patch: "@@ -1,1 +1,1 @@\n-old\n+new",
          isGenerated: false,
          collapsed: false,
          tooLarge: false,
        },
        {
          oldPath: "README.md",
          newPath: "README.md",
          status: "modified",
          patch: "@@ -1,1 +1,1 @@\n-old\n+new",
          isGenerated: false,
          collapsed: false,
          tooLarge: false,
        },
      ],
    };
    renderModal();
    expect(screen.getByTestId("diff-file-list")).toBeInTheDocument();
    const names = screen.getAllByTestId("diff-file-name").map((el) => el.textContent);
    expect(names).toEqual(["src/new.ts", "README.md"]);
  });

  it("renders the reconnect message for a 409 error", () => {
    mockState.error = new ApiError(
      "mr_connection_unresolved",
      409,
      "Conflict",
      { error: "mr_connection_unresolved" },
    );
    renderModal();
    expect(
      screen.getByText(
        "This merge request predates connection tracking — reconnect or wait for the next webhook sync.",
      ),
    ).toBeInTheDocument();
  });
});
