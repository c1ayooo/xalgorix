import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import { Check, X } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ApprovalCard } from "@/components/approval-card";
import { EmptyState, ErrorState } from "@/components/states";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useApprovals,
  useBatchDecideApprovals,
  useDecideApproval,
} from "@/api/queries";
import { useActiveProjectId } from "@/lib/use-active-project";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { Approval } from "@/types/api";

const isMac =
  typeof navigator !== "undefined" && /mac/i.test(navigator.platform || "");

/**
 * Approval queue — the HITL decision surface.
 *
 * 4zreco-pattern: inline decisions on each card, j/k to move the cursor,
 * a to approve / r to reject the focused item, b to toggle select-all for
 * batch decisions. Deep link: /approvals?focus=<id> scrolls to and
 * highlights one card (consumed via replaceState).
 */
export default function ApprovalsPage() {
  const { t } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const projectId = useActiveProjectId();
  const [statusFilter, setStatusFilter] = useState<"pending" | "decided">("pending");
  const [cursor, setCursor] = useState(0);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(() => new Set());
  const [focusedId, setFocusedId] = useState<string | null>(
    searchParams.get("focus"),
  );

  const { data, isLoading, error, refetch } = useApprovals({
    status: statusFilter,
    projectId: projectId ?? undefined,
  });
  const decide = useDecideApproval();
  const batch = useBatchDecideApprovals();

  const approvals = useMemo(() => {
    const list = data?.approvals ?? [];
    // Pending first by deadline (nearest expiry on top), decided by recency.
    return [...list].sort((a, b) =>
      statusFilter === "pending"
        ? a.deadline_at < b.deadline_at
          ? -1
          : 1
        : (b.decided_at ?? "") < (a.decided_at ?? "")
          ? -1
          : 1,
    );
  }, [data, statusFilter]);

  const visibleIds = useMemo(() => approvals.map((a) => a.id), [approvals]);

  // Consume ?focus= deep link: keep the id for highlight, drop it from the URL.
  useEffect(() => {
    if (searchParams.get("focus")) {
      const sp = new URLSearchParams(searchParams);
      sp.delete("focus");
      setSearchParams(sp, { replace: true });
    }
  }, [searchParams, setSearchParams]);

  const onDecide = useCallback(
    (a: Approval, decision: "approved" | "rejected") => {
      decide.mutate(
        { id: a.id, decision },
        {
          onSuccess: () =>
            toast.success(
              decision === "approved"
                ? t("toast.approvalApproved")
                : t("toast.approvalRejected"),
            ),
          onError: (err) =>
            toast.error(`${t("toast.approvalFailed")}: ${err.message}`),
        },
      );
    },
    [decide, t],
  );

  // Keyboard flow: j/k move, a approve, r reject, b select-all.
  const approvalsRef = useRef(approvals);
  approvalsRef.current = approvals;
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      if (
        target &&
        (target.tagName === "INPUT" ||
          target.tagName === "TEXTAREA" ||
          target.tagName === "SELECT" ||
          target.isContentEditable)
      ) {
        return;
      }
      const list = approvalsRef.current;
      if (list.length === 0) return;
      if (e.key === "j") {
        setCursor((c) => Math.min(c + 1, list.length - 1));
      } else if (e.key === "k") {
        setCursor((c) => Math.max(c - 1, 0));
      } else if (e.key === "a" || e.key === "r") {
        const item = list[Math.min(cursor, list.length - 1)];
        if (item?.status === "pending") {
          onDecide(item, e.key === "a" ? "approved" : "rejected");
        }
      } else if (e.key === "b") {
        setSelectedIds((current) => {
          const all = new Set(visibleIds);
          const isAll = visibleIds.every((id) => current.has(id));
          return isAll ? new Set() : all;
        });
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [cursor, onDecide, visibleIds]);

  // Keep the cursor pointing at a valid entry as the list changes.
  useEffect(() => {
    if (cursor > approvals.length - 1) setCursor(0);
  }, [approvals.length, cursor]);

  const pendingCount = approvals.filter((a) => a.status === "pending").length;
  const selectedPending = [...selectedIds].filter((id) =>
    approvals.some((a) => a.id === id && a.status === "pending"),
  );

  function batchDecide(decision: "approved" | "rejected") {
    batch.mutate(
      { ids: selectedPending, decision },
      {
        onSuccess: (res) =>
          toast.success(
            `${t("toast.batchDone")}: ${decision === "approved" ? res.approved : res.rejected}`,
          ),
        onError: (err) =>
          toast.error(`${t("toast.approvalFailed")}: ${err.message}`),
      },
    );
    setSelectedIds(new Set());
  }

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="font-sans text-2xl font-semibold tracking-tight">
            {t("approvals.title")}
          </h1>
          <p className="text-sm text-muted-foreground">{t("approvals.subtitle")}</p>
        </div>
        <div className="flex items-center gap-2">
          <span className="hidden items-center gap-1 text-[11px] text-muted-foreground md:flex">
            <Kbd>{isMac ? "⌘" : "Ctrl"}</Kbd>
            <span>+</span>
            <Kbd>K</Kbd>
            <span>·</span>
            <Kbd>j</Kbd>
            <Kbd>k</Kbd>
            <Kbd>a</Kbd>
            <Kbd>r</Kbd>
            <Kbd>b</Kbd>
          </span>
          <Select
            value={statusFilter}
            onValueChange={(v) => {
              setStatusFilter(v as "pending" | "decided");
              setSelectedIds(new Set());
              setCursor(0);
            }}
          >
            <SelectTrigger className="w-40" aria-label={t("approvals.filter")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="pending">{t("approvals.filterPending")}</SelectItem>
              <SelectItem value="decided">{t("approvals.filterDecided")}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </header>

      {selectedPending.length > 0 && statusFilter === "pending" && (
        <Card>
          <CardContent className="flex flex-wrap items-center gap-2 p-3">
            <span className="text-xs text-muted-foreground">
              {t("approvals.selected")} {selectedPending.length}
            </span>
            <Button size="sm" variant="destructive" disabled={batch.isPending} onClick={() => batchDecide("rejected")}>
              <X className="h-3.5 w-3.5" />
              {t("approval.reject")}
            </Button>
            <Button size="sm" disabled={batch.isPending} onClick={() => batchDecide("approved")}>
              <Check className="h-3.5 w-3.5" />
              {t("approval.approve")}
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setSelectedIds(new Set())}>
              {t("common.clearSelection")}
            </Button>
          </CardContent>
        </Card>
      )}

      {error ? (
        <ErrorState
          title={t("approvals.loadFailed")}
          description={error instanceof Error ? error.message : ""}
          action={
            <Button size="sm" variant="outline" onClick={() => refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : isLoading ? (
        <div className="space-y-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-40 w-full" />
          ))}
        </div>
      ) : approvals.length === 0 ? (
        <EmptyState
          title={statusFilter === "pending" ? t("approvals.emptyPendingTitle") : t("approvals.emptyDecidedTitle")}
          description={
            statusFilter === "pending"
              ? t("approvals.emptyPendingDescription")
              : t("approvals.emptyDecidedDescription")
          }
        />
      ) : (
        <div className="space-y-3">
          {approvals.map((a, i) => (
            <div key={a.id} className="flex items-start gap-2">
              <label className="flex h-10 shrink-0 items-center">
                <input
                  type="checkbox"
                  checked={selectedIds.has(a.id)}
                  onChange={(e) =>
                    setSelectedIds((current) => {
                      const next = new Set(current);
                      if (e.target.checked) next.add(a.id);
                      else next.delete(a.id);
                      return next;
                    })
                  }
                  aria-label={`${t("approvals.selectAria")} ${a.id}`}
                  className="h-4 w-4 rounded border-border bg-input accent-primary focus:outline-none focus:ring-1 focus:ring-ring"
                />
              </label>
              <div
                className={cn(
                  "min-w-0 flex-1 cursor-pointer rounded-md transition-shadow",
                  i === cursor && "ring-1 ring-ring/40",
                )}
                onClick={() => setCursor(i)}
              >
                <ApprovalCard
                  approval={a}
                  deciding={decide.isPending}
                  focused={focusedId === a.id}
                  onDecide={onDecide}
                />
              </div>
            </div>
          ))}
          {statusFilter === "pending" && (
            <p className="text-center text-xs text-muted-foreground">
              {t("pulse.pending")} {pendingCount} · {t("approvals.autoTimeoutNote")}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

function Kbd({ children }: { children: string }) {
  return (
    <kbd className="rounded-sm border border-border bg-muted px-1 py-0.5 text-[10px] mono">
      {children}
    </kbd>
  );
}
