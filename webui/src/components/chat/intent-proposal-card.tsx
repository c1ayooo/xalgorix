import { useState } from "react";
import { Link } from "react-router-dom";
import { ArrowUpRight, Check, Crosshair, Loader2, X } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  useConfirmIntentProposal,
  useInstanceAgents,
  useRejectIntentProposal,
  useScanDecisions,
  useScanPlan,
} from "@/api/queries";
import { cn } from "@/lib/utils";
import { useI18n } from "@/i18n";
import type { IntentProposal } from "@/types/api";

/**
 * Main-agent intent draft card (提案 → 确认 → 写入).
 *
 * The main agent never writes the plan directly: its propose_intent call lands
 * here as a pending draft. The operator's click is the deterministic
 * confirmation path (no model turn); reject discards the draft without
 * touching the exploration graph. Both pages' conversation projections render
 * this same card.
 */
export function IntentProposalCard({
  instanceId,
  taskId,
  proposal,
}: {
  instanceId?: string;
  /** Platform task id, used to deep-link the worker workspace. */
  taskId?: string;
  proposal: IntentProposal;
}) {
  const { t } = useI18n();
  const confirm = useConfirmIntentProposal();
  const reject = useRejectIntentProposal();
  // Local override so the card flips immediately; the SSE/refetch projection
  // is the durable source and will agree a moment later. task_id comes from
  // the confirm response so the plan row is visible before the next refetch.
  const [acted, setActed] = useState<"confirmed" | "rejected" | null>(null);
  const [confirmedTaskID, setConfirmedTaskID] = useState<string | undefined>(undefined);
  const status = acted ?? proposal.status ?? "pending";
  const taskID = confirmedTaskID ?? proposal.task_id;

  // Live dispatch receipt: the graph plan supplies intent state, the worker
  // projection supplies executions, the decision inbox supplies blocked
  // reasons. This is the "what happened after dispatch" view — it never
  // streams worker tool output into the operator conversation.
  const plan = useScanPlan(instanceId, !!instanceId);
  const agents = useInstanceAgents(instanceId, !!instanceId);
  const decisions = useScanDecisions(instanceId ?? "", !!instanceId);
  const planTask = plan.data?.tasks?.find((task) => task.id === taskID);
  const unit = agents.data?.units?.find((entry) => entry.unit_id === taskID);
  const blockedReason = decisions.data?.blocked?.find((entry) => entry.unit_id === taskID)?.blocked_reason;
  const receipt = deriveReceipt(unit?.state, planTask?.status, status);
  const worker = unit?.agent_ids?.length ? unit.agent_ids[unit.agent_ids.length - 1] : undefined;
  const workerLink =
    instanceId && taskId && taskID
      ? `/tasks/${taskId}?tab=sessions&run=${encodeURIComponent(instanceId)}&agent=${encodeURIComponent(`unit:${taskID}`)}`
      : undefined;
  const pending = status === "pending";
  const busy = confirm.isPending || reject.isPending;
  const actionable = pending && !!instanceId;

  const tone =
    status === "confirmed"
      ? "border-emerald-500/40 bg-emerald-500/5"
      : status === "rejected"
        ? "border-border bg-muted/30"
        : "border-sky-500/40 bg-sky-500/5";

  function onConfirm() {
    if (!instanceId || busy) return;
    confirm.mutate(
      { instanceId, proposalId: proposal.id },
      {
        onSuccess: (result) => {
          setActed("confirmed");
          setConfirmedTaskID(result.task_id);
          toast.success(t("chat.proposalConfirmedToast"));
        },
        onError: (error: Error) =>
          toast.error(`${t("chat.proposalConfirmFailed")}: ${error.message}`),
      },
    );
  }

  function onReject() {
    if (!instanceId || busy) return;
    reject.mutate(
      { instanceId, proposalId: proposal.id },
      {
        onSuccess: () => {
          setActed("rejected");
          toast.success(t("chat.proposalRejectedToast"));
        },
        onError: (error: Error) =>
          toast.error(`${t("chat.proposalRejectFailed")}: ${error.message}`),
      },
    );
  }

  return (
    <div className={cn("my-2 min-w-0 rounded-lg border p-3", tone)}>
      <div className="flex min-w-0 items-center gap-2">
        <Crosshair className="size-3.5 shrink-0 text-sky-600 dark:text-sky-400" aria-hidden />
        <span className="font-medium text-xs">{t("chat.proposalTitle")}</span>
        <span
          className={cn(
            "ml-auto shrink-0 rounded-full border px-1.5 py-0.5 text-[10px] leading-none",
            status === "confirmed"
              ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
              : status === "rejected"
                ? "border-border bg-muted text-muted-foreground"
                : "border-sky-500/30 bg-sky-500/10 text-sky-600 dark:text-sky-400",
          )}
        >
          {t(`chat.proposalStatus.${status}`)}
        </span>
      </div>

      <p className="mt-1.5 break-words font-medium text-sm">{proposal.title || proposal.id}</p>

      <dl className="mt-1.5 grid min-w-0 gap-x-3 gap-y-0.5 font-mono text-[10px] text-muted-foreground sm:grid-cols-2">
        {proposal.target && (
          <div className="min-w-0">
            <dt className="inline">{t("chat.proposalTarget")}: </dt>
            <dd className="inline break-all">
              {proposal.target}
              {proposal.target_kind ? ` (${proposal.target_kind})` : ""}
            </dd>
          </div>
        )}
        {proposal.phase ? (
          <div>
            <dt className="inline">{t("chat.proposalPhase")}: </dt>
            <dd className="inline">P{proposal.phase}</dd>
          </div>
        ) : null}
        {proposal.vuln_class && (
          <div>
            <dt className="inline">{t("chat.proposalVuln")}: </dt>
            <dd className="inline">{proposal.vuln_class}</dd>
          </div>
        )}
        {proposal.endpoint && (
          <div className="min-w-0">
            <dt className="inline">{t("chat.proposalEndpoint")}: </dt>
            <dd className="inline break-all">{proposal.endpoint}</dd>
          </div>
        )}
        {status === "confirmed" && taskID && (
          <div className="min-w-0">
            <dt className="inline">{t("chat.proposalTask")}: </dt>
            <dd className="inline break-all">{taskID}</dd>
          </div>
        )}
      </dl>

      {status === "confirmed" && (
        <div className="mt-2 rounded-md border border-border/70 bg-background/50 p-2 text-[11px]">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <span className="font-medium text-foreground">{t("chat.receiptTitle")}</span>
            <span
              className={cn(
                "rounded-full border px-1.5 py-0.5 text-[10px] leading-none",
                receipt === "done"
                  ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
                  : receipt === "blocked"
                    ? "border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-400"
                    : receipt === "running"
                      ? "border-sky-500/30 bg-sky-500/10 text-sky-600 dark:text-sky-400"
                      : "border-border bg-muted text-muted-foreground",
              )}
            >
              {t(`chat.receiptState.${receipt}`)}
            </span>
            {worker && <span className="font-mono text-muted-foreground">{worker}</span>}
            <Link
              to={workerLink ?? "#"}
              aria-disabled={!workerLink}
              className={cn(
                "ml-auto inline-flex items-center gap-0.5 underline underline-offset-2",
                workerLink
                  ? "text-primary hover:text-primary/80"
                  : "pointer-events-none text-muted-foreground/50 no-underline",
              )}
            >
              {t("chat.receiptViewWorker")}
              <ArrowUpRight className="size-3" aria-hidden />
            </Link>
          </div>
          {receipt === "blocked" && blockedReason && (
            <p className="mt-1 break-words text-red-600 dark:text-red-400">{blockedReason}</p>
          )}
          {receipt === "done" && (
            <p className="mt-1 text-muted-foreground">{t("chat.receiptDoneHint")}</p>
          )}
        </div>
      )}

      {proposal.notes && (
        <p className="mt-1.5 break-words text-muted-foreground text-xs">{proposal.notes}</p>
      )}

      {actionable && (
        <div className="mt-2 flex items-center gap-2">
          <Button size="sm" className="h-7 px-2 text-xs" disabled={busy} onClick={onConfirm}>
            {confirm.isPending ? (
              <Loader2 className="size-3.5 animate-spin" aria-hidden />
            ) : (
              <Check className="size-3.5" aria-hidden />
            )}
            {t("chat.proposalConfirm")}
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="h-7 px-2 text-xs"
            disabled={busy}
            onClick={onReject}
          >
            <X className="size-3.5" aria-hidden />
            {t("chat.proposalReject")}
          </Button>
        </div>
      )}
    </div>
  );
}

type ReceiptState = "pending" | "queued" | "running" | "done" | "blocked";

function deriveReceipt(
  unitState: string | undefined,
  planStatus: string | undefined,
  proposalStatus: string,
): ReceiptState {
  if (proposalStatus === "pending") return "pending";
  if (proposalStatus === "rejected") return "blocked";
  switch ((unitState ?? "").toLowerCase()) {
    case "open":
      return "queued";
    case "running":
    case "paused":
      return "running";
    case "done":
      return "done";
    case "blocked":
    case "exhausted":
    case "stopped":
    case "deleted":
      return "blocked";
  }
  switch ((planStatus ?? "").toLowerCase()) {
    case "pending":
      return "queued";
    case "active":
      return "running";
    case "completed":
      return "done";
    case "skipped":
      return "blocked";
  }
  return "queued";
}
