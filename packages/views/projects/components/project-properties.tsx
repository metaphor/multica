"use client";

import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Check, ChevronRight, Lock } from "@multica/ui/icons";
import { cn } from "@multica/ui/lib/utils";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import type { Project, ProjectPriority, ProjectStatus, UpdateProjectRequest } from "@multica/core/types";
import { PROJECT_PRIORITY_ORDER, PROJECT_STATUS_CONFIG, PROJECT_STATUS_ORDER } from "@multica/core/projects/config";
import { useActorName } from "@multica/core/workspace/hooks";
import { ActorAvatar } from "../../common/actor-avatar";
import { PriorityIcon } from "../../issues/components/priority-icon";
import { ProjectWorkflowControl } from "../../workflows/project-workflow-row";
import { ObjectPanelSection, ObjectProperty } from "../../layout/object-page";
import { useT } from "../../i18n";
import { useFormatRelativeDate, useProjectPriorityLabels, useProjectStatusLabels } from "./labels";
import { ProjectAccessControl } from "./project-access";
import { ProjectDueDatePicker } from "./project-due-date-picker";
import { ProjectLeadPicker } from "./project-lead-picker";
import { ProjectStartDatePicker } from "./project-start-date-picker";

const PROPERTY_TRIGGER =
  "flex min-w-0 items-center gap-1.5 rounded-sm text-caption outline-none transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50";

/** The project's editable properties, as the panel section every object page opens with. */
export function ProjectPropertiesPanel({
  project,
  onUpdate,
  onStatusChange,
  onOpenWorkflow,
  readOnly = false,
}: {
  project: Project;
  onUpdate: (data: UpdateProjectRequest) => void;
  onStatusChange: (status: ProjectStatus) => void;
  onOpenWorkflow: () => void;
  /** Shows each property as it is, with nothing to open (a guest's view). */
  readOnly?: boolean;
}) {
  const { t } = useT("projects");
  const { t: tIssues } = useT("issues");
  const { getActorName } = useActorName();
  const statusLabels = useProjectStatusLabels();
  const priorityLabels = useProjectPriorityLabels();
  const formatRelativeDate = useFormatRelativeDate();

  const field = (control: ReactNode) =>
    readOnly ? (
      <>
        <div inert className="flex min-w-0 flex-1 items-center">{control}</div>
        <Lock className="size-3 shrink-0 text-faint-foreground" aria-hidden="true" />
      </>
    ) : (
      control
    );

  const enableAgentWorkdir = Boolean(project.settings?.enable_agent_workdir);
  const agentWorkdir = typeof project.settings?.agent_workdir === "string" ? project.settings.agent_workdir : "";

  const [runtimeOpen, setRuntimeOpen] = useState(true);
  const [agentWorkdirDraft, setAgentWorkdirDraft] = useState("");
  const [agentWorkdirError, setAgentWorkdirError] = useState<string | null>(null);
  const [enableAgentWorkdirOpen, setEnableAgentWorkdirOpen] = useState(enableAgentWorkdir);

  useEffect(() => {
    setEnableAgentWorkdirOpen(enableAgentWorkdir);
  }, [enableAgentWorkdir]);

  useEffect(() => {
    setAgentWorkdirDraft(agentWorkdir);
    setAgentWorkdirError(null);
  }, [agentWorkdir, project.id]);

  const validateAgentWorkdir = useCallback((value: string): string | null => {
    const trimmed = value.trim();
    if (!trimmed) return t(($) => $.detail.agent_workdir_error);
    if (trimmed.startsWith("/")) return t(($) => $.detail.agent_workdir_error);
    if (trimmed.split(/[/\\]/).some((segment) => segment === "..")) return t(($) => $.detail.agent_workdir_error);
    return null;
  }, [t]);

  const handleAgentWorkdirBlur = useCallback(() => {
    const trimmed = agentWorkdirDraft.trim();
    const error = validateAgentWorkdir(trimmed);
    if (error) {
      setAgentWorkdirError(error);
      return;
    }
    setAgentWorkdirError(null);
    if (!enableAgentWorkdirOpen) return;
    onUpdate({
      settings: {
        ...project.settings,
        enable_agent_workdir: true,
        agent_workdir: trimmed,
      },
    });
  }, [agentWorkdirDraft, enableAgentWorkdirOpen, project.settings, onUpdate, validateAgentWorkdir]);

  const handleToggleAgentWorkdir = useCallback((checked: boolean) => {
    setEnableAgentWorkdirOpen(checked);
    if (checked) {
      const trimmed = agentWorkdirDraft.trim();
      const error = validateAgentWorkdir(trimmed);
      if (error) {
        setAgentWorkdirError(error);
        return;
      }
      setAgentWorkdirError(null);
      onUpdate({
        settings: {
          ...project.settings,
          enable_agent_workdir: true,
          agent_workdir: trimmed,
        },
      });
    } else {
      setAgentWorkdirError(null);
      onUpdate({
        settings: {
          ...project.settings,
          enable_agent_workdir: false,
          agent_workdir: agentWorkdirDraft.trim(),
        },
      });
    }
  }, [agentWorkdirDraft, project.settings, onUpdate, validateAgentWorkdir]);

  return (
    <div data-testid="project-properties">
      <ObjectPanelSection title={t(($) => $.detail.section_properties)}>
        <ObjectProperty label={t(($) => $.table.status)}>
          {field(
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <button type="button" className={PROPERTY_TRIGGER}>
                    <span className={cn("size-2 shrink-0 rounded-full", PROJECT_STATUS_CONFIG[project.status].dotColor)} />
                    <span className="truncate">{statusLabels[project.status]}</span>
                  </button>
                }
              />
              <DropdownMenuContent align="start" className="w-44">
                {PROJECT_STATUS_ORDER.map((s) => (
                  <DropdownMenuItem key={s} onClick={() => onStatusChange(s as ProjectStatus)}>
                    <span className={cn("size-2 rounded-full", PROJECT_STATUS_CONFIG[s].dotColor)} />
                    <span>{statusLabels[s]}</span>
                    {s === project.status && <Check className="ml-auto h-3.5 w-3.5" />}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>,
          )}
        </ObjectProperty>
        <ObjectProperty label={t(($) => $.table.priority)}>
          {field(
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <button type="button" className={PROPERTY_TRIGGER}>
                    <PriorityIcon priority={project.priority} />
                    <span className="truncate">{priorityLabels[project.priority]}</span>
                  </button>
                }
              />
              <DropdownMenuContent align="start" className="w-44">
                {PROJECT_PRIORITY_ORDER.map((p) => (
                  <DropdownMenuItem key={p} onClick={() => onUpdate({ priority: p as ProjectPriority })}>
                    <PriorityIcon priority={p} />
                    <span>{priorityLabels[p]}</span>
                    {p === project.priority && <Check className="ml-auto h-3.5 w-3.5" />}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>,
          )}
        </ObjectProperty>
        <ObjectProperty label={t(($) => $.table.lead)}>
          {field(
            <ProjectLeadPicker
              project={project}
              handleUpdate={onUpdate}
              renderTrigger={(leadName) => (
                <button type="button" className={PROPERTY_TRIGGER}>
                  {project.lead_type && project.lead_id ? (
                    <>
                      <ActorAvatar actorType={project.lead_type} actorId={project.lead_id} size="sm" enableHoverCard showStatusDot />
                      <span className="truncate">{leadName}</span>
                    </>
                  ) : (
                    <span className="text-muted-foreground">{t(($) => $.lead.no_lead)}</span>
                  )}
                </button>
              )}
            />,
          )}
        </ObjectProperty>
        <ObjectProperty label={t(($) => $.access.label)}>
          {field(<ProjectAccessControl project={project} />)}
        </ObjectProperty>
        <ObjectProperty label={t(($) => $.detail.prop_start_date)}>
          {field(<ProjectStartDatePicker startDate={project.start_date} onUpdate={onUpdate} />)}
        </ObjectProperty>
        <ObjectProperty label={t(($) => $.detail.prop_due_date)}>
          {field(<ProjectDueDatePicker dueDate={project.due_date} onUpdate={onUpdate} />)}
        </ObjectProperty>
        <ObjectProperty label={tIssues(($) => $.workflows.project.label)}>
          {field(<ProjectWorkflowControl project={project} onOpen={onOpenWorkflow} />)}
        </ObjectProperty>
        {project.archived_at && (
          <ObjectProperty label={t(($) => $.archive.prop_label)}>
            <span className="tabular-nums">{formatRelativeDate(project.archived_at)}</span>
            {project.archived_by_type && project.archived_by_id && (
              <>
                <span className="text-muted-foreground">·</span>
                <ActorAvatar actorType={project.archived_by_type} actorId={project.archived_by_id} size="sm" enableHoverCard />
                <span className="truncate">{getActorName(project.archived_by_type, project.archived_by_id)}</span>
              </>
            )}
          </ObjectProperty>
        )}
        {readOnly && (
          <p className="pt-2 text-caption text-muted-foreground">{t(($) => $.detail.guest_readonly_note)}</p>
        )}
      </ObjectPanelSection>

      {/* Runtime */}
      {!readOnly && (
        <div>
          <button
            type="button"
            className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors mb-2 hover:bg-accent/70 ${runtimeOpen ? "" : "text-muted-foreground hover:text-foreground"}`}
            onClick={() => setRuntimeOpen(!runtimeOpen)}
          >
            {t(($) => $.detail.section_runtime)}
            <ChevronRight className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${runtimeOpen ? "rotate-90" : ""}`} />
          </button>
          {runtimeOpen && (
            <div className="pl-2 space-y-2">
              <label className="flex cursor-pointer items-center justify-between gap-2 rounded-md px-2 -mx-2 py-1.5 hover:bg-accent/50 transition-colors">
                <span className="text-caption">{t(($) => $.detail.agent_workdir_toggle)}</span>
                <Switch
                  checked={enableAgentWorkdirOpen}
                  onCheckedChange={handleToggleAgentWorkdir}
                  size="sm"
                  aria-label={t(($) => $.detail.agent_workdir_toggle)}
                />
              </label>
              {enableAgentWorkdirOpen && (
                <div className="space-y-1">
                  <Input
                    value={agentWorkdirDraft}
                    onChange={(e) => {
                      setAgentWorkdirDraft(e.target.value);
                      if (agentWorkdirError) setAgentWorkdirError(null);
                    }}
                    onBlur={handleAgentWorkdirBlur}
                    placeholder={t(($) => $.detail.agent_workdir_placeholder)}
                    aria-invalid={!!agentWorkdirError}
                    aria-label={t(($) => $.detail.agent_workdir_placeholder)}
                  />
                  {agentWorkdirError && (
                    <p className="text-caption text-destructive">{agentWorkdirError}</p>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
