import { useMemo, useState } from "react"
import { useI18n } from "@/i18n"
import { useQueries } from "@tanstack/react-query"
import {
  eventOrderMs,
  mergeFeedEvents,
  toFeedEvent,
  useEventsStore,
} from "@/store/events"
import { LiveFeed, type FeedFilter } from "@/components/live-feed"
import { LiveTraffic } from "@/components/live-traffic"
import { LiveFindings } from "@/components/live-findings"
import { ConnectionStatus } from "@/components/connection-status"
import { api } from "@/api/client"
import { qk, useInstances } from "@/api/queries"
import { useActiveProjectId } from "@/lib/use-active-project"
import { scopeEventsToProject } from "@/lib/events-scope"

export default function LivePage() {
  const { t } = useI18n()
  const events = useEventsStore((s) => s.events)
  const clearEvents = useEventsStore((s) => s.clearEvents)
  const projectId = useActiveProjectId()
  const { data: instances } = useInstances(projectId ?? undefined)
  const [filter, setFilter] = useState<FeedFilter>("all")
  const [clearedAt, setClearedAt] = useState<number | null>(null)
  const activeInstances = useMemo(
    () =>
      (instances?.instances ?? [])
        .filter((i) => isActiveStatus(i.status))
        .slice(0, 8),
    [instances],
  )
  const activeInstanceIds = useMemo(
    () => activeInstances.map((i) => i.id),
    [activeInstances],
  )
  // SSE store 是全局的（/api/stream 只支持 instance 过滤）：按当前项目的实例集
  // 裁剪，否则别的项目的事件会混进来，与本页项目级的「发现」面板口径打架。
  const projectInstanceIds = useMemo(
    () => new Set((instances?.instances ?? []).map((i) => i.id)),
    [instances],
  )
  const historyQueries = useQueries({
    queries: activeInstanceIds.map((id) => ({
      queryKey: qk.instanceEvents(id),
      queryFn: () => api.instanceEvents(id),
      staleTime: 1000,
      refetchInterval: 5000,
    })),
  })
  const feedEvents = useMemo(() => {
    const persisted = historyQueries.flatMap((q, queryIndex) =>
      (q.data ?? []).map((event, eventIndex) =>
        toFeedEvent(
          event,
          `live:${activeInstanceIds[queryIndex] || queryIndex}`,
          eventIndex,
        ),
      ),
    )
    const merged = mergeFeedEvents(
      persisted,
      scopeEventsToProject(events, projectInstanceIds),
    )
    if (!clearedAt) return merged
    return merged.filter((event) => eventOrderMs(event) > clearedAt)
  }, [activeInstanceIds, clearedAt, events, historyQueries, projectInstanceIds])

  return (
    <div className="space-y-6">
      <header className="flex items-center justify-between gap-3">
        <div>
          <h1 className="font-sans text-2xl font-semibold tracking-tight">{t("live.title")}</h1>
          <p className="text-sm text-muted-foreground">
            {t("live.subtitle")}
          </p>
        </div>
        <ConnectionStatus />
      </header>
      <LiveFeed
        events={feedEvents}
        filter={filter}
        onFilterChange={setFilter}
        exportFilePrefix="xalgorix-live-feed"
        exportScope="active scans"
        onClearEvents={() => {
          setClearedAt(Date.now())
          clearEvents()
        }}
        emptyTitle={t("live.empty.title")}
        emptyDescription={t("live.empty.description")}
        httpPanel={<LiveTraffic instances={activeInstances} />}
        findingsPanel={<LiveFindings projectId={projectId} />}
      />
    </div>
  )
}

function isActiveStatus(status?: string): boolean {
  const s = (status || "").toLowerCase()
  return s === "running" || s === "pending" || s === "paused"
}
