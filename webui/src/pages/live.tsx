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
import { LiveTraffic, type TrafficRun } from "@/components/live-traffic"
import { LiveFindings } from "@/components/live-findings"
import { LiveLLM } from "@/components/live-llm"
import { ConnectionStatus } from "@/components/connection-status"
import { api } from "@/api/client"
import { qk, useInstances, useProjects, useScansPage } from "@/api/queries"
import { useActiveProjectId } from "@/lib/use-active-project"
import { scopeEventsToProject } from "@/lib/events-scope"

export default function LivePage() {
  const { t } = useI18n()
  const events = useEventsStore((s) => s.events)
  const clearEvents = useEventsStore((s) => s.clearEvents)
  const projectId = useActiveProjectId()
  const { data: projectsData } = useProjects()
  const projectName = projectsData?.projects.find((p) => p.id === projectId)?.name
  const { data: instances } = useInstances(projectId ?? undefined)
  // 进程重启后已结束运行不在内存 instances 里，但归档仍在 PG/磁盘。HTTP 视图
  // 的候选集因此还从持久扫描列表补齐（instance_id = run 身份，sibling session
  // 由 /traffic 合并）。
  const { data: recentScans } = useScansPage({
    page: 1,
    size: 50,
    projectId: projectId ?? undefined,
  })
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
  // HTTP 视图不止看活跃运行：运行结束后归档仍在，已结束运行的历史包正是漏洞
  // 证据。活跃运行优先，剩余槽位按最近启动补齐（最多 8 个，限制轮询面）。
  // 内存 instances 优先（资产/子域信息最全）；持久扫描列表补齐重启后丢失的运行。
  const trafficInstances = useMemo<TrafficRun[]>(() => {
    const byId = new Map<string, TrafficRun>()
    for (const inst of instances?.instances ?? []) byId.set(inst.id, inst)
    for (const sc of recentScans?.items ?? []) {
      const id = sc.instance_id || sc.id
      if (byId.has(id)) continue
      byId.set(id, {
        id,
        name: sc.target,
        targets: sc.target,
        status: sc.status,
        started_at: sc.started_at,
      })
    }
    const all = [...byId.values()]
    const active = all.filter((i) => isActiveStatus(i.status))
    const finished = all
      .filter((i) => !isActiveStatus(i.status))
      .sort(
        (a, b) =>
          Date.parse(b.started_at || "") - Date.parse(a.started_at || ""),
      )
      .slice(0, Math.max(0, 8 - active.length))
    return [...active, ...finished].slice(0, 8)
  }, [instances, recentScans])
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
        httpPanel={<LiveTraffic instances={trafficInstances} projectName={projectName} />}
        findingsPanel={<LiveFindings projectId={projectId} />}
        llmPanel={<LiveLLM />}
      />
    </div>
  )
}

function isActiveStatus(status?: string): boolean {
  const s = (status || "").toLowerCase()
  return s === "running" || s === "pending" || s === "paused"
}
