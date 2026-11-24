import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import { toast } from "sonner";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { EmptyState, ErrorState } from "@/components/states";
import { IntelAccessPanel } from "@/components/intel-access-panel";
import {
  useApplyScanDecision,
  useScanDecisions,
  useScanExploration,
} from "@/api/queries";
import { useI18n } from "@/i18n";
import { explorationDot, explorationShortID } from "@/lib/exploration";
import {
  EXPLORATION_REL_STROKE,
  countRelations,
  curvePath,
  layoutLayered,
  relStroke,
} from "@/lib/layered-graph";
import { cn } from "@/lib/utils";
import type {
  DecisionAction,
  DecisionRequest,
  ExplorationKind,
  ExplorationNode,
  ExplorationView,
} from "@/types/api";

/**
 * 「探索链路」tab 装配（Run 级，跟随 ?run= 选择）：三态 + 投影诚实标志 +
 * 拓扑图。数据与播报板同源（同一查询缓存），图是拓扑视角。
 */
export function ExplorationTab({
  scanId,
  running,
}: {
  scanId?: string;
  running?: boolean;
}) {
  const { t } = useI18n();
  const { data, isLoading, error, refetch } = useScanExploration(scanId ?? "", running);

  if (!scanId) {
    return (
      <EmptyState title={t("explore.noRunTitle")} description={t("ledger.noRunDescription")} />
    );
  }
  if (error) {
    return (
      <ErrorState
        title={t("explore.loadFailed")}
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
    return <Skeleton className="h-72 w-full" />;
  }
  if (!data) return null;

  return (
    <div className="space-y-3">
      <DecisionInbox scanId={scanId} running={running} />
      <IntelAccessPanel scanId={scanId} running={running} />
      {data.incomplete && (
        <p className="rounded-sm border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-600 dark:text-amber-400">
          {data.note || t("explore.incomplete")}
        </p>
      )}
      {data.nodes.length === 0 ? (
        <EmptyState
          title={t("explore.emptyTitle")}
          description={data.journal_present ? t("explore.emptyDescription") : t("explore.absentDescription")}
        />
      ) : (
        <ExplorationGraph view={data} />
      )}
    </div>
  );
}

/**
 * 决策收件箱（PLANNER_OBSERVATION_AND_CORRECTION §2.4/§5.6）：只展示「需要
 * 人决定的少数事项」——停滞 worker、blocked intent、open intent、执行偏离；
 * 不展示事件流/token 流。四个动作（steer / kill / retire / reprioritize）都走
 * 同一个 POST，actor=human，引擎按当前状态 CAS；冲突显示引擎原文。
 */
const KILL_REASON_CODES = [
  "human",
  "stagnant",
  "repeated_failure",
  "lane_violation",
  "scope_violation",
  "duplicate",
  "superseded",
] as const;

interface DecisionForm {
  action: DecisionAction;
  unitId: string;
}

function DecisionInbox({ scanId, running }: { scanId: string; running?: boolean }) {
  const { t } = useI18n();
  const { data, isLoading, error, refetch } = useScanDecisions(scanId, running);
  const apply = useApplyScanDecision(scanId);
  const [form, setForm] = useState<DecisionForm | null>(null);
  const [message, setMessage] = useState("");
  const [reasonCode, setReasonCode] = useState<string>("human");
  const [reason, setReason] = useState("");
  const [priority, setPriority] = useState("");
  // issue_capability 的表单：目标、可用次数、有效期（秒）。
  const [targets, setTargets] = useState("");
  const [usesLeft, setUsesLeft] = useState("1");
  const [ttlSeconds, setTtlSeconds] = useState("600");

  function openForm(action: DecisionAction, unitId: string, target?: string) {
    setForm({ action, unitId });
    setMessage("");
    setReason("");
    setReasonCode("human");
    setPriority("");
    setTargets(target ?? "");
    setUsesLeft("1");
    setTtlSeconds("600");
  }

  function closeForm() {
    setForm(null);
  }

  function submit() {
    if (!form) return;
    const req: DecisionRequest = { action: form.action, unit_id: form.unitId };
    if (form.action === "steer_work") req.message = message.trim();
    if (form.action === "kill_work") {
      req.reason_code = reasonCode;
      if (reason.trim()) req.reason = reason.trim();
    }
    if (form.action === "retire_intent") req.reason = reason.trim();
    if (form.action === "reprioritize_intent") req.priority = Number(priority);
    if (form.action === "issue_capability") {
      req.targets = targets
        .split(/[,\s]+/)
        .map((v) => v.trim())
        .filter(Boolean);
      req.uses_left = Number(usesLeft);
      req.ttl_s = Number(ttlSeconds);
    }
    apply.mutate(req, {
      onSuccess: (res) => {
        toast.success(res.message || t("decisions.applied"));
        setForm(null);
      },
      onError: (err) => {
        toast.error(`${t("decisions.failed")}: ${err instanceof Error ? err.message : String(err)}`);
      },
    });
  }

  if (isLoading && !data) return <Skeleton className="h-24 w-full" />;
  if (error) {
    return (
      <ErrorState
        title={t("decisions.loadFailed")}
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
  if (!data) return null;

  const total =
    data.stalled.length +
    data.blocked.length +
    data.paused.length +
    data.open_intents.length +
    data.deviations.length;

  const formValid =
    form?.action === "steer_work"
      ? message.trim().length > 0
      : form?.action === "retire_intent"
        ? reason.trim().length > 0
        : form?.action === "reprioritize_intent"
          ? priority.trim() !== "" &&
            Number.isFinite(Number(priority)) &&
            Number(priority) >= 0 &&
            Number(priority) <= 100000
          : form?.action === "issue_capability"
            ? targets.trim() !== "" &&
              Number.isFinite(Number(usesLeft)) &&
              Number(usesLeft) >= 1 &&
              Number(usesLeft) <= 10 &&
              Number.isFinite(Number(ttlSeconds)) &&
              Number(ttlSeconds) >= 60 &&
              Number(ttlSeconds) <= 7200
            : form?.action === "kill_work";

  return (
    <section className="rounded-md border border-border bg-background/40">
      <header className="flex flex-wrap items-baseline justify-between gap-2 border-b border-border px-3 py-2">
        <h3 className="text-xs font-medium text-foreground">{t("decisions.title")}</h3>
        <span className="text-[11px] text-muted-foreground">{t("decisions.subtitle")}</span>
      </header>

      {total === 0 ? (
        <p className="px-3 py-3 text-xs text-muted-foreground">{t("decisions.empty")}</p>
      ) : (
        <div className="divide-y divide-border">
          {data.stalled.map((row) => (
            <DecisionRow
              key={`stalled-${row.unit_id}`}
              bucket={t("decisions.bucket.stalled")}
              unitId={row.unit_id}
              title={row.unit_id}
              meta={[
                row.lane,
                `${row.idle_seconds}s ${t("decisions.idle")}`,
                row.active_tool ? t("decisions.activeTool") : "",
              ]
                .filter(Boolean)
                .join(" · ")}
              actions={["steer_work", "kill_work"]}
              form={form}
              onOpen={openForm}
              onClose={closeForm}
              pending={apply.isPending}
            >
              <DecisionFormFields
                action={form?.unitId === row.unit_id ? form.action : null}
                message={message}
                setMessage={setMessage}
                reasonCode={reasonCode}
                setReasonCode={setReasonCode}
                reason={reason}
                setReason={setReason}
                priority={priority}
                setPriority={setPriority}
                targets={targets}
                setTargets={setTargets}
                usesLeft={usesLeft}
                setUsesLeft={setUsesLeft}
                ttlSeconds={ttlSeconds}
                setTtlSeconds={setTtlSeconds}
                valid={!!formValid}
                pending={apply.isPending}
                onSubmit={submit}
                onCancel={closeForm}
              />
            </DecisionRow>
          ))}

          {data.blocked.map((row) => (
            <DecisionRow
              key={`blocked-${row.unit_id}`}
              bucket={t("decisions.bucket.blocked")}
              unitId={row.unit_id}
              title={row.title || row.unit_id}
              meta={row.blocked_reason || t("decisions.noReason")}
              actions={["reprioritize_intent"]}
              form={form}
              onOpen={openForm}
              onClose={closeForm}
              pending={apply.isPending}
            >
              <DecisionFormFields
                action={form?.unitId === row.unit_id ? form.action : null}
                message={message}
                setMessage={setMessage}
                reasonCode={reasonCode}
                setReasonCode={setReasonCode}
                reason={reason}
                setReason={setReason}
                priority={priority}
                setPriority={setPriority}
                targets={targets}
                setTargets={setTargets}
                usesLeft={usesLeft}
                setUsesLeft={setUsesLeft}
                ttlSeconds={ttlSeconds}
                setTtlSeconds={setTtlSeconds}
                valid={!!formValid}
                pending={apply.isPending}
                onSubmit={submit}
                onCancel={closeForm}
              />
            </DecisionRow>
          ))}

          {data.paused.map((row) => (
            <DecisionRow
              key={`paused-${row.unit_id}`}
              bucket={t("decisions.bucket.paused")}
              unitId={row.unit_id}
              title={row.title || row.unit_id}
              meta={row.reason || t("decisions.noReason")}
              actions={["issue_capability", "retire_intent"]}
              form={form}
              onOpen={(action, unitId) => openForm(action, unitId, row.target)}
              onClose={closeForm}
              pending={apply.isPending}
            >
              <DecisionFormFields
                action={form?.unitId === row.unit_id ? form.action : null}
                message={message}
                setMessage={setMessage}
                reasonCode={reasonCode}
                setReasonCode={setReasonCode}
                reason={reason}
                setReason={setReason}
                priority={priority}
                setPriority={setPriority}
                targets={targets}
                setTargets={setTargets}
                usesLeft={usesLeft}
                setUsesLeft={setUsesLeft}
                ttlSeconds={ttlSeconds}
                setTtlSeconds={setTtlSeconds}
                valid={!!formValid}
                pending={apply.isPending}
                onSubmit={submit}
                onCancel={closeForm}
              />
            </DecisionRow>
          ))}

          {data.open_intents.map((row) => (
            <DecisionRow
              key={`open-${row.unit_id}`}
              bucket={t("decisions.bucket.open")}
              unitId={row.unit_id}
              title={row.title || row.unit_id}
              meta={[row.target, row.target_kind, `p=${row.priority}`]
                .filter(Boolean)
                .join(" · ")}
              actions={["retire_intent", "reprioritize_intent"]}
              form={form}
              onOpen={openForm}
              onClose={closeForm}
              pending={apply.isPending}
            >
              <DecisionFormFields
                action={form?.unitId === row.unit_id ? form.action : null}
                message={message}
                setMessage={setMessage}
                reasonCode={reasonCode}
                setReasonCode={setReasonCode}
                reason={reason}
                setReason={setReason}
                priority={priority}
                setPriority={setPriority}
                targets={targets}
                setTargets={setTargets}
                usesLeft={usesLeft}
                setUsesLeft={setUsesLeft}
                ttlSeconds={ttlSeconds}
                setTtlSeconds={setTtlSeconds}
                valid={!!formValid}
                pending={apply.isPending}
                onSubmit={submit}
                onCancel={closeForm}
              />
            </DecisionRow>
          ))}

          {data.deviations.map((row, i) => (
            <div key={`dev-${row.unit_id}-${i}`} className="px-3 py-2 text-xs">
              <div className="flex flex-wrap items-center gap-2">
                <span className="rounded-sm bg-muted px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-muted-foreground">
                  {t("decisions.bucket.deviation")}
                </span>
                <span className="font-mono text-[10px] text-muted-foreground">
                  {row.unit_id || "—"}
                </span>
                {row.recorded_at && (
                  <span className="ml-auto text-[10px] text-muted-foreground">
                    {new Date(row.recorded_at).toLocaleString()}
                  </span>
                )}
              </div>
              <p className="mt-1 whitespace-pre-wrap text-muted-foreground">{row.summary}</p>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function DecisionRow({
  bucket,
  unitId,
  title,
  meta,
  actions,
  form,
  onOpen,
  onClose,
  pending,
  children,
}: {
  bucket: string;
  unitId: string;
  title: string;
  meta: string;
  actions: DecisionAction[];
  form: DecisionForm | null;
  onOpen: (action: DecisionAction, unitId: string) => void;
  onClose: () => void;
  pending: boolean;
  children: ReactNode;
}) {
  const { t } = useI18n();
  const active = form?.unitId === unitId;
  return (
    <div className="px-3 py-2 text-xs">
      <div className="flex flex-wrap items-center gap-2">
        <span className="rounded-sm bg-muted px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-muted-foreground">
          {bucket}
        </span>
        <span className="min-w-0 flex-1 truncate font-medium text-foreground" title={title}>
          {title}
        </span>
        {meta && (
          <span className="font-mono text-[10px] text-muted-foreground">{meta}</span>
        )}
      </div>
      <div className="mt-1 flex flex-wrap gap-1.5">
        {actions.map((action) => (
          <Button
            key={action}
            type="button"
            size="sm"
            variant={active && form?.action === action ? "secondary" : "outline"}
            disabled={pending}
            onClick={() =>
              active && form?.action === action ? onClose() : onOpen(action, unitId)
            }
          >
            {t(`decisions.action.${action}`)}
          </Button>
        ))}
      </div>
      {active && children}
    </div>
  );
}

function DecisionFormFields({
  action,
  message,
  setMessage,
  reasonCode,
  setReasonCode,
  reason,
  setReason,
  priority,
  setPriority,
  targets,
  setTargets,
  usesLeft,
  setUsesLeft,
  ttlSeconds,
  setTtlSeconds,
  valid,
  pending,
  onSubmit,
  onCancel,
}: {
  action: DecisionAction | null;
  message: string;
  setMessage: (v: string) => void;
  reasonCode: string;
  setReasonCode: (v: string) => void;
  reason: string;
  setReason: (v: string) => void;
  priority: string;
  setPriority: (v: string) => void;
  targets: string;
  setTargets: (v: string) => void;
  usesLeft: string;
  setUsesLeft: (v: string) => void;
  ttlSeconds: string;
  setTtlSeconds: (v: string) => void;
  valid: boolean;
  pending: boolean;
  onSubmit: () => void;
  onCancel: () => void;
}) {
  const { t } = useI18n();
  if (!action) return null;
  return (
    <form
      className="mt-2 space-y-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        if (valid && !pending) onSubmit();
      }}
    >
      {action === "steer_work" && (
        <Textarea
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          placeholder={t("decisions.placeholder.message")}
          rows={2}
          disabled={pending}
        />
      )}
      {action === "kill_work" && (
        <div className="flex flex-wrap gap-1.5">
          <Select value={reasonCode} onValueChange={setReasonCode} disabled={pending}>
            <SelectTrigger className="h-8 w-44 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {KILL_REASON_CODES.map((code) => (
                <SelectItem key={code} value={code}>
                  {t(`decisions.reasonCode.${code}`, code)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Input
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={t("decisions.placeholder.reason")}
            className="h-8 flex-1 text-xs"
            disabled={pending}
          />
        </div>
      )}
      {action === "retire_intent" && (
        <Input
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder={t("decisions.placeholder.reasonRequired")}
          className="h-8 text-xs"
          disabled={pending}
        />
      )}
      {action === "reprioritize_intent" && (
        <div className="flex flex-wrap gap-1.5">
          <Input
            type="number"
            min={0}
            max={100000}
            value={priority}
            onChange={(e) => setPriority(e.target.value)}
            placeholder={t("decisions.placeholder.priority")}
            className="h-8 w-32 text-xs"
            disabled={pending}
          />
          <Input
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={t("decisions.placeholder.reason")}
            className="h-8 flex-1 text-xs"
            disabled={pending}
          />
        </div>
      )}
      {action === "issue_capability" && (
        <div className="space-y-1.5">
          <Input
            value={targets}
            onChange={(e) => setTargets(e.target.value)}
            placeholder={t("decisions.placeholder.targets")}
            className="h-8 font-mono text-xs"
            disabled={pending}
            aria-label={t("decisions.placeholder.targets")}
          />
          <div className="flex flex-wrap items-end gap-1.5">
            <label className="flex flex-col gap-0.5 text-[10px] uppercase tracking-wide text-muted-foreground">
              {t("decisions.capability.uses")}
              <Input
                type="number"
                min={1}
                max={10}
                value={usesLeft}
                onChange={(e) => setUsesLeft(e.target.value)}
                className="h-8 w-20 font-mono text-xs tabular-nums"
                disabled={pending}
              />
            </label>
            <label className="flex flex-col gap-0.5 text-[10px] uppercase tracking-wide text-muted-foreground">
              {t("decisions.capability.ttl")}
              <Input
                type="number"
                min={60}
                max={7200}
                step={60}
                value={ttlSeconds}
                onChange={(e) => setTtlSeconds(e.target.value)}
                className="h-8 w-24 font-mono text-xs tabular-nums"
                disabled={pending}
              />
            </label>
          </div>
        </div>
      )}
      <div className="flex gap-1.5">
        <Button type="submit" size="sm" disabled={!valid || pending}>
          {t("decisions.action.confirm")}
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={onCancel} disabled={pending}>
          {t("decisions.action.cancel")}
        </Button>
      </div>
    </form>
  );
}

/**
 * 探索链路图（拓扑视角）——探索投影的 node/edge 模型的分层 SVG 渲染
 * （association-graph 房规：无新图库、确定性布局、legend 全列零计数置灰、
 * 无障碍平行列表）。固定五列 = 七环的领域链路：起点 → 执行(Eino) → 假设 →
 * 事实 → 发现；只画投影里结构存在的边，不软匹配不编造。
 */

/** 固定列序（observation 与 evidence 同居事实列）。 */
const COL_ORDER: ExplorationKind[] = [
  "goal",
  "agent",
  "hypothesis",
  "evidence",
  "finding",
];

const LAYOUT = {
  columnWidth: 230,
  rowHeight: 78,
  marginX: 24,
  headerHeight: 34,
  cardWidth: 196,
  cardHeight: 62,
};

const REL_STROKE = EXPLORATION_REL_STROKE;

const REL_LEGEND: { rel: string; key: string }[] = [
  { rel: "claim", key: "explore.rel.claim" },
  { rel: "yields", key: "explore.rel.yields" },
  { rel: "proves", key: "explore.rel.proves" },
  { rel: "supports", key: "explore.rel.supports" },
];

interface Positioned extends ExplorationNode {
  col: number;
  x: number;
  y: number;
}

export function ExplorationGraph({ view }: { view: ExplorationView }) {
  const { t } = useI18n();
  const [selectedID, setSelectedID] = useState<string | null>(null);

  const { positioned, edges, width, height, relCounts } = useMemo(() => {
    const colOf = (kind: ExplorationKind): number => {
      if (kind === "observation") return COL_ORDER.indexOf("evidence");
      return COL_ORDER.indexOf(kind);
    };
    // Shared layered layout (lib/layered-graph) — same geometry the
    // association graph uses, so the two canvases cannot drift apart.
    const laid = layoutLayered(
      view.nodes
        .map((node) => ({ ...node, column: colOf(node.kind) }))
        .filter((node) => node.column >= 0),
      LAYOUT,
    );
    const positioned: Positioned[] = laid.positioned.map((entry) => ({
      ...entry.node,
      col: entry.column,
      x: entry.x,
      y: entry.y,
    }));
    const posByID = new Map(positioned.map((n) => [n.id, n]));
    const edges = view.edges
      .map((edge) => {
        const from = posByID.get(edge.src);
        const to = posByID.get(edge.dst);
        if (!from || !to) return null;
        return { from, to, rel: edge.rel };
      })
      .filter((e): e is NonNullable<typeof e> => e !== null);
    return {
      positioned,
      edges,
      // The column header row is the contract: keep all five columns wide even
      // when a kind has no nodes yet.
      width: LAYOUT.marginX + COL_ORDER.length * LAYOUT.columnWidth + 24,
      height: Math.max(
        200,
        LAYOUT.headerHeight + laid.maxRows * LAYOUT.rowHeight + 8,
      ),
      relCounts: countRelations(edges),
    };
  }, [view]);

  const posByID = useMemo(() => new Map(positioned.map((n) => [n.id, n])), [positioned]);
  const selected = selectedID ? posByID.get(selectedID) : null;
  const neighbors = useMemo(() => {
    if (!selected) return { up: [] as ExplorationNode[], down: [] as ExplorationNode[] };
    const up: ExplorationNode[] = [];
    const down: ExplorationNode[] = [];
    for (const e of view.edges) {
      if (e.dst === selected.id) {
        const n = posByID.get(e.src);
        if (n) up.push(n);
      }
      if (e.src === selected.id) {
        const n = posByID.get(e.dst);
        if (n) down.push(n);
      }
    }
    return { up, down };
  }, [selected, posByID, view.edges]);

  return (
    <div className="space-y-3">
      <div className="overflow-x-auto rounded-md border border-border bg-background/40">
        <svg
          role="img"
          aria-label={t("explore.graphLabel")}
          viewBox={`0 0 ${width} ${height}`}
          className="min-w-[880px]"
        >
          {/* 列头 = 领域链路阶段。 */}
          {COL_ORDER.map((kind, col) => (
            <text
              key={kind}
              x={LAYOUT.marginX + col * LAYOUT.columnWidth + 8}
              y={20}
              className="fill-muted-foreground text-[10px] uppercase tracking-wide"
            >
              {t(`explore.col.${kind}`)}
            </text>
          ))}
          {edges.map((edge, i) => (
            // 右缘中点 → 左缘中点的三次贝塞尔（同列/相邻列均可读）。
            <path
              key={`e-${i}`}
              d={curvePath(edge.from, edge.to, LAYOUT)}
              fill="none"
              stroke={relStroke(edge.rel, REL_STROKE)}
              strokeWidth={1.4}
              strokeOpacity={0.75}
            />
          ))}
          {positioned.map((node) => (
            <g
              key={node.id}
              tabIndex={0}
              role="listitem"
              aria-label={`${node.title} · ${t(`explore.kind.${node.kind}`)}`}
              className="cursor-pointer outline-none"
              onClick={() => setSelectedID(node.id)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  setSelectedID(node.id);
                }
              }}
            >
              <rect
                x={node.x}
                y={node.y}
                width={LAYOUT.cardWidth}
                height={LAYOUT.cardHeight}
                rx={6}
                className="fill-card stroke-border"
                strokeWidth={1}
              />
              <circle cx={node.x + 12} cy={node.y + 14} r={4} fill={explorationDot(node)} />
              <text x={node.x + 22} y={node.y + 18} className="fill-foreground text-[11px] font-medium">
                {truncate(node.title || explorationShortID(node), 20)}
              </text>
              <text x={node.x + 10} y={node.y + 36} className="fill-muted-foreground font-mono text-[9px]">
                {truncate(explorationShortID(node), 24)}
              </text>
              <text x={node.x + 10} y={node.y + 52} className="fill-muted-foreground text-[9px]">
                {truncate(
                  [
                    node.state && node.kind !== "finding" ? t(`explore.state.${node.state}`) : "",
                    node.kind === "finding" && node.severity ? node.severity : "",
                    node.up > 0 ? `↑${node.up}` : "",
                    node.down > 0 ? `↓${node.down}` : "",
                  ]
                    .filter(Boolean)
                    .join(" · "),
                  30,
                )}
              </text>
            </g>
          ))}
        </svg>
      </div>

      {/* Legend: 边词表全列，零计数置灰（词表即契约——association-graph 同规）。 */}
      <div className="border-t border-border pt-3">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5" aria-label={t("explore.legendLabel")}>
          {REL_LEGEND.map((entry) => {
            const count = relCounts[entry.rel] ?? 0;
            return (
              <span
                key={entry.rel}
                className="inline-flex items-center gap-1.5 text-xs text-muted-foreground"
              >
                <span
                  aria-hidden
                  className={cn("h-0.5 w-3 rounded-full", count === 0 && "opacity-40")}
                  style={{ background: REL_STROKE[entry.rel] }}
                />
                {t(entry.key)}
                <span className="font-mono text-[10px] tabular-nums">{count}</span>
              </span>
            );
          })}
        </div>
      </div>

      {/* 无障碍平行列表（图的主键盘/读屏面）。 */}
      <ul className="space-y-1.5" aria-label={t("explore.listLabel")}>
        {view.nodes.map((node) => (
          <li key={node.id}>
            <button
              type="button"
              onClick={() => setSelectedID(node.id)}
              className="flex w-full flex-wrap items-center gap-2 rounded-sm border border-border/70 px-2.5 py-1.5 text-left text-xs transition-colors hover:bg-accent/40"
            >
              <span aria-hidden className={cn("size-1.5 shrink-0 rounded-full")} style={{ background: explorationDot(node) }} />
              <span className="text-[10px] uppercase tracking-wide text-muted-foreground">
                {t(`explore.kind.${node.kind}`)}
              </span>
              <span className="min-w-0 flex-1 truncate">{node.title || explorationShortID(node)}</span>
              <span className="font-mono text-[10px] text-muted-foreground">{explorationShortID(node)}</span>
            </button>
          </li>
        ))}
      </ul>

      {/* 节点详情：属性 + 上游/下游（可点击切换选中）。 */}
      <Dialog open={!!selected} onOpenChange={(open) => !open && setSelectedID(null)}>
        <DialogContent className="sm:max-w-md">
          {selected && (
            <>
              <DialogHeader>
                <DialogTitle className="flex items-center gap-2">
                  <span aria-hidden className="size-2 rounded-full" style={{ background: explorationDot(selected) }} />
                  {selected.title || explorationShortID(selected)}
                </DialogTitle>
                <DialogDescription className="font-mono text-[11px]">
                  {explorationShortID(selected)} · {t(`explore.kind.${selected.kind}`)}
                </DialogDescription>
              </DialogHeader>
              <div className="space-y-2 text-xs">
                <dl className="space-y-1">
                  {selected.state && (
                    <DetailRow label={t("explore.detail.state")} value={t(`explore.state.${selected.state}`)} />
                  )}
                  {selected.severity && (
                    <DetailRow label={t("explore.detail.severity")} value={selected.severity} />
                  )}
                  {selected.nature && (
                    <DetailRow
                      label={t("explore.detail.nature")}
                      value={t(selected.nature === "observed" ? "info.nature.observed" : "info.nature.inferred")}
                    />
                  )}
                  {selected.actor && (
                    <DetailRow label={t("explore.detail.actor")} value={selected.actor} mono />
                  )}
                  <DetailRow label={t("explore.detail.seq")} value={`#${selected.seq}`} mono />
                  <DetailRow label={t("explore.detail.at")} value={new Date(selected.at).toLocaleString()} mono />
                </dl>
                {selected.summary && (
                  <p className="whitespace-pre-wrap rounded-sm border border-border/70 bg-muted/30 p-2 leading-relaxed">
                    {selected.summary}
                  </p>
                )}
                {(neighbors.up.length > 0 || neighbors.down.length > 0) && (
                  <div className="space-y-1 border-t border-border pt-2">
                    {neighbors.up.length > 0 && (
                      <NeighborRow
                        label={t("explore.detail.upstream")}
                        nodes={neighbors.up}
                        onSelect={setSelectedID}
                      />
                    )}
                    {neighbors.down.length > 0 && (
                      <NeighborRow
                        label={t("explore.detail.downstream")}
                        nodes={neighbors.down}
                        onSelect={setSelectedID}
                      />
                    )}
                  </div>
                )}
              </div>
            </>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}

function DetailRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex gap-3">
      <span className="w-16 shrink-0 text-muted-foreground">{label}</span>
      <span className={cn("min-w-0 break-all", mono && "font-mono text-[11px]")}>{value}</span>
    </div>
  );
}

function NeighborRow({
  label,
  nodes,
  onSelect,
}: {
  label: string;
  nodes: ExplorationNode[];
  onSelect: (id: string) => void;
}) {
  const { t } = useI18n();
  return (
    <p className="flex flex-wrap items-center gap-1.5">
      <span className="text-muted-foreground">{label}</span>
      {nodes.map((n) => (
        <button
          key={n.id}
          type="button"
          onClick={() => onSelect(n.id)}
          className="rounded-sm border border-border/70 px-1.5 py-0.5 font-mono text-[10px] transition-colors hover:bg-accent/40"
          title={n.title || n.id}
        >
          {explorationShortID(n)}
        </button>
      ))}
      <span className="font-mono text-[10px] text-muted-foreground">
        {nodes.length} {t("explore.detail.nodes")}
      </span>
    </p>
  );
}

function truncate(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max)}…` : s;
}
