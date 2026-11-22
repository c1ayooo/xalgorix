import { useState } from "react";
import { ChevronRight } from "lucide-react";

import { useScanTraffic } from "@/api/queries";
import { timeAgo, cn } from "@/lib/utils";
import { Skeleton } from "@/components/ui/skeleton";
import { useI18n } from "@/i18n";
import type { HttpExchange } from "@/types/api";

// 单个 run 的流量代理 raw 交互（任务详情「运行」tab 行内展开）。归因流量的
// 请求/响应 wire 包原样展示（后端不脱敏，契约 §3.8）；无归档给一行诚实空态，
// 不渲染假数据。
export function RunTraffic({ scanId }: { scanId: string }) {
  const { t } = useI18n();
  const { data, isLoading } = useScanTraffic(scanId);

  if (isLoading) {
    return (
      <div className="space-y-2 px-4 py-3">
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-4/5" />
        <Skeleton className="h-8 w-3/5" />
      </div>
    );
  }

  const exchanges = data?.exchanges ?? [];
  if (exchanges.length === 0) {
    return (
      <div className="px-4 py-3 text-xs text-muted-foreground">
        {t("runs.traffic.absent")}
      </div>
    );
  }

  return (
    <div className="divide-y divide-border border-t border-border">
      {exchanges.map((ex) => (
        <TrafficRow key={ex.seq} ex={ex} />
      ))}
      {data?.truncated && (
        <div className="px-4 py-2 text-[11px] text-muted-foreground mono">
          {t("findings.http.moreHidden").replace("%d", String(data.total_matched))}
        </div>
      )}
    </div>
  );
}

// 一条交换：摘要行是展开开关（无 raw 时禁用），展开后渲染请求/响应原文。
function TrafficRow({ ex }: { ex: HttpExchange }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const hasRaw = !!(ex.request_raw || ex.response_raw);

  return (
    <div>
      <button
        type="button"
        onClick={() => hasRaw && setOpen((v) => !v)}
        disabled={!hasRaw}
        aria-expanded={hasRaw ? open : undefined}
        className="flex w-full min-w-0 flex-wrap items-center gap-2 px-4 py-2 text-left text-xs hover:bg-muted/30 disabled:cursor-default"
      >
        {hasRaw && (
          <ChevronRight
            className={cn(
              "h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform",
              open && "rotate-90",
            )}
            aria-hidden
          />
        )}
        <span className="shrink-0 text-[11px] text-muted-foreground">
          {timeAgo(ex.at)}
        </span>
        <span className="mono shrink-0 font-medium text-foreground">{ex.method}</span>
        <span className="mono min-w-0 flex-1 truncate text-foreground/90">{ex.url}</span>
        {typeof ex.status === "number" && (
          <span
            className={cn(
              "mono shrink-0",
              ex.denied
                ? "text-destructive"
                : ex.status < 400
                  ? "text-emerald-600 dark:text-emerald-400"
                  : "text-amber-600 dark:text-amber-400",
            )}
          >
            {ex.status}
          </span>
        )}
        {ex.high_risk && (
          <span className="mono max-w-[28ch] min-w-0 truncate text-amber-600 dark:text-amber-400">
            {ex.high_risk}
          </span>
        )}
      </button>
      {open && hasRaw && (
        <div className="min-w-0 space-y-2 px-4 pb-3">
          {ex.request_raw && (
            <pre className="max-h-64 min-w-0 max-w-full overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted p-3 text-xs leading-relaxed text-foreground [overflow-wrap:anywhere]">
              <code>
                {ex.request_raw}
                {ex.request_truncated ? `\n${t("findings.http.truncated")}` : ""}
              </code>
            </pre>
          )}
          {ex.response_raw && (
            <pre className="max-h-64 min-w-0 max-w-full overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted p-3 text-xs leading-relaxed text-foreground [overflow-wrap:anywhere]">
              <code>
                {ex.response_raw}
                {ex.response_truncated ? `\n${t("findings.http.truncated")}` : ""}
              </code>
            </pre>
          )}
        </div>
      )}
    </div>
  );
}
