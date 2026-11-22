import { useQuery } from "@tanstack/react-query";
import { Activity, Boxes, Layers, ShieldCheck, ShieldAlert } from "lucide-react";

import { api } from "@/api/client";
import { qk } from "@/api/queries";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";

/**
 * Read-only projection of the container tool runtime.
 *
 * Nothing here is actionable on purpose: project containers are owned by the
 * scan lifecycle, and letting the dashboard start/stop them would duplicate the
 * control plane. The card only answers "where do tool calls execute, and is the
 * execution plane healthy?".
 */
export function RuntimeStatusCard() {
  const { data, isLoading, error } = useQuery({
    queryKey: qk.runtime,
    queryFn: () => api.runtimeStatus(),
    refetchInterval: 15_000,
    staleTime: 10_000,
  });

  if (isLoading) {
    return <Skeleton className="h-40 w-full" />;
  }

  const backend = data?.backend ?? "native";
  const isNative = backend === "native";
  const healthy = data?.healthy ?? !error;
  const profileTone =
    data?.profile === "full"
      ? "border-warning/40 text-warning"
      : data?.profile === "web"
        ? "border-primary/40 text-foreground"
        : "border-success/40 text-success";

  return (
    <Card className="overflow-hidden">
      <CardHeader className="flex flex-row items-start justify-between gap-4 pb-3">
        <div className="space-y-1">
          <CardTitle className="flex items-center gap-2 text-base">
            <Activity className="h-4 w-4 text-muted-foreground" />
            工具运行时
          </CardTitle>
          <CardDescription className="text-xs">
            {isNative
              ? "工具在平台进程内执行（native）"
              : "每次扫描的工具调用在独立容器中执行，产物仍写回同一扫描目录"}
          </CardDescription>
        </div>
        <Badge
          variant="outline"
          className={
            healthy
              ? "border-success/40 text-success"
              : "border-destructive/40 text-destructive"
          }
        >
          {healthy ? (
            <ShieldCheck className="mr-1 h-3 w-3" />
          ) : (
            <ShieldAlert className="mr-1 h-3 w-3" />
          )}
          {healthy ? "健康" : "降级"}
        </Badge>
      </CardHeader>

      <CardContent className="grid gap-4 sm:grid-cols-2">
        <div className="grid grid-cols-3 gap-3">
          <Metric
            label="项目容器"
            value={
              data ? `${data.projects_active}/${data.project_limit || "∞"}` : "—"
            }
            hint="正在运行的项目容器数 / 当前上限（按宿主内存推导，可用 XALGORIX_MAX_PROJECTS 覆盖）"
          />
          <Metric
            label="执行队列"
            value={data ? String(data.exec_queue_depth) : "—"}
            hint="等待容器内执行槽的调用数；槽位由 XALGORIX_TOOLBOX_CONCURRENCY 控制"
          />
          <Metric
            label="槽位占用"
            value={
              data ? `${data.project_slot_used}/${data.project_slot_max}` : "—"
            }
            hint="平台侧的准入槽位占用；容器实际数量以运行时为准"
          />
        </div>

        <div className="flex flex-col justify-between gap-2 rounded-md border border-border/60 bg-muted/30 p-3">
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Layers className="h-3.5 w-3.5" />
            <span className="font-mono text-[11px] text-foreground">
              {isNative ? "native" : (data?.transport ?? "socket")}
            </span>
            <span>·</span>
            <span className="font-mono text-[11px] text-foreground">
              {data?.image_digest || "无镜像"}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="outline" className={`font-mono text-[11px] ${profileTone}`}>
              <Boxes className="mr-1 h-3 w-3" />
              {data?.profile || "core"}
            </Badge>
            {data?.reason ? (
              <span className="line-clamp-2 text-[11px] text-muted-foreground">
                {data.reason}
              </span>
            ) : null}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

function Metric({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint: string;
}) {
  return (
    <div
      title={hint}
      className="rounded-md border border-border/60 bg-background/60 px-3 py-2 transition-colors hover:border-border"
    >
      <div className="text-[11px] uppercase tracking-wide text-muted-foreground">
        {label}
      </div>
      <div className="mt-1 font-mono text-lg font-semibold tabular-nums">
        {value}
      </div>
    </div>
  );
}
