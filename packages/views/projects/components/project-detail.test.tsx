import type { Project, ProjectResource } from "@multica/core/types";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";
import { ProjectDetail } from "./project-detail";

const projectFixture = vi.hoisted(
  () =>
    ({
      id: "p-1",
      workspace_id: "ws-1",
      title: "Test Project",
      description: null,
      icon: null,
      status: "in_progress",
      priority: "none",
      lead_type: null,
      lead_id: null,
      start_date: null,
      due_date: null,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
      issue_count: 0,
      done_count: 0,
      resource_count: 0,
    }) as Project,
);

const projectResourcesFixture = vi.hoisted(() => ({ resources: [] as ProjectResource[] }));

const updateProjectSpy = vi.hoisted(() =>
  vi.fn().mockImplementation((_id: string, data: { settings?: Record<string, unknown> }) => {
    if (data.settings) {
      projectFixture.settings = { ...projectFixture.settings, ...data.settings };
    }
    return Promise.resolve({});
  }),
);

// Deletion flow: the component gates the delete action on the caller's role in
// the member list, so the member mock exposes a controllable role. The delete
// mutation must stay pending until the test resolves it, otherwise the real
// useDeleteProject's onSuccess fires synchronously and the navigation/timing
// assertions are meaningless.
const memberRole = vi.hoisted(() => ({ value: "admin" as string }));
const deleteProjectSpy = vi.hoisted(() => vi.fn());
const navigationPush = vi.hoisted(() => vi.fn());
const toastSuccess = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    getProject: vi.fn().mockResolvedValue(projectFixture),
    updateProject: updateProjectSpy,
    deleteProject: deleteProjectSpy,
    listProjectResources: vi.fn().mockImplementation(() => ({
      resources: projectResourcesFixture.resources,
      total: projectResourcesFixture.resources.length,
    })),
  },
  getApi: () => ({} as any),
  setApiInstance: vi.fn(),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign(
    (selector?: any) => (selector ? selector({ user: { id: "user-1" } }) : { user: { id: "user-1" } }),
    { getState: () => ({ user: { id: "user-1" } }) },
  ) as any,
  registerAuthStore: vi.fn(),
  createAuthStore: vi.fn(),
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({
    queryKey: ["workspaces", "ws-1", "members"],
    queryFn: () =>
      Promise.resolve([
        { user_id: "user-1", name: "User One", role: memberRole.value },
      ]),
  }),
  agentListOptions: () => ({
    queryKey: ["workspaces", "ws-1", "agents"],
    queryFn: () => Promise.resolve([]),
  }),
  squadListOptions: () => ({
    queryKey: ["workspaces", "ws-1", "squads"],
    queryFn: () => Promise.resolve([]),
  }),
  assigneeFrequencyOptions: () => ({
    queryKey: ["workspaces", "ws-1", "assignee-frequency"],
    queryFn: () => Promise.resolve([]),
  }),
  workspaceListOptions: () => ({
    queryKey: ["workspaces"],
    queryFn: () => Promise.resolve([]),
  }),
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: () => "Unknown",
    getMemberName: () => "Unknown",
    getAgentName: () => "Unknown",
  }),
}));

vi.mock("@multica/core/pins", () => ({
  pinListOptions: () => ({ queryKey: ["pins"], queryFn: () => Promise.resolve([]) }),
  useCreatePin: () => ({ mutate: vi.fn(), isPending: false }),
  useDeletePin: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ projects: () => "/projects" }),
  useCurrentWorkspace: () => ({ id: "ws-1", name: "Test Workspace", slug: "test" }),
}));

vi.mock("@multica/core/chat", () => ({
  useRecentContextStore: Object.assign(
    (selector?: (s: { recordVisit: typeof vi.fn }) => any) => {
      const state = { recordVisit: vi.fn() };
      return selector ? selector(state) : state;
    },
    { getState: () => ({ recordVisit: vi.fn() }) },
  ) as any,
}));

vi.mock("@multica/ui/hooks/use-mobile", () => ({
  useIsMobile: () => false,
}));

vi.mock("react-resizable-panels", () => ({
  useDefaultLayout: () => ({ defaultLayout: undefined, onLayoutChanged: vi.fn() }),
  usePanelRef: () => ({
    current: { isCollapsed: () => false, expand: vi.fn(), collapse: vi.fn() },
  }),
  Group: ({ children }: any) => <div>{children}</div>,
  Panel: ({ children }: any) => <div>{children}</div>,
  Separator: () => null,
}));

vi.mock("sonner", () => ({
  toast: { success: toastSuccess, error: vi.fn() },
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => ({ push: navigationPush, pathname: "/", getShareableUrl: (p: string) => p }),
  AppLink: ({ children, href }: any) => <a href={href}>{children}</a>,
  NavigationProvider: ({ children }: any) => children,
}));

vi.mock("../../editor", () => ({
  TitleEditor: ({ defaultValue, onBlur, placeholder }: any) => (
    <input
      data-testid="title-editor"
      defaultValue={defaultValue}
      onBlur={(e) => onBlur?.(e.target.value)}
      placeholder={placeholder}
    />
  ),
  ContentEditor: ({ value, onUpdate, placeholder }: any) => (
    <textarea
      data-testid="content-editor"
      value={value ?? ""}
      onChange={(e) => onUpdate?.(e.target.value)}
      placeholder={placeholder}
    />
  ),
}));

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => null,
}));

vi.mock("../../issues/components/priority-icon", () => ({
  PriorityIcon: () => null,
}));

vi.mock("../../layout/breadcrumb-header", () => ({
  BreadcrumbHeader: ({ actions }: any) => <header>{actions}</header>,
}));

vi.mock("../../layout/animated-right-sidebar", () => ({
  AnimatedRightSidebar: ({ children }: any) => <div>{children}</div>,
  getAnimatedRightSidebarInitialOpen: () => true,
  rightSidebarPanelMotionProps: {},
  useAnimatedRightSidebarState: () => ({
    open: true,
    visualOpen: true,
    motionEnabled: false,
    beginToggle: vi.fn(),
    handleResize: vi.fn(),
  }),
}));

vi.mock("./project-start-date-picker", () => ({
  ProjectStartDatePicker: () => null,
}));

vi.mock("./project-due-date-picker", () => ({
  ProjectDueDatePicker: () => null,
}));

vi.mock("./project-resources-section", () => ({
  ProjectResourcesSection: () => null,
}));

vi.mock("../../issues/surface/issue-surface", () => ({
  IssueSurface: () => null,
}));

vi.mock("@multica/ui/components/ui/dropdown-menu", () => ({
  DropdownMenu: ({ children }: any) => <>{children}</>,
  DropdownMenuTrigger: ({ render }: any) => <>{render}</>,
  DropdownMenuContent: ({ children }: any) => <div>{children}</div>,
  DropdownMenuItem: ({ children, onClick }: any) => (
    <button type="button" onClick={onClick}>
      {children}
    </button>
  ),
  DropdownMenuSeparator: () => <hr />,
}));

vi.mock("@multica/ui/components/ui/popover", () => ({
  Popover: ({ children }: any) => <>{children}</>,
  PopoverTrigger: ({ render }: any) => <>{render}</>,
  PopoverContent: ({ children }: any) => <div>{children}</div>,
}));

vi.mock("@multica/ui/components/ui/tooltip", () => ({
  Tooltip: ({ children }: any) => <>{children}</>,
  TooltipTrigger: ({ render }: any) => <>{render}</>,
  TooltipContent: ({ children }: any) => <div>{children}</div>,
}));

vi.mock("@multica/ui/components/ui/sheet", () => ({
  Sheet: ({ children }: any) => <>{children}</>,
  SheetContent: ({ children }: any) => <div>{children}</div>,
}));

vi.mock("@multica/ui/components/ui/alert-dialog", () => ({
  AlertDialog: ({ open, children }: any) => (open ? <div role="alertdialog">{children}</div> : null),
  AlertDialogContent: ({ children }: any) => <div>{children}</div>,
  AlertDialogHeader: ({ children }: any) => <div>{children}</div>,
  AlertDialogTitle: ({ children }: any) => <h2>{children}</h2>,
  AlertDialogDescription: ({ children }: any) => <p>{children}</p>,
  AlertDialogFooter: ({ children }: any) => <div>{children}</div>,
  AlertDialogCancel: ({ children }: any) => <button type="button">{children}</button>,
  AlertDialogAction: ({ children, onClick }: any) => (
    <button type="button" onClick={onClick}>
      {children}
    </button>
  ),
}));

vi.mock("@multica/ui/components/common/emoji-picker", () => ({
  EmojiPicker: () => null,
}));

function createTestQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  });
}

function renderProjectDetail(settings: Record<string, unknown> = {}) {
  projectFixture.settings = settings;
  const queryClient = createTestQueryClient();
  queryClient.setQueryData(["projects", "ws-1", "detail", "p-1"], projectFixture);
  queryClient.setQueryData(["projects", "ws-1", "detail", "p-1", "resources"], {
    resources: projectResourcesFixture.resources,
    total: projectResourcesFixture.resources.length,
  });
  // Seed members so the admin gate resolves synchronously — the delete
  // affordance depends on it and must not race the query.
  queryClient.setQueryData(["workspaces", "ws-1", "members"], [
    { user_id: "user-1", name: "User One", role: memberRole.value },
  ]);
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <ProjectDetail projectId="p-1" />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  projectFixture.settings = {};
  projectResourcesFixture.resources = [];
  updateProjectSpy.mockClear();
  deleteProjectSpy.mockClear();
  navigationPush.mockClear();
  toastSuccess.mockClear();
  memberRole.value = "admin";
});

describe("ProjectDetail runtime section", () => {
  it("renders the runtime section", async () => {
    renderProjectDetail();
    await waitFor(() => {
      expect(screen.getByText("Runtime")).toBeInTheDocument();
    });
  });

  it("toggles the custom workdir switch and reveals the input", async () => {
    const { container } = renderProjectDetail();
    await screen.findByText("Runtime");
    const switchEl = container.querySelector('[data-slot="switch"]');
    expect(switchEl).not.toBeNull();
    fireEvent.click(switchEl!);
    await waitFor(() => {
      expect(container.querySelector('[data-slot="input"]')).toBeInTheDocument();
    });
    expect(updateProjectSpy).not.toHaveBeenCalled();
  });

  it("commits the existing workdir when toggling on", async () => {
    const { container } = renderProjectDetail({ agent_workdir: "src" });
    await screen.findByText("Runtime");
    const switchEl = container.querySelector('[data-slot="switch"]');
    expect(switchEl).not.toBeNull();
    fireEvent.click(switchEl!);
    await waitFor(() => {
      expect(updateProjectSpy).toHaveBeenCalledOnce();
    });
    expect(updateProjectSpy).toHaveBeenLastCalledWith(
      "p-1",
      expect.objectContaining({
        settings: expect.objectContaining({ enable_agent_workdir: true, agent_workdir: "src" }),
      }),
    );
  });

  it("commits a valid relative workdir on blur", async () => {
    const { container } = renderProjectDetail({ enable_agent_workdir: true, agent_workdir: "" });
    await waitFor(() => {
      expect(container.querySelector('[data-slot="input"]')).toBeInTheDocument();
    });
    const input = container.querySelector('[data-slot="input"]') as HTMLInputElement;
    fireEvent.change(input, { target: { value: "src" } });
    fireEvent.blur(input);
    await waitFor(() => {
      expect(updateProjectSpy).toHaveBeenLastCalledWith(
        "p-1",
        expect.objectContaining({ settings: { enable_agent_workdir: true, agent_workdir: "src" } }),
      );
    });
  });

  it("blocks an absolute path and shows an error", async () => {
    const { container } = renderProjectDetail({ enable_agent_workdir: true, agent_workdir: "" });
    await waitFor(() => {
      expect(container.querySelector('[data-slot="input"]')).toBeInTheDocument();
    });
    const input = container.querySelector('[data-slot="input"]') as HTMLInputElement;
    fireEvent.change(input, { target: { value: "/src" } });
    fireEvent.blur(input);
    await waitFor(() => {
      expect(screen.getByText(/Must be a non-empty relative path/i)).toBeInTheDocument();
    });
    expect(updateProjectSpy).not.toHaveBeenCalled();
  });

  it("blocks a path containing .. and shows an error", async () => {
    const { container } = renderProjectDetail({ enable_agent_workdir: true, agent_workdir: "" });
    await waitFor(() => {
      expect(container.querySelector('[data-slot="input"]')).toBeInTheDocument();
    });
    const input = container.querySelector('[data-slot="input"]') as HTMLInputElement;
    fireEvent.change(input, { target: { value: "src/../other" } });
    fireEvent.blur(input);
    await waitFor(() => {
      expect(screen.getByText(/Must be a non-empty relative path/i)).toBeInTheDocument();
    });
    expect(updateProjectSpy).not.toHaveBeenCalled();
  });

  it("blocks an empty workdir and shows an error", async () => {
    const { container } = renderProjectDetail({ enable_agent_workdir: true, agent_workdir: "src" });
    await waitFor(() => {
      expect(container.querySelector('[data-slot="input"]')).toBeInTheDocument();
    });
    const input = container.querySelector('[data-slot="input"]') as HTMLInputElement;
    fireEvent.change(input, { target: { value: "" } });
    fireEvent.blur(input);
    await waitFor(() => {
      expect(screen.getByText(/Must be a non-empty relative path/i)).toBeInTheDocument();
    });
    expect(updateProjectSpy).not.toHaveBeenCalled();
  });

  it("allows the workdir to match a repo name", async () => {
    projectResourcesFixture.resources = [
      { resource_type: "github_repo", resource_ref: { url: "https://github.com/owner/myrepo.git" } },
    ] as ProjectResource[];
    const { container } = renderProjectDetail({ enable_agent_workdir: true, agent_workdir: "other" });
    await waitFor(() => {
      expect(container.querySelector('[data-slot="input"]')).toBeInTheDocument();
    });
    const input = container.querySelector('[data-slot="input"]') as HTMLInputElement;
    fireEvent.change(input, { target: { value: "myrepo" } });
    fireEvent.blur(input);
    await waitFor(() => {
      expect(screen.queryByText(/Must not match a project repository name/i)).not.toBeInTheDocument();
    });
    expect(updateProjectSpy).toHaveBeenCalled();
  });
});

describe("ProjectDetail project deletion", () => {
  it("requires confirmation and navigates only after deletion succeeds", async () => {
    const user = userEvent.setup();
    let resolveDelete: (() => void) | undefined;
    deleteProjectSpy.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveDelete = resolve;
        }),
    );
    renderProjectDetail();

    await user.click(screen.getByRole("button", { name: "Delete project" }));

    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
    expect(deleteProjectSpy).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Delete" }));

    expect(deleteProjectSpy).toHaveBeenCalledWith("p-1");
    expect(navigationPush).not.toHaveBeenCalled();

    resolveDelete?.();
    await waitFor(() => {
      expect(toastSuccess).toHaveBeenCalledWith("Project deleted");
    });
    expect(navigationPush).toHaveBeenCalledWith("/projects");
  });

  it("does not offer project deletion to regular members", () => {
    memberRole.value = "member";

    renderProjectDetail();

    expect(
      screen.queryByRole("button", { name: "Delete project" }),
    ).not.toBeInTheDocument();
  });
});
