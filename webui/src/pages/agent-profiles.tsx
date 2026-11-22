import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import { Bot, Lock, Pencil, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { EmptyState, ErrorState } from "@/components/states";
import { InvocationRecords } from "@/components/invocation-records";
import { menuContentClass, menuItemClass } from "@/lib/utils";
import { useI18n } from "@/i18n";
import {
  useAgentProfiles,
  useCreateAgentProfile,
  useUpdateAgentProfile,
  useDeleteAgentProfile,
  useAgentInvocations,
} from "@/api/queries";
import type { AgentProfile, InvocationListParams } from "@/types/api";

/**
 * Agent 档案管理（契约 §integration-agent-profiles）。内置专家档案
 * （builtin=true）只读展示；用户档案参与自动委派与 spawn_agent 派生。
 */
export default function AgentProfilesPage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = searchParams.get("tab") === "records" ? "records" : "config";
  const setTab = (v: string) =>
    setSearchParams(v === "config" ? {} : { tab: v }, { replace: true });

  return (
    <div className="space-y-6">
      <header>
        <h1 className="font-sans text-2xl font-semibold tracking-tight">{t("agents.title")}</h1>
        <p className="text-sm text-muted-foreground">{t("agents.subtitle")}</p>
      </header>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="config">{t("agents.tabConfig")}</TabsTrigger>
          <TabsTrigger value="records">{t("agents.tabRecords")}</TabsTrigger>
        </TabsList>
        <TabsContent value="config">
          <ProfilesConfigTab />
        </TabsContent>
        <TabsContent value="records">
          <ProfilesRecordsTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function ProfilesConfigTab() {
  const { t } = useI18n();
  const { data, isLoading, isError, error, refetch } = useAgentProfiles();
  const update = useUpdateAgentProfile();
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<AgentProfile | null>(null);
  const [deleting, setDeleting] = useState<AgentProfile | null>(null);

  const profiles = data?.profiles ?? [];

  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <Button
          onClick={() => {
            setEditing(null);
            setFormOpen(true);
          }}
        >
          <Plus className="mr-1 h-4 w-4" />
          {t("agents.create")}
        </Button>
      </div>

      {isError ? (
        <ErrorState
          title={t("agents.loadFailed")}
          description={error instanceof Error ? error.message : ""}
          action={
            <Button size="sm" variant="outline" onClick={() => refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-14 w-full" />
          ))}
        </div>
      ) : profiles.length === 0 ? (
        <EmptyState
          icon={<Bot className="h-6 w-6" />}
          title={t("agents.emptyTitle")}
          description={t("agents.emptyDescription")}
        />
      ) : (
        <div className="space-y-3">
          {profiles.map((p) => (
            <div
              key={p.id}
              className="flex flex-col gap-3 rounded-md border border-border bg-card p-4 lg:flex-row lg:items-center"
            >
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-mono text-sm font-medium">{p.role}</span>
                  {p.builtin ? (
                    <Badge variant="muted">
                      <Lock className="mr-1 h-3 w-3" />
                      {t("agents.builtin")}
                    </Badge>
                  ) : null}
                  {(p.vuln_classes ?? []).slice(0, 6).map((c) => (
                    <Badge key={c} variant="outline">
                      {c}
                    </Badge>
                  ))}
                  {(p.vuln_classes ?? []).length > 6 ? (
                    <span className="text-[11px] text-muted-foreground tabular-nums">
                      +{(p.vuln_classes ?? []).length - 6}
                    </span>
                  ) : null}
                </div>
                <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">{p.focus}</p>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <Switch
                    checked={p.enabled}
                    disabled={p.builtin}
                    onCheckedChange={(v) =>
                      update.mutate(
                        { id: p.id, patch: { enabled: v } },
                        {
                          onSuccess: () => toast.success(t("toast.profileUpdated")),
                          onError: (err: Error) =>
                            toast.error(`${t("toast.profileFailed")}: ${err.message}`),
                        },
                      )
                    }
                  />
                  {t("mcp.fieldEnabled")}
                </label>
                {p.builtin ? (
                  <span className="text-[11px] text-muted-foreground">{t("agents.readonly")}</span>
                ) : (
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button size="sm" variant="outline">
                        {t("common.actions")}
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" className={menuContentClass}>
                      <DropdownMenuItem
                        className={menuItemClass}
                        onClick={() => {
                          setEditing(p);
                          setFormOpen(true);
                        }}
                      >
                        <Pencil className="mr-2 h-3.5 w-3.5" />
                        {t("common.edit")}
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        className={`${menuItemClass} text-red-600 focus:text-red-600 dark:text-red-400`}
                        onClick={() => setDeleting(p)}
                      >
                        <Trash2 className="mr-2 h-3.5 w-3.5" />
                        {t("common.delete")}
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      <ProfileFormDialog open={formOpen} onOpenChange={setFormOpen} editing={editing} />
      <ProfileDeleteDialog target={deleting} onClear={() => setDeleting(null)} />
    </div>
  );
}

function ProfilesRecordsTab() {
  const { t } = useI18n();
  const [params, setParams] = useState<InvocationListParams>({ page: 1, size: 25 });
  const query = useAgentInvocations(params);
  return (
    <InvocationRecords
      query={query}
      params={params}
      onParamsChange={setParams}
      namePlaceholder={t("agents.filterName")}
    />
  );
}

const ROLE_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/;

function ProfileFormDialog({
  open,
  onOpenChange,
  editing,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  editing: AgentProfile | null;
}) {
  const { t } = useI18n();
  const create = useCreateAgentProfile();
  const update = useUpdateAgentProfile();
  const [role, setRole] = useState("");
  const [focus, setFocus] = useState("");
  const [vulnClasses, setVulnClasses] = useState("");
  const [evidenceContract, setEvidenceContract] = useState("");
  const [stoppingRule, setStoppingRule] = useState("");
  const [toolScopeHidden, setToolScopeHidden] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [formError, setFormError] = useState("");

  const [wasOpen, setWasOpen] = useState(false);
  if (open && !wasOpen) {
    setRole(editing?.role ?? "");
    setFocus(editing?.focus ?? "");
    setVulnClasses((editing?.vuln_classes ?? []).join(", "));
    setEvidenceContract(editing?.evidence_contract ?? "");
    setStoppingRule(editing?.stopping_rule ?? "");
    setToolScopeHidden((editing?.tool_scope_hidden ?? []).join(", "));
    setEnabled(editing?.enabled ?? true);
    setFormError("");
    setWasOpen(true);
  } else if (!open && wasOpen) {
    setWasOpen(false);
  }

  function csv(text: string): string[] {
    return text
      .split(/[\n,]/)
      .map((s) => s.trim())
      .filter(Boolean);
  }

  function submit() {
    setFormError("");
    const trimmedRole = role.trim().toLowerCase();
    if (!ROLE_PATTERN.test(trimmedRole)) {
      setFormError(t("agents.roleFormatError"));
      return;
    }
    if (!focus.trim() || !evidenceContract.trim() || !stoppingRule.trim()) {
      setFormError(t("agents.contractRequired"));
      return;
    }
    const onSaved = (isEdit: boolean) => {
      toast.success(isEdit ? t("toast.profileUpdated") : t("toast.profileCreated"));
      onOpenChange(false);
    };
    const onError = (err: Error) =>
      toast.error(`${t("toast.profileFailed")}: ${err.message}`);
    if (editing) {
      update.mutate(
        {
          id: editing.id,
          patch: {
            role: trimmedRole,
            focus: focus.trim(),
            vuln_classes: csv(vulnClasses),
            evidence_contract: evidenceContract.trim(),
            stopping_rule: stoppingRule.trim(),
            tool_scope_hidden: csv(toolScopeHidden),
            enabled,
          },
        },
        { onSuccess: () => onSaved(true), onError },
      );
    } else {
      create.mutate(
        {
          role: trimmedRole,
          focus: focus.trim(),
          vuln_classes: csv(vulnClasses),
          evidence_contract: evidenceContract.trim(),
          stopping_rule: stoppingRule.trim(),
          tool_scope_hidden: csv(toolScopeHidden),
          enabled,
        },
        { onSuccess: () => onSaved(false), onError },
      );
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{editing ? t("agents.editTitle") : t("agents.createTitle")}</DialogTitle>
          <DialogDescription>{t("agents.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="profile-role">{t("agents.fieldRole")}</Label>
            <Input id="profile-role" value={role} onChange={(e) => setRole(e.target.value)} placeholder="api-fuzzing" className="font-mono" />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="profile-focus">{t("agents.fieldFocus")}</Label>
            <Input id="profile-focus" value={focus} onChange={(e) => setFocus(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="profile-classes">{t("agents.fieldClasses")}</Label>
            <Input id="profile-classes" value={vulnClasses} onChange={(e) => setVulnClasses(e.target.value)} placeholder="api-auth, mass-assignment" />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="profile-evidence">{t("agents.fieldEvidence")}</Label>
            <Textarea id="profile-evidence" rows={3} value={evidenceContract} onChange={(e) => setEvidenceContract(e.target.value)} />
            <p className="text-[11px] text-muted-foreground">{t("agents.evidenceHint")}</p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="profile-stopping">{t("agents.fieldStopping")}</Label>
            <Textarea id="profile-stopping" rows={2} value={stoppingRule} onChange={(e) => setStoppingRule(e.target.value)} />
            <p className="text-[11px] text-muted-foreground">{t("agents.stoppingHint")}</p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="profile-scope">{t("agents.fieldScope")}</Label>
            <Input id="profile-scope" value={toolScopeHidden} onChange={(e) => setToolScopeHidden(e.target.value)} placeholder="browser_action, spawn_agent" className="font-mono" />
            <p className="text-[11px] text-muted-foreground">{t("agents.scopeHint")}</p>
          </div>
          <label className="flex items-center gap-2 text-sm">
            <Switch checked={enabled} onCheckedChange={setEnabled} />
            {t("mcp.fieldEnabled")}
          </label>
          {formError ? <p className="text-xs text-red-600 dark:text-red-400">{formError}</p> : null}
        </div>
        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button size="sm" onClick={submit} disabled={create.isPending || update.isPending}>
            {editing ? t("common.save") : t("common.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ProfileDeleteDialog({ target, onClear }: { target: AgentProfile | null; onClear: () => void }) {
  const { t } = useI18n();
  const del = useDeleteAgentProfile();
  return (
    <AlertDialog open={!!target} onOpenChange={(v) => !v && onClear()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("agents.deleteTitle")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("agents.deleteDescriptionPrefix")} “{target?.role}”.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={() => {
              if (!target) return;
              del.mutate(target.id, {
                onSuccess: () => toast.success(t("toast.profileDeleted")),
                onError: (err: Error) => toast.error(`${t("toast.profileFailed")}: ${err.message}`),
              });
              onClear();
            }}
          >
            {t("common.delete")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
