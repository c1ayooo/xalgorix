# 核心设计思想 — 信息驱动的渗透循环（二开宪章 v3.0）

> **地位**：本仓库二次开发的目标设计与验收基线。目标设计与实现冲突时，现状视为待改造项；但“现状是什么”的事实描述一律以代码和测试为准，不得用目标文档覆盖事实。
>
> **判据**：宪章正文只保留可被代码、测试或运行实验验收的主张。不可验收的主张移入「研究项」，不进入当前路线图。
>
> **血缘**：v1 研究稿全文存档于 `docs/archive/CORE_PHILOSOPHY-research-2026-11-20.md`。v2 将抽象主张改写为本仓对象与里程碑；v2.1 进一步修正现状描述、Evidence 字段冲突、验证权威、审计完整性、PG 追赶协议及确定性验收口径。**v3.0（2026-11-22 用户裁决，路线 B）把状态与面板数据的主存储从每会话目录文件改为 PostgreSQL**：PG 从“冷路径检索副本”升格为唯一持久态，文件只承载字节产物；单二进制形态保留，PG 成为必需依赖。修订详单见 §17。
>
> **存储选型**：PostgreSQL（用户裁决）。v3.0 起 PG 承载全部状态与面板数据（R6，规格见 `docs/PG_PRIMARY_STORAGE_SPEC.md`）；文件只承载字节产物与沙箱工作区。
>
> **开发前提**：当前未上生产环境，允许调整内部 API、JSON schema 和测试夹具，不背负生产数据迁移；但仍须保持每步可运行、可测试、可回滚，不得把“无生产”解释为跳过正确性与安全回归。

> **v3.1 / R7 修订（2026-11-23，双图编排，用户裁决）**：本节是**显式例外**，与下文冲突时以本节为准；其余条款继续有效。实现规格见 `docs/DUAL_GRAPH_ORCHESTRATION.md`。
>
> 1. **§2 对象映射**：Intent 的权威是探索图 `intent` 节点（`exploration_nodes(kind='intent')`），PlanTask/PlanView 是它的读写视图；Belief 的权威是 `belief` 节点（Hypothesis 生命周期/owner/lease 在节点，Evidence 明细仍在 ledger/InfoItem）；Fact/Finding/Goal/Hint 分别由 `fact`/`finding`/`goal`/`hint` 节点承载，Finding 仍只由 `report_vulnerability` 成功后写入。
> 2. **§5 字段级权威**：探索对象的生命周期归图；finding 接受仍归报告门；goal 判定仍归确定性 `objective.Evaluate`；InfoItem 的 observed/inferred 仍归 InfoItem；模型自述仍为最低权威。图节点只存权威引用与摘要，不复制正文。
> 3. **§6/D12**：探索图（`exploration_nodes/edges`）与资产图（`assets`）是 **PG 实时权威（读写 PG）**；其余扫描状态仍保持“内存 + PG”。事件、activity、图投影是审计/读模型，不是决策权威。PG 不可用时 planner/worker 退避、Run 进入可恢复 pause，不以内存计划静默续跑。
> 4. **不变项**：Verifier 判定/工具面/时限、`report_vulnerability` 闸门顺序、HITL 语义、SSE 单向、扫描隔离、单二进制形态、M0–M5/R6 既有验收全部不变。
> 5. **落地状态**：M1（图权威/PlanTask 视图/节点投影/资产图/API）完成；M2（planner_worker：main chat 按用户消息**提案**、操作者确认后才写入 intent（`propose_intent` → confirm 端点/`confirm_intent`），hint 只承载上下文、LLM planner、1–10 worker 池、工具面硬裁剪）已实现并作为**默认内置拓扑**。`XALGORIX_ORCHESTRATOR=legacy` 仅为隐藏的紧急回滚，不出现在设置页；worker 并发 1–10 由设置页 `XALGORIX_WORKER_CONCURRENCY` 配置。

> **v3.2 现状校准（2026-11-23）**：本版只校准“现状事实”与里程碑状态，不改变目标设计。§1.1/§1.2/§1.3/§3.4/§4.2/§4.3/§5.2/§6.1/§6.3/§7/§8/§9.5/§13/§16 按代码现状改写；§10 增加落地状态快照；§17.1 记录校准项。事实基线：PG 主存储（R6.0–R6.3）、Eino 单运行时、River 异步层、HITL 审批链、MCP 客户端、SSE 事件流、planner_worker 默认拓扑、探索图/资产图 PG 权威均已落地。

> **v3.3 / 信息即图（2026-11-24，用户裁决）**：本节补充而非替代 §3/§4/§7/§8；冲突时以本节为准，实现规格见 `docs/INFO_AS_GRAPH_SPEC.md`。
>
> 1. **两层同一空间**：探索图是信息空间的**聚合层**（节点=信息聚合，边=信息关系+工作关系）；InfoItem 是**原子层**（一次观测/推导一条实例），Evidence 是原子信息在 hypothesis 下的投影。两者不是两个世界，而是同一空间的粒度差异。
> 2. **规范键是 `run_id`**：`exploration_nodes.scan_id` 是 run 身份（D11 instance id）；`info_items.scan_id` 是产生它的 ScanContext id（一个 run 可跨 sibling 上下文）。原子经 `run_journal(scan_id, seq) -> instance_id` 归一化，缺失回退自身 `scan_id`。跨层查询一律以 `run_id` 为准。
> 3. **图节点坐标**：聚合节点 payload 的 `info` 块承载 subject/purpose/nature/observed_at/context；`state/owner/lease/priority` 是控制字段，不属于信息内容。图节点只存摘要/引用，不复制正文。
> 4. **激活入口**：三类激活（区域/关联/切片）以统一只读视图 `info_space_items` / `info_space_edges` 为查询入口；直接调用 ledger/surface 的确定性 API 仍是合法实现与回退。图可用时 worker 的 `search_info` 默认 `source=auto` 优先读统一空间。
> 5. **不变项**：图状态权威（`ConcludeIntent`、claim 谓词、precondition/confirmation）、`report_vulnerability` 门管线与 Verifier、事件非权威、PG 单库、无 A*/MCTS/POMDP、无 pgvector、扫描隔离、单二进制形态。
> 6. **落地状态（2026-11-24）**：IG0（统一读视图 + `run_id` 归一化）、IG1a（`infospace` reader、`search_info source=graph`）、IG1b（Web `layer` 过滤 + 索引/EXPLAIN）、IG2（planner 消费点亮）、IG3（聚合↔原子链接与 `materializes` 跨层关联）、IG4（intent 级 VOI）、IG5（gap→候选）全部完成；验收见 `docs/INFO_AS_GRAPH_SPEC.md` §7。

> **v3.4 / 上下文压缩（2026-11-24，用户裁决）**：本节是 §3/§4/§5/§6/§8 在压缩路径上的显式修订；冲突时以本节为准，实现规格见 `docs/CONTEXT_COMPRESSION_SPEC.md`，里程碑见 `docs/INFO_AS_GRAPH_SPEC.md` IG6。
>
> 1. **压缩是投影，不是摘要算法**：上下文压缩的产物是探索图的 `kind='digest'` 节点（`active → superseded`）+ 一条 `nature=inferred` 的 InfoItem；不是匿名文本消息。热路径（下一轮消息装配）从 `scanctx` 内存权威（ledger/coverage/findings/notes）确定性渲染快照，PG 只承载持久化与跨会话读取，不得为压缩把热路径全走 PG。
> 2. **无出处不得注入**：digest 的每一行必须携带可解析 ID（hypothesis/evidence/finding/InfoItem/archive id），InfoItem 的 `supports` 非空；没有 ID 的摘要不得进入判定、收工依据或“已测/已确认”的叙述。finding 行只来自 `report_vulnerability` 报告门，候选区必须标注 UNVERIFIED。
> 3. **不得递归摘要**：快照每轮从权威 store 重算；只允许窗口外 transcript 老化。新 digest 以 `derived_from` 连上一条 digest 与本次老化区间，旧节点置 `superseded`，血缘显式保留。
> 4. **触发以真实计量为准**：主触发用 provider 返回的 `usage.PromptTokens` 与窗口比例（默认 0.75），字节估计只在无 usage 时兜底并按 CJK 修正；`XALGORIX_CONTEXT_COMPACT_TOKENS=0` 仍表示禁用。不新增用户可见开关。
> 5. **人工输入不是聊天记录**：主会话裁剪前必须把操作者消息投影为 `hint`（`origin=human`，observed，幂等）；投影失败时不得静默丢弃，宁可本轮不裁剪。
> 6. **冷路径边界**：模型生成的摘要只允许在 River 冷路径产出 `inferred` InfoItem（带 `supports`），不得进入热循环，不得阻塞装配。
> 7. **不变项**：Verifier、`report_vulnerability` 门管线、claim 谓词、`ConcludeIntent`、工具面、HITL、SSE 单向、扫描隔离、PG 单库、无搜索算法、无 pgvector 全部不变。
> 8. **落地状态（2026-11-24）**：IG6a（digest 角色/确定性/真实 token 触发/notes 预算/主会话 hint 投影）、IG6b（digest 节点 active→superseded、`derived_from`、`OpContextDigest` InfoItem inferred+supports、`graph_info_links` materializes）、IG6c（热路径状态快照替换文本 digest，段序/预算/权威标注）、IG6d（冷路径 River、worker 8KB 种子、resume 12KB digest、`list_hints`）全部完成；验收见 `docs/CONTEXT_COMPRESSION_SPEC.md` §9 与 `docs/INFO_AS_GRAPH_SPEC.md` IG6。hint 的 `consumed` 生命周期仍保持“扫描结束前不消费”。

---

## 1. 系统定位与适用边界

### 1.1 当前系统是什么

以下只描述当前代码事实，不是目标设计：

- Xalgorix 是单 Go 二进制、单进程运行形态的自主渗透 agent，内嵌 React 仪表盘，同时提供 CLI/TUI、REST API 与 SSE 事件流（`GET /api/stream`；旧 WebSocket hub 已移除，`WSEvent` 仅为历史类型名）。
- 一个 target session 围绕一个目标和一条 instruction 运行；系统同时存在多目标队列、多实例资源准入、定时任务与 wildcard 子扫描，不能把整个系统描述为“只能单目标串行”。
- 主循环由 Eino ADK 单运行时承载（`internal/engine`；`internal/agent.Agent.Run` 无条件进入 `runLoop`，不存在引擎开关或回滚分岔）。
- 方法论由代码强制：22 阶段定义于 `internal/methodology/registry.go`，漏洞类注册表、endpoint×class 覆盖矩阵与 coverage epoch 共同约束探索范围。
- 判定由代码强制：扫描级 hypothesis/evidence ledger、受限工具面的独立 Verifier，以及 `report_vulnerability` 的报告门管线共同约束 finding 的接受与标签。
- 内置工具面在编译期注册；另有运行时注入面（MCP 服务器 `internal/tools/mcp`、动态工具检索、agent profiles/user skills）。工具数、漏洞类数和报告门数量是随代码变化的派生事实，不在宪章中写死；需要展示时由代码或测试生成。
- 持久化（v3.0/R6）：状态与面板数据的唯一持久态是 PostgreSQL；`XALGORIX_DATABASE_URL` 必需，无 DSN 启动 fail-fast（`cmd/xalgorix/main.go`），文件只承载字节产物。River 只做异步补全/保洁（`internal/jobs`、`internal/coldpath`）。
- 平台层已落地：项目/任务/Run 模型（`internal/platform`）、HITL 审批链（`internal/approval` + `internal/engine/approval.go` + `approvals` 表）、探索图/资产图 PG 权威（`internal/graph`、`internal/assets`）、MCP 客户端（`internal/tools/mcp`）。
- 执行拓扑默认 `planner_worker`（main chat + planner + 1–10 worker 池，`internal/orchestrator`）；`XALGORIX_ORCHESTRATOR=legacy` 只是隐藏的紧急回滚。

### 1.2 当前系统不是什么

- 不是通用 Fact/Intent 求解器；但已有扫描内共享的探索图（intent/belief/fact/finding/goal/hint 节点）与跨扫描资产图（`assets`），两者是 PG 实时权威，不是搜索策略。
- 当前**存在扫描内共享的 Hypothesis/Evidence ledger**：main chat、planner 与 workers 读写同一扫描级账本；Evidence 明细仍在 ledger/InfoItem，图节点只存权威引用与摘要。
- `agentsgraph` 有 runner、并发信号量、取消、结果收集与共享预算；R7 起它同时承载 planner_worker 的 `Dispatch`/`DispatchWithSurface` 池路径（intent 租约、`SteerUnit`/`KillUnit`），不再是固定一波。
- 项目/任务/审批/资产平台已落地（D6/D7/D12、R1–R4、R7）；它仍不是企业级多租户运营平台（无组织、计费、跨租户运营面）。

### 1.3 本宪章的主语

本宪章首先约束**一次 target session 的生命周期**，同时规定它与以下边界的交互：

1. 扫描级共享 ledger 与 agentsgraph；
2. Web 层多目标队列和实例生命周期；
3. 只读审计投影；
4. PostgreSQL 主存储及跨扫描检索（InfoItem 冷补全、资产图）。

脱离这些对象写出的通用求解框架条款，不属于本期实现范围。

---

## 2. 主循环与本仓对象映射

```text
观察 Observation
   ↓
信念 Belief（Hypothesis：待证主张 + 前置条件 + baseline）
   ↓
义务 Obligation（阶段义务 / 覆盖缺口 / 收工闸门）
   ↓
行动 Operation（PlanTask 或 tool call）
   ↓
证据 Evidence（append-only；区分 observed / inferred）
   ↓
判定 Verdict（Verifier 三态 / 确定性验证证据 / finding 标签）
   ↓
收工 Close（finish gate：proven 必须有 finding；适用阶段必须有 disposition）
```

v1 概念与本仓对象的对照，**不得反向使用**：

| v1 概念 | 本仓真实对象 | 不得误认为 |
|---|---|---|
| Fact | 原始 Observation、ledger Evidence、已接受 finding | 任意模型自述 |
| Belief | Hypothesis（`record_hypothesis`、六态状态机） | Intent |
| Intent | PlanTask（`build_plan` / `update_plan`）及具体 tool call | Hypothesis |
| Hint | 引擎义务：phase obligation、completion gate、coverage epoch、hook nudge | 人工输入 |
| Verdict | Verifier 三态、引擎拥有的确定性验证结果、报告管线标签 | InfoItem 置信度 |

**v3.1/R7 起**：Intent 的权威是探索图 `intent` 节点（PlanTask/PlanView 是其读写视图），Belief 的权威是 `belief` 节点；本表保留对象语义对照，不再表示 PlanTask 拥有独立持久权威。

**入门测试**：任何新能力动手前必须回答“它落在七环的哪一环、由哪个对象承载、如何验收”。回答不出来的不进入实现。

---

## 3. InfoItem、Evidence 与验证权威

### 3.1 InfoItem 与 Evidence 不是替代关系

- **InfoItem** 是信息模型的全局实例，承担跨扫描检索、来源追踪与审计。
- **Evidence** 是信息在某条 hypothesis 下的投影视角，回答“这条信息为什么支持、反驳或控制该假设”。
- 一个 InfoItem 可以投影到多个 hypothesis；一个 hypothesis 可以关联多个 InfoItem。不得通过复制全文制造多份无来源关系的真相。
- v3.3：图节点是信息**聚合**，InfoItem 是**原子实例**；同一信息空间的不同粒度，规范连接键为 `run_id`。

### 3.2 InfoItem 最小模型

每条 InfoItem 至少包含：

- 全局唯一 `info_item_id`；同一坐标的重复观测也是不同实例；
- 七维坐标：内容 / 来源 / 时间 / 主体 / 目的 / 情境 / 关系；
- `nature = observed | inferred`；
- 有效期与 `scan_id`；
- 对冷路径同步有意义的 `source_seq`；
- inferred 信息非空的 `supports[]`，指向其支持证据；
- 坐标补全状态与置信度。坐标置信度只描述标注可靠性，不是漏洞成立概率。

`observed` 保存原始请求、响应、工具输出或人工原始输入；`inferred` 保存模型或规则形成的解释。原始观测与其解释可以关联，但不得因写在同一条记录里就混为同一认识层级。

### 3.3 Evidence 的两个分类维度必须分开

当前 `Evidence.Kind` 已承担 `baseline/control/probe/exploit/artifact/finding_ref` 等“证据在假设中的作用”。observed/inferred 是另一维度，**禁止复用或覆盖 `kind`**。

目标 schema 至少包含：

| 字段 | 含义 |
|---|---|
| `kind` | 证据用途：baseline、control、probe、exploit、artifact、finding_ref 等 |
| `nature` | 认识类型：observed 或 inferred |
| `info_item_id` | 可选的冷路径全局信息实例链接 |
| `supports` | inferred 判断的支持证据 ID 列表 |
| `source` | 引擎拥有的验证器或确定性来源；模型不得伪造 |

历史或缺省 `nature` 按 `inferred` 处理，避免把未知信息自动提升为原始观测。现有 `kind=finding_ref`、证据上限及证明闸门语义必须保持不变。

Evidence 继续保留每假设 40 条、单字段 4096 字符及现有内存上限；InfoItem 冷存储不得反向放宽扫描热路径上限。

### 3.4 写路径

```text
热路径：工具/引擎/人工输入
  → scanctx / ledger Evidence（进程内权威）
  → 变更边界 PG 事务（幂等键 scan_id + seq，行级 instance_id 盖章）
  → 失败记 failed 水位，扫描继续，reconciler 按幂等键追赶

字节产物（工具输出归档、流量原始包、上传物、交付物）
  → storage.WriteAtomic 原子写文件

冷路径：domain journal 变更
  → River 异步 InfoItem 补全 / 关系候选 / 审计投影
  → reconciler 检查 PG 水位缺口并补投
  → 审计视图展示 pending / applied / gap / failed
```

- 热路径 PG 写失败按 §6.4 处理（内存权威撑住、记 failed 水位、reconciler 追赶），不得伪装成功。
- PG 或坐标补全失败不阻断核心扫描，但必须形成可见的 pending/failed/gap 状态，不能静默丢失。
- 模型生成的 inferred InfoItem 未关联支持证据时，可以保存为待修正信息，但不得进入证明、判定或收工依据。

### 3.5 字段级权威，而非全局“高置信度覆盖”

| 字段或判断 | 权威来源 |
|---|---|
| 原始请求、响应、工具输出 | 对应原始 Observation |
| finding 是否接受及验证标签 | `report_vulnerability` 报告门管线 |
| hypothesis 生命周期、归属和调度状态 | ledger |
| InfoItem 的来源、目的、情境与关系标注 | InfoItem 及其补全记录 |
| 独立复测结论 | 所属 Agent 的 Verifier |
| 引擎确定性验证结果 | 对应引擎拥有的 `verify_*`、浏览器执行信号等来源 |
| 模型自述 | 最低等级，仅是待证输入 |

规则：

1. InfoItem 置信度不得直接产生 finding、修改验证标签或推翻 Verifier。
2. 不同来源对同一字段冲突时，保留冲突、来源和时间，不得由低权威来源静默覆盖高权威状态。
3. 报告管线是 finding 标签的唯一出口，但不是所有原始事实的唯一生产者。
4. 确定性验证证据可以按现有报告语义跳过 LLM Verifier；因此不得把 Verifier 描述为唯一验证来源。

---

## 4. 三类激活：查询入口、消费者与共同契约

三类激活是三个可测试的查询入口，不是修辞，也不是对现有确定性直查的禁令。

### 4.1 区域点亮

**语义**：按主体、时间、来源、目的、类型、扫描等维度做确定性过滤。

**消费者**：

- `claim_next_hypothesis` 的可调度池；
- `taskCoverageComplete` 与 coverage epoch 的缺口计算；
- `read_ledger(filter=schedulable)`；
- M3 之后的跨扫描同主体候选检索。

**实现**：内存索引或 SQL 属性查询，不要求语义向量空间。

### 4.2 关联点亮

**语义**：沿支持、矛盾、引用、同会话、同主体等关系边传播；跳数不超过 3，并受结果数与时间预算截断。

**消费者**：

- Verifier 的 baseline/控制组装（**部分**：Verifier 独立复测自带负向/基线对照；引擎尚未从关系边装配 baseline 注入 `VerificationRequest`）；
- `report_vulnerability` 的同类同端点去重与证据引用（已接线）；
- `verify_*` 复用既有 baseline（已接线，按类各自实现）；
- 跨扫描同主体历史证据查询（已接线：`read_ledger(filter=schedulable)` / `claim_next_hypothesis` 的 same-subject 提示，`internal/agent/ledger_cross_scan.go`）。

**实现**：扫描内关系索引或 PostgreSQL 递归 CTE。返回结果必须保留路径、深度和截断标志，不能把无界遍历交给调用方。生产端现写四类确定性边：`supports`（ledger 证据）、`references`（冷路径词法召回）、`same_subject`/`same_session`（字段精确比较）、`contradicts`（同一 subject+purpose+hypothesis 下 control/baseline 对 probe/exploit/finding_ref）；矛盾边来自证据角色，不来自文本猜测。

### 4.3 语境切片

**语义**：固定部分维度，投影出同一语境下的混合信息视图。

**消费者**：

- `VerificationRequest` 的目标、端点、方法和证据字段装配（字段已装配于 `internal/tools/reporting`，但来源是 `report_vulnerability` 入参，尚未经语境切片查询）；
- 报告生成时的 finding + evidence + traffic 引用（已接线）；
- 人工审计视图（已接线：`/info` 语境切片 Tab、账本审计时间线）。

### 4.4 共同查询契约

每个入口至少明确：

- 调用方与消费场景；
- 输入过滤条件、`limit`、预算和确定性排序；
- 返回信息的 ID、来源、nature 与关系引用；
- 是否截断、是否存在同步缺口；
- 空结果与错误的区别。

直接调用 ledger/surface/reporting 的确定性 API 是允许的，可视为激活的一种实现。**禁止的是绕过来历与证据，直接凭模型上下文记忆复述事实。**
- v3.3：查询入口优先统一只读视图 `info_space_items/info_space_edges`（聚合层 + 原子层，以 `run_id` 连接）；InfoItem 表与 ledger 确定性 API 是合法实现与回退。

不得为了“使用点亮”而把已有热路径全部经过 PostgreSQL，也不得要求每个操作机械地依次执行三类查询。

---

## 5. 事件、状态与审计投影

### 5.1 状态权威与事件用途

- 状态权威（v3.0）：**进程内权威 = `scanctx.ScanContext`（内存）；持久权威 = PostgreSQL**。原 `scan.json` / `ledger.json` 等状态文件自 R6.1 起停写，字段由 runs 表与状态表接管（R6 规格 §批次切分）。
- 事件流：写入 PG（对话/遥测 journal 表），用于审计、回归对比、实时遥测与续跑重放。
- 本期不做“当前状态 = 事件投影”，也不承诺事件重放得到相同 Agent 决策。

原因均可复核：system prompt 动态拼装、对话窗口存在有损剪枝与压缩、超长工具输出落盘后只保留引用（该归档仍在文件层）。

### 5.2 审计完整性是 M2 的前置条件

普通 SSE/遥测事件允许背压策略，但**领域审计记录不得复用可静默丢弃的遥测通道**。要还原“谁在何时认领哪条 hypothesis”，必须先保证：

1. 每次 ledger 状态变化在变更边界产生专用审计记录；
2. 审计记录带 `scan_id + seq`、操作者、操作、对象、前后状态摘要和时间；
3. 自动认领与模型工具认领使用同一协议；
4. 每扫描 seq 单调递增，投影能识别重复、乱序与缺口；
5. 审计持久化失败不得被当成成功；
6. 审计投影只读，不反向修改 ledger，不参与漏洞判定。

若历史事件不足，投影必须显式显示 `incomplete` 和缺口范围，不得生成貌似完整的历史。

---

## 6. PostgreSQL、River 与最终一致协议（v3.0：PG 主存储）

### 6.1 持久化边界

- per-scan 状态继续经 `scanctx` 路由；进程内权威是内存，读路径不依赖网络（探索图/资产图按顶部 v3.1 第 3 条直接读写 PG，是显式例外）。
- 扫描生命周期继续由现有 Web 状态机和资源准入控制。
- 字节产物（工具输出归档、流量原始包、上传物、交付物）继续使用 `storage.WriteAtomic` 写文件；状态与面板数据的持久化走 PG 事务。
- 持久化写失败**不得阻断**已启动 Run 的推理、工具执行、finding 保存、现有报告或扫描收工：失败记 `failed` 水位并审计可见，恢复后由 reconciler 追赶（§6.4）。调度入口在派发前无法读取项目 scope 时按 D7 授权边界 fail-closed（跳过本次排期），这是授权前置检查，不是热路径对可用性的依赖；已在途 Run 不受影响。
- 启动门：无 DSN 拒绝启动（fail-fast），错误信息含部署指引（compose 模板）。这是 v3.0 对 §6.6 降级模式的**有意废止**，用户裁决（2026-11-22，路线 B）。

### 6.2 主存储范围

PG 承载（R6 全量清单见 `docs/PG_PRIMARY_STORAGE_SPEC.md`）：

- run 状态（原 scan.json 字段，runs 表接管）；
- events（对话/遥测 journal）、domain journal（领域审计）；
- ledger（假设/证据状态权威）、intelligence、notes、coverage 计数；
- InfoItem（JSONB）、关系边（关系表 + 递归 CTE）、审计投影与同步水位；
- 项目/任务/审批（已落地）、queue_state、dispatch 快照、token 归档。

文件只承载字节产物：工具输出归档、流量原始包、上传物、logo、报告交付物、扫描工作区（沙箱 scratch）。pgvector 仍不是必选项；只有存在具体相似度消费者和基准后才引入。

### 6.3 真相源与幂等

- 进程内权威是内存；**PG 是唯一持久态**（v3.0 起不再有并行的 JSON 持久态）。不做跨存储分布式事务。
- 每扫描变更使用 `(scan_id, seq)` 作为幂等键（会话内 seq 语义不变）；状态行带写入时盖章的 `instance_id` 列，run 级聚合与清理按 instance_id 进行。身份在写入点由 scanctx 携带 `instance_id`，新写入不再事后回填；`legacy_scan_id` 列保留双重职责（2026-11-24 校准，以代码为准）：①运行期 run↔scan 身份关联——run 派发时 fill-once 写入（`ProjectScanIDs` 依赖它学到 scan id，`internal/web/scan_session.go`），scan record 保存以它作 fallback 身份匹配（`internal/runstate/scan_record_backend.go`）；②存量导入器幂等（`ImportLegacyRun`）。它是 `instance_id` 的关联视图，不是并行身份权威。
- 同一幂等键重复投递不得产生重复状态变化。
- 不同 scan 的 seq 不相互比较；跨会话/跨扫描排序必须使用时间或独立全局标识，不能假设各扫描 seq 可比较。

### 6.4 崩溃追赶协议

v3.0 把 M3 水位协议从 InfoItem 扩展到全部状态表。内存权威使写失败不阻断运行，但必须同时存在持久化重试与 reconciler：

```text
正常路径：变更边界 → PG 事务写 → 推进 applied watermark
写失败： 记 failed + 审计可见 → 扫描继续（内存权威撑住）
恢复追赶：启动/定期 reconciler → 对比内存/源 seq 与 PG 水位
          → 按幂等键补写缺失区间 → 校验连续水位 → 清除 pending/gap
永久失败：保留 failed 与错误原因 → 有界重试 → 审计视图可见
崩溃窗口：未持久化尾部如实标 gap，不假装完整（§5.2 纪律不变）
```

- 追赶必须幂等，允许重复扫描和重复投递。
- 审计视图至少区分 `pending / applied / gap / failed`。
- 入队姿态（2026-11-24 校准，以代码为准）：当前 River 入队走非事务 `client.Insert`，PG 写与入队不在同一事务；"写成功但入队前崩溃"的窗口由 reconciler 按连续水位追赶覆盖，不依赖同事务入队。若未来引入同事务入队，必须与 reconciler 协议并存而非替代。

### 6.5 River 的允许与禁止范围

允许：InfoItem 坐标判读、可选嵌入、关系推断、异步审计投影和可恢复的冷路径后处理。

禁止接管：扫描主队列与生命周期、HITL 审批状态机、agentsgraph 子图、事件流广播。

通知、PDF、retention 只有在对应迁移验收通过后才能异步化；不得因引入 River 造成功能回退。

### 6.6 部署形态（v3.0 修订）

- **单二进制形态保留**：go:embed 前端 + 自包含引擎不变；PG 是部署要求，不是进程形态变化。
- **PG 必需**：操作者自备 PG（`XALGORIX_DATABASE_URL`），仓库提供 docker-compose 模板；无 DSN 启动 fail-fast 并给出指引。v2.1 的“无 DSN 降级纯文件”模式自此废止（用户裁决 2026-11-22）。

---

## 7. 规划、VOI 与搜索算法边界

### 7.1 当前调度保持确定性

在 VOI 落地前，`claim_next_hypothesis` 保持 confidence 排序、queued/blocked 规则和稳定 tie-break。不得引入随机排序或不可重放的模型即时打分。

### 7.2 VOI 只能作为受限加权项

VOI 必须在信息模型、审计与纯排序测试就绪后单独接入：

- confidence 与 VOI 都归一到 `[0,1]`；缺失 VOI 使用固定默认值并记录，不得随机补值；
- 初始公式为 `score = (1-w)*confidence + w*VOI`，`0 ≤ w ≤ 0.30`；
- `w=0.30` 是初始工程上限，不是经过效果验证的最佳值；
- 同分依次使用现有状态规则和稳定插入顺序；
- 同一不可变快照的纯排序结果必须完全一致；
- 排序与认领分离测试：纯函数负责排序，`ClaimNext` 负责原子状态迁移。

**落地状态（M5，2026-11-23）**：VOI 已接入 `claim_next_hypothesis` 排序（`XALGORIX_VOI_WEIGHT`，默认 0，`MaxVOIWeight=0.30`）；planner/worker 的 intent 生成尚未消费 VOI，因此本节公式目前只作用于假设认领。
**v3.3 扩展（IG4，已落地）**：同一公式已作用于 intent 排序（关联 belief/fact 的 VOI 取 max，缺省 0），仍保留 `w<=0.30` 与稳定 tie-break；`w=0` 时保持原 phase 顺序。

“连续调用两次 ClaimNext 结果相同”不是有效验收，因为第一次调用会改变快照。金样本必须测试不可变快照排序，认领则另测并发互斥与状态迁移。

### 7.3 搜索算法不进入本期路线图

A* / MCTS / POMCP / DESPOT 作为研究项。若未来要引入，至少先证明：

1. 计划语义明确，真实世界副作用不会因树搜索被重复执行；
2. 存在不伪造目标响应的可采样结果模型；
3. 扫描级共享预算可以按分支隔离并准确核算。

这三条是本仓的进入门槛，不断言它们是所有搜索算法的普遍理论必要条件。不得用 LLM 编造 rollout 观察，也不得以“局部引入”为名替换现有调度语义。

---

## 8. 原语：State + Operation + Obligation

- **State**：当前状态集合，包括 ledger、surface、findings 的内存权威与 PG 持久态（v3.0 起无并行 JSON 持久态）。
- **Operation**：作用于状态的动作，包括 PlanTask 与 tool call。
- **Obligation**：引擎对 Agent 的硬约束，包括 phase obligation、收工闸门、coverage epoch 与 hook 拦截。

人工输入不新增顶层原语：R7 起 guidance 同时投影为 `hint` 节点（计划面）与 `source=human` 的 observed InfoItem（`OpRecordGuidance`，信息层）；结构化目标经 `OpRecordObjective` 投影为 `source=human` 的 InfoItem。区域点亮因此能看到人工指引与目标。
v3.3：信息聚合视图（`info_space_*`）是**读模型**，不是新的写权威；它把 State 里的图节点与原子 InfoItem 投影成一个可查询空间，操作仍落在 State/Operation/Obligation 三个原语上。

Obligation 不得被 InfoItem 置信度或 VOI 绕过。删掉 Obligation 就无法表达“适用阶段没有 disposition 不许收工”等防烂尾约束。goal 收工义务（R-goal，REDESIGN D10）是同一原语的操作者目标实例：required 判据未达成不许主动收工——它只能被物理上限（预算/操作者停止/进程关停）与有界阀终止，不能被“置信度高所以达成”绕过；判定用 `internal/objective` 确定性评估器，不引入 LLM。

---

## 9. 不可破坏的约束

1. **扫描隔离**：per-scan 状态经 `scanctx`；agentsgraph 必须挂在根扫描上下文上，扫描间不得交叉。
2. **Verifier 归属**：Verifier 只使用所属 Agent 的 client 与受限工具面；所属 Agent 在报告验证期间保持阻塞。不得跨 Agent 串用 verifier/client，不得把同一 Agent 的验证并发化。
3. **判定边界**：InfoItem 不得替代 Verifier 或确定性验证证据；finding 标签只能由报告管线给出。
4. **安全链**：scope → redirects → dial → sandbox → proxy fail-closed → 环境剥离不得绕过；同时不得把特定 Go transport 或文件策略夸大为覆盖所有浏览器、终端和 Python 行为的 OS 沙箱。
5. **遥测单向**：工具与 Agent 的实时遥测经 per-scan channel → `processEvent` → SSE（`/api/stream`）；不得在工具中直接 broadcast 或写 HTTP 响应。领域审计持久化位于状态变更边界，不依赖可丢弃的遥测投递。
6. **密钥边界**：API 永不回传密钥，只回 `*_configured` 布尔；现有令牌摘要与常数时间比较不得退化。
7. **持久化纪律（v3.0）**：状态与面板数据的持久化走 PG 事务（变更边界，§6.3/§6.4）；仍留在文件层的字节产物（工具输出归档、流量原始包、上传物、交付物）统一走安全原子写，不得新增固定临时文件或直接截断式写入。
8. **报告语义**：`verified / exploit-proven / needs-manual-verification` 三标签及报告门顺序不得被信息模型改写。

---

## 10. 落地顺序与验收

每一步独立可回滚；前一步验收不过，不进入下一步。工期是排期参考，不是验收标准。

> **v3.0 追加（2026-11-22）**：R6 系列（PG 主存储，批次 R6.0–R6.3）按同一纪律推进——批次切分、验收与测试接缝见 `docs/PG_PRIMARY_STORAGE_SPEC.md`，里程碑归口 `REDESIGN.md` §9。M3 的水位/幂等协议是 R6.1 追赶协议的直接前身。
>
> **状态快照（2026-11-23，以代码为准）**：P0、M0–M5、R6.0–R6.3、R7 M1–M2/R8 已落地；R7 剩余缺口见 `docs/DUAL_GRAPH_ORCHESTRATION.md` §7（资产摄取、planner 工具循环、真实 run 验收等）。下表是验收判据，不是当前进度。

| 步骤 | 内容 | 必须通过的验收 |
|---|---|---|
| P0 | 稳定现有基线 | 修复全局停止状态污染、工具 Error/Success 与熔断语义、ledger 快照写序、agentsgraph 持久化失败回滚、审计丢失风险及 Verifier 实际 deadline；现有安全与报告回归全部通过 |
| M0 | Evidence 认识类型 | 保留现有 `kind`；新增 `nature`、可选 `supports/info_item_id`；缺省按 inferred；inferred 支持关系可校验；finding_ref 与现有证明门测试通过 |
| M1 | Hypothesis 前置条件 | 缺少 preconditions 时 `record_hypothesis` 返回可纠正提示；历史/未知值不伪装成已满足；Obligation 与收工门回归通过 |
| M2 | 完整审计记录与只读投影 | 自动和模型认领均有持久化记录；重复/乱序/缺口可识别；可从记录还原认领历史；投影不参与判定，缺口显示为 incomplete |
| M3 | InfoItem 冷路径落 PG | 无 DSN 全核心测试通过；幂等重投通过；杀死进程模拟“JSON 已写未入队”后 reconciler 能追平；pending/applied/gap/failed 可查询 |
| M4 | 激活入口接真实消费者 | 区域、关联、语境分别至少一个消费者有契约测试；关联跳数/预算有上限；结果携带来源和截断标志；无 PG 时热路径不依赖这些跨扫描查询 |
| M5 | 确定性 VOI 加权 | 不可变快照金样本排序稳定；`w=0` 完全保持旧排序；边界值和缺失值有测试；ClaimNext 并发认领不重复 |

### 10.1 M3 参考性能验收

“跨扫描检索 ≤200ms”必须绑定固定基准，不作为任意环境下的绝对承诺。参考基准为：

- 本地单实例 PostgreSQL；
- 100,000 条 InfoItem、300,000 条关系边；
- 16 个并发只读查询；
- 单次最多返回 100 条；
- 只计算 SQL 查询与结果装配，不含 LLM 坐标补全；
- 预热后按主体+时间过滤查询的 p95 不超过 200ms；
- 同时记录 p50/p95/p99、执行计划与截断结果，结果存入基准测试输出。

硬件或 CI 环境变化时重建基线，不用不同环境的绝对数值做直接比较。基准实现：`internal/infoitem/postgres/benchmark_regression_test.go`（`XALGORIX_TEST_DATABASE_URL` 门控，输出 p50/p95/p99）。

### 10.2 变更隔离

P0 的正确性修复与 M0–M5 的架构变更分开提交、分开回归。不得借信息模型改造顺带重排报告门、扩大 Verifier 工具面或改变扫描生命周期。

---

## 11. Verifier 与报告管线的变更边界

M0–M5 默认冻结：

- Verifier 的判定语义、证据标准与工具面裁剪；
- 轮次/时限的设计目标；
- `report_vulnerability` 的闸门顺序；
- 三标签语义。

但**明确、独立且带回归测试的正确性 Bugfix 不受冻结限制**，例如 deadline 未约束正在执行的请求、工具超时未取消或状态清理错误。此类修复属于 P0，不得与 InfoItem、PG 或 VOI 功能提交混合。

任何确需改变判定语义或证据标准的后续工作，必须单独记录设计决策、风险与对照测试，不能由本宪章自动授权。

---

## 12. 本期明确不做

- Neo4j / MongoDB / Redis 等第二数据库；
- 全局搜索树、MCTS rollout、POMDP belief 求解；
- 用 InfoItem 置信度推翻 Verifier 或直接生成 finding；
- 把 Hypothesis 当 Intent、把引擎义务当 Hint；
- 复用 `Evidence.Kind` 表达 observed/inferred；
- 跨存储分布式事务；
- 用 River 接管扫描生命周期或 HITL；
- ~~任何让“没有 PG 就跑不起来”的改动~~（v3.0 废止：PG 自 R6 起为必需依赖，见 §6.6；此条保留删除线以存裁决痕迹）；
- 以“点亮”为名禁止 ledger/surface/reporting 的确定性直查；
- 把可丢弃的实时遥测当成完整审计记录；
- 把项目/企业/资产/审批平台并入 M0–M5 信息层验收（R 系列已独立落地）；
- 在没有具体相似度消费者前强制引入 pgvector；
- 借架构改造并发化同一 Agent 的 Verifier 或重排报告闸门。

---

## 13. 研究项

以下内容不影响本期实现，也不构成默认路线图：

- Cairn_Y 的 FGS（Fact-Goal-Step）作为另一种组织方式；
- Intentest 的 POMDP 表述与信念更新；
- 事件溯源作为状态权威，前置是 prompt 装配、上下文压缩、预算与外部副作用均可完整记录；
- 模型辅助 VOI 估计，前置是可校准的决策价值标签集与离线评估；
- 跨任务资产运营 UI（`/assets` API 已有，前端尚未建运营面；observed 摄取与 `record_asset` 已落地）；
- 企业级多租户/组织模型（项目/任务/HITL 审批已落地，不在研究项）；
- pgvector 相似度检索，需先证明确定性属性和关系查询不能满足消费者需求。

---

## 14. 相对 v1 的主要修正

| # | v1 问题 | v2 及本版处理 |
|---|---|---|
| 1 | 将 Hypothesis 近似为 Intent，对象映射方向错误 | 拆成 Belief/Hypothesis 与 Intent/PlanTask |
| 2 | 把事件投影当成完整当前状态 | 事件仅用于审计与回归；状态权威 = 内存 + PG（v3.0）；不再有并行 JSON 持久态 |
| 3 | 二原语删除引擎强制义务 | 改为 State + Operation + Obligation |
| 4 | 三类点亮没有消费者，且误伤确定性直查 | 为每类指定消费者、共同查询契约和预算 |
| 5 | 以仓外系统和不可验收周期作为主要基线 | 改为本仓对象、金样本和逐里程碑验收 |
| 6 | PG 写入与入队“同事务”前提不清 | 区分 JSON 热路径与 DB 冷路径，禁止宣称跨存储同事务 |
| 7 | 搜索算法进入条件模糊 | 明确副作用、结果模型与预算隔离门槛 |

---

## 15. v2.1 相对 v2 的补强

| # | 补强项 | 目的 |
|---|---|---|
| 1 | 修正“无共享图/无 Worker”和“系统只能单目标串行”的描述 | 避免重复建设已有扫描内共享能力 |
| 2 | 将 `kind` 与 `nature` 分离，并增加 supports/info_item_id | 防止破坏 baseline、exploit、finding_ref 等既有语义 |
| 3 | 将线性真相源阶梯改为字段级权威与冲突留痕 | 防止不同职责对象互相覆盖 |
| 4 | 修正 Verifier client 归属和“唯一验证来源”描述 | 对齐当前 registry-local verifier 与确定性证明路径 |
| 5 | 增加领域审计完整性、seq 和 incomplete 缺口语义 | 防止由可丢弃遥测生成伪完整审计 |
| 6 | 增加 reconciler、连续水位和崩溃追赶协议 | 覆盖 JSON 成功但异步入队前崩溃的永久丢失窗口 |
| 7 | 将 VOI 排序与原子认领分开验收 | 避免使用会改变快照的连续 ClaimNext 作为确定性测试 |
| 8 | 为 200ms 指标补充数据量、并发、p95 与计时边界 | 使性能验收可重复 |
| 9 | 增加 P0 基线稳定步骤 | 避免在现有生命周期和持久化缺陷上叠加新架构 |
| 10 | 将项目/HITL/pgvector 移出首期 | 控制范围，避免信息模型改造演变为平台重写 |

---

## 16. 术语表

- **InfoItem**：信息模型的全局实例，七维坐标、唯一 ID、observed/inferred 类型及来源关系。
- **Observation**：原始请求、响应、工具输出或人工原始输入，不等于对它作出的解释。
- **Evidence**：信息在 hypothesis 下的投影视角；`kind` 表示用途，`nature` 表示 observed/inferred。
- **Belief / Hypothesis**：关于世界的待证主张，带前置条件、baseline、置信度与六态生命周期。
- **Operation**：PlanTask 或 tool call 等作用于状态的动作。
- **Obligation**：引擎对 Agent 的硬约束，来自阶段义务、覆盖收敛、收工闸门与 hook 拦截。
- **Verdict**：Verifier 三态或引擎确定性验证结果；finding 标签只能由报告管线给出。
- **区域 / 关联 / 语境点亮**：三类有明确消费者、预算和来源信息的确定性检索入口。
- **领域审计记录**：状态变更边界产生的持久化记录，不使用可静默丢弃的实时遥测语义。
- **审计投影**：由领域审计记录还原的只读历史视图，不参与判定，必须能显示缺口。
- **source seq / watermark**：每扫描单调变化序号及 PG 已应用的连续位置，用于幂等和崩溃追赶。
- **reconciler**：比较源 seq（内存/PG `run_journal`，遗留目录仅兜底）与 PG 水位并补投缺口的恢复机制；与 River 正常异步处理互补。
- **VOI**：信息决策价值，仅作为受限、确定性的排序加权项，不改变验证权威或 Obligation。

---

## 17. v3.0 相对 v2.1 的修订（2026-11-22，用户裁决路线 B）

| # | 条目 | v2.1 | v3.0 |
|---|---|---|---|
| 1 | 持久权威 | `scan.json` / `ledger.json` 等原子文件（§5.1） | PostgreSQL（内存为进程内权威，§5.1/§6.3） |
| 2 | 存储分层 | JSON 写入源 + PG 检索副本（§6.3） | 内存权威 + PG 同步持久化；无并行 JSON 持久态（§6.1/§6.3） |
| 3 | PG 范围 | 冷路径：InfoItem/边/审计投影/水位（§6.2） | 全部状态与面板数据 + 原 M3 范围（§6.2） |
| 4 | 事件流 | 文件 journal（events.jsonl / domain_journal.jsonl） | PG events/journal 表；文件只留字节产物（§5.1/§6.2） |
| 5 | 无 DSN | 降级纯文件、核心能力不减（§6.6） | 废止：PG 必需，无 DSN fail-fast + compose 模板（§6.1/§6.6） |
| 6 | run↔scan 关联 | fill-once `legacy_scan_id` 事后回填 | 写入点盖章 `instance_id` 为身份权威；`legacy_scan_id` 保留为运行期 run↔scan 关联列（派发时 fill-once，`ProjectScanIDs` 依赖）与存量导入幂等（§6.3，2026-11-24 校准） |
| 7 | 幂等键 | `(scan_id, seq)`（M3 协议） | 保持 `(scan_id, seq)`，扩展到全部状态表（§6.4） |
| 8 | 续跑重放 | 上一会话 events.jsonl（D11 L2） | PG 全历史 + 保尾预算（REDESIGN D12） |
| 9 | 单二进制 | 单二进制 + 无 DSN 可跑 | 单二进制保留；PG 为部署要求（§6.6） |

不变项：主循环七环、对象映射、Verifier/闸门冻结（§11）、审计完整性纪律（§5.2）、§12 其余全部条目、VOI 确定性边界（§7）。落地规格：`docs/PG_PRIMARY_STORAGE_SPEC.md`。

### 17.1 v3.2 现状校准（2026-11-23）

| # | 原表述 | 校准后（以代码为准） |
|---|---|---|
| 1 | 仅 REST/WebSocket | 仅 REST/SSE（`/api/stream`）；`WSEvent` 为历史类型名 |
| 2 | 权威持久化为每扫描 JSON/JSONL；无 PG/River/Eino/HITL/MCP | PG 主存储且 DSN 必需；River/Eino/HITL/MCP 客户端/项目任务资产平台均已落地 |
| 3 | agentsgraph 固定一波、非 Worker 池 | agentsgraph 同时承载 planner_worker 池路径（intent 租约、steer/kill） |
| 4 | 不是项目/资产/审批平台 | 项目/任务/审批/资产平台已落地；仍非企业级多租户 |
| 5 | 热路径写 JSON、冷路径 River 写 PG（有 DSN 时） | 热路径写 PG（内存权威 + 变更边界事务）；文件只留字节产物；无 DSN fail-fast |
| 6 | `legacy_scan_id` 关联退役 | 新写入不再回填；列与 `ImportLegacyRun` 保留给存量导入幂等 |
| 7 | VOI 未落地 | `claim_next_hypothesis` 已接 VOI（默认权重 0，上限 0.30）；planner intent 生成未消费 |
| 8 | 人类输入统一为 `source=human` InfoItem | objective 与 delivered guidance 均投影为 observed InfoItem；guidance 另有 hint 节点 |
| 9 | 关联点亮含矛盾/同会话语义 | 已接线：`supports`/`references`/`same_subject`/`same_session`/`contradicts` 均有确定性生产者 |
| 10 | 跨扫描资产图/项目 HITL 属研究项 | 已落地并移出研究项；资产 observed 摄取与 `record_asset` 已落地，剩余资产运营 UI |

修订依据：`internal/web/sse_hub.go`、`cmd/xalgorix/main.go:266`、`internal/jobs/jobs.go`、`internal/engine`、`internal/approval`、`internal/tools/mcp`、`internal/tools/agentsgraph/agentsgraph.go`、`internal/graph`、`internal/assets`、`internal/scanctx/ledger_voi.go`、`internal/agent/ledger_cross_scan.go`、`internal/infoitem/postgres/store.go`。
