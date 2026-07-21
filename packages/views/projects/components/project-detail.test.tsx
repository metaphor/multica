import type { Project, ProjectResource } from "@multica/core/types";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
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

vi.mock("@multica/core/api", () => ({
  api: {
    getProject: vi.fn().mockResolvedValue(projectFixture),
    updateProject: updateProjectSpy,
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
    queryFn: () => Promise.resolve([]),
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
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => ({ push: vi.fn(), pathname: "/", getShareableUrl: (p: string) => p }),
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

vi.mock("../../layout/breadcrumb-header", () => ({
  BreadcrumbHeader: ({ leaf }: any) => <div>{leaf}</div>,
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
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <ProjectDetail projectId="p-1" />
    </QueryClientProvider>,
  );
}

describe("ProjectDetail runtime section", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    projectFixture.settings = {};
    projectResourcesFixture.resources = [];
    updateProjectSpy.mockClear();
  });

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
