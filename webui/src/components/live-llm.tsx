import { useEffect, useMemo, useState } from "react";
import { RotateCcw } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState, ErrorState } from "@/components/states";
import { Skeleton } from "@/components/ui/skeleton";
import { useKeyPool, useResetKeyPool } from "@/api/queries";
import { cn, timeAgo } from "@/lib/utils";
import { useI18n } from "@/i18n";
import type { KeyFailoverEvent, KeyPoolEntry } from "@/types/api";

/**
 * 实时动态页的「LLM」视图。
 *
 * 数据源是 /api/pool（按设计只回掩码，不回密钥）：进程级 key 池的熔断状态
 * + 最近一次自动切换流水。事件流里没有 llm/token 事件（后端从不发），所以这
 * 一块刻意走轮询口径——健康变化发生在请求路径上，没有推送。
 *
 * 口径：这里的 key 是 LLM API key 池，与项目/任务无关；「已切换」表示某个 key
 * 被熔断后请求转到了另一个 key（或没有可用 key）。冷却倒计时是服务端
 * open_until 的剩余时间，不是本地估算的失败次数。
 */
export function LiveLLM() {
  const { t } = useI18n();
  const query = useKeyPool(5000);
  const reset = useResetKeyPool();
  const [now, setNow] = useState(() => Date.now());

  const cooling = useMemo(
    () => (query.data?.entries ?? []).some((e) => e.state === "cooling"),
    [query.data],
  );
  // 只有存在冷却 key 时才走秒级刷新，否则保持 5s 轮询即可。
  useEffect(() => {
    if (!cooling) return;
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [cooling]);

  if (query.error) {
    return (
      <ErrorState
        className="m-3"
        title={t("live.llm.error")}
        description={query.error instanceof Error ? query.error.message : ""}
        action={
          <Button size="sm" variant="outline" onClick={() => query.refetch()}>
            {t("common.retry")}
          </Button>
        }
      />
    );
  }
  if (query.isLoading && !query.data) {
    return (
      <div className="space-y-2 p-4">
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-4/5" />
        <Skeleton className="h-8 w-3/5" />
      </div>
    );
  }

  const data = query.data;
  if (!data || data.keys === 0) {
    return (
      <EmptyState
        className="m-3"
        title={t("live.llm.empty.title")}
        description={t("live.llm.empty.description")}
      />
    );
  }

  const coolingCount = data.entries.filter((e) => e.state === "cooling").length;
  const events = data.events ?? [];

  return (
    <div className="divide-y divide-border">
      <div className="flex flex-wrap items-center gap-3 px-3 py-1.5 text-[11px] text-muted-foreground mono">
        <span>{t("live.llm.strategy")}: {strategyLabel(data.strategy, t)}</span>
        <span>{t("live.llm.keys").replace("%d", String(data.keys))}</span>
        <span className={cn(coolingCount > 0 && "text-amber-600 dark:text-amber-400")}>
          {t("live.llm.coolingCount").replace("%d", String(coolingCount))}
        </span>
        <span className="ml-auto">{t("live.llm.hint")}</span>
      </div>

      {data.entries.map((entry) => (
        <KeyRow
          key={entry.id}
          entry={entry}
          now={now}
          resetting={reset.isPending}
          onReset={() => reset.mutate(entry.id)}
        />
      ))}

      <div className="px-3 py-2">
        <div className="mb-1 text-[10px] uppercase tracking-wider text-muted-foreground">
          {t("live.llm.recent")}
        </div>
        {events.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t("live.llm.noEvents")}</p>
        ) : (
          <ul className="space-y-1">
            {events.map((ev, i) => (
              <FailoverRow key={`${ev.at}-${i}`} event={ev} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function KeyRow({
  entry,
  now,
  resetting,
  onReset,
}: {
  entry: KeyPoolEntry;
  now: number;
  resetting: boolean;
  onReset: () => void;
}) {
  const { t } = useI18n();
  const cooling = entry.state === "cooling";
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2 px-3 py-2 text-xs">
      <span className="mono shrink-0 text-foreground">{entry.masked || entry.id}</span>
      <Badge variant={cooling ? "warning" : "success"} className="shrink-0">
        {cooling ? t("live.llm.state.cooling") : t("live.llm.state.ok")}
      </Badge>
      {cooling && entry.open_until && (
        <span className="mono shrink-0 tabular-nums text-amber-600 dark:text-amber-400">
          {t("live.llm.coolingUntil").replace("%s", countdown(entry.open_until, now))}
        </span>
      )}
      {entry.trips ? (
        <span className="mono shrink-0 text-muted-foreground">
          {t("live.llm.trips").replace("%d", String(entry.trips))}
        </span>
      ) : null}
      {entry.last_error && (
        <span className="mono min-w-0 flex-1 truncate text-muted-foreground" title={entry.last_error}>
          {entry.last_error}
        </span>
      )}
      {entry.last_at && (
        <span className="shrink-0 text-[11px] text-muted-foreground">{timeAgo(entry.last_at)}</span>
      )}
      {cooling && (
        <Button
          size="sm"
          variant="ghost"
          className="h-6 shrink-0 px-2"
          disabled={resetting}
          onClick={onReset}
        >
          <RotateCcw className="mr-1 h-3 w-3" aria-hidden />
          {t("live.llm.reset")}
        </Button>
      )}
    </div>
  );
}

function FailoverRow({ event }: { event: KeyFailoverEvent }) {
  const { t } = useI18n();
  return (
    <li className="flex min-w-0 flex-wrap items-center gap-2 text-xs">
      <span className="shrink-0 text-[11px] text-muted-foreground">{timeAgo(event.at)}</span>
      <Badge variant="outline" className="shrink-0">
        {reasonLabel(event.reason, t)}
      </Badge>
      <span className="mono min-w-0 truncate">
        {event.from || "—"} → {event.to || t("live.llm.noKey")}
      </span>
      {event.cooldown_until && (
        <span className="mono shrink-0 text-[11px] text-muted-foreground">
          {t("live.llm.cooldownShort").replace("%s", event.cooldown_until)}
        </span>
      )}
    </li>
  );
}

function strategyLabel(strategy: string, t: (k: string) => string): string {
  switch (strategy) {
    case "round_robin":
      return t("live.llm.strategy.roundRobin");
    case "sticky":
      return t("live.llm.strategy.sticky");
    default:
      return t("live.llm.strategy.off");
  }
}

function reasonLabel(reason: string, t: (k: string) => string): string {
  switch (reason) {
    case "quota_exhausted":
      return t("live.llm.reason.quota");
    case "overloaded":
      return t("live.llm.reason.overloaded");
    case "auth":
      return t("live.llm.reason.auth");
    case "rate_limit":
      return t("live.llm.reason.rateLimit");
    default:
      return reason || "—";
  }
}

/** countdown renders the remaining cooldown as m:ss (server open_until, UTC). */
function countdown(openUntil: string, now: number): string {
  const ms = Date.parse(openUntil) - now;
  if (!Number.isFinite(ms) || ms <= 0) return "0s";
  const total = Math.ceil(ms / 1000);
  const m = Math.floor(total / 60);
  const s = total % 60;
  return m > 0 ? `${m}m ${String(s).padStart(2, "0")}s` : `${s}s`;
}
