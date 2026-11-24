import { KeyRound, RefreshCw, RotateCcw, ShieldAlert, ShieldCheck } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useKeyPool, useResetKeyPool } from "@/api/queries";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { KeyPoolEntry } from "@/types/api";

/**
 * API 池卡片（4zreco llmpool 的读模型移植）：出站 LLM 请求在
 * XALGORIX_API_KEY(S) 的 key 池内轮换；额度/密钥失败的 key 进入进程级熔断
 * （quota 15/30 分钟、限流 90 秒），后续请求立即切到其他 key。这里只展示
 * 掩码后的 key 与熔断状态，并可手动恢复某个 key —— 不显示、不回传任何密钥。
 */
export function KeyPoolCard({ className }: { className?: string }) {
  const { t } = useI18n();
  const pool = useKeyPool();
  const reset = useResetKeyPool();

  const entries = pool.data?.entries ?? [];
  const strategy = pool.data?.strategy ?? "off";
  const cooling = entries.filter((entry) => entry.state === "cooling").length;

  return (
    <Card className={className}>
      <CardHeader className="flex flex-row items-start justify-between gap-3">
        <div className="space-y-1">
          <CardTitle className="flex items-center gap-2">
            <KeyRound className="size-4" aria-hidden />
            {t("settings.keyPool.title")}
            <Badge variant="muted" className="ml-1 font-normal">
              {strategyLabel(strategy, t)}
            </Badge>
          </CardTitle>
          <CardDescription>{t("settings.keyPool.description")}</CardDescription>
        </div>
        <Button
          size="sm"
          variant="outline"
          disabled={cooling === 0 || reset.isPending}
          onClick={() => reset.mutate(undefined)}
        >
          <RotateCcw className="mr-1 size-3.5" aria-hidden />
          {t("settings.keyPool.resetAll")}
        </Button>
      </CardHeader>
      <CardContent className="space-y-2">
        {pool.isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : entries.length === 0 ? (
          <p className="text-muted-foreground text-sm">{t("settings.keyPool.empty")}</p>
        ) : (
          <ul className="divide-y divide-border rounded-md border border-border">
            {entries.map((entry) => (
              <KeyPoolRow
                key={entry.id}
                entry={entry}
                resetting={reset.isPending}
                onReset={() => reset.mutate(entry.id)}
              />
            ))}
          </ul>
        )}
        <p className="text-muted-foreground text-xs">{t("settings.keyPool.hint")}</p>
      </CardContent>
    </Card>
  );
}

function KeyPoolRow({
  entry,
  resetting,
  onReset,
}: {
  entry: KeyPoolEntry;
  resetting: boolean;
  onReset: () => void;
}) {
  const { t } = useI18n();
  const cooling = entry.state === "cooling";
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-sm">
      <span
        className={cn("size-1.5 shrink-0 rounded-full", cooling ? "bg-amber-500" : "bg-emerald-500")}
        aria-hidden
      />
      <span className="min-w-0 flex-1 font-mono text-xs">{entry.masked}</span>
      <Badge variant={cooling ? "warning" : "success"} className="shrink-0 font-normal">
        {cooling ? (
          <>
            <ShieldAlert className="mr-1 size-3" aria-hidden />
            {entry.open_until
              ? t("settings.keyPool.coolingUntil").replace("{time}", shortTime(entry.open_until))
              : t("settings.keyPool.stateCooling")}
          </>
        ) : (
          <>
            <ShieldCheck className="mr-1 size-3" aria-hidden />
            {t("settings.keyPool.stateOk")}
          </>
        )}
      </Badge>
      {entry.trips ? (
        <span className="shrink-0 text-[11px] text-muted-foreground">
          {t("settings.keyPool.trips").replace("{n}", String(entry.trips))}
        </span>
      ) : null}
      {entry.last_error ? (
        <span
          className="w-full truncate text-[11px] text-muted-foreground"
          title={entry.last_error}
        >
          {t("settings.keyPool.lastError")}: {entry.last_error}
        </span>
      ) : null}
      {cooling ? (
        <Button
          size="sm"
          variant="ghost"
          className="h-6 shrink-0 px-2 text-[11px]"
          disabled={resetting}
          onClick={onReset}
        >
          <RefreshCw className="mr-1 size-3" aria-hidden />
          {t("settings.keyPool.reset")}
        </Button>
      ) : null}
    </li>
  );
}

function strategyLabel(strategy: string, t: (key: string) => string): string {
  switch (strategy) {
    case "round_robin":
      return t("settings.keyPool.strategyRoundRobin");
    case "sticky":
      return t("settings.keyPool.strategySticky");
    default:
      return t("settings.keyPool.strategyOff");
  }
}

function shortTime(iso: string): string {
  const at = Date.parse(iso);
  if (!Number.isFinite(at)) return iso;
  const d = new Date(at);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}:${String(
    d.getSeconds(),
  ).padStart(2, "0")}`;
}
