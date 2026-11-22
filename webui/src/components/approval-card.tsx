import { useMemo, useState } from "react";
import { Check, ChevronDown, Clock, TriangleAlert, X } from "lucide-react";
import type { Approval, ApprovalRuleDraft } from "@/types/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/i18n";
import { useCountdown, formatCountdown } from "@/lib/use-countdown";
import { approvalRuleDraft } from "@/lib/approval-rule";
import { cn } from "@/lib/utils";

/**
 * One HITL approval card.
 *
 * 4zreco-pattern: the action content (command / request line) sits on top in
 * mono, the full JSON folds away, and the context slice (facts + contradiction
 * count) sits beside the decision buttons. The deadline countdown is the
 * visual anchor — timeout means rejection, so remaining time is always shown
 * and turns urgent under a minute.
 *
 * 「批准并记住」（契约 §3.4）：记住开关打开后显示预填规则草稿（程序名 +
 * 目标主机子串 / method+host+path 前缀），批准时经 onDecideWithRule 一并
 * 创建规则。deny 形状不受规则影响——后端匹配期兜底。
 */
export function ApprovalCard({
  approval,
  deciding,
  focused,
  onDecide,
  onDecideWithRule,
}: {
  approval: Approval;
  deciding?: boolean;
  focused?: boolean;
  onDecide: (a: Approval, decision: "approved" | "rejected") => void;
  onDecideWithRule?: (
    a: Approval,
    decision: "approved" | "rejected",
    draft: ApprovalRuleDraft,
  ) => void;
}) {
  const { t } = useI18n();
  const [showJson, setShowJson] = useState(false);
  const pending = approval.status === "pending";
  const { remainingMs, expired } = useCountdown(
    pending ? approval.deadline_at : undefined,
  );
  const urgent = pending && !expired && (remainingMs ?? 0) < 60_000;

  // 「批准并记住」：草稿按 approval kind 预填一次（pending 期间不随输入抖动）。
  const initialDraft = useMemo(() => approvalRuleDraft(approval), [approval]);
  const [remember, setRemember] = useState(false);
  const [draft, setDraft] = useState<ApprovalRuleDraft | null>(initialDraft);
  const rememberAvailable = pending && !!onDecideWithRule && !!initialDraft;

  function onApprove() {
    if (remember && rememberAvailable && draft) {
      onDecideWithRule(approval, "approved", draft);
      return;
    }
    onDecide(approval, "approved");
  }

  return (
    <div
      id={`approval-${approval.id}`}
      className={cn(
        "rounded-md border bg-card transition-colors",
        focused ? "border-ring ring-1 ring-ring" : "border-border",
        !pending && "opacity-75",
      )}
    >
      {/* Card head: kind + target + status + countdown */}
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-2.5">
        <span
          className={cn(
            "rounded-sm px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider mono",
            approval.kind === "command"
              ? "bg-blue-500/10 text-blue-700 dark:text-blue-300"
              : "bg-sky-500/10 text-sky-700 dark:text-sky-300",
          )}
        >
          {approval.kind === "command" ? t("approval.kindCommand") : t("approval.kindHttp")}
        </span>
        <span className="min-w-0 flex-1 truncate mono text-sm font-medium">
          {approval.payload.command || `${approval.payload.method} ${approval.payload.url}`}
        </span>
        {pending ? (
          <span
            className={cn(
              "flex shrink-0 items-center gap-1 rounded-sm px-1.5 py-0.5 text-xs tabular-nums mono",
              urgent
                ? "bg-red-500/10 text-red-700 dark:text-red-300"
                : "bg-amber-500/10 text-amber-700 dark:text-amber-300",
            )}
          >
            <Clock className="h-3 w-3" aria-hidden />
            {expired ? t("approval.timeout") : formatCountdown(remainingMs ?? 0)}
          </span>
        ) : (
          <ApprovalStatusChip status={approval.status} />
        )}
      </div>

      <div className="space-y-3 p-4">
        {/* Payload detail */}
        {approval.kind === "http" && (
          <div className="space-y-1 text-xs">
            {approval.payload.url && (
              <div className="flex gap-2">
                <span className="w-14 shrink-0 text-muted-foreground">{t("approval.url")}</span>
                <span className="min-w-0 break-all mono text-foreground">{approval.payload.url}</span>
              </div>
            )}
            {approval.payload.method && (
              <div className="flex gap-2">
                <span className="w-14 shrink-0 text-muted-foreground">{t("approval.method")}</span>
                <span className="mono text-foreground">{approval.payload.method}</span>
              </div>
            )}
            {approval.payload.body_excerpt && (
              <div className="flex gap-2">
                <span className="w-14 shrink-0 text-muted-foreground">{t("approval.body")}</span>
                <pre className="min-w-0 flex-1 overflow-auto whitespace-pre-wrap break-words rounded-sm bg-muted/60 p-2 mono text-[11px]">
                  {approval.payload.body_excerpt}
                </pre>
              </div>
            )}
          </div>
        )}
        {approval.payload.workdir && (
          <div className="flex gap-2 text-xs">
            <span className="w-14 shrink-0 text-muted-foreground">{t("approval.workdir")}</span>
            <span className="min-w-0 break-all mono">{approval.payload.workdir}</span>
          </div>
        )}
        {approval.payload.reason && (
          <div className="flex gap-2 text-xs">
            <span className="w-14 shrink-0 text-muted-foreground">{t("approval.reason")}</span>
            <span className="flex min-w-0 items-center gap-1.5 text-amber-700 dark:text-amber-300">
              <TriangleAlert className="h-3 w-3 shrink-0" aria-hidden />
              {approval.payload.reason}
            </span>
          </div>
        )}

        {/* Context slice (the approval's evidence panorama) */}
        {approval.slice && approval.slice.facts.length > 0 && (
          <div className="rounded-sm border border-border bg-muted/30 p-3">
            <div className="mb-2 flex items-center justify-between gap-2">
              <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                {t("approval.slice")}
              </span>
              {(approval.slice.contradiction_count ?? 0) > 0 && (
                <span className="rounded-sm bg-orange-500/10 px-1.5 py-0.5 text-[10px] text-orange-700 dark:text-orange-300">
                  {t("approval.contradictions")} {approval.slice.contradiction_count}
                </span>
              )}
            </div>
            <div className="space-y-1.5">
              {approval.slice.subject && (
                <div className="text-xs text-muted-foreground">
                  {t("approval.subject")}: <span className="mono text-foreground">{approval.slice.subject}</span>
                </div>
              )}
              <ul className="space-y-1">
                {approval.slice.facts.map((f, i) => (
                  <li key={i} className="flex items-start gap-2 text-xs">
                    <span
                      className={cn(
                        "mt-0.5 h-1.5 w-1.5 shrink-0 rounded-full",
                        f.kind === "observed"
                          ? "bg-sky-500"
                          : f.kind === "inferred"
                            ? "bg-violet-500"
                            : "bg-emerald-500",
                      )}
                      aria-hidden
                    />
                    <span className="min-w-0 flex-1 text-foreground">{f.content}</span>
                    <span className="shrink-0 text-[10px] uppercase text-muted-foreground mono">
                      {t(`fact.${f.kind}`)}
                      {typeof f.confidence === "number"
                        ? ` · ${(f.confidence * 100).toFixed(0)}%`
                        : ""}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          </div>
        )}

        {/* Full payload JSON, folded */}
        <div>
          <button
            type="button"
            onClick={() => setShowJson((v) => !v)}
            className="flex items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
            aria-expanded={showJson}
          >
            <ChevronDown
              className={cn("h-3 w-3 transition-transform", showJson && "rotate-180")}
              aria-hidden
            />
            {t("approval.rawJson")}
          </button>
          {showJson && (
            <pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-sm bg-muted/60 p-2 text-[11px] text-muted-foreground">
              {JSON.stringify(
                { kind: approval.kind, payload: approval.payload, run_id: approval.run_id },
                null,
                2,
              )}
            </pre>
          )}
        </div>

        {/* Decision row */}
        {pending ? (
          <div className="space-y-2 border-t border-border pt-3">
            {rememberAvailable && (
              <div className="flex items-center gap-2">
                <Switch
                  id={`remember-${approval.id}`}
                  checked={remember}
                  onCheckedChange={(v) => setRemember(v)}
                  aria-label={t("approvalRules.remember")}
                />
                <Label
                  htmlFor={`remember-${approval.id}`}
                  className="cursor-pointer text-xs text-muted-foreground"
                >
                  {t("approvalRules.remember")}
                </Label>
              </div>
            )}
            {remember && rememberAvailable && draft && (
              <div className="rounded-sm border border-border bg-muted/30 p-2.5">
                {draft.kind === "command" ? (
                  <div className="grid gap-2 sm:grid-cols-[minmax(0,120px)_minmax(0,1fr)]">
                    <div className="space-y-1">
                      <Label htmlFor={`rule-program-${approval.id}`} className="text-[11px] text-muted-foreground">
                        {t("approvalRules.fieldProgram")}
                      </Label>
                      <Input
                        id={`rule-program-${approval.id}`}
                        value={draft.match.program ?? ""}
                        onChange={(e) =>
                          setDraft({ ...draft, match: { ...draft.match, program: e.target.value } })
                        }
                        className="mono h-8 text-xs"
                      />
                    </div>
                    <div className="space-y-1">
                      <Label htmlFor={`rule-contains-${approval.id}`} className="text-[11px] text-muted-foreground">
                        {t("approvalRules.fieldContains")}
                      </Label>
                      <Input
                        id={`rule-contains-${approval.id}`}
                        value={draft.match.contains ?? ""}
                        onChange={(e) =>
                          setDraft({ ...draft, match: { ...draft.match, contains: e.target.value } })
                        }
                        placeholder={t("approvalRules.containsPlaceholder")}
                        className="mono h-8 text-xs"
                      />
                    </div>
                  </div>
                ) : (
                  <div className="grid gap-2 sm:grid-cols-[minmax(0,96px)_minmax(0,1fr)_minmax(0,1fr)]">
                    <div className="space-y-1">
                      <Label htmlFor={`rule-method-${approval.id}`} className="text-[11px] text-muted-foreground">
                        {t("approval.method")}
                      </Label>
                      <Input
                        id={`rule-method-${approval.id}`}
                        value={draft.match.method ?? ""}
                        disabled
                        className="mono h-8 text-xs"
                      />
                    </div>
                    <div className="space-y-1">
                      <Label htmlFor={`rule-host-${approval.id}`} className="text-[11px] text-muted-foreground">
                        {t("approvalRules.fieldHost")}
                      </Label>
                      <Input
                        id={`rule-host-${approval.id}`}
                        value={draft.match.host ?? ""}
                        onChange={(e) =>
                          setDraft({ ...draft, match: { ...draft.match, host: e.target.value } })
                        }
                        className="mono h-8 text-xs"
                      />
                    </div>
                    <div className="space-y-1">
                      <Label htmlFor={`rule-path-${approval.id}`} className="text-[11px] text-muted-foreground">
                        {t("approvalRules.fieldPathPrefix")}
                      </Label>
                      <Input
                        id={`rule-path-${approval.id}`}
                        value={draft.match.path_prefix ?? ""}
                        onChange={(e) =>
                          setDraft({ ...draft, match: { ...draft.match, path_prefix: e.target.value } })
                        }
                        placeholder={t("approvalRules.anyPath")}
                        className="mono h-8 text-xs"
                      />
                    </div>
                  </div>
                )}
                <p className="mt-1.5 text-[11px] text-muted-foreground">{t("approvalRules.rememberHint")}</p>
              </div>
            )}
            <div className="flex items-center gap-2">
              <Button
                size="sm"
                variant="destructive"
                disabled={deciding}
                onClick={() => onDecide(approval, "rejected")}
              >
                <X className="h-3.5 w-3.5" />
                {t("approval.reject")}
              </Button>
              <Button size="sm" disabled={deciding} onClick={onApprove}>
                <Check className="h-3.5 w-3.5" />
                {t("approval.approve")}
              </Button>
              <span className="ml-auto text-[11px] text-muted-foreground">
                {t("approval.timeoutHint")}
              </span>
            </div>
          </div>
        ) : (
          <div className="flex flex-wrap gap-x-4 gap-y-1 border-t border-border pt-3 text-xs text-muted-foreground">
            {approval.reason && (
              <span>
                {t("approval.decisionReason")}: <span className="text-foreground">{approval.reason}</span>
              </span>
            )}
            {approval.decided_by && (
              <span className="mono">{t("approval.decidedBy")}: {approval.decided_by}</span>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function ApprovalStatusChip({ status }: { status: Approval["status"] }) {
  const { t } = useI18n();
  const cls =
    status === "approved"
      ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300"
      : status === "rejected"
        ? "border-red-500/30 bg-red-500/10 text-red-700 dark:text-red-300"
        : status === "timeout"
          ? "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300"
          : "border-border bg-muted text-muted-foreground";
  return (
    <span className={cn("shrink-0 rounded-sm border px-1.5 py-0.5 text-[10px] uppercase tracking-wider mono", cls)}>
      {t(`approval.status.${status}`)}
    </span>
  );
}
