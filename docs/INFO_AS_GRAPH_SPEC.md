# 信息即图 — 统一信息空间实施规格（v0.2）

> **状态**：IG0–IG7 已落地（2026-11-24）；IG6b–IG6d 仍为待实施项。本文定义目标模型、迁移步骤与验收口径，现状事实以代码与测试为准。
>
> **定位**：补充 `docs/CORE_PHILOSOPHY.md` §3/§4/§7/§8，把“图 = 信息汇总”这一认识落到可实施的查询、存储与调度改动上。
>
> **边界**：`docs/INFO_GRAPH_CONTRACT.md` 是 intent 生命周期与确认门/不变量的运行期契约（由另一会话维护）。本文不修改 `ConcludeIntent`、claim 谓词、precondition/confirmation 语义；两者冲突时以该契约为准。
>
> **代码基线**：2026-11-24，`internal/graph`、`internal/infoitem`、`internal/coldpath`、`internal/orchestrator`、`internal/agent` 当前实现。

---

## 0. 结论

1. **信息就是图**：探索图节点是“信息聚合对象”，边是“信息关系 + 工作关系”。图不是信息的索引副本，而是信息在决策粒度上的汇总。
2. **InfoItem 是原子信息层**：一次观测/一次推导一条实例，承担来源、坐标、审计与跨扫描检索。它不是“另一个信息世界”，而是图的叶子/证据层。
3. **Evidence 是投影视角**：同一原子信息在某条 hypothesis 下的“支持/反驳/控制”角色。
4. 当前代码的差距不是“没有信息模型”，而是：
   - 图节点没有统一的坐标块，坐标只存在于 InfoItem 表；
   - 三类点亮查询实现在 `info_items` 上，不在 `exploration_nodes/edges` 上；
   - planner 消费的是图摘要（counters/intents/ledger/notes），不是图上的点亮结果；
   - 图边混有信息关系（`supports`）与工作关系（`yields/depends_on`），没有显式分层。
5. 本规格的目标：**在不引入图数据库、不改状态权威、不加用户可见开关的前提下，把“点亮”落到统一读视图上，让 planner/worker 能对“图这个信息空间”做确定性查询。**

---

## 1. 术语与不变式

### 1.1 术语

| 术语 | 定义 | 代码位置 |
|---|---|---|
| 信息聚合节点 | 图节点，表达一条可被调度的信息对象（目标/假设/事实/发现/提示/摘要/意图） | `exploration_nodes` |
| 信息关系边 | 表达信息之间支持、证明、派生、矛盾、引用等关系 | `exploration_edges` |
| 工作关系边 | 表达意图的派生、产出、依赖、覆盖等执行关系 | `exploration_edges` |
| 原子信息 | 一次观测或一次推导的全局实例，带七维坐标 | `info_items` |
| 证据投影 | 原子信息在 hypothesis 下的角色视图 | ledger `Evidence` |
| 点亮 | 对信息空间做区域过滤、关系传播或有界切片 | 目标：统一读视图 |

### 1.2 不可破坏的不变式

1. **状态权威不变**：图节点生命周期（open/running/done/...）与 `ConcludeIntent` 事务仍是 intent 的唯一权威；InfoItem 置信度、VOI、点亮结果都不得直接改状态或产生 finding。
2. **报告门不变**：finding 接受与验证标签仍只由 `report_vulnerability` 门管线产生；Verifier 判定、工具面、时限不动。
3. **observed/inferred 不混层**：inferred 必须有 `supports`；没有支持的 inferred 不得进入证明、判定或收工依据。
4. **事件不是权威**：`run_events`、domain journal、图投影仍是审计/读模型；状态不由事件重放重建。
5. **单库形态**：PostgreSQL 是唯一持久态；不引入 Neo4j/Mongo/pgvector。
6. **确定性**：同一快照上的点亮查询与排序必须给出相同结果；不得引入模型即时打分或随机补值。
7. **不新增用户可见环境变量**：迁移通过代码接口与里程碑推进，不把开关丢给设置页。

---

## 2. 现状（代码事实）

### 2.1 图的形状

`internal/infoitem/postgres/migrations/0019_exploration_graph_authority.sql`：

- `exploration_nodes(id, scan_id, instance_id, kind, state, priority, origin, owner, lease_until, blocked_reason, delete_reason, payload, content_version, created_at, updated_at, completed_at)`
- `kind IN ('goal','intent','belief','fact','finding','hint','digest')`
- `exploration_edges(scan_id, src_id, rel, dst_id)`，`rel IN ('spawns','derived_from','yields','proves','supports','covers')`；`0022` 增加 `depends_on`。
- `0023` 增加 `unit_key` 及 `(scan_id, kind, unit_key)` 部分唯一索引；`payload` 仍是 TEXT（0013 字节保真规则）。
- `content_version` 由触发器维护，只有 `state` 或 `payload` 真正变化才递增。

### 2.2 原子信息的形状

`internal/infoitem/postgres/migrations/0001_infoitem_coldpath.sql`：

- `info_items(id, scan_id, source_seq, nature, content, coordinates, source, subject, purpose, context, relation_ids, supports, confidence, coordinate_status, valid_from, valid_until, observed_at, created_at)`，主键 `(scan_id, id)`。
- `info_item_edges(scan_id, source_seq, from_id, to_id, type, weight)`，类型 `supports/contradicts/references/same_session/same_subject`。
- `info_audit_records`、`info_sync_watermarks`（`pending/applied/gap/failed`）。

### 2.3 已有的投影与查询

- `internal/agent/graph_projection.go:30` 把 ledger 的 hypothesis/evidence、accepted findings 投影成 belief/fact 节点；fact payload 带 `info_item_id`（`:318`）。
- `internal/scanctx/ledger_journal.go:332` 用 `StableEvidenceID` 把 evidence 映射到 InfoItem；`:385` 写 `supports` 关系。
- 三类点亮已实现于 `internal/infoitem/postgres/store.go`：`Region:412`、`Associate:510`、`ContextSlice:611`；`RegionQuery/AssociationQuery/ContextQuery` 在 `internal/infoitem/query.go`。
- 消费者：worker 的 `search_info`（`internal/agent/info_tools.go:15`）、Web 只读 API（`internal/web/scan_info.go`）、跨扫描历史（`internal/agent/ledger_cross_scan.go:72`，限本项目任务之间）、报告渲染的 InfoItem 引用。
- planner 的 prior-work 是 `infoitem.Rank` 对 **ledger 文档**做词法排序（`internal/orchestrator/planner.go:516`），不是查 `info_items`；planner 的 intent 优先级固定 `1000 - phase*10`（`:859`）。
- VOI 只在 hypothesis 认领排序：`(1-w)*confidence + w*VOI`，`w<=0.30`，稳定排序（`internal/scanctx/ledger_voi.go:53`）。

### 2.4 关键缺口

1. 图节点没有统一的坐标块，无法按 subject/purpose/nature/time 直接过滤图。
2. `graph.Repository` 只提供整表 `ListEdges`，没有按起点/边类型/深度的遍历接口或切片查询；点亮只能走 InfoItem 表。
3. 图与原子层之间只有 payload 里的 `info_item_id`，没有可用于 join/遍历的链接表。
4. 图边没有区分“信息关系”与“工作关系”，也没有 `contradicts`。
5. planner 的“点亮”是文献式词法召回，不是信息空间的确定性查询。

---

## 3. 目标模型

### 3.1 三层，一张读视图

```text
                 ┌─────────────────────────────────────────┐
                 │ 统一信息空间（读视图 info_space_*）        │
                 │  aggregate 层：exploration_nodes/edges    │
                 │  atom 层：info_items / info_item_edges    │
                 └─────────────────────────────────────────┘
                          ▲                         ▲
            聚合/调度权威  │                         │ 原子/审计权威
        exploration graph（PG）              InfoItem cold path（PG）
                          ▲                         ▲
                     planner/worker            domain journal → River
```

- **写路径不变**：图仍由 `UpsertNode/Transition/ConcludeIntent` 写；InfoItem 仍由 journal → River → PG 写。
- **新增的是读路径**：`internal/infospace`（新包，名字暂定）提供 `Region/Associate/ContextSlice`，SQL 同时读聚合层与原子层。
- 不做双写、不复制正文、不引入第二套状态。

### 3.2 节点坐标块（payload 约定，非 DB 迁移）

图节点 payload 新增可选 `info` 块，承载“可被点亮”的坐标；正文仍只放摘要/引用：

```json
{
  "info": {
    "subject": {"kind": "endpoint", "identifier": "/api/users"},
    "purpose": "sqli",
    "nature": "observed",
    "observed_at": "2026-11-24T08:00:00Z",
    "context": {"target": "10.0.1.5:8080", "phase": "6"},
    "confidence": 0.8
  },
  "refs": {"evidence_id": "EV-3", "info_item_id": "01J..."}
}
```

坐标约定（由 `graph_projection` 与 planner 落图时填充）：

| kind | subject | purpose | nature |
|---|---|---|---|
| `goal` | task/target | `goal` 或 criterion id | 人工目标=observed；派生判据=inferred |
| `intent` | target | vuln_class 或 objective_ref | inferred（行动意图，不承载真值） |
| `belief` | endpoint/target | vuln_class | inferred（必须有 supports/evidence 引用） |
| `fact` | endpoint/subject | evidence kind | observed 或 inferred |
| `finding` | endpoint | vuln_class | 标注性质，接受与否仍由报告门决定 |
| `hint` | task | `human_guidance` | observed |
| `digest` | 区域标识 | `summary` | inferred |

规则：

- `subject`/`purpose`/`observed_at` 不足以表达坐标时，保持为空而不是编造。
- `state/owner/lease_until/priority` 是控制字段，**不进入** `info` 块；它们是信息生命周期，不是信息内容。
- 图节点不复制正文；正文仍在 ledger/InfoItem/工具归档里。

### 3.3 边的分层

新增读视图层面的关系分类（不要求立即改写在存储里的 rel）：

| 现有 rel | 分类 | 说明 |
|---|---|---|
| `supports` | 信息关系 | fact/belief 的证据支持 |
| `proves` | 信息关系 | 证明关系 |
| `spawns` / `derived_from` | 信息关系（谱系） | 派生来源 |
| `yields` | 工作关系 | intent 产出 fact |
| `depends_on` | 工作关系 | 调度依赖 |
| `covers` | 工作关系 | 覆盖关系 |
| `contradicts`（新增建议） | 信息关系 | 与 InfoItem 的 contradicts 对齐 |
| `materializes`（读模型） | 聚合→原子 | `graph_info_links` 投影，只存在于 `info_space_edges`，不写入 `exploration_edges` |

若 IG1 需要关联点亮跨“矛盾”传播，则新增 migration 放宽 `ck_exploration_edge_rel` 并加 `contradicts`；否则先在读视图里由 ledger 证据角色推导，不落图。

### 3.4 原子层链接

目标不是把每条 InfoItem 变成图节点（会放大节点量并与“聚合”语义冲突），而是建立**可 join 的链接**：

```sql
CREATE TABLE IF NOT EXISTS graph_info_links (
    run_id       TEXT   NOT NULL,
    node_id      BIGINT NOT NULL,
    info_item_id TEXT   NOT NULL,
    role         TEXT   NOT NULL DEFAULT 'provenance', -- provenance|supports|contradicts|materializes
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, node_id, info_item_id, role)
);
CREATE INDEX ... ON graph_info_links (run_id, info_item_id);
```

- IG0 先用 payload 里的 `info_item_id` 做只读 join，不建表；
- IG3 再引入链接表并回填（fact payload、finding refs、ledger evidence 的 stable id）。
- 链接表不是状态权威；它只服务查询与审计。

### 3.5 统一读视图

IG0 已落地两个只读视图（迁移 `9000_info_space_views.sql` + `9004_info_items_instance.sql`（信息空间保留 9000+ 区间，避免并行会话按顺序号撞车））：

- `info_space_items`：`scan_id, run_id, info_id, layer, kind, state, unit_key, priority, origin, subject, subject_kind, purpose, nature, observed_at, context, payload, created_at`
- `info_space_edges`：`scan_id, run_id, src_info_id, dst_info_id, rel, layer, created_at`

**规范键是 `run_id`，不是 `scan_id`。** 两层的原生身份不同：

- `exploration_nodes.scan_id` 是 run 身份（D11 instance id）；
- `info_items.scan_id` 是产生它的 ScanContext id（一个 run 可能跨 sibling 上下文）；
- 原子的 `run_id` 优先取写入时从 `run_journal(scan_id, seq)` 盖上的 `info_items.instance_id`；历史行由 9004 回填，缺失时回退到自身 `scan_id`。

实现：`internal/graph/infospace.go` 的 `Region/Associate/ContextSlice`；聚合层 subject/purpose 与原子层 run 键的表达式索引见 9003/9004，`EXPLAIN` 验收见 IG1b。

聚合层的 `subject/purpose/nature` 优先读 payload 的 `info` 块，缺失时回退到当前投影字段（`target/endpoint/target_kind/vuln_class/objective_ref`），因此 IG0 在 IG2 之前也可用。视图对 `payload` 做防御性 JSON 解码（`xalgorix_safe_jsonb`），单条坏行不会让整个信息空间查询失败。

视图只读（`UNION ALL` 不可写）；不用于状态迁移，不参与 claim。

---

## 4. 激活查询（目标接口）

新包 `internal/infospace`（只读）：

```go
type Reader interface {
    Region(ctx context.Context, q RegionQuery) (RegionResult, error)
    Associate(ctx context.Context, q AssociationQuery) (AssociationResult, error)
    ContextSlice(ctx context.Context, q ContextQuery) (ContextResult, error)
}
```

### 4.1 Region

- 过滤：`run_ids/all_runs, layer, kinds, state, subject, subject_kind, purpose, nature, context_equals, observed_after/before`。
- 排序：`observed_at DESC, info_id ASC`（keyset 游标同 InfoItem 语义）。
- 上限：默认 50，最大 200。
- 返回：`info_id, layer, kind, state, subject, purpose, nature, observed_at, confidence?, refs?`，正文不返回；详情由 ledger/InfoItem 显式取。
- 缺口：只查 `aggregate` 时完整；跨到 `atom` 时读取 `info_sync_watermarks`，`status IN (gap,failed)` 返回 `Incomplete=true` + `GapReason`。
- v0.2/IG7：Region 的**聚合形态**是 `Heatmap`（主体 × 循环阶段计数，行数有上限），用于 `/info` 项目热力图；它只返回计数与坐标，drill-down 回到 Region（同 subject + 同阶段 + 同时间窗）。

### 4.2 Association

- 起点：`info_id` 列表（聚合层或原子层）。
- 边集：`exploration_edges` + `info_item_edges`（通过 `graph_info_links` join；IG0 用 payload 字段）。
- 深度：默认 2，硬上限 3；`limit` 默认 20，最大 200。
- 返回：命中项 + 路径 + 深度 + `Truncated`/`Incomplete`。
- 禁止：无界递归、跨 run 关系边（跨 run 只支持 Region 的 `AllRuns`）。

### 4.3 ContextSlice

- 固定 `run_id + subject(+kind) + context_equals`，开放 `kinds/layer`，返回混合视图。
- 排序与上限同 Region；不返回正文全文，只返回摘要 + 引用。

### 4.4 索引

- 聚合层：现有 `exploration_nodes_scan_kind_idx`；补表达式索引：
  ```sql
  CREATE INDEX ... ON exploration_nodes
    (scan_id,
     (payload::jsonb->'info'->'subject'->>'identifier'),
     (payload::jsonb->'info'->>'purpose'),
     created_at DESC);
  ```
  （IG0 用 `payload->>'target'`/`vuln_class` 先跑，IG2 再规范到 `info` 块。）
- 原子层：现有 `info_items_subject_idx`、`info_items_purpose_idx`、`info_items_context_gin_idx`。
- 视图查询必须在 `EXPLAIN` 中命中索引；IG1 验收包含查询计划检查（无全表扫描，除非 `AllScans` 明确允许）。

---

## 5. 写路径与投影

保持不变，只补“坐标填充”和“链接”：

1. `graph_projection.syncExplorationGraph` 投影 belief/fact/finding 时，按 §3.2 填 `info` 块；正文仍不复制。
2. planner `apply` 落 intent 时填 `info.subject/purpose/nature=inferred/observed_at`，控制字段留在节点列。
3. `report_vulnerability` 成功后的 finding 投影补 `info_item_id` 引用（已有路径）。
4. ledger evidence → InfoItem 的 stable id 不变；inferred 无 supports 仍不得进入决策。
5. 冷路径写失败仍走水位 + reconciler；不得为了“点亮完整”而同步阻塞 journal。

---

## 6. planner / worker 消费

### 6.1 planner overview

替换/补充 `priorWorkBlock` 的输入：

- `Region{scan_id, subject=target, purpose=VulnClass 列表, nature in (observed, inferred), limit=20}`；
- `ContextSlice{scan_id, subject=target, context={phase?}, limit=20}`；
- 仅返回 ID/摘要/来源/nature；正文经 `read_info_item` 或 ledger 取。
- 若 `infospace` 不可用（PG 未配置/视图未迁移），回退到现有 `infoitem.Rank` 词法召回；行为不变。

验收：

- 同一不可变快照，`overview` 输出稳定（ID 顺序、摘要顺序一致）。
- 不再出现“同一方向重复建 intent”的增量（与现有 dedupe 联合测试）。
- prompt token 不高于当前 +10%（有界摘要）。

### 6.2 intent 排序（IG4，需单独裁决）

当前 `Priority: 1000 - phase*10`。目标：

```text
base = 1000 - phase*10            // 现有确定性基线
score = (1-w)*normalize(base) + w*VOI
w <= 0.30
tie-break: score desc, priority desc, id asc
```

- VOI 来源：intent 关联的 belief/fact 的 VOI 聚合（缺省 0，记录 `voi_estimated=false`）。
- 必须先通过纯排序测试：同一快照两次排序结果一致；不得读取时间/随机数。
- 未通过 IG4 前，planner 继续用现有 priority，不引入 VOI。

### 6.3 gap→intent（IG5，可选）

确定性 `GapScanner`：输入 coverage epoch、ledger 可调度池、surface 缺口；输出候选 `{subject,purpose,vuln_class,refs}`；由 planner 决定是否落 intent（仍走 apply 校验与去重）。

- 不替代 planner；不直接写图。
- 无具体 target 的候选必须被丢弃，不允许占位符。

---

## 7. 迁移里程碑与验收

> 每个里程碑独立可运行、可回滚；不跳步。IG0–IG3 不改调度语义；IG4/IG5 才触及排序/生成。

### IG0 — 统一读视图（只读）— 已落地（2026-11-24）

- 迁移：`9000_info_space_views.sql`（两视图 + 防御性 JSON/时间解码 + `run_id` 归一化）；`9001_info_space_indexes.sql`；`9002_graph_info_links.sql`；`9003_info_space_index_fix.sql`（按视图 COALESCE 形状重建表达式索引）；`9004_info_items_instance.sql`（`info_items.instance_id` + 回填 + run 键索引 + 视图去 join）。信息空间迁移统一使用 9000+ 保留区，普通功能继续用顺序号。
- 测试：`internal/infoitem/postgres/info_space_integration_test.go::TestInfoSpaceViewsProjectBothLayers`。
  - 聚合/原子行数与源表一致，跨 sibling 上下文按 `run_id` 可见；
  - 旧 payload 的 `target/endpoint/target_kind/vuln_class` 回退可用；
  - `info_space_items` 拒绝写入（`UNION ALL` 不可更新）；
  - 原子关系边与聚合边在 `info_space_edges` 同形返回。
- 回滚：drop 两视图与两个 helper 函数；无写入变化。

### IG1 — 图上的三类点亮

#### IG1a — 读取层 + worker 工具 — 已落地（2026-11-24）

- `internal/infospace`：查询类型与校验（`RegionQuery`/`AssociationQuery`/`ContextQuery`、limits、depth、known relations）。
- `internal/graph/infospace.go`：`PGStore` 实现 `infospace.Reader`，读 IG0 视图，`run_id` 为规范键。
  - Region：属性过滤 + keyset（`observed_at DESC, info_id ASC`）+ `HasMore/NextCursor`；
  - Association：递归 CTE，深度 ≤3、结果上限、路径与关系类型、`Truncated`；
  - ContextSlice：固定 `run_id + subject(+kind) + context`，有界；
  - inferred 原子无 `supports` 不进入激活；聚合层不因此过滤（图是权威对象）；
  - 冷路径 gap：原子参与查询时读 `info_sync_watermarks`，返回 `Incomplete/GapReason`；aggregate-only 永远是完整结果。
- worker `search_info`：`source=auto`（默认，图可用时优先）/`graph`/`info`；`layer=aggregate|atom|all`；输出只含 ID/坐标/摘要，正文仍走 `read_info_item`/ledger。
- 测试：
  - `internal/graph/infospace_integration_test.go::TestInfoSpaceRegionContextAndAssociation`
  - `internal/infospace/query_test.go`
  - `internal/agent/info_tools_test.go::TestInfoSpaceResult...`

#### IG1b — Web 只读 API 的 layer 过滤 + 查询计划验收 — 已落地（2026-11-24）

- `internal/web/scan_info.go`：`/api/info/region|context|association` 增加 `layer=aggregate|atom|all`（默认 all）；图可用时按 `run_id` 走 `infospace`，否则回退冷路径；`layer=aggregate` 无图时返回显式 503，不伪装空结果。响应新增可选 `layer`/`source`，旧字段不变。
- 测试：`internal/web/scan_info_graph_test.go`（非法 layer、透传、run-id 白名单、跨层 association、回退/503、确定性合并）。
- 表达式索引与 `EXPLAIN` 验收（已落地）：`9003_info_space_index_fix.sql` + `TestInfoSpaceSchemaObjectsAndIndexPlan`；
  - 聚合层 `(scan_id, COALESCE(info.subject, target, endpoint), COALESCE(info.purpose, vuln_class, objective_ref), created_at)`；
  - 原子层 run 键：9004 给 `info_items` 增加 `instance_id`（写入时从 `run_journal` 盖章、历史回填），视图 `run_id = COALESCE(NULLIF(instance_id,''), scan_id)`，索引 `info_items_run_observed_idx`；`EXPLAIN` 已验收。

- 回滚：删除 `infospace` 包、`search_info` graph 分支与 Web 的 graph 路径；工具/接口回退到 InfoItem store。

### IG2 — planner 消费点亮 — 已落地（2026-11-24）

- `overview()` 新增有界 `Information space` 块：`Region`（subject=目标，purpose=唯一明确类，kinds 排除 intent 避免与 Existing intents 重复，limit 12）+ `ContextSlice`（limit 8），总行数 ≤20；只渲染 layer/kind/state/id/unit/subject/purpose/nature，不内联 payload；`Incomplete/GapReason` 如实展示。
- 读取器来源：`graph.Backend().(infospace.Reader)`；`PlannerConfig.InfoSpace` 作为测试注入。读取成功即权威：空结果不回退词法 `priorWorkBlock`，`Incomplete` 即使零条也渲染 `GapReason`；只有读取器缺失/查询失败才回退。
- 详情入口：聚合层的 `info_id` 是图节点数字 ID，不是 ledger ID；`read_info_item` 在 ledger miss 后通过 `infospace.ItemReader`（`ItemQuery{run_id, info_id}`，layer 可空、先 aggregate 后 atom）解析详情，避免“看得到但报 not found”。
- 测试：`planner_infospace_test.go`（有界、确定性、坐标、回退、空结果权威、缺口可见、详情解析）。
- 回滚：删除该块与配置项；恢复旧 overview。

### IG3 — 聚合↔原子链接 — 已落地（2026-11-24）

- `9002_graph_info_links.sql`：`graph_info_links(run_id,node_id,info_item_id,role)`，PK `(run_id,node_id,info_item_id,role)`，FK 指向 `exploration_nodes(scan_id,id) ON DELETE CASCADE`；回填 fact payload 的 `info_item_id` 与 `info_item_ids` 数组；`info_space_edges` 增加 `materializes` / `layer='link'` 分支。
- 写路径：`graph.PGStore.UpsertInfoLink`（`ON CONFLICT DO NOTHING`）；`graph_projection` 在 belief/fact/finding 投影成功后 best-effort 写链接；失败只记日志，不影响投影。
- 测试：`TestInfoSpaceRegionContextAndAssociation`（aggregate→atom 跨层 `materializes`）、`TestGraphInfoLinkUpsertIdempotentAndBackfillCompatible`（幂等/role 校验/回填兼容）。
- 回滚：删除写调用与 `graph_info_links`；payload 字段仍在。

### IG4 — intent 级 VOI — 已落地（2026-11-24）

- 公式：`base = 1 - (phase-1)/21`，`score = (1-w)*base + w*voi`，`priority = int(score*1000)`；`w = clamp(Ledger.VOIWeight(), 0, 0.30)`，缺省 0 时保持原 phase 顺序（数值幅度变化，排序不变）。
- VOI 聚合口径（已定）：匹配 intent 坐标（endpoint 或 target + vuln_class）且 `VOIEstimated` 的假设取 **max**；缺失/未估计 = 0。
- 测试：`planner_voi_test.go`（确定性、phase 顺序、w 上限、max 聚合、只改 priority）。
- 回滚：`intentPriority` 固定回 `1000-phase*10`；VOI 字段保留但不参与。

### IG5 — 缺口生成候选 — 已落地（2026-11-24，可选转为默认只读）

- `gapgen.go`：确定性候选生成（queued 且无 open/running/paused intent 覆盖的假设优先，其后是已探测未验证端点；按 `endpoint|purpose` 去重，score desc → endpoint → purpose → refs 排序，上限 8）；无具体/可派发 subject 时整体抑制，不产生占位符。
- `overview()` 仅以 advisory block 展示，不写图；planner 仍走 `apply` 的 target 校验与去重。
- 测试：`gapgen_test.go`（确定性/有界/空输入/不可派发 subject/只读）。
- 回滚：删除 block 与生成器；planner 行为不变。

### IG6 — 上下文压缩接入信息空间（IG6a 已落地；规格 `docs/CONTEXT_COMPRESSION_SPEC.md`）
> `kind='digest'` 节点与 `active → superseded` 状态机早已存在于迁移与 `internal/graph/types.go`，但此前**没有写入者**；现行压缩（`einoObserver.CompactMessages`）产出匿名 `role=user` 文本块，既不进图也不进 InfoItem，属 CORE §4 的旁路。IG6 把压缩改造为信息空间的合法投影。

#### IG6a — 修 bug + 确定性 + 真实计量 — 已落地（2026-11-24）

- `compactMessages` 识别 `role="tool"` 与 native `ToolCalls`（此前默认 eino 路径的工具/发现三节恒为空）；XML `<function=` 分支保留给 legacy；
- 工具计数、URL 列表排序去随机；notes 注入按 key 排序并加总预算（默认 6KB）；
- 触发主用 `usage.PromptTokens`（`Agent.lastPromptTokens`），字节估计只作首轮/无 usage 兜底，并修正 CJK 换算；
- 措辞改为 “Candidate Finding Mentions (UNVERIFIED — report gate decides acceptance)”，避免摘要越权；
- 压缩日志带 before/after bytes、prompt tokens、token 触发标志、notes 预算；
- 主会话裁剪（`main_chat.go`）在丢弃 `[USER]` 消息前把它投影为 `hint`（observed，`origin=human`，幂等 unit_key）；写入失败时本轮不裁剪，不静默丢失人工输入。
- 测试：`context_digest_test.go`（native tool call、确定性、权威措辞）、`context_compact_test.go`（token 触发/不触发）、`tools/notes` 预算测试、`main_chat` hint 投影测试。

#### IG6b — digest 节点落地 — 已落地（2026-11-24）

- 每次压缩写 `kind=digest`（`active`，`UnitKey` 为 region+snapshot 的内容哈希），同区域旧节点 `Transition → superseded`；
- `derived_from` 连上一条 digest，并连本次引用到的 hint/intent 节点；
- InfoItem `nature=inferred`、`purpose=summary`、`supports`=快照引用 ID（非空）；经 `scanctx.AppendContextDigest` → `domainjournal.OpContextDigest` → 既有 journal/River→PG 冷路径；append 失败只记日志，不阻塞装配；`supports` 为空时不发布 InfoItem；
- PG store 通过 `graph.InfoLinkWriter` 写 `graph_info_links(role='materializes')`（memory store 无链接面，自动跳过）；
- 验收：`TestPersistContextDigestChainAndInfoItem`（节点链、`derived_from`、journal inferred+supports+purpose、source_seq 对齐）、`TestContextDigestRequiresSupports`；`/api/scans/{id}/graph` 的 kind 过滤已含 `digest`。

#### IG6c — 热路径改投影 — 已落地（2026-11-24）

- `einoObserver.CompactMessages` 用 `buildContextSnapshot` 替换文本 digest；无权威状态可用时回退 `compactMessages` + notes（CLI/单测接线），窗口永不被替换成空内容；
- 段序见 `CONTEXT_COMPRESSION_SPEC` §5：hints → intents → beliefs（含 rejected/exhausted）→ findings（报告门）→ observed evidence（ledger）→ coverage gaps（`CoverageGaps(ScanState)`）→ notes → 老化窗口动作 + archive id → recovery；
- 每行权威标注（`[gate]/[ledger]/[graph]/[info]/[engine]/[digest]`）；总预算 25% 窗口、硬上限 128KB、每段硬上限；
- 只老化窗口外 transcript，快照每轮重算，新 digest 以 `derived_from` 连旧 digest，不做“摘要的摘要”；
- 验收：`TestContextSnapshotRendersAuthoritativeSections`、`TestContextSnapshotDeterministic`、`TestCompactMessagesProjectsSnapshotAndDigestNode`。

#### IG6d — 冷路径与消费面 — 已落地（2026-11-24）

- 冷路径复用既有 domain journal → River → PG（`OpContextDigest`），不新增队列、不接管扫描循环；
- worker 起手种子：`DispatchWorkerTaskWithSurface` 注入 8KB 区域快照，新 lane 不再从零重踩死路；
- resume：`formatResumeBriefing` 读取最新 active digest（≤12KB），notes 改用预算版；
- 主会话 `list_hints` 只读工具（`state=all|active|consumed`）；hint 的 `consumed` 生命周期仍未启用（保持 active 到扫描结束，避免一个 worker 读过后其他 worker 丢失），见 `CONTEXT_COMPRESSION_SPEC` §12；
- 验收：`TestWorkerSeedIsBoundedAndCarriesDeadEnds`、`TestMainChatListHints`、`TestLatestDigestBriefingReadsActiveDigest`、`TestLatestDigestBriefingIsBounded`；
- 回滚：删除 digest 写入与各消费者；压缩退回 IG6a 行为（文本 digest + notes）。

### IG7 — 信息空间项目热力图（循环进度 + 冷热）— 已落地（2026-11-24）

- **读模型**：`info_space_items` 上的 `GROUP BY subject, subject_kind, type`；类型由 layer/nature/kind 确定性分类：
  - `atom` + `nature=observed` → `observation`；`atom` 其余 → `inference`（必须有 `supports`，与 IG1a 的激活规则一致，无支持推断不上图）；
  - `aggregate` + `kind in (belief,intent,fact,finding)` → 同名阶段（行动/证据/判定都在图上）；
  - `goal/hint/digest` 不上图（工作/控制对象，经 Region 查询）。
- **热力语义**：每个主体取**到达的最远环**（finding > fact > intent > belief > inference > observation）作为颜色（冷→热）；块面积 = 信息条数。前端从计数推导阶段，不在后端引入第二套真实状态。
- **API**：`GET /api/info/heatmap`（契约见 `docs/API_CONTRACT.md` §3.3）。行数上限默认 40、最大 200（主体行数，不是条目数）；排序 `total DESC, subject ASC, subject_kind ASC`；`q` 是主体子串搜索（区分大小写无关）；返回 `truncated/subject_total/unlabeled/incomplete/gap_reason`。授权与 Region 相同：project 白名单 + 可选 `task_id` + 可选 `scan_id`（跨界 400）；无 graph reader 返回 503 `heatmap_unavailable`。
- **前端**：`/info` 就是项目信息星云（canvas 散点），不是表格/表单——一个主体一个点；**正圆布局**（面板居中，圆直径 = 短边）；**热量在核心**：离中心距离、点大小、颜色（冷灰蓝 → 品牌红）都编码信息量（`log1p(total)` 固定标尺 + 幂曲线，高量贴近圆心、低量在外圈）；角度与抖动来自 subject 哈希，同快照同图；对相连主体做关系感知松弛（角度靠拢、半径保持 ±20% 内），边裁剪到节点边缘、轻微弧形、长边淡化。**A 档动效**：暗色热核光晕（sprite + `lighter`）、动作/证据链光点流动（方向可视化、无箭头）、选中波纹、约 0.02 rad/s 极慢自转（拖拽/平移/切后台暂停）、首次数据从中心爆发 0.52s + 页面淡入 0.22s；`prefers-reduced-motion` 关闭全部循环动效，15s 刷新不重播。**节点可拖、空白平移、滚轮缩放、可重置**；选中节点高亮其关系边、其余变淡。Top 200 + 服务端 `q` 搜索；点一个点写 `?subject=&subjectKind=` 开详情，详情里的阶段 chips 把 Region 收窄到某一环，并列出该主体的关系段（可跳到另一端）；列表视图是键盘/读屏兜底。性能：单 canvas + DPR、30fps ambient loop、光晕 sprite 缓存、粒子只跑 chain 边且上限、手势期间从 ref 重绘（不触发 React state）、悬停只动 DOM tooltip、查询 15s 节流。URL 契约：`?project=&task=&run=&window=24h|7d|all&subject=&subjectKind=&focus=`；`?project=` 是一次性作用域指令（切到侧栏项目切换器并清理参数，之后页面跟随项目切换）。
- **关系图（2026-11-24 增补）**：`GET /api/info/relations` 把 `info_space_edges` 投影成**主体级边**（同 run 内 join 两端 `info_space_items`，排除同主体，继承白名单/时间窗/激活规则）。`category=chain` = `spawns/derived_from/yields/proves/supports/covers/depends_on`，`info` = `references/contradicts/same_session/same_subject/materializes`；前端默认只画 chain（实线），信息关联由开关打开（虚线）；**边不画箭头**，方向在详情的关系段里读。**没有 pivot/tunnel 一等关系**：图上看到的是真实关系链，边方向就是存储方向；不得把 `supports` 反推成攻击步骤或给边改名。
- **测试**：`internal/web/scan_info_heatmap_test.go`（授权/参数/q/503/非法类型）、`internal/graph/heatmap_integration_test.go`（双层计数、intent 阶段、无支持推断剔除、行上限、类型收窄、时间窗）、`internal/infospace/query_test.go`（query 校验）。
- **回滚**：删除 heatmap handler、`PGStore.Heatmap` 与项目树图组件；Region/Context/Association 行为不变。

---

## 8. 非目标

- 不引入 Neo4j/MongoDB 或任何图数据库；PostgreSQL 仍是唯一持久态。
- 不把事件溯源作为状态权威；`run_events` 仍是审计投影。
- 不引入 A*/MCTS/POMCP/DESPOT；不引入 POMDP 状态转移/观测模型。
- 不引入 pgvector/embedding；相似度仍是确定性词法重叠。
- 不支持跨扫描关系边；跨扫描 Region 历史只限本项目任务之间（项目是隔离边界）。
- 不绕过 `ConcludeIntent`、claim 谓词、precondition/confirmation、Verifier、`report_vulnerability` 门管线。
- 不新增用户可见环境变量或设置页开关。

---

## 9. 风险与对策

| 风险 | 对策 |
|---|---|
| 视图 `UNION ALL` 变慢 | 先按 layer 分开查询；聚合层走表达式索引，原子层走现有索引；`EXPLAIN` 验收 |
| 聚合与原子重复计数 | 查询必须显式 `layer`；Region 返回 `layer`，消费方按层去重 |
| 权威混淆 | `nature/confidence` 只是标注；finding 接受仍由报告门；图 state 仍是调度权威 |
| 冷路径 lag 被当成“无信息” | 跨层查询读水位，返回 `Incomplete/GapReason`；不得静默当作空结果 |
| planner prompt 变长 | 只返回 ID/摘要/引用；limit ≤20；token 回归测试 |
| 与 `INFO_GRAPH_CONTRACT` 冲突 | 不改 intent 契约/确认门；坐标块与读视图是旁路；改动前先对齐该文档 |
| 图边语义混杂 | 读视图里显式分类；新增 `contradicts` 需单独 migration，不静默复用 `supports` |

---

## 10. CORE 修订建议（已应用，2026-11-24）

已在 `docs/CORE_PHILOSOPHY.md` 顶部加入 v3.3 块，并修订 §3.1/§4.4/§7.2/§8：

```text
> **v3.3 / 信息即图（草案）**：探索图是信息空间的聚合层（节点=信息聚合，边=信息关系+工作关系）；
> InfoItem 是原子信息层（一次观测/推导一条实例），Evidence 是它在 hypothesis 下的投影。
> 三类激活（区域/关联/切片）以统一读视图 `info_space_*` 为查询入口，允许直接调用 ledger 的确定性 API。
> 图节点 payload 的 `info` 块承载坐标，控制字段（state/owner/lease）不属于信息内容。
> 本节不改变：图状态权威、ConcludeIntent、报告门、Verifier、事件非权威、无搜索算法、PG 单库。
```

- §3.1：在“InfoItem 不是 Evidence 的替代”后补一句“图节点是信息聚合，InfoItem 是原子实例；两者是同一信息空间的不同粒度”。
- §4.4：把“查询入口”改为“优先 `info_space` 统一读视图；InfoItem 表与 ledger 确定性 API 是合法实现与回退”。
- §7.2：当 IG4 验收通过后，把 VOI 的适用范围从“假设认领”扩展为“假设认领 + intent 排序”，仍保留 `w<=0.30` 与稳定 tie-break。
- §8：在 State/Operation/Obligation 后补“信息聚合视图”作为读模型，明确它不是新的写权威。

v3.3 已落地；后续冲突仍以 CORE 的更新版本为准。

---

## 11. 开放问题

1. **原子层是否升级为图节点（`kind='info'`）？** 已定：不升级。IG3 用 `graph_info_links` + `materializes` 读模型跨层，原子仍是 InfoItem 实例；节点化会放大图规模并与“聚合”语义冲突。
2. **`contradicts` 是否落图？** 未落图。当前由 ledger 证据角色与 InfoItem 关系边表达；若后续关联点亮需要图上矛盾传播，再单独加 migration。
3. **intent 是否属于“信息”？** 已定：是，按“关于下一步行动的信息”纳入统一视图；它不承载真值，state/owner/lease 仍是控制字段。
4. **跨扫描关联**：维持“仅 Region 跨扫描”，且范围严格限于本项目任务之间；跨扫描关系边需要单独的存储与权限设计，不并入本期。
5. **VOI 的 intent 聚合口径**：已定并落地为 **max**（匹配 endpoint/target + vuln_class 且 `VOIEstimated` 的假设），缺失=0；见 `planner_voi_test.go`。
