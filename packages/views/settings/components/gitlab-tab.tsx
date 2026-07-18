"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Link2, PanelRight, Plus, Trash2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Badge } from "@multica/ui/components/ui/badge";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import { memberListOptions, workspaceKeys } from "@multica/core/workspace/queries";
import {
  deriveGitLabSettings,
  gitlabKeys,
} from "@multica/core/gitlab";
import { api } from "@multica/core/api";
import type { Workspace, GitLabConnection, GitLabHookTarget } from "@multica/core/types";
import { useT } from "../../i18n";
import { SettingsTab } from "./settings-layout";
import { GitLabMark } from "./gitlab-mark";

type SettingsKey =
  | "gitlab_enabled"
  | "gitlab_mr_sidebar_enabled"
  | "gitlab_auto_link_mrs_enabled";

export function GitLabTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canView = !!currentMember;

  const { data: connectionData } = useQuery({
    queryKey: gitlabKeys.connections(wsId),
    queryFn: () => api.listGitLabConnections(wsId),
    enabled: !!wsId && canView,
  });
  const connections = connectionData?.connections ?? [];
  const configured = connectionData?.configured ?? false;
  const canManage = connectionData?.can_manage === true;
  const connected = connections.length > 0;

  const flags = deriveGitLabSettings(workspace);
  const [savingKey, setSavingKey] = useState<SettingsKey | null>(null);

  // Connection form state
  const [showForm, setShowForm] = useState(false);
  const [formInstanceUrl, setFormInstanceUrl] = useState("");
  const [formToken, setFormToken] = useState("");
  const [formDisplayName, setFormDisplayName] = useState("");
  const [connecting, setConnecting] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  // Disconnect state
  const [disconnectTarget, setDisconnectTarget] = useState<GitLabConnection | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);

  // Hook add state per connection
  const [hookConnId, setHookConnId] = useState<string | null>(null);
  const [hookTargetType, setHookTargetType] = useState<"project" | "group">("project");
  const [hookTargetPath, setHookTargetPath] = useState("");
  const [hookSubmitting, setHookSubmitting] = useState(false);
  const [hookError, setHookError] = useState<string | null>(null);

  // Hook remove state
  const [removeHookTarget, setRemoveHookTarget] = useState<{ connId: string; target: GitLabHookTarget } | null>(null);
  const [removingHook, setRemovingHook] = useState(false);

  async function persistSetting(key: SettingsKey, next: boolean) {
    if (!workspace || savingKey) return;
    setSavingKey(key);
    try {
      const merged = {
        ...((workspace.settings as Record<string, unknown>) ?? {}),
        [key]: next,
      };
      const updated = await api.updateWorkspace(workspace.id, { settings: merged });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      toast.success(t(($) => $.auto_save.toast_saved), {
        id: "settings-auto-save",
      });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.gitlab.toast_failed));
    } finally {
      setSavingKey(null);
    }
  }

  function resetForm() {
    setFormInstanceUrl("");
    setFormToken("");
    setFormDisplayName("");
    setFormError(null);
    setShowForm(false);
  }

  async function handleConnect() {
    setFormError(null);
    // Validate
    const url = formInstanceUrl.trim();
    if (!url) {
      setFormError(t(($) => $.gitlab.form_instance_url_required));
      return;
    }
    if (!/^https?:\/\/.+/.test(url)) {
      setFormError(t(($) => $.gitlab.form_instance_url_invalid));
      return;
    }
    if (!formToken.trim()) {
      setFormError(t(($) => $.gitlab.form_token_required));
      return;
    }
    setConnecting(true);
    try {
      await api.createGitLabConnection(wsId, {
        instance_url: url,
        access_token: formToken.trim(),
        display_name: formDisplayName.trim() || undefined,
      });
      await qc.invalidateQueries({ queryKey: gitlabKeys.connections(wsId) });
      toast.success(t(($) => $.gitlab.toast_connected));
      resetForm();
    } catch (e) {
      const msg = e instanceof Error ? e.message : t(($) => $.gitlab.toast_connect_failed);
      setFormError(msg);
      toast.error(msg);
    } finally {
      setConnecting(false);
      setFormToken("");
    }
  }

  async function handleDisconnect() {
    if (!disconnectTarget || disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteGitLabConnection(wsId, disconnectTarget.id);
      await qc.invalidateQueries({ queryKey: gitlabKeys.connections(wsId) });
      toast.success(t(($) => $.gitlab.toast_disconnected));
      setDisconnectTarget(null);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.gitlab.toast_disconnect_failed));
    } finally {
      setDisconnecting(false);
    }
  }

  async function handleAddHook() {
    if (!hookConnId || hookSubmitting) return;
    setHookError(null);
    if (!hookTargetPath.trim()) {
      setHookError(t(($) => $.gitlab.hook_path_required));
      return;
    }
    setHookSubmitting(true);
    try {
      await api.addGitLabHookTarget(wsId, hookConnId, {
        target_type: hookTargetType,
        target_path: hookTargetPath.trim(),
      });
      await qc.invalidateQueries({ queryKey: gitlabKeys.connections(wsId) });
      setHookConnId(null);
      setHookTargetPath("");
      setHookTargetType("project");
    } catch (e) {
      const msg = e instanceof Error ? e.message : t(($) => $.gitlab.hook_failed);
      setHookError(msg);
      toast.error(msg);
    } finally {
      setHookSubmitting(false);
    }
  }

  async function handleRemoveHook() {
    if (!removeHookTarget || removingHook) return;
    setRemovingHook(true);
    try {
      await api.removeGitLabHookTarget(wsId, removeHookTarget.connId, {
        target_type: removeHookTarget.target.target_type,
        target_path: removeHookTarget.target.target_path,
      });
      await qc.invalidateQueries({ queryKey: gitlabKeys.connections(wsId) });
      setRemoveHookTarget(null);
    } catch (e) {
      const msg = e instanceof Error ? e.message : t(($) => $.gitlab.hook_failed);
      toast.error(msg);
    } finally {
      setRemovingHook(false);
    }
  }

  if (!workspace) return null;

  const httpWarning =
    formInstanceUrl.trim().startsWith("http://")
      ? t(($) => $.gitlab.http_warning)
      : null;

  return (
    <SettingsTab
      title={t(($) => $.page.tabs.gitlab)}
      description={t(($) => $.gitlab.page_description)}
    >
      {/* Master switch */}
      <section className="space-y-3">
        <Card>
          <CardContent>
            <div className="flex items-start justify-between gap-4">
              <div className="flex items-start gap-3">
                <div className="rounded-md border bg-muted/50 p-2 text-muted-foreground">
                  <GitLabMark className="h-4 w-4" />
                </div>
                <div className="space-y-1">
                  <Label htmlFor="gitlab-master" className="text-sm font-medium">
                    {t(($) => $.gitlab.section_master)}
                  </Label>
                  <p className="text-sm text-muted-foreground">
                    {flags.enabled
                      ? t(($) => $.gitlab.master_description_on)
                      : t(($) => $.gitlab.master_description_off)}
                  </p>
                </div>
              </div>
              <Switch
                id="gitlab-master"
                checked={flags.enabled}
                onCheckedChange={(v) => persistSetting("gitlab_enabled", v)}
                disabled={!canManage || savingKey === "gitlab_enabled"}
              />
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Connection section */}
      <section className="space-y-3">
        <h2 className="text-sm font-semibold">{t(($) => $.gitlab.section_connection)}</h2>
        <Card>
          <CardContent className="space-y-4">
            {/* Connection list */}
            {connections.map((conn) => (
              <div key={conn.id} className="flex items-start justify-between gap-4">
                <div className="flex items-start gap-3">
                  <GitLabMark className="h-6 w-6 mt-0.5 shrink-0" />
                  <div className="space-y-1">
                    <p className="text-sm font-medium">
                      {conn.display_name || conn.instance_url}
                    </p>
                    <p className="text-xs text-muted-foreground">
                      {t(($) => $.gitlab.connected_to, { login: conn.account_login })}
                      {conn.instance_url && (
                        <> · <span className="font-mono">{conn.instance_url}</span></>
                      )}
                    </p>
                    {/* Hook target chips */}
                    {conn.hooks.length > 0 && (
                      <div className="flex flex-wrap gap-1.5 pt-1">
                        {conn.hooks.map((hook) => (
                          <Badge key={`${hook.target_type}:${hook.target_path}`} variant="secondary" className="gap-1 text-xs">
                            <span className="text-muted-foreground">{hook.target_type}:</span>
                            {hook.target_path}
                            {canManage && (
                              <button
                                type="button"
                                className="ml-0.5 rounded-full p-0.5 hover:bg-muted"
                                onClick={() => setRemoveHookTarget({ connId: conn.id, target: hook })}
                                aria-label={t(($) => $.gitlab.hook_remove)}
                              >
                                <Trash2 className="h-3 w-3" />
                              </button>
                            )}
                          </Badge>
                        ))}
                      </div>
                    )}
                    {conn.hooks.length === 0 && (
                      <p className="text-xs text-muted-foreground">
                        {t(($) => $.gitlab.hook_empty)}
                      </p>
                    )}
                  </div>
                </div>
                {canManage && (
                  <div className="flex items-center gap-2">
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => {
                        setHookConnId(hookConnId === conn.id ? null : conn.id);
                        setHookTargetPath("");
                        setHookTargetType("project");
                        setHookError(null);
                      }}
                    >
                      <Plus className="h-3.5 w-3.5" />
                      {t(($) => $.gitlab.hook_add)}
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setDisconnectTarget(conn)}
                    >
                      {t(($) => $.gitlab.disconnect)}
                    </Button>
                  </div>
                )}
              </div>
            ))}

            {/* Hook add form */}
            {hookConnId && canManage && (
              <div className="space-y-3 rounded-md border p-3">
                <p className="text-xs font-medium">{t(($) => $.gitlab.hook_add_title)}</p>
                <div className="flex items-end gap-2">
                  <div className="space-y-1.5">
                    <Select
                      items={[
                        { value: "project", label: t(($) => $.gitlab.hook_target_type_project) },
                        { value: "group", label: t(($) => $.gitlab.hook_target_type_group) },
                      ]}
                      value={hookTargetType}
                      onValueChange={(v) => setHookTargetType(v as "project" | "group")}
                    >
                      <SelectTrigger className="h-8 w-24 text-xs">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="project">{t(($) => $.gitlab.hook_target_type_project)}</SelectItem>
                        <SelectItem value="group">{t(($) => $.gitlab.hook_target_type_group)}</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="flex-1 space-y-1.5">
                    <Input
                      className="h-8 text-xs"
                      placeholder={t(($) => $.gitlab.hook_target_path_placeholder)}
                      value={hookTargetPath}
                      onChange={(e) => setHookTargetPath(e.target.value)}
                      onKeyDown={(e) => { if (e.key === "Enter") handleAddHook(); }}
                    />
                  </div>
                  <Button size="sm" className="h-8" onClick={handleAddHook} disabled={hookSubmitting}>
                    {hookSubmitting
                      ? t(($) => $.gitlab.hook_add_submitting)
                      : t(($) => $.gitlab.hook_add_submit)}
                  </Button>
                </div>
                {hookError && (
                  <p className="text-xs text-destructive">{hookError}</p>
                )}
              </div>
            )}

            {/* No connection state */}
            {!connected && canManage && (
              <div className="flex items-start justify-between gap-4">
                <div className="flex items-start gap-3">
                  <GitLabMark className="h-6 w-6 mt-0.5 shrink-0" />
                  <div className="space-y-1">
                    <p className="text-sm font-medium">{t(($) => $.gitlab.connection_title)}</p>
                    <p className="text-xs text-muted-foreground">
                      {t(($) => $.gitlab.connection_description)}
                    </p>
                  </div>
                </div>
                <Button
                  size="sm"
                  onClick={() => { setShowForm(true); setFormError(null); }}
                  disabled={!configured}
                  title={
                    !configured
                      ? t(($) => $.gitlab.connect_disabled_tooltip)
                      : undefined
                  }
                >
                  {t(($) => $.gitlab.connect)}
                </Button>
              </div>
            )}

            {/* No connection, non-manager */}
            {!connected && !canManage && (
              <div className="flex items-start gap-3">
                <GitLabMark className="h-6 w-6 mt-0.5 shrink-0" />
                <p className="text-xs text-muted-foreground">
                  {t(($) => $.gitlab.contact_admin_to_connect)}
                </p>
              </div>
            )}

            {/* Connection form */}
            {showForm && canManage && (
              <div className="space-y-3 rounded-md border p-3">
                <div className="space-y-1.5">
                  <Label htmlFor="gitlab-instance-url" className="text-xs">
                    {t(($) => $.gitlab.form_instance_url_label)}
                  </Label>
                  <Input
                    id="gitlab-instance-url"
                    className="h-8 text-xs"
                    placeholder={t(($) => $.gitlab.form_instance_url_placeholder)}
                    value={formInstanceUrl}
                    onChange={(e) => { setFormInstanceUrl(e.target.value); setFormError(null); }}
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="gitlab-token" className="text-xs">
                    {t(($) => $.gitlab.form_token_label)}
                  </Label>
                  <Input
                    id="gitlab-token"
                    type="password"
                    className="h-8 text-xs"
                    placeholder={t(($) => $.gitlab.form_token_placeholder)}
                    value={formToken}
                    onChange={(e) => { setFormToken(e.target.value); setFormError(null); }}
                    onKeyDown={(e) => { if (e.key === "Enter") handleConnect(); }}
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="gitlab-display-name" className="text-xs">
                    {t(($) => $.gitlab.form_display_name_label)}
                  </Label>
                  <Input
                    id="gitlab-display-name"
                    className="h-8 text-xs"
                    placeholder={t(($) => $.gitlab.form_display_name_placeholder)}
                    value={formDisplayName}
                    onChange={(e) => setFormDisplayName(e.target.value)}
                    onKeyDown={(e) => { if (e.key === "Enter") handleConnect(); }}
                  />
                </div>
                {httpWarning && (
                  <p className="text-xs text-amber-600 dark:text-amber-400">{httpWarning}</p>
                )}
                {formError && (
                  <p className="text-xs text-destructive">{formError}</p>
                )}
                <div className="flex items-center gap-2">
                  <Button size="sm" onClick={handleConnect} disabled={connecting}>
                    {connecting
                      ? t(($) => $.gitlab.form_submitting)
                      : t(($) => $.gitlab.form_submit)}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={resetForm} disabled={connecting}>
                    {t(($) => $.gitlab.form_cancel)}
                  </Button>
                </div>
              </div>
            )}

            {/* Not configured hint */}
            {canManage && !configured && (
              <p className="text-xs text-muted-foreground">
                {t(($) => $.gitlab.not_configured)}{" "}
                <code className="rounded bg-muted px-1 py-0.5 text-[10px]">MULTICA_GITLAB_SECRET_KEY</code>.
              </p>
            )}

            {/* Read-only hint for connected non-managers */}
            {!canManage && connected && (
              <p className="text-xs text-muted-foreground">
                {t(($) => $.gitlab.read_only_hint)}
              </p>
            )}
          </CardContent>
        </Card>
      </section>

      {/* Features section */}
      <section className="space-y-3">
        <h2 className="text-sm font-semibold">{t(($) => $.gitlab.section_features)}</h2>
        <Card className="gap-0 py-0">
          <CardContent className="divide-y divide-surface-border px-0">
            <FeatureRow
              id="gitlab-mr-sidebar"
              icon={<PanelRight className="h-4 w-4" />}
              label={t(($) => $.gitlab.feature_mr_sidebar_label)}
              description={
                <p className="text-sm text-muted-foreground">
                  {t(($) => $.gitlab.feature_mr_sidebar_description)}
                </p>
              }
              checked={flags.mrSidebar}
              disabled={!canManage || !flags.enabled || savingKey === "gitlab_mr_sidebar_enabled"}
              onCheckedChange={(v) => persistSetting("gitlab_mr_sidebar_enabled", v)}
            />

            <FeatureRow
              id="gitlab-auto-link"
              icon={<Link2 className="h-4 w-4" />}
              label={t(($) => $.gitlab.feature_auto_link_label)}
              description={
                <p className="text-sm text-muted-foreground">
                  {t(($) => $.gitlab.feature_auto_link_description)}
                </p>
              }
              checked={flags.autoLinkMRs}
              disabled={!canManage || !flags.enabled || savingKey === "gitlab_auto_link_mrs_enabled"}
              onCheckedChange={(v) => persistSetting("gitlab_auto_link_mrs_enabled", v)}
            />
          </CardContent>
        </Card>
      </section>

      {/* Disconnect confirmation dialog */}
      <AlertDialog
        open={!!disconnectTarget}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setDisconnectTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.gitlab.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.gitlab.disconnect_confirm_description, {
                name: disconnectTarget?.display_name || disconnectTarget?.instance_url || "",
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.gitlab.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.gitlab.disconnecting)
                : t(($) => $.gitlab.disconnect_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Remove hook confirmation dialog */}
      <AlertDialog
        open={!!removeHookTarget}
        onOpenChange={(v) => {
          if (!v && !removingHook) setRemoveHookTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.gitlab.hook_remove_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.gitlab.hook_remove_confirm_description, {
                target: removeHookTarget
                  ? `${removeHookTarget.target.target_type}:${removeHookTarget.target.target_path}`
                  : "",
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={removingHook}>
              {t(($) => $.gitlab.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleRemoveHook} disabled={removingHook}>
              {removingHook
                ? t(($) => $.gitlab.hook_removing)
                : t(($) => $.gitlab.hook_remove_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}

function FeatureRow({
  id,
  icon,
  label,
  description,
  checked,
  disabled,
  onCheckedChange,
}: {
  id: string;
  icon: React.ReactNode;
  label: string;
  description: React.ReactNode;
  checked: boolean;
  disabled: boolean;
  onCheckedChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-start justify-between gap-4 px-4 py-3.5">
      <div className="flex items-start gap-3">
        <div className="rounded-md border bg-muted/50 p-2 text-muted-foreground">{icon}</div>
        <div className="space-y-1">
          <Label htmlFor={id} className="text-sm font-medium">
            {label}
          </Label>
          {description}
        </div>
      </div>
      <Switch
        id={id}
        checked={checked}
        disabled={disabled}
        onCheckedChange={onCheckedChange}
      />
    </div>
  );
}
