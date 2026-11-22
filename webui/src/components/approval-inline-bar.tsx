import { useState } from "react";
import { Link } from "react-router-dom";
import { toast } from "sonner";
import { Check, Clock, ExternalLink, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { useCreateApprovalRule, useDecideApproval } from "@/api/queries";
import { useI18n } from "@/i18n";
import { useActiveProjectId } from "@/lib/use-active-project";
import { useCountdown, formatCountdown } from "@/lib/use-countdown";
import { approvalRuleDraft } from "@/lib/approval-rule";
import { cn } from "@/lib/utils";
import type { Approval } from "@/types/api";

/**
 * Compact decision bar rendered inline in the event feed under an
 * `approval_requested` event (REDESIGN §7: approvals live where the operator
 * is looking, not only in the queue page). Decisions hit the same API as the
 * approvals page; a 409 (already decided elsewhere) flips the bar to a
 * decided chip with a deep link. Unfolded by default — HITL never hides
 * behind a collapsed row. 「批准并记住」走紧凑开关：按默认草稿建规则（契约
 * §3.4；要改粒度去队列页的卡片）。
 */
export function ApprovalInlineBar({ approval }: { approval: Approval }) {
  const { t } = useI18n();
  const decide = useDecideApproval();
  const createRule = useCreateApprovalRule();
  const projectId = useActiveProjectId();
  const [remember, setRemember] = useState(false);
  const [resolved, setResolved] = useState<
    "approved" | "rejected" | "conflict" | null
  >(null);
  const pending = approval.status === "pending" && !resolved;
  const { remainingMs, expired } = useCountdown(
    pending ? approval.deadline_at : undefined,
  );
  const urgent = pending && !expired && (remainingMs ?? 0) < 60_000;
  const draft = pending ? approvalRuleDraft(approval) : null;

  const summary =
    approval.payload.command ||
    `${approval.payload.method ?? ""} ${approval.payload.url ?? ""}`.trim();

  function onDecide(decision: "approved" | "rejected") {
    decide.mutate(
      { id: approval.id, decision },
      {
        onSuccess: () => {
          setResolved(decision);
          toast.success(
            decision === "approved"
              ? t("toast.approvalApproved")
              : t("toast.approvalRejected"),
          );
          if (decision === "approved" && remember && projectId && draft) {
            createRule.mutate(
              {
                project_id: projectId,
                kind: approval.kind,
                match: draft.match,
                created_from_approval_id: approval.id,
              },
              {
                onSuccess: () => toast.success(t("approvalRules.createdToast")),
                onError: (err) =>
                  toast.error(`${t("approvalRules.failedToast")}: ${err.message}`),
              },
            );
          }
        },
        onError: (err: unknown) => {
          const status =
            typeof err === "object" && err && "status" in err
              ? (err as { status: number }).status
              : 0;
          if (status === 409) {
            setResolved("conflict");
          } else {
            toast.error(`${t("toast.approvalFailed")}: ${String(err)}`);
          }
        },
      },
    );
  }

  return (
    <div
      className={cn(
        "rounded-md border bg-card px-3 py-2",
        pending
          ? urgent
            ? "border-red-500/40"
            : "border-amber-500/40"
          : "border-border opacity-80",
      )}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 truncate mono text-xs font-medium">
          {summary}
        </span>
        {pending && (
          <span
            className={cn(
              "flex shrink-0 items-center gap-1 rounded-sm px-1.5 py-0.5 text-[11px] tabular-nums mono",
              urgent
                ? "bg-red-500/10 text-red-700 dark:text-red-300"
                : "bg-amber-500/10 text-amber-700 dark:text-amber-300",
            )}
          >
            <Clock className="h-3 w-3" aria-hidden />
            {expired ? t("approval.timeout") : formatCountdown(remainingMs ?? 0)}
          </span>
        )}
        {resolved && (
          <span
            className={cn(
              "shrink-0 rounded-sm border px-1.5 py-0.5 text-[10px] uppercase tracking-wider mono",
              resolved === "approved"
                ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300"
                : resolved === "rejected"
                  ? "border-red-500/30 bg-red-500/10 text-red-700 dark:text-red-300"
                  : "border-border bg-muted text-muted-foreground",
            )}
          >
            {resolved === "conflict"
              ? t("approval.alreadyDecided")
              : t(`approval.status.${resolved}`)}
          </span>
        )}
      </div>
      {pending ? (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Button size="sm" variant="destructive" disabled={decide.isPending} onClick={() => onDecide("rejected")}>
            <X className="h-3.5 w-3.5" />
            {t("approval.reject")}
          </Button>
          <Button size="sm" disabled={decide.isPending} onClick={() => onDecide("approved")}>
            <Check className="h-3.5 w-3.5" />
            {t("approval.approve")}
          </Button>
          {draft && projectId && (
            <label className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
              <Switch
                checked={remember}
                onCheckedChange={setRemember}
                aria-label={t("approvalRules.remember")}
                className="h-4 w-7"
              />
              {t("approvalRules.rememberShort")}
            </label>
          )}
          <Link
            to={`/approvals?focus=${approval.id}`}
            className="ml-auto flex items-center gap-1 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
          >
            <ExternalLink className="h-3 w-3" aria-hidden />
            {t("approval.openQueue")}
          </Link>
        </div>
      ) : (
        <Link
          to={`/approvals?focus=${approval.id}`}
          className="mt-1.5 inline-flex items-center gap-1 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
        >
          <ExternalLink className="h-3 w-3" aria-hidden />
          {t("approval.openQueue")}
        </Link>
      )}
    </div>
  );
}
