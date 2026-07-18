import type { ReactNode } from "react";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

const mockUpdateWorkspace = vi.hoisted(() => vi.fn());
const mockDeleteConnection = vi.hoisted(() => vi.fn());
const mockCreateConnection = vi.hoisted(() => vi.fn());
const mockListConnections = vi.hoisted(() => vi.fn());
const mockAddHook = vi.hoisted(() => vi.fn());
const mockRemoveHook = vi.hoisted(() => vi.fn());
const mockInvalidate = vi.hoisted(() => vi.fn());
const mockSetQueryData = vi.hoisted(() => vi.fn());
const mockToastSuccess = vi.hoisted(() => vi.fn());

const workspaceRef = vi.hoisted(() => ({
  current: {
    id: "workspace-1",
    name: "Acme",
    slug: "acme",
    settings: {} as Record<string, unknown>,
    repos: [{ url: "https://gitlab.com/acme/api" }] as { url: string }[],
  },
}));
type MemberRole = "owner" | "admin" | "member" | "guest";
const membersRef = vi.hoisted(() => ({
  current: [{ user_id: "user-1", role: "owner" as MemberRole }],
}));
const connectionsRef = vi.hoisted(() => ({
  current: {
    connections: [] as {
      id: string;
      instance_url: string;
      display_name: string;
      account_login: string;
      hooks: { target_type: "project" | "group"; target_path: string; hook_id: number; url: string; created_at: string; last_error: string | null }[];
      created_at: string;
    }[],
    configured: true,
    can_manage: true as boolean,
  },
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { queryKey: unknown[]; queryFn?: () => unknown }) => {
    const key = JSON.stringify(opts.queryKey);
    if (key.includes("members")) return { data: membersRef.current };
    if (key.includes("gitlab") && key.includes("connections")) return { data: connectionsRef.current };
    return { data: undefined };
  },
  useQueryClient: () => ({
    setQueryData: mockSetQueryData,
    invalidateQueries: mockInvalidate,
  }),
  queryOptions: <T,>(opts: T) => opts,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => workspaceRef.current,
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: vi.fn() }),
  workspaceKeys: { list: () => ["workspaces"] },
}));

vi.mock("@multica/core/gitlab", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/gitlab")>("@multica/core/gitlab");
  return {
    ...actual,
  };
});

vi.mock("@multica/core/api", () => ({
  api: {
    updateWorkspace: mockUpdateWorkspace,
    deleteGitLabConnection: mockDeleteConnection,
    createGitLabConnection: mockCreateConnection,
    listGitLabConnections: mockListConnections,
    addGitLabHookTarget: mockAddHook,
    removeGitLabHookTarget: mockRemoveHook,
  },
}));

vi.mock("@multica/core/auth", () => {
  const useAuthStore = Object.assign(
    (sel?: (s: { user: { id: string } }) => unknown) =>
      sel ? sel({ user: { id: "user-1" } }) : { user: { id: "user-1" } },
    { getState: () => ({ user: { id: "user-1" } }) },
  );
  return { useAuthStore };
});

vi.mock("../../navigation", () => ({
  useNavigation: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/settings",
    searchParams: new URLSearchParams("tab=gitlab"),
    getShareableUrl: (p: string) => `https://app.example${p}`,
  }),
}));

vi.mock("sonner", () => ({
  toast: { success: mockToastSuccess, error: vi.fn() },
}));

import { GitLabTab } from "./gitlab-tab";

const TEST_RESOURCES = {
  en: { common: enCommon, settings: enSettings },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function resetFixtures() {
  vi.clearAllMocks();
  workspaceRef.current = {
    id: "workspace-1",
    name: "Acme",
    slug: "acme",
    settings: {},
    repos: [{ url: "https://gitlab.com/acme/api" }],
  };
  membersRef.current = [{ user_id: "user-1", role: "owner" }];
  connectionsRef.current = { connections: [], configured: true, can_manage: true };
}

describe("GitLabTab", () => {
  beforeEach(resetFixtures);

  it("renders the master switch and page description", () => {
    render(<GitLabTab />, { wrapper: I18nWrapper });
    expect(screen.getByRole("switch", { name: /enable gitlab features/i })).toBeTruthy();
  });

  it("disables feature switches when the master switch is off", () => {
    workspaceRef.current.settings = { gitlab_enabled: false };
    render(<GitLabTab />, { wrapper: I18nWrapper });

    const master = screen.getByRole("switch", { name: /enable gitlab features/i });
    expect(master.getAttribute("aria-checked")).toBe("false");

    const switches = screen.getAllByRole("switch");
    const features = switches.slice(1);
    expect(features.length).toBeGreaterThan(0);
    for (const sw of features) {
      const ariaDisabled = sw.getAttribute("aria-disabled");
      const disabled = sw.hasAttribute("disabled");
      expect(ariaDisabled === "true" || disabled).toBe(true);
    }
  });

  it("flipping the master switch persists gitlab_enabled=false and merges existing settings", async () => {
    const user = userEvent.setup();
    workspaceRef.current.settings = { gitlab_mr_sidebar_enabled: true };
    mockUpdateWorkspace.mockResolvedValue({
      ...workspaceRef.current,
      settings: { gitlab_mr_sidebar_enabled: true, gitlab_enabled: false },
    });

    render(<GitLabTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("switch", { name: /enable gitlab features/i }));

    await waitFor(() => {
      expect(mockUpdateWorkspace).toHaveBeenCalledWith("workspace-1", {
        settings: { gitlab_mr_sidebar_enabled: true, gitlab_enabled: false },
      });
      expect(mockToastSuccess).toHaveBeenCalledWith("Changes saved", {
        id: "settings-auto-save",
      });
    });
  });

  it("shows Connect button when no connection exists and user can manage", () => {
    render(<GitLabTab />, { wrapper: I18nWrapper });
    expect(screen.getByRole("button", { name: /^Connect$/ })).toBeTruthy();
  });

  it("shows connection form when Connect is clicked and submits with correct body", async () => {
    const user = userEvent.setup();
    mockCreateConnection.mockResolvedValue({
      id: "conn-1",
      instance_url: "https://gitlab.example.com",
      display_name: "My GitLab",
      account_login: "root",
      hooks: [],
      created_at: "2025-01-01T00:00:00Z",
    });

    render(<GitLabTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: /^Connect$/ }));

    // Form should now be visible
    expect(screen.getByLabelText(/instance url/i)).toBeTruthy();
    expect(screen.getByLabelText(/personal access token/i)).toBeTruthy();

    // Fill and submit
    await user.type(screen.getByLabelText(/instance url/i), "https://gitlab.example.com");
    await user.type(screen.getByLabelText(/personal access token/i), "glpat-test-token");
    await user.type(screen.getByLabelText(/display name/i), "My GitLab");

    // There are now two "Connect" buttons (the initial one + the form submit).
    // Click the last one (the form submit, which is rendered later in the DOM).
    const connectButtons = screen.getAllByRole("button", { name: /^Connect$/ });
    await user.click(connectButtons[connectButtons.length - 1]!);

    await waitFor(() => {
      expect(mockCreateConnection).toHaveBeenCalledWith("workspace-1", {
        instance_url: "https://gitlab.example.com",
        access_token: "glpat-test-token",
        display_name: "My GitLab",
      });
    });

    // Token field should be cleared after submit
    await waitFor(() => {
      const tokenInput = screen.queryByLabelText(/personal access token/i) as HTMLInputElement | null;
      expect(tokenInput?.value ?? "").toBe("");
    });
  });

  it("disables the Connect button when configured is false", () => {
    connectionsRef.current = { connections: [], configured: false, can_manage: true };
    render(<GitLabTab />, { wrapper: I18nWrapper });
    const btn = screen.getByRole("button", { name: /^Connect$/ });
    expect(btn.hasAttribute("disabled")).toBe(true);
  });

  it("shows not-configured hint when configured is false", () => {
    connectionsRef.current = { connections: [], configured: false, can_manage: true };
    render(<GitLabTab />, { wrapper: I18nWrapper });
    expect(screen.getByText(/MULTICA_GITLAB_SECRET_KEY/)).toBeTruthy();
  });

  it("shows connected instance and Disconnect button", () => {
    connectionsRef.current = {
      configured: true,
      can_manage: true,
      connections: [
        {
          id: "conn-1",
          instance_url: "https://gitlab.example.com",
          display_name: "My GitLab",
          account_login: "root",
          hooks: [],
          created_at: "2025-01-01T00:00:00Z",
        },
      ],
    };
    render(<GitLabTab />, { wrapper: I18nWrapper });
    expect(screen.getByText(/My GitLab/)).toBeTruthy();
    expect(screen.getByText(/root/)).toBeTruthy();
  });

  it("clicking Disconnect opens confirmation and fires delete on confirm", async () => {
    const user = userEvent.setup();
    connectionsRef.current = {
      configured: true,
      can_manage: true,
      connections: [
        {
          id: "conn-1",
          instance_url: "https://gitlab.example.com",
          display_name: "My GitLab",
          account_login: "root",
          hooks: [],
          created_at: "2025-01-01T00:00:00Z",
        },
      ],
    };
    mockDeleteConnection.mockResolvedValue(undefined);

    render(<GitLabTab />, { wrapper: I18nWrapper });

    await user.click(screen.getByRole("button", { name: /^Disconnect$/ }));
    expect(screen.getByText(/Multica will stop receiving webhooks/i)).toBeTruthy();
    expect(mockDeleteConnection).not.toHaveBeenCalled();

    const dialogConfirm = screen
      .getAllByRole("button", { name: /^Disconnect$/ })
      .find((b) => b.getAttribute("data-slot")?.includes("alert-dialog"));
    await user.click(dialogConfirm ?? screen.getAllByRole("button", { name: /^Disconnect$/ })[1]!);

    await waitFor(() => {
      expect(mockDeleteConnection).toHaveBeenCalledWith("workspace-1", "conn-1");
    });
  });

  it("non-admin sees existing connection but no management controls", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    connectionsRef.current = {
      configured: true,
      can_manage: false,
      connections: [
        {
          id: "conn-1",
          instance_url: "https://gitlab.example.com",
          display_name: "My GitLab",
          account_login: "root",
          hooks: [],
          created_at: "2025-01-01T00:00:00Z",
        },
      ],
    };
    render(<GitLabTab />, { wrapper: I18nWrapper });

    expect(screen.getByText(/root/)).toBeTruthy();
    expect(screen.getByText(/Read-only view/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Connect$/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Disconnect$/ })).toBeNull();
  });

  it("non-admin with no connection sees contact-admin hint", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    connectionsRef.current = {
      configured: true,
      can_manage: false,
      connections: [],
    };
    render(<GitLabTab />, { wrapper: I18nWrapper });

    expect(screen.getByText(/Ask an admin or owner/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Connect$/ })).toBeNull();
  });
});
