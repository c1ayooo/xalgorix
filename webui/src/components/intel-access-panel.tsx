import { useState } from "react";
import { ChevronDown, ChevronRight, DoorOpen } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { ErrorState } from "@/components/states";
import { useScanIntelAccess } from "@/api/queries";
import { useI18n } from "@/i18n";
import { cn } from "@/lib/utils";
import type { IntelAccess, IntelAccessRow, IntelUseDecision } from "@/types/api";

/**
 * 运行级情报门槛（INTEL_HANDOFF_SPEC M2/M3/M4）：这场 run 手里有哪些可用的
 * 战果（access）、签发过哪些一次性通行证（capability）、最近谁在什么时候
 * 用过或判过（audit）。只读投影——状态权威在引擎侧，这里不做任何判定。
 */

const STATE_VARIANT: Record<
  IntelAccess["state"],
  "default" | "success" | "muted" | "warning" | "destructive"
> = {
  proposed: "muted",
  confirmed: "success",
  active: "success",
  expired: "warning",
  revoked: "destructive",
};

const DECISION_VARIANT: Record<
  IntelUseDecision,
  "success" | "destructive" | "warning" | "default"
> = {
  allow: "success",
  deny: "destructive",
  needs_capability: "warning",
  detail_fetch: "default",
};

function AccessRow({ row }: { row: IntelAccessRow }) {
  const { t } = useI18n();
  return (
    <li className="flex flex-wrap items-center gap-x-2 gap-y-1 px-3 py-2">
      <span className="font-mono text-[11px] text-foreground">{row.fingerprint}</span>
      <Badge variant={STATE_VARIANT[row.state]}>{t(`intelAccess.state.${row.state}`)}</Badge>
      {row.grants && row.grants.length > 0 && (
        <span className="min-w-0 flex-1 truncate font-mono text-[10px] text-muted-foreground">
          {row.grants.join(", ")}
        </span>
      )}
      <span className="ml-auto font-mono text-[10px] tabular-nums text-muted-foreground">
        {row.use_count > 0 && `${t("intelAccess.used")} ${row.use_count}`}
        {row.fail_count > 0 && ` · ${t("intelAccess.failed")} ${row.fail_count}`}
      </span>
      {row.revoke_reason && (
        <span className="w-full text-[10px] text-muted-foreground">{row.revoke_reason}</span>
      )}
    </li>
  );
}

export function IntelAccessPanel({ scanId, running }: { scanId: string; running?: boolean }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const { data, isLoading, error, refetch } = useScanIntelAccess(scanId, running !== false);

  const accessCount = data?.access.length ?? 0;
  const capabilityCount = data?.capabilities.length ?? 0;

  return (
    <section className="rounded-md border border-border bg-background/40">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full flex-wrap items-center gap-2 px-3 py-2 text-left"
      >
        {open ? (
          <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        )}
        <DoorOpen className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <span className="text-xs font-medium text-foreground">{t("intelAccess.title")}</span>
        <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
          {accessCount} {t("intelAccess.accessCount")}
          {" · "}
          {capabilityCount} {t("intelAccess.capabilityCount")}
        </span>
      </button>

      {open && (
        <div className="border-t border-border">
          {error ? (
            <div className="p-3">
              <ErrorState
                title={t("intelAccess.loadFailed")}
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
            </div>
          ) : isLoading && !data ? (
            <Skeleton className="m-3 h-20" />
          ) : !data ? null : (
            <div className="divide-y divide-border">
              <div>
                <p className="px-3 pt-2 text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
                  {t("intelAccess.section.access")}
                </p>
                {data.access.length === 0 ? (
                  <p className="px-3 py-2 text-xs text-muted-foreground">
                    {t("intelAccess.accessEmpty")}
                  </p>
                ) : (
                  <ul>
                    {data.access.map((row) => (
                      <AccessRow key={row.id} row={row} />
                    ))}
                  </ul>
                )}
              </div>

              <div>
                <p className="px-3 pt-2 text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
                  {t("intelAccess.section.capabilities")}
                </p>
                {data.capabilities.length === 0 ? (
                  <p className="px-3 py-2 text-xs text-muted-foreground">
                    {t("intelAccess.capabilityEmpty")}
                  </p>
                ) : (
                  <ul>
                    {data.capabilities.map((cap) => (
                      <li
                        key={cap.id}
                        className="flex flex-wrap items-center gap-2 px-3 py-2 font-mono text-[10px] text-muted-foreground"
                      >
                        <span className="text-foreground">{cap.target}</span>
                        <span>
                          {t("intelAccess.usesLeft")} {cap.uses_left}
                        </span>
                        <span>
                          {t("intelAccess.expiresAt")}{" "}
                          {new Date(cap.expires_at).toLocaleTimeString()}
                        </span>
                        <span className="ml-auto">{cap.issued_by}</span>
                      </li>
                    ))}
                  </ul>
                )}
              </div>

              <div>
                <p className="px-3 pt-2 text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
                  {t("intelAccess.section.uses")}
                </p>
                {data.uses.length === 0 ? (
                  <p className="px-3 py-2 text-xs text-muted-foreground">
                    {t("intelAccess.usesEmpty")}
                  </p>
                ) : (
                  <ul>
                    {data.uses.slice(0, 20).map((use, i) => (
                      <li
                        key={`${use.id ?? i}-${use.created_at}`}
                        className={cn(
                          "flex flex-wrap items-center gap-2 px-3 py-1.5 font-mono text-[10px] text-muted-foreground",
                        )}
                      >
                        <Badge variant={DECISION_VARIANT[use.decision]}>
                          {t(`intelAccess.decision.${use.decision}`)}
                        </Badge>
                        {use.fingerprint && (
                          <span className="text-foreground">{use.fingerprint}</span>
                        )}
                        {use.target && <span>{use.target}</span>}
                        <span className="ml-auto">
                          {new Date(use.created_at).toLocaleTimeString()}
                        </span>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
