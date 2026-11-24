# 信息图契约与运行期合规（R8）

> 状态：**R8.1 契约 + 运行期合规、R8.2 上下文分层/注入硬化、R8.3 缺口/词法召回、
> R8.4 审计与韧性均已实现**（本批）。剩余为后续可选增强，见各节“未做”。
> 上位约束：`docs/CORE_PHILOSOPHY.md` v3.0（含 v3.1/R7 修订）、`docs/REDESIGN.md`
> D1–D12、`docs/DUAL_GRAPH_ORCHESTRATION.md`、`docs/PG_PRIMARY_STORAGE_SPEC.md`。
> 与宪章冲突时宪章赢；本文不改变 Verifier、`report_vulnerability` 闸门顺序、
> Evidence 上限、HITL 语义与六环安全链。

本文回答两个问题，缺一不可：

1. **设计是什么**：intent 的契约、结论事务、确认门、上下文分层、缺口/召回。
2. **运行期是否按设计走**：平台如何在不信任模型、不信任投影的前提下，
   持续证明“写路径没有绕行、投影没有漂移、模型没有越权”，并把漂移变成
   可见、可审计、可定位的信号。

只写 1 不写 2，代码会慢慢退化成“看起来像图”的旁路集合；只写 2 不写 1，
检查器没有判据。两者必须同批验收。

---

## 1. 设计契约

### 1.1 权威边界（不变）

| 对象 | 权威 | 说明 |
|---|---|---|
| Intent | PG `exploration_nodes(kind='intent')` | 生命周期、owner、lease、契约 |
| Belief | PG `exploration_nodes(kind='belief')` | Hypothesis 生命周期；Evidence 明细仍在 ledger |
| Fact / Finding / Goal / Hint | 对应 kind 节点 | Finding 仍只由 `report_vulnerability` 成功后投影 |
| Evidence 明细 | `scanctx` ledger | 40 条/假设、4096 字符上限不变 |
| finding 标签 | 报告门管线 | 不得被图或 InfoItem 改写 |
| goal 达成 | `internal/objective.Evaluate` | 确定性纯函数 |

图只存权威引用与摘要，不复制正文；投影只镜像来源，不产生判定。

### 1.2 Intent 契约（payload.contract）

Intent 的 `payload` 是小型 JSON 文档（migration 0013 字节保真规则），新增
`contract` 子对象，**不新增表、不复制正文**：

```json
{
  "task_id": "phase-6-sqli-users",
  "phase": 6,
  "title": "验证 /api/users 的 SQL 注入",
  "contract": {
    "version": 1,
    "preconditions": [
      {"ref_kind": "fact",   "ref": "cred:admin",       "need_state": "confirmed"},
      {"ref_kind": "belief", "ref": "H-12",             "need_state": "proven"}
    ],
    "yields": [{"kind": "fact"}],
    "timebox_s": 900,
    "requires_confirmation": false,
    "confirmed_at": "2026-11-23T10:00:00Z",
    "confirmed_by": "operator"
  }
}
```

规则：

- `preconditions` 用 `(kind, unit_key, state)` 三元组引用**已存在的图节点**；
  缺失、状态不符一律不可认领。它表达的是“行动的前提事实”，不是模型自述。
- `yields` 声明本次行动的业务预期产出种类，供 planner/覆盖消费者使用，
  **不是结论白名单**。`done` intent 必须至少产出一个节点（默认 `origin`
  fact 作为执行回执），并以 `yields` 边连接（见 §1.3）。结论事务只校验
  产出种类是 `fact` 或 `finding`，执行回执永远是 `origin`，**不是**
  `confirmed`。回执只存结构字段（`result_of`/`agent_id`/`status`/`nature`/
  `summary_chars`），**不复制模型正文、工具输出或凭据**——权威证据在
  ledger/finding，图不是泄密通道。
- `timebox_s` 是执行预算，不是判定；超时由 worker 处理为 `exhausted`。
- `requires_confirmation` 是计划级确认门，与工具级 HITL 审批（D5）是两回事，
  互不替代。确认只能由操作者端点写入，**不提供任何 force/any 旁路参数**。
- 契约字段缺省等于“无前置条件、不要求确认”，旧 intent 行为不变。

### 1.3 结论事务（ConcludeIntent）

一个 intent 的结束是**一次事务**，不是若干次独立 UPDATE：

```text
BEGIN
  SELECT intent ... FOR UPDATE          -- 锁目标行
  校验 state=running && owner 匹配 && lease 未过期
  校验 requires_confirmation => confirmed_at 非空
  校验 preconditions（FOR SHARE 锁引用行，状态复核）
  幂等 upsert 产出节点（unit_key 唯一）
  INSERT exploration_edges(intent -yields-> produced)
  UPDATE intent -> done/blocked/exhausted/stopped, owner='', lease=NULL, completed_at=now()
COMMIT
```

它解决三个历史窗口：

1. **孤儿产出**：状态已 done、产出节点未写；
2. **双 worker 结论**：两个执行者都认为自己完成了同一 intent；
3. **依赖漂移**：认领时满足、结论时已被删除或拒绝。

非 `done` 的终态（blocked/exhausted/stopped）允许零产出；`done` 必须至少一个。
重复结论返回冲突错误，不产生第二份产出。计划面（planner / `build_plan` /
`update_plan`）的完成走同一事务的 `ConcludePlanned` 变体：不要求租约，但仍
写回执与 `yields`，仍检查确认门与前置条件；它**不得**结论仍被 `work#*`
活租约持有的 intent（worker 的结论才是权威）。

**结构性拒绝（2026-11-24）**：`Repository.Transition` 在 PG 与 Memory 双实现
中均直接返回错误，拒绝 `intent -> done` 的裸 CAS 迁移（错误信息指向
`ConcludeIntent`）——"done 只能经结论事务"从接口契约 + 运行期检查升级为
结构性强制，绕行代码在调用点即失败，无需等 `/graph/validate` 事后发现。

### 1.4 三层上下文（InfoItem / Evidence）

不改变 `scanctx` 热路径上限，只增加**读视图**：

| 层 | 内容 | 消费者 |
|---|---|---|
| L0 index | id / nature / subject / title(≤80) / summary(≤240) / has_detail / has_artifact | planner overview、worker `prior_work`、面板列表 |
| L1 detail | 有界正文、supports、来源/时间/坐标 | 执行者按 ID 显式拉取 |
| L2 artifact | 工具归档 ID、流量包 ID、文件引用 | 只回引用，不回字节 |

图节点 payload 只允许 L0；任何把 `Content.Text` 写进 `exploration_nodes` 的
代码都视为违规（由 §2 的不变量检查器发现）。

### 1.5 缺口与召回

- **缺口 pass**：确定性部分只读 coverage + 已确认 facts + goal + open intents，
  输出 `Gap{area, risk, question, refs[], score}`。LLM 增强只能产出
  `nature=inferred` 的 InfoItem，必须带 `supports[]`，**不得直接写 intent**。
- **词法召回**：标题×2 + token 重叠 + 稳定 tie-break；只作为
  planner/worker 上下文与 coldpath 关系候选，不进入 Verifier baseline
  装配与报告去重（冻结区）。pgvector 仍不引入。

---

## 2. 不变量清单（运行期判据）

每条都必须同时具备：**违背后果、写路径强制点、读路径检测点、修复策略**。
检查器实现于 `internal/graph/conformance.go`，错误码稳定，供 UI/审计/测试引用。

| 码 | 不变量 | 严重级 | 强制点 | 检测/修复 |
|---|---|---|---|---|
| `E_EDGE_DANGLING` | 边的两端节点必须存在 | error | `AddEdge` 外键 / Memory 校验 | 只报告；PG 外键已阻止，出现即跨库损坏 |
| `E_INTENT_DONE_NO_YIELD` | done intent 至少一条 `yields` 出边 | error | `ConcludeIntent` | 报告；历史数据只读展示，不自动补 |
| `E_YIELD_KIND` | `yields` 源必须是 intent，目标只能是 fact/finding | error | `ConcludeIntent` | 报告 |
| `E_INTENT_RUNNING_NO_OWNER` | running intent 必须有 owner | error | `ClaimNextIntent` 原子写 | 报告；`ResetRunning` 可回收 |
| `E_INTENT_TERMINAL_OWNER` | 终态 intent 不得保留 owner/lease | error | `ConcludeIntent`/`Transition` | 报告；可安全清理 |
| `E_INTENT_LEASE_EXPIRED` | running 的 lease 不应已过期 | warning | `ReclaimExpired` | 报告；下一次 claim 自动回收 |
| `E_CONFIRM_MISSING` | 需确认的 intent 不得 running/done | error | claim 谓词 + `ConcludeIntent` 复核 | 报告；转 paused 待人工 |
| `E_PRECONDITION_UNMET` | 运行/完成的 intent 前置条件必须成立 | error | claim 谓词 + 结论复核 | 报告；不得自动判 done |
| `E_INTENT_OPEN_COMPLETED` | open/running 不得有 completed_at | error | 状态迁移 | 报告 |
| `E_FACT_NO_PROVENANCE` | fact 至少有一条来源边（出边 `supports` 或入边 `yields`） | error | 投影/结论事务 | 报告；禁止无来源事实 |
| `E_BELIEF_PROVEN_NO_FACT` | proven belief 至少一条 `supports` 入边 | warning | 投影 | 报告；由 Verifier/ledger 复核 |
| `E_GOAL_MET_NO_PROOF` | met goal 有 `proves` 入边，或判据为 manual/capability | warning | objective 投影 | 报告；不阻断收工判定 |
| `E_UNIT_KEY_DUP` | (scan_id, kind, unit_key) 唯一 | error | PG 唯一索引 / Memory 检查 | 报告；PG 已阻止 |
| `E_PROJECTION_MISSING` | 字段权威存在的 ID 必须有图节点 | error | 投影同步 | `CheckProjection`；修投影，不自动重建 |
| `E_PROJECTION_STALE` | 有 expected 集合时，图节点必须有权威来源 | warning | 投影同步 | `CheckProjection`；人工确认后清理 |
| `E_PAYLOAD_INVALID` | payload 必须是合法 JSON **对象** | error | claim/conclude 的严格解析 | 报告；写路径已阻止 |

`error` 使报告 `ok=false`；`warning` 不影响 `ok`，但必须出现在报告里。
**检查器只读，不修复、不反向写图、不参与判定。**

---

## 3. 三层合规模型

### 3.1 L1 写路径守卫（预防）

- 单一 `graph.Repository` 接口；planner/worker/main 只能经工具面写，工具面由
  `SetToolFilter`+`Retain` 在注册期裁剪，不是提示词约束。
- `ClaimNextIntent` 在 SQL 内同时完成：候选、依赖终态、契约前置条件、确认门、
  租约写入。不存在“先查再写”的 Go 侧竞态。
- `ConcludeIntent` 单事务保证状态、产出、边一致。
- HITL 确认只由操作者端点写 `confirmed_at`；没有 force/any 开关。
- **上下文传递**：planner/worker 使用原始 task instruction（`sess.userInstruction`），
  不再截取 `buildAutonomousInstruction()` 拼接结果的开头；操作者的凭据/API 规则
  不会因为通用方法论过长而被截掉。
- **任务三层输入**：planner 同时接收 `GOAL`（`tasks.goal`，人工写、最高权威）、
  `DESCRIPTION`（`tasks.instruction`，人工写的流程/约束）、`OBJECTIVE`
  （goal analysis 派生，仅辅助，不得覆盖前两者）。overview 分节渲染，prompt
  要求每个 intent 的 `objective_ref` 指向 `GOAL` 或具体 criterion id。
- **目标守卫**：`planner.apply` 与 `Pool.targetsForIntent` 双重拒绝
  `CONTAINER_ADDR`/`pending-*` 等占位符；`control_plane_api` 的 target 必须是
  配置的控制面主机，存在域名控制面时拒绝裸 IP（如 VPN 健康检查地址）。
- blocked 落库时写 `blocked_reason`，面板/审计可直接看出原因。

### 3.2 L2 读投影契约（暴露）

- `GET /api/scans/{id}/graph` 返回节点、边、counters，并附带
  `conformance` 摘要（`ok`、错误/警告计数、首条违规）。
- `GET /api/scans/{id}/graph/validate` 返回完整报告（按严重级分组、截断标志）。
- 面板/报告/审计消费这些只读投影；任何“看起来完整”的视图必须显式携带
  `partial` 或 `incomplete`。

### 3.3 L3 周期审计（发现漂移）

- **图内自检**：`graph.ValidateStore(ctx, repo, scanID)` 在
  （a）投影同步后（默认 30s 节流）、（b）API 校验请求、（c）测试中运行。
- **投影对账**：`graph.CheckProjection(ctx, repo, scanID, expected)` 比较
  来源全集与图节点：
  - ledger hypothesis ID ↔ belief `unit_key`
  - ledger evidence ID ↔ fact `unit_key`
  - 已接受 finding ID ↔ finding `unit_key`
  - objective criterion ID ↔ goal `unit_key`
  - `expected` 为空的 kind 跳过 stale 检查（允许投影尚未发生）。
- **漂移信号**：对账失败写日志（含码、数量、样本 ID）并计入报告；不阻塞
  扫描，不自动改图。操作者在 `/graph/validate` 看到同一份图内不变量结果；
  投影对账随每次投影周期写入 `graph_conformance` 表（迁移 0025，干净快照也
  落行），日志保留。该表只读，不参与判定，也不自动修复。
- **接口边界**：`/graph/validate` 只跑图内不变量（可脱离 scanctx 执行），
  因此其响应不含 `projection` 字段；`projection` 结构出现在投影日志与
  测试断言中。
- **连续失败**：同一码在同一 run 连续 N 次（默认 3）→ run 级可见告警；
  是否暂停由操作者/后续批次决定，本批不自动暂停。

### 3.4 漂移分类与处理

| 类型 | 例子 | 处理 |
|---|---|---|
| 写路径绕行 | 某工具直接 `Transition(done)` 而不产出 | 检测为 `E_INTENT_DONE_NO_YIELD`；修代码，不补数据 |
| 投影滞后 | ledger 有新假设、图还没同步 | 下一轮投影追平；报告允许瞬时 missing，但需带 `expected`/`observed` 计数 |
| 投影丢失 | 节点 unit_key 拼写不一致 | `CheckProjection` 报 missing；修投影，不自动重建 |
| 模型越权 | intent 自行写 finding | 写路径不存在该能力；若出现即 `E_YIELD_KIND`/投影对账失败 |
| 脏历史 | 旧 intent 无 yields | 只报告；不自动补边，避免伪造历史 |

**自动修复边界**：只允许 `ResetRunning`/`ReclaimExpired` 这类“恢复在途”
操作；不允许补写产出、补写边、补写 finding、改写 goal。

### 3.5 操作者验收顺序

一次真实 run 结束后，按顺序做三件事，缺一不算通过：

1. `GET /api/scans/{id}/graph/validate` → `ok=true`；若 false，按 §2 码表
   定位：`E_INTENT_DONE_NO_YIELD`/`E_YIELD_KIND` 先查 worker 结论路径；
   `E_PRECONDITION_UNMET`/`E_CONFIRM_MISSING` 先查 claim 谓词与确认端点；
   `E_FACT_NO_PROVENANCE`/`E_PROJECTION_*` 先查投影写入与边写失败。
2. 对照 `/graph` 的 `conformance` 摘要与 UI/报告中的节点数量，确认没有
   “图里少节点、面板却显示完整”的假完整视图。
3. 查看日志中的 `[graph-conformance]` 行：`missing`/`stale` 必须为 0，
   或能逐条解释为“尚未投影的瞬时状态”。

---

## 4. 落地批次与验收

### R8.1 意图契约与结论事务（本批实现）

- `internal/graph/contract.go`：`Contract`/`Precondition`/`Yield` 解析与校验。
- `internal/graph/conformance.go`：不变量检查器 + 投影对账。
- `Repository.ConcludeIntent`：PG/Memory 双实现；`ClaimNextIntent` 增加契约谓词。
- `Repository.ConcludePlanned`：计划面完成同一事务规则（不要求租约、不得抢
  `work#*` 活租约、仍产出回执与 `yields`）；`plan_graph.go` 的
  `build_plan`/`update_plan` 完成路径已切换。
- `orchestrator.Pool`：completed → `ConcludeIntent`，产出 `origin` fact 并连
  `yields`；失败降级为 blocked，不再“假 done”。
- API：`/graph` 附 `conformance`；`/graph/validate` 全量报告。

验收：

1. 两个 worker 并发结论同一 intent：一成一败，产出节点恰好一个。
2. 事务中途失败：intent 仍 running，无产出节点、无 `yields` 边。
3. 前置条件缺失/状态不符：不可认领；认领后被删：结论失败，不写 done。
4. `requires_confirmation` 未确认：不可认领、不可结论。
5. 一个真实投影周期后 `ValidateStore().OK()`；人为制造 missing 后
   `CheckProjection` 报出且样本 ID 正确。
6. `w=0` 排序金样本不变；报告门/Verifier 测试零改动。

### R8.2 上下文分层 + 注入硬化（已实现）

- `internal/infoitem/view.go`：L0 `Index` / L1 `DetailOf` / L2 `ArtifactOf`
  纯函数视图；L1 有界 8192 runes，L2 只回引用。
- `read_info_item(id, layer, scan_id?)` 工具：先读本扫描 ledger evidence；
  未命中时回退到 coldpath `Reader.GetByID`（同扫描，或经
  `SetCrossScanResolver` 授权的同项目扫描）。`read_` 前缀使 main/planner
  自动可用，worker 全量面可用，orchestrator 面显式加入。跨项目读取一律拒绝。
- 图 payload 保持 L0（不写 request/response）；planner overview 只装配 L0。
- `internal/promptrender`：单次占位符替换、控制字符清洗、反引号/表格行防
  伪造、`[UNTRUSTED DATA ...]` 封套、确定性注入特征扫描。
- planner overview 与 worker task text 的 notes/hints/belief/findings/prior
  work 均经 `Untrusted` 处理；命中特征时由
  `LedgerStore.AppendInjectionObservation` 写一条 `purpose=injection_suspected`
  的 observed InfoItem（source=engine/prompt-guard），不执行。
- 验收：`promptrender` 金样本、`read_info_item` 三层/错误/隔离测试、注入
  journal 测试、Evidence 40 条/4096 回归全绿。

### R8.3 缺口 pass + 词法召回（已实现）

- `internal/gaps`：确定性纯函数，输入 coverage 快照 + queued hypotheses +
  open intent keys，输出 `{area,risk,question,refs,score}`，上限 20，稳定排序。
  planner overview 渲染 top 10，**只作建议，不直接建 intent**。
- `CoverageStore.Snapshot()`：只读返回 probed/verified 矩阵副本（类名排序）。
- `internal/infoitem/recall.go`：标题×2 + token 重叠 + 子串加分，CJK 逐字
  分词，输出 `(score desc, id asc)`，无嵌入。
- consumers：planner/worker 的 `Prior work`（只给 L0 ID/摘要）、coldpath
  `Store.Apply` 的 `references` 候选边（同扫描、最近 50 条、文本 ≥40 runes、
  score ≥0.5、幂等按 source_seq）。不进入 Verifier baseline 与报告去重。
- 验收：recall/gaps 金样本、planner/worker prompt 测试、PG 关联边集成测试。

### R8.4 审计与韧性（已实现）

- `timebox_s`：planner 可写 intent contract；`Pool` 用
  `min(WorkerTimeout, timebox)` 约束该 worker 的等待窗口，绝不放大全局超时。
  `Pool` 在 worker 运行期间以 `LeaseTTL/3` 心跳调用 `RenewLease`，因此
  timebox 长于默认 lease 也不会让活租约过期、被其他 worker 回收，或让
  结论因 `ErrLeaseExpired` 失败。
- `deviations[]`：新增 `OpRecordDeviation`（journal，含 `intent_id`）与
  `record_deviation` worker 工具；审计只读，不改计划状态。worker task text
  带 Intent ID，便于引用。
- deviations 聚合：`domainjournal.Project` 产出 `deviations[]`；
  `GET /api/scans/{id}/audit` 投影直接携带；审计面板新增“计划偏离”卡片；
  PDF 报告在 Disclaimer 前输出“Planning Deviations”页（最多 25 条）。
- 故障分类：`orchestrator.ClassifyWorkerStatus` 稳定码
  `worker_timeout / worker_missing / worker_error / worker_rejected`，映射到
  exhausted / open / blocked；日志带 class。PG 不可用→可恢复 pause、租约过期
  →回收、配额→轮换、工具超时→不重试副作用保持既有语义。
- 恢复逻辑只重放/回收，不做语义判定。

---

## 5. 验收矩阵（对齐 CORE §10）

| 宪章条款 | 本方案落点 | 验收 |
|---|---|---|
| §2 七环（信念→义务→行动→证据） | R8.1 契约 + 结论事务 | 并发/事务/依赖测试 |
| §3 InfoItem 与 Evidence 上限 | R8.2 只加读视图 | 40/4096 回归 |
| §4 三类点亮有消费者 | R8.2 语境切片、R8.3 区域/关联 | consumer 契约测试 |
| §5 字段级权威 | §1.1 + 投影对账 | `CheckProjection` |
| §6.4 崩溃追赶 | R8.4 故障分类 | 恢复测试 |
| §7.1 确定性 | claim 排序 + 词法召回金样本 | 快照排序测试 |
| §10 M1/M5 | 前置条件、确定性 tie-break | 同 R8.1 |
| §11 冻结区 | 未触碰 | Verifier/报告门测试零 diff |

---

## 6. 明确不做

- 不引入第二数据库、pgvector、MCTS/POMDP；
- 不把 gap/recall 直接写成 intent；
- 不让图/InfoItem 置信度改写 finding 标签或推翻 Verifier；
- 不自动补历史 `yields` 边、不自动重建丢失节点、不伪造完整审计；
- 不加 `--any`/force 类绕过确认的开关；
- 不改 Evidence 40/4096、Verifier 三态、报告门顺序、HITL 语义；
- 不用“检查通过”替代真实行为测试；检查器只证明结构与对账，不证明渗透质量。

---

## 7. 附录：报告结构

```json
{
  "scan_id": "run-...",
  "checked_at": "2026-11-23T10:00:00Z",
  "ok": false,
  "errors": 2,
  "warnings": 1,
  "violations": [
    {"code": "E_INTENT_DONE_NO_YIELD", "severity": "error", "kind": "intent",
     "unit_key": "phase-6-sqli", "node_id": 42, "detail": "done intent has no yields edge"},
    {"code": "E_INTENT_LEASE_EXPIRED", "severity": "warning", "kind": "intent",
     "unit_key": "phase-7-idor", "node_id": 43, "detail": "lease expired at ..."}
  ],
  "projection": {"expected": 12, "observed": 11, "missing": ["H-9"], "truncated": false},
  "truncated": false
}
```

码表见 §2；`detail` 只描述结构事实，不包含目标响应正文，避免把不可信内容
带进审计界面。`projection` 段只出现在投影边界日志与测试断言中；
`GET /api/scans/{id}/graph/validate` 返回的是图内不变量报告，不含该字段。
