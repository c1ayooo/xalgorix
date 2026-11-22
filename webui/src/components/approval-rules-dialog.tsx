import { useState } from "react";
import { Link } from "react-router-dom";
import { toast } from "sonner";
import { ExternalLink, Plus, ShieldCheck, Trash2 } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
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
import { useApprovalRules, useCreateApprovalRule, useDeleteApprovalRule } from "@/api/queries";
import { useI18n } from "@/i18n";
import { formatTime, shortId } from "@/lib/utils";
import type { ApprovalRule, ApprovalRuleRequest } from "@/types/api";

/**
 * 审批豁免规则面板（approve & remember，契约 §3.4）。命令 / HTTP 两个
 * 层面的既有规则各占一个 Tab：mono 匹配式 + 命中次数（调默认值的运行
 * 数据）+ 过期状态 + 来源审批溯源；新建为内联表单（预填由审批卡的
 * 「记住」路径负责，这里只做手工创建）。删除走 AlertDialog + 后果文案
 * （UI_DESIGN §6）。
 */
export function ApprovalRulesDialog({
  open,
  onOpenChange,
  projectId,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  projectId?: string;
}) {
  const { t } = useI18n();
  const [creating, setCreating] = useState(false);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-auto">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <ShieldCheck className="h-4 w-4" aria-hidden />
            {t("approvalRules.title")}
          </DialogTitle>
          <DialogDescription>{t("approvalRules.subtitle")}</DialogDescription>
        </DialogHeader>
        <Tabs defaultValue="command">
          <div className="flex items-center justify-between gap-2">
            <TabsList>
              <TabsTrigger value="command">{t("approval.kindCommand")}</TabsTrigger>
              <TabsTrigger value="http">{t("approval.kindHttp")}</TabsTrigger>
            </TabsList>
            <Button
              size="sm"
              variant="outline"
              disabled={!projectId || creating}
              onClick={() => setCreating(true)}
            >
              <Plus className="h-3.5 w-3.5" />
              {t("approvalRules.create")}
            </Button>
          </div>
          <TabsContent value="command" className="mt-3">
            <RuleList kind="command" projectId={projectId} creating={creating} onCreatingChange={setCreating} />
          </TabsContent>
          <TabsContent value="http" className="mt-3">
            <RuleList kind="http" projectId={projectId} creating={creating} onCreatingChange={setCreating} />
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}

function RuleList({
  kind,
  projectId,
  creating,
  onCreatingChange,
}: {
  kind: "command" | "http";
  projectId?: string;
  creating: boolean;
  onCreatingChange: (v: boolean) => void;
}) {
  const { t } = useI18n();
  const { data, isLoading } = useApprovalRules({ projectId, kind });
  const del = useDeleteApprovalRule();
  const [deleting, setDeleting] = useState<ApprovalRule | null>(null);
  const rules = data?.rules ?? [];

  if (creating) {
    return (
      <RuleCreateForm
        kind={kind}
        projectId={projectId}
        onDone={() => onCreatingChange(false)}
      />
    );
  }
  if (isLoading) {
    return <p className="py-6 text-center text-sm text-muted-foreground">…</p>;
  }
  if (rules.length === 0) {
    return (
      <p className="rounded-md border border-dashed border-border p-6 text-center text-sm text-muted-foreground">
        {t("approvalRules.empty")}
      </p>
    );
  }
  return (
    <>
      <ul className="space-y-2">
        {rules.map((rule) => {
          const expired = rule.expires_at ? new Date(rule.expires_at) <= new Date() : false;
          return (
            <li key={rule.id} className="rounded-md border border-border bg-card p-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className="min-w-0 flex-1 truncate mono text-sm font-medium">
                  {ruleMatchText(rule)}
                </span>
                <span
                  className={
                    rule.decision === "deny"
                      ? "shrink-0 rounded-sm border border-destructive/40 bg-destructive/10 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-destructive"
                      : "shrink-0 rounded-sm border border-border bg-muted px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-muted-foreground"
                  }
                >
                  {rule.decision === "deny"
                    ? t("approvalRules.decisionDeny")
                    : t("approvalRules.decisionAllow")}
                </span>
                {expired && (
                  <span className="shrink-0 rounded-sm border border-border bg-muted px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-muted-foreground">
                    {t("approvalRules.expired")}
                  </span>
                )}
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 px-2 text-muted-foreground hover:text-destructive"
                  aria-label={t("approvalRules.delete")}
                  onClick={() => setDeleting(rule)}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
              <div className="mt-1.5 flex flex-wrap gap-x-4 gap-y-0.5 text-[11px] text-muted-foreground">
                <span className="tabular-nums">
                  {t("approvalRules.hits")}: <span className="mono">{rule.hit_count}</span>
                  {rule.last_hit_at && (
                    <span className="ml-1">{formatTime(rule.last_hit_at)}</span>
                  )}
                </span>
                {rule.expires_at && !expired && (
                  <span>
                    {t("approvalRules.expiresAt")}: <span className="mono">{formatTime(rule.expires_at)}</span>
                  </span>
                )}
                {rule.created_by && <span className="mono">{rule.created_by}</span>}
                {rule.created_from_approval_id && (
                  <Link
                    to={`/approvals?focus=${rule.created_from_approval_id}`}
                    className="inline-flex items-center gap-0.5 transition-colors hover:text-foreground"
                  >
                    <ExternalLink className="h-3 w-3" aria-hidden />
                    {shortId(rule.created_from_approval_id)}
                  </Link>
                )}
              </div>
              {rule.reason && (
                <p className="mt-1 truncate text-[11px] text-muted-foreground">{rule.reason}</p>
              )}
            </li>
          );
        })}
      </ul>
      <AlertDialog open={!!deleting} onOpenChange={(v) => !v && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("approvalRules.deleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("approvalRules.deleteDescription")}{" "}
              <span className="mono">{deleting ? ruleMatchText(deleting) : ""}</span>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (!deleting) return;
                del.mutate(deleting.id, {
                  onSuccess: () => toast.success(t("approvalRules.deletedToast")),
                  onError: (err) => toast.error(`${t("approvalRules.failedToast")}: ${err.message}`),
                });
                setDeleting(null);
              }}
            >
              {t("approvalRules.delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

// RuleCreateForm 手工创建一条规则：kind 由外层 Tab 决定，字段随 kind 切换。
function RuleCreateForm({
  kind,
  projectId,
  onDone,
}: {
  kind: "command" | "http";
  projectId?: string;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const create = useCreateApprovalRule();
  const [program, setProgram] = useState("");
  const [contains, setContains] = useState("");
  const [method, setMethod] = useState("POST");
  const [host, setHost] = useState("");
  const [pathPrefix, setPathPrefix] = useState("");
  const [expiresInDays, setExpiresInDays] = useState("30");
  // v4.6: allow = 豁免（approve & remember）；deny = 范围观测 Agent 自动拒绝。
  const [decision, setDecision] = useState<"allow" | "deny">("allow");

  const valid =
    !!projectId &&
    (kind === "command" ? program.trim() !== "" : host.trim() !== "");

  function submit() {
    if (!projectId || !valid) return;
    const match =
      kind === "command"
        ? { program: program.trim(), contains: contains.trim() }
        : { method, host: host.trim(), path_prefix: pathPrefix.trim().replace(/^\/+/, "") };
    const req: ApprovalRuleRequest = {
      project_id: projectId,
      kind,
      decision,
      match,
    };
    if (expiresInDays !== "never") {
      req.expires_at = new Date(
        Date.now() + Number(expiresInDays) * 86_400_000,
      ).toISOString();
    }
    create.mutate(req, {
      onSuccess: () => {
        toast.success(t("approvalRules.createdToast"));
        onDone();
      },
      onError: (err) => toast.error(`${t("approvalRules.failedToast")}: ${err.message}`),
    });
  }

  return (
    <div className="space-y-3 rounded-md border border-border bg-card p-4">
      <div className="space-y-1">
        <Label>{t("approvalRules.fieldDecision")}</Label>
        <Select value={decision} onValueChange={(v) => setDecision(v as "allow" | "deny")}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="allow">{t("approvalRules.decisionAllow")}</SelectItem>
            <SelectItem value="deny">{t("approvalRules.decisionDeny")}</SelectItem>
          </SelectContent>
        </Select>
        <p className="text-[11px] text-muted-foreground">
          {t("approvalRules.decisionHint")}
        </p>
      </div>
      {kind === "command" ? (
        <>
          <div className="space-y-1">
            <Label htmlFor="rule-program">{t("approvalRules.fieldProgram")}</Label>
            <Input
              id="rule-program"
              value={program}
              onChange={(e) => setProgram(e.target.value)}
              placeholder="curl"
              className="mono"
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="rule-contains">{t("approvalRules.fieldContains")}</Label>
            <Input
              id="rule-contains"
              value={contains}
              onChange={(e) => setContains(e.target.value)}
              placeholder="api.target-x.com"
              className="mono"
            />
          </div>
        </>
      ) : (
        <>
          <div className="grid grid-cols-[110px_1fr] gap-2">
            <div className="space-y-1">
              <Label htmlFor="rule-method">{t("approval.method")}</Label>
              <Select value={method} onValueChange={setMethod}>
                <SelectTrigger id="rule-method">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {["POST", "PUT", "PATCH", "DELETE"].map((m) => (
                    <SelectItem key={m} value={m} className="mono">
                      {m}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1">
              <Label htmlFor="rule-host">{t("approvalRules.fieldHost")}</Label>
              <Input
                id="rule-host"
                value={host}
                onChange={(e) => setHost(e.target.value)}
                placeholder="api.target-x.com"
                className="mono"
              />
            </div>
          </div>
          <div className="space-y-1">
            <Label htmlFor="rule-path">{t("approvalRules.fieldPathPrefix")}</Label>
            <Input
              id="rule-path"
              value={pathPrefix}
              onChange={(e) => setPathPrefix(e.target.value)}
              placeholder="api/login"
              className="mono"
            />
          </div>
        </>
      )}
      <div className="flex items-center justify-between gap-2 pt-1">
        <Select value={expiresInDays} onValueChange={setExpiresInDays}>
          <SelectTrigger className="w-44" aria-label={t("approvalRules.expiresIn")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="7">{t("approvalRules.expires7d")}</SelectItem>
            <SelectItem value="30">{t("approvalRules.expires30d")}</SelectItem>
            <SelectItem value="never">{t("approvalRules.expiresNever")}</SelectItem>
          </SelectContent>
        </Select>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="ghost" onClick={onDone}>
            {t("common.cancel")}
          </Button>
          <Button size="sm" disabled={!valid || create.isPending} onClick={submit}>
            {t("approvalRules.create")}
          </Button>
        </div>
      </div>
    </div>
  );
}

// ruleMatchText 渲染规则的 mono 匹配式（与后端 ruleDisplayText 对齐）。
export function ruleMatchText(rule: ApprovalRule): string {
  if (rule.kind === "http") {
    const path = (rule.match.path_prefix ?? "").replace(/^\/+/, "");
    return `${(rule.match.method ?? "").toUpperCase()} ${rule.match.host ?? ""}${path ? `/${path}` : ""}`;
  }
  const program = rule.match.program ?? "";
  return rule.match.contains ? `${program} ~${rule.match.contains}` : program;
}
