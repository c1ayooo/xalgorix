import { useMemo, useState } from "react";
import {
  ChevronDown,
  ChevronRight,
  Compass,
  Database,
  DoorOpen,
  KeyRound,
} from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, ErrorState } from "@/components/states";
import { IntelInfoSummary } from "@/components/intel-info-summary";
import { useIntelTriage, useTaskIntelligence } from "@/api/queries";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type {
  IntelAccess,
  IntelItem,
  IntelKind,
  IntelTriageStatus,
} from "@/types/api";

/**
 * 任务情报（契约 §3.7）——上半区是冷路径信息项切片（InfoItem 区域/关联
 * 点亮，IntelInfoItems），下半区是凭据/线索/敏感数据三分视图 + 操作者
 * 分诊。二者是不同对象：intelligence.json 情报项 vs InfoItem 冷路径投影，
 * 分诊是独立的人类标注（overlay），不回写观测记录。
 */

const KIND_META: Record<
  IntelKind,
  { icon: typeof KeyRound; chip: string; key: string }
> = {
  credential: {
    icon: KeyRound,
    chip: "border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400",
    key: "intel.kind.credential",
  },
  lead: {
    icon: Compass,
    chip: "border-sky-500/30 bg-sky-500/10 text-sky-600 dark:text-sky-400",
    key: "intel.kind.lead",
  },
  sensitive_data: {
    icon: Database,
    chip: "border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-400",
    key: "intel.kind.sensitiveData",
  },
};

const ACCESS_VARIANT: Record<
  IntelAccess["state"],
  "default" | "success" | "muted" | "warning" | "destructive"
> = {
  proposed: "muted",
  confirmed: "success",
  active: "success",
  expired: "warning",
  revoked: "destructive",
};

const TRIAGE_VARIANT: Record<
  IntelTriageStatus,
  "default" | "success" | "muted"
> = {
  open: "default",
  confirmed: "success",
  dismissed: "muted",
};

function IntelRow({
  item,
  taskId,
}: {
  item: IntelItem;
  taskId: string;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [grantsOpen, setGrantsOpen] = useState(false);
  const [grantsValue, setGrantsValue] = useState(
    (item.access?.grants ?? []).join(", "),
  );
  const triage = useIntelTriage(taskId);
  const meta = KIND_META[item.kind];
  const Icon = meta.icon;
  const status: IntelTriageStatus = item.triage?.status ?? "open";
  const decided = status !== "open";
  return (
    <li className="border-b border-border last:border-b-0">
      <div className="flex flex-wrap items-center gap-2 px-3 py-2.5">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="flex min-w-0 flex-1 items-center gap-2 text-left"
          aria-expanded={open}
        >
          {open ? (
            <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          )}
          <span
            className={cn(
              "inline-flex shrink-0 items-center gap-1 rounded-full border px-1.5 text-[10px] uppercase tracking-wide",
              meta.chip,
            )}
          >
            <Icon className="h-3 w-3" aria-hidden />
            {t(meta.key)}
          </span>
          <span className="min-w-0 flex-1 truncate text-sm font-medium">
            {item.title}
          </span>
        </button>
        {item.endpoint && (
          <span className="max-w-56 shrink-0 truncate font-mono text-[11px] text-muted-foreground">
            {item.endpoint}
          </span>
        )}
        <span className="flex shrink-0 items-center gap-2 font-mono text-[10px] tabular-nums text-muted-foreground">
          {typeof item.confidence === "number" && (
            <span>{item.confidence.toFixed(2)}</span>
          )}
          <span title={item.scan_ids.join(", ")}>
            {item.scan_ids.length > 1
              ? `${item.scan_ids[0]} +${item.scan_ids.length - 1}`
              : item.scan_ids[0]}
          </span>
        </span>
        <Badge variant={TRIAGE_VARIANT[status]} className="shrink-0">
          {t(`intel.status.${status}`)}
        </Badge>
        {item.access && (
          <Badge
            variant={ACCESS_VARIANT[item.access.state]}
            className="shrink-0"
            title={item.access.grants?.join(", ")}
          >
            {t(`intelAccess.state.${item.access.state}`)}
          </Badge>
        )}
        {item.kind !== "sensitive_data" && (
          <Button
            size="sm"
            variant="ghost"
            className="h-7 shrink-0 px-1.5 text-muted-foreground"
            title={t("intel.grants.edit")}
            aria-label={t("intel.grants.edit")}
            onClick={() => {
              setGrantsValue((item.access?.grants ?? []).join(", "));
              setGrantsOpen(true);
            }}
          >
            <DoorOpen className="h-3.5 w-3.5" aria-hidden />
          </Button>
        )}
        {/* 分诊动作：未决给 确认/驳回，已决给 重开。 */}
        {!decided ? (
          <span className="flex shrink-0 items-center gap-1">
            <Button
              size="sm"
              variant="outline"
              className="h-7 px-2 text-xs"
              disabled={triage.isPending}
              onClick={() =>
                triage.mutate({ fingerprint: item.fingerprint, status: "confirmed" })
              }
            >
              {t("intel.triage.confirm")}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2 text-xs text-muted-foreground"
              disabled={triage.isPending}
              onClick={() =>
                triage.mutate({ fingerprint: item.fingerprint, status: "dismissed" })
              }
            >
              {t("intel.triage.dismiss")}
            </Button>
          </span>
        ) : (
          <Button
            size="sm"
            variant="ghost"
            className="h-7 px-2 text-xs text-muted-foreground"
            disabled={triage.isPending}
            onClick={() =>
              triage.mutate({ fingerprint: item.fingerprint, status: "open" })
            }
          >
            {t("intel.triage.reopen")}
          </Button>
        )}
      </div>
      {open && (
        <div className="space-y-2 px-3 pb-3 pl-[2.1rem]">
          <p className="whitespace-pre-wrap text-xs leading-relaxed text-foreground">
            {item.content}
          </p>
          {item.access && (
            <p className="font-mono text-[10px] text-muted-foreground">
              {t("intel.grants.current")}:{" "}
              {item.access.grants && item.access.grants.length > 0
                ? item.access.grants.join(", ")
                : t("intel.grants.none")}
            </p>
          )}
          {item.triage && (
            <p className="font-mono text-[10px] text-muted-foreground">
              {t("intel.triage.by")} {item.triage.decided_by} ·{" "}
              {new Date(item.triage.decided_at).toLocaleString()}
            </p>
          )}
        </div>
      )}
      <Dialog open={grantsOpen} onOpenChange={setGrantsOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{t("intel.grants.title")}</DialogTitle>
            <DialogDescription>{t("intel.grants.description")}</DialogDescription>
          </DialogHeader>
          <Input
            value={grantsValue}
            onChange={(e) => setGrantsValue(e.target.value)}
            placeholder={t("intel.grants.placeholder")}
            className="font-mono text-xs"
          />
          <div className="flex justify-end gap-2">
            <Button
              size="sm"
              variant="outline"
              onClick={() => setGrantsOpen(false)}
              disabled={triage.isPending}
            >
              {t("decisions.action.cancel")}
            </Button>
            <Button
              size="sm"
              disabled={triage.isPending}
              onClick={() => {
                const grants = grantsValue
                  .split(/[,\s]+/)
                  .map((v) => v.trim())
                  .filter(Boolean);
                triage.mutate(
                  {
                    fingerprint: item.fingerprint,
                    status,
                    grants: grants.length > 0 ? grants : undefined,
                  },
                  {
                    onSuccess: () => {
                      toast.success(t("intel.grants.saved"));
                      setGrantsOpen(false);
                    },
                    onError: (err) => {
                      toast.error(
                        `${t("intel.grants.savedFailed")}: ${
                          err instanceof Error ? err.message : String(err)
                        }`,
                      );
                    },
                  },
                );
              }}
            >
              {t("intel.grants.save")}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </li>
  );
}

export function IntelTab({
  taskId,
  projectId,
}: {
  taskId: string;
  projectId?: string;
}) {
  const { t } = useI18n();
  const { data, isLoading, error, refetch } = useTaskIntelligence(taskId);
  const [kindFilter, setKindFilter] = useState<IntelKind | "all">("all");
  const [statusFilter, setStatusFilter] = useState<IntelTriageStatus | "all">(
    "all",
  );

  const items = useMemo(() => {
    const all = data?.items ?? [];
    return all.filter(
      (it) =>
        (kindFilter === "all" || it.kind === kindFilter) &&
        (statusFilter === "all" || (it.triage?.status ?? "open") === statusFilter),
    );
  }, [data, kindFilter, statusFilter]);

  if (error) {
    return (
      <ErrorState
        title={t("intel.loadFailed")}
        description={error instanceof Error ? error.message : ""}
        action={
          <button
            type="button"
            onClick={() => refetch()}
            className="text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            {t("common.retry")}
          </button>
        }
      />
    );
  }
  if (isLoading && !data) {
    return <Skeleton className="h-64 w-full" />;
  }
  if (!data) return null;

  const counts = data.counts;
  const total = (counts?.credential ?? 0) + (counts?.lead ?? 0) + (counts?.sensitive_data ?? 0);

  return (
    <div className="space-y-3">
      {/* 信息项摘要：完整信息空间在 /info（作用域条 + 主体导航 + 三视图），
          这里只留计数与最近几条并深链——避免第二份实现漂移。
          project_id 用任务归属项目，不用活跃项目——后端按归属校验 task_id。 */}
      <IntelInfoSummary taskId={taskId} projectId={projectId} />
      <div className="border-t border-border" aria-hidden />

      {/* kind 过滤 chips（带计数）+ 分诊状态过滤。 */}
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={() => setKindFilter("all")}
          className={cn(
            "rounded-full border px-2.5 py-0.5 text-xs transition-colors",
            kindFilter === "all"
              ? "border-foreground bg-foreground text-background"
              : "border-border text-muted-foreground hover:text-foreground",
          )}
        >
          {t("intel.kindAll")}
          <span className="ml-1 font-mono tabular-nums">{total}</span>
        </button>
        {(Object.keys(KIND_META) as IntelKind[]).map((kind) => {
          const meta = KIND_META[kind];
          const Icon = meta.icon;
          return (
            <button
              key={kind}
              type="button"
              onClick={() => setKindFilter(kindFilter === kind ? "all" : kind)}
              className={cn(
                "inline-flex items-center gap-1 rounded-full border px-2.5 py-0.5 text-xs transition-colors",
                kindFilter === kind
                  ? meta.chip
                  : "border-border text-muted-foreground hover:text-foreground",
              )}
            >
              <Icon className="h-3 w-3" aria-hidden />
              {t(meta.key)}
              <span className="font-mono tabular-nums">
                {counts?.[kind] ?? 0}
              </span>
            </button>
          );
        })}
        <span className="mx-1 h-4 w-px bg-border" aria-hidden />
        {(["all", "open", "confirmed", "dismissed"] as const).map((st) => (
          <button
            key={st}
            type="button"
            onClick={() => setStatusFilter(st)}
            className={cn(
              "rounded-full px-2 py-0.5 text-xs transition-colors",
              statusFilter === st
                ? "bg-accent text-foreground"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {st === "all" ? t("intel.statusAll") : t(`intel.status.${st}`)}
          </button>
        ))}
      </div>

      {data.damaged_runs ? (
        <p className="rounded-sm border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-600 dark:text-amber-400">
          {data.note || t("intel.damaged")}
        </p>
      ) : null}

      {items.length === 0 ? (
        <EmptyState
          title={total === 0 ? t("intel.emptyTitle") : t("intel.filterEmptyTitle")}
          description={
            total === 0 ? t("intel.emptyDescription") : t("intel.filterEmptyDescription")
          }
        />
      ) : (
        <Card>
          <CardContent className="p-0">
            <ul>
              {items.map((it) => (
                <IntelRow key={it.fingerprint} item={it} taskId={taskId} />
              ))}
            </ul>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
