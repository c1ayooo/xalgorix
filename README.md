# Xalgorix

> 单 Go 二进制运行的自主 AI 渗透测试引擎：内嵌 React 仪表盘，本地执行 22 阶段方法论扫描，BYO-LLM（自带模型/密钥），全流程证据可追溯。Apache-2.0。
>
> 当前维护仓库：**[c1ayooo/xalgorix](https://github.com/c1ayooo/xalgorix)**。本项目源自 **[xalgord/xalgorix](https://github.com/xalgord/xalgorix)**，原作者 **xalgord**，遵循 Apache-2.0 许可。详见 [15. 来源与致谢](#15-来源与致谢)。

本文档以**架构**为主线，内容以本仓代码与 `docs/CORE_PHILOSOPHY.md`（v2.1 二开宪章）、`docs/ARCHITECTURE.md` 为准。凡是与实现冲突的目标设计，以宪章为验收基线；凡是“现状是什么”的事实描述，以代码和测试为准。

---

## 目录

- [1. 项目定位与边界](#1-项目定位与边界)
- [2. 核心设计思想：信息驱动的渗透循环](#2-核心设计思想信息驱动的渗透循环)
- [3. 22 阶段方法论](#3-22-阶段方法论)
- [4. 系统架构](#4-系统架构)
- [5. 数据流](#5-数据流)
- [6. 持久化、隔离与冷路径](#6-持久化隔离与冷路径)
- [7. 安全链（不可绕过）](#7-安全链不可绕过)
- [8. 配置](#8-配置)
- [9. 构建与运行](#9-构建与运行)
- [10. 目录结构](#10-目录结构)
- [11. 落地顺序与验收里程碑](#11-落地顺序与验收里程碑)
- [12. 本期明确不做](#12-本期明确不做)
- [13. 术语表](#13-术语表)
- [14. 授权与合规](#14-授权与合规)
- [15. 来源与致谢](#15-来源与致谢)

---

## 1. 项目定位与边界

### 1.1 是什么

- **单二进制、单进程**的自主渗透 Agent：一个 Go 可执行文件同时提供 CLI/TUI、REST API、SSE 事件流和内嵌 React 仪表盘（默认 `127.0.0.1:9137`）。
- **方法论由代码强制**：22 个规范阶段定义于 `internal/methodology/registry.go`，漏洞类注册表、`endpoint × class` 覆盖矩阵与 coverage epoch 共同约束探索范围。
- **判定由代码强制**：扫描级 hypothesis/evidence ledger、工具面受限的独立 Verifier、`report_vulnerability` 的报告门管线三者共同约束 finding 的接受与标签。
- **信息驱动**：主循环不是“prompt 里塞一堆上下文”，而是 观察 → 信念 → 义务 → 行动 → 证据 → 判定 → 收工 的闭环，且每一环都有可验收的承载对象。
- **可降级**：无 PostgreSQL 时自动退化为“纯文件 + 无跨扫描检索”，核心扫描能力不减。

### 1.2 不是什么

- 不是通用 Fact/Intent 求解器，也没有跨扫描任务图。
- `agentsgraph` 是**固定一波、受预算约束**的子 Agent 执行图，不是通用 Worker 池，也不是 Eino 之外的第二个引擎。
- 不是项目/企业/资产/审批运营平台；扫描队列与定时任务不等于项目管理和完整 HITL 平台。
- 本期不使用 Neo4j/MongoDB/Redis，不做跨存储分布式事务，不做 MCTS/POMDP 搜索。

### 1.3 系统主语

宪章首先约束**一次 target session 的生命周期**，并规定它与四个边界的交互：

1. 扫描级共享 ledger 与 `agentsgraph`；
2. Web 层多目标队列与实例生命周期；
3. 只读审计投影；
4. PostgreSQL 冷路径与跨扫描检索。

---

## 2. 核心设计思想：信息驱动的渗透循环

### 2.1 主循环七环

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

**入门测试**：任何新能力动手前必须回答“它落在七环的哪一环、由哪个对象承载、如何验收”。回答不出来的不进入实现。

### 2.2 对象映射（不得反向使用）

| 抽象概念 | 本仓真实对象 | 不得误认为 |
|---|---|---|
| Fact | 原始 Observation、ledger Evidence、已接受 finding | 任意模型自述 |
| Belief | Hypothesis（`record_hypothesis`，六态状态机） | Intent |
| Intent | PlanTask（`build_plan` / `update_plan`）及具体 tool call | Hypothesis |
| Hint | 引擎义务：phase obligation、completion gate、coverage epoch、hook nudge | 人工输入 |
| Verdict | Verifier 三态、引擎确定性验证结果、报告管线标签 | InfoItem 置信度 |

人工输入 = `source=human` 的 InfoItem，经区域点亮进入视野，不新增顶层原语。

### 2.3 InfoItem 与 Evidence

两者不是替代关系：

- **InfoItem** 是信息模型的全局实例，承担跨扫描检索、来源追踪与审计。
- **Evidence** 是信息在某条 hypothesis 下的投影视角，回答“这条信息为什么支持、反驳或控制该假设”。
- 一个 InfoItem 可投影到多个 hypothesis；一个 hypothesis 可关联多个 InfoItem。不得复制全文制造多份无来源关系的“真相”。

**InfoItem 最小模型**：

- 全局唯一 `info_item_id`（同一坐标的重复观测也是不同实例）；
- 七维坐标：内容 / 来源 / 时间 / 主体 / 目的 / 情境 / 关系；
- `nature = observed | inferred`；
- 有效期与 `scan_id`；
- 对冷路径同步有意义的 `source_seq`；
- inferred 信息非空的 `supports[]`，指向其支持证据；
- 坐标补全状态与置信度（只描述标注可靠性，不是漏洞成立概率）。

**Evidence 两个维度必须分开**：

| 字段 | 含义 |
|---|---|
| `kind` | 证据用途：`baseline` / `control` / `probe` / `exploit` / `artifact` / `finding_ref` 等 |
| `nature` | 认识类型：`observed` 或 `inferred`（历史/缺省值一律按 `inferred` 处理） |
| `info_item_id` | 可选的冷路径全局信息实例链接 |
| `supports` | inferred 判断的支持证据 ID 列表 |
| `source` | 引擎拥有的验证器或确定性来源（模型不得伪造） |

`Evidence` 继续保留每假设 40 条、单字段 4096 字符及现有内存上限；InfoItem 冷存储**不得反向放宽**扫描热路径上限。

### 2.4 字段级权威（而非全局“高置信度覆盖”）

| 字段或判断 | 权威来源 |
|---|---|
| 原始请求、响应、工具输出 | 对应原始 Observation |
| finding 是否接受及验证标签 | `report_vulnerability` 报告门管线 |
| hypothesis 生命周期、归属与调度状态 | ledger |
| InfoItem 的来源、目的、情境与关系标注 | InfoItem 及其补全记录 |
| 独立复测结论 | 所属 Agent 的 Verifier |
| 引擎确定性验证结果 | `verify_*`、浏览器执行信号等引擎来源 |
| 模型自述 | 最低等级，仅是待证输入 |

规则：

1. InfoItem 置信度不得直接产生 finding、修改验证标签或推翻 Verifier。
2. 不同来源冲突时保留冲突、来源与时间，低权威不得静默覆盖高权威。
3. 报告管线是 finding 标签的唯一出口，但不是所有原始事实的唯一生产者。
4. 确定性验证证据可按现有报告语义跳过 LLM Verifier——Verifier 不是唯一验证来源。

### 2.5 三类激活（查询入口 + 消费者）

三类激活是三个**可测试的查询入口**，不是修辞，也不是对确定性直查的禁令。每类都必须点名消费者。

| 类型 | 语义 | 消费者 |
|---|---|---|
| **区域点亮** | 按主体/时间/来源/目的/类型/扫描做确定性过滤 | `claim_next_hypothesis` 可调度池；`taskCoverageComplete` 与 coverage epoch 缺口计算；`read_ledger(filter=schedulable)`；跨扫描同主体候选检索 |
| **关联点亮** | 沿支持/矛盾/引用/同会话/同主体关系边传播（跳数 ≤ 3，受结果数与时间预算截断） | Verifier 的 baseline/控制组装；`report_vulnerability` 同类同端点去重与证据引用；`verify_*` 复用既有 baseline；跨扫描同主体历史 |
| **语境切片** | 固定部分维度，投影同一语境下的混合信息视图 | `VerificationRequest` 的字段装配；报告生成的 finding + evidence + traffic 引用；人工审计视图 |

**共同查询契约**：明确调用方与场景、输入过滤与 `limit`/预算/确定性排序、返回项的 ID/来源/nature/关系引用、是否截断与同步缺口、空结果与错误的区别。

直接调用 ledger/surface/reporting 的确定性 API 是允许的，可视为激活的一种实现。**禁止的是绕过来历与证据、凭模型上下文记忆复述事实。**

### 2.6 事件、状态与审计投影

- **本期状态权威**：`scanctx.ScanContext` + `scan.json` / `ledger.json` 等原子文件。
- **事件流**：只用于审计、回归对比与实时遥测。
- **不做**“当前状态 = 事件投影”，也不承诺事件重放得到相同 Agent 决策。

原因可复核：system prompt 每次动态拼装、对话窗口存在有损剪枝与压缩、超长工具输出落盘后只保留引用。

**领域审计记录不得复用可静默丢弃的遥测通道**，必须：在 ledger 变更边界产生专用记录；带 `scan_id + seq`、操作者、操作、对象、前后状态摘要与时间；自动认领与模型认领使用同一协议；每扫描 seq 单调；持久化失败不得当成功；投影只读、不反向修改 ledger、不参与漏洞判定。历史不足时投影必须显式标记 `incomplete` 与缺口范围。

### 2.7 PostgreSQL / River / 最终一致

**热路径不变**：per-scan 状态继续经 `scanctx`；生命周期仍由 Web 状态机与资源准入控制；文件写入统一 `storage.WriteAtomic`；PG 不可用不得阻断已启动 Run 的推理、工具执行、finding 保存、报告或收工。

**冷路径范围**（本期 PG 只承载）：InfoItem（JSONB）、关系边（关系表 + 递归 CTE）、审计投影与同步水位、已明确接入消费者的跨扫描检索索引。

**真相源与幂等**：

- JSON/扫描内状态是写入源，PG 是检索与审计副本；不做跨存储分布式事务。
- 每扫描变更以 `(scan_id, seq)` 为幂等键；PG 保存已应用的连续水位。
- 不同 scan 的 seq 不互相比较；跨扫描排序必须使用时间或独立全局标识。

**崩溃追赶协议**（River + reconciler 缺一不可）：

```text
正常路径：变更记录 → River 处理 → PG upsert → 推进 applied watermark
崩溃恢复：启动/定期 reconciler → 对比 source seq 与 PG 水位
          → 补投缺失区间 → 校验连续水位 → 清除 pending/gap
永久失败：保留 failed 与错误原因 → 有界重试 → 审计视图可见
```

- 追赶必须幂等，允许重复扫描与重复投递。
- 审计视图至少区分 `pending / applied / gap / failed`。
- **River 允许**：InfoItem 坐标判读、可选嵌入、关系推断、异步审计投影、可恢复的冷路径后处理。
- **River 禁止接管**：扫描主队列与生命周期、HITL 审批状态机、`agentsgraph` 子图、SSE 广播。

**单二进制降级**：无 DSN 时运行于“纯文件 + 无跨扫描检索”模式；API 明确报告冷路径 `disabled` 而非 `failed`；PG 恢复后由 reconciler 追赶，无需重跑扫描。

### 2.8 规划、VOI 与搜索边界

- 在 VOI 落地前，`claim_next_hypothesis` 保持 confidence 排序、queued/blocked 规则与稳定 tie-break；不得引入随机排序或不可重放的模型即时打分。
- VOI 只能作为**受限加权项**：`score = (1-w)*confidence + w*VOI`，`0 ≤ w ≤ 0.30`；缺失 VOI 使用固定默认值并记录；同分回落到现有状态规则与稳定插入顺序；同一不可变快照的排序结果必须完全一致；**排序（纯函数）与认领（原子状态迁移）分开测试**。
- A*/MCTS/POMCP/DESPOT 属于研究项，进入门槛是：动作可回滚或幂等重放、存在可采样的结果模型、预算可分区。当前一条都不满足。

### 2.9 原语：State + Operation + Obligation

- **State**：ledger、surface、findings 及其权威文件。
- **Operation**：PlanTask 与 tool call。
- **Obligation**：引擎对 Agent 的硬约束（阶段义务、收工闸门、coverage epoch、hook 拦截）。

Obligation 不得被 InfoItem 置信度或 VOI 绕过——删掉它就无法表达“适用阶段没有 disposition 不许收工”这类防烂尾约束。goal 收工义务（R-goal）是同一原语的操作者目标实例：required 判据未达成不许主动收工，只能被物理上限（预算/操作者停止/进程关停）与有界阀终止，判定用 `internal/objective` 确定性评估器，不引入 LLM。

---

## 3. 22 阶段方法论

阶段编号是**分类法而非时序**：引擎按观测到的表面适用性调度，后面的阶段可能先于前面执行。该包保证的是——每个被选中/适用的阶段在扫描宣称完成前，必须获得真实的、引擎可见的义务或显式 disposition。

| ID | 阶段 | ID | 阶段 |
|---|---|---|---|
| 1 | Deep Reconnaissance & Attack Surface Mapping | 12 | Race Conditions & Business Logic |
| 2 | Manual Vulnerability Discovery | 13 | Subdomain Takeover |
| 3 | Directory & File Discovery | 14 | Open Redirect Testing |
| 4 | CORS & Cookie Analysis | 15 | Email Security Testing |
| 5 | Authentication & Session Testing | 16 | Cloud & Infrastructure |
| 6 | Injection Testing | 17 | WebSocket Testing |
| 7 | SSRF / XXE Testing | 18 | CMS-Specific Testing |
| 8 | IDOR & Broken Access Control | 19 | Broken Link Hijacking & Content Spoofing |
| 9 | API & GraphQL Testing | 20 | Exploit Verification |
| 10 | File Upload Testing | 21 | Novel Vulnerability Discovery |
| 11 | Deserialization & RCE | 22 | Final Report |

阶段 ID（1–22）是跨系统契约：plan task、阶段限制、扫描记录、报告与 UI 都以此为键。

---

## 4. 系统架构

### 4.1 分层总览

```text
Runtime            cmd/xalgorix        二进制入口 + OS 服务装配（web 模式 / scan 模式）
   │  dispatches
   ▼
Web Platform       internal/web        本地 HTTP API + 内嵌 React 仪表盘、
   │                                  扫描编排、持久化、通知
   ▼
Agent Core         internal/agent      自主循环：reason → call tool → observe → repeat
   │                                  （受 scope / phase / 速率守卫约束）
   ▼
Engine             internal/engine     唯一扫描运行时（Eino ADK ChatModelAgent + Runner）
   ▼
Tooling            internal/tools      工具注册表 + 全部能力（terminal/browser/http/…）
   ▼
Providers / LLM    internal/llm,       模型路由、KeyStore、provider 目录、认证驱动
                   internal/providers, internal/auth
```

横切能力：`internal/scanctx`（per-scan 隔离）、`internal/reporting`（findings 存储 + PDF）、`internal/storage`（原子写）、`internal/sandbox` + `internal/scopeguard`（安全）、`internal/config`（配置）。

### 4.2 Runtime — `cmd/xalgorix`

- `main.go`：解析 flag/config 并分发。**web 模式**启动仪表盘 + API server（`internal/web`）；**scan 模式**（含 `--start` / `--restart` 等子命令）直接驱动引擎。持有经 `-ldflags` 注入的 `version` 字符串，并有 `debug.ReadBuildInfo` 回退。
- `exec_unix.go` / `exec_windows.go`：OS 相关的进程/服务辅助（Linux systemd 集成 + 后台回退等）。

### 4.3 Web Platform — `internal/web`

对外提供 REST API + SSE 事件流（`/api/stream`，原 WebSocket hub 已移除），并托管由 `webui/` 编译后经 `//go:embed static/*` 内嵌的 React 仪表盘。同时拥有扫描编排与持久化。

v4.5.44 拆分后 `server.go` 只是薄核心 + 路由枢纽：

| 文件 | 职责 |
|---|---|
| `server.go` | `Server` 结构、`NewServer`/`Start`、生命周期、小型 handler |
| `handlers_router.go` | 路由表 / mux 装配 |
| `auth_session.go` | 仪表盘认证：会话、登录退避、CSRF、中间件 |
| `sse_hub.go` | SSE hub：扇出、环形缓冲、`Last-Event-ID` 重放 |
| `orchestrator.go` | 扫描编排：single / DAST / wildcard / multi + 子域收集 |
| `scan_session.go` | 单次扫描会话 + 事件处理 + 阶段推断 |
| `scan_audit.go` | 只读领域审计投影与冷路径同步状态 API |
| `queue_state.go` | 扫描队列持久化、恢复、准入控制 |
| `scan_record.go` / `scan_query.go` / `scan_list.go` | 扫描记录装配、查询、列表与缓存 |
| `notify.go` | Discord + Telegram 通知（按严重级别门控） |
| `chat.go` | 扫描中 / 扫描后 Agent 对话 |
| `schedules.go` / `scheduler.go` | 定时扫描：tick 在 `schedulesMu` 下快照到期项并推进时间，任务反查/项目 scope 校验/run 挂载均无锁执行 |
| `handlers_platform.go` | projects/tasks/runs REST API、run 挂载、项目 scope 目标校验、`project_id` 过滤 |
| `handlers_approvals.go` / `approvals_gate.go` | 审批与 guidance REST；`ApprovalService` 槽位队列、超时拒绝、启动对账、WS 事件、载荷脱敏 |
| `platform_legacy_import.go` | 启动时把既有扫描导入为 run 挂载 + 定时任务（幂等、只读） |
| `uploads.go` / `report.go` | 目标/指令/logo/上下文上传；报告交付 |
| `settings_env.go` / `handlers_profiles.go` | 设置、provider keys、凭据 profile、OAuth |
| `retention.go` / `data_dirs.go` / `legacy_import.go` | 数据目录管理与保留策略 |

并发模型：`Server` 持有运行中的扫描实例与若干独立加锁的缓存；每个扫描跑在独立 goroutine + 独立 `scanctx` 中，并发扫描永不串线。

### 4.4 Agent Core — `internal/agent`

`Agent.Run` 构建编码了 22 阶段方法论、全部扫描状态以及预算/检查点前奏的 system prompt，然后把 model/tool 循环交给 Eino ADK 运行时，直到目标达成或触达预算（迭代数 / 时长 / token）。

| 文件 | 职责 |
|---|---|
| `agent.go` | 扫描前奏、生命周期（`Run`/`Stop`）、工具装配、预算/看门狗 |
| `eino_runtime.go` | 启动 ADK runner、映射事件/中断、终结结果 |
| `eino_observer.go` | 义务、模型/provider 恢复、工具门与结果、上下文压缩 |
| `agent_guard.go` | scope 守卫、阶段限制守卫、被动侦察守卫、host/URL 提取 |
| `agent_prompt.go` | system prompt / 收尾指令 / 白盒指引构造 |
| `agent_messages.go` | 工具结果格式化 + 对话裁剪/压缩 |
| `agent_ratepolicy.go` | 从指令解析请求速率策略 |
| `hooks.go` | 义务 hook，含卡死/循环检测（重复相同调用） |
| `verifier.go` | 独立 Verifier Agent，复测候选 finding |
| `ledger_cross_scan.go` | M4 引擎侧消费者：区域/关联点亮的跨扫描注记（只影响输出） |
| `authz_matrix.go` | 授权矩阵：以多身份重放同一请求做差分 |

守卫把 Agent 限制在授权 scope 与已选阶段内，并按请求速率策略节流——这是“自主攻击型 Agent 可以安全指向目标”的边界。

### 4.5 Engine — `internal/engine`（Eino ADK）

`internal/engine` 是**唯一的扫描运行时**。`agent.Agent.Run` 构建 prompt/state/checkpoint 前奏后，把循环交给 `adk.ChatModelAgent` + `adk.Runner`；不存在第二套循环，也没有引擎选择变量。

- `runtime.go`：构建 agent/runner，强制 `ExecuteSequentially`（报告与验证调用不可重叠），安装构建期的工具守卫中间件。守卫拒绝与预期内的工具失败变成普通工具结果，绝不变成杀死图的错误；守卫可返回规范化参数（如终端命令规范化）。
- `chatmodel.go`：在既有 `internal/llm.Client` 之上实现 `ToolCallingChatModel`。原生 `tools`/`tool_calls` 是主通道；对 Anthropic/Gemini/Responses 等端点，同一适配器把工具调用/结果渲染成 XML 方言再解析回 `schema.ToolCalls`，ADK 始终只看到一种原生工具协议。流式被显式禁用。
- `model_recovery.go`：扫描自有的 provider 策略缝。适配器请求重试，Agent 决定退避、有界 provider 等待预算、上下文溢出压缩、空响应重试与无工具恢复——把传输重试留在同一个 ADK turn 内，避免重放工具副作用。
- `tools.go`：把每个注册表工具包装为 Eino `InvokableTool`，每次调用都经 `Registry.ExecuteContext`，保留安全链、熔断器与审批边界。
- `approval.go`：用 `tool.StatefulInterrupt` 包装工具，首次调用产生待审批，`Runner.ResumeWithParams` 投递操作者决定；拒绝返回普通工具结果。
- `obligation.go`：定义 per-turn/tool 观察者缝，ADK 中间件在每次模型调用前施加 nudge，并可阻断 `finish` 等工具。
- `checkpoint.go` / `checkpoint_file.go` / `checkpoint_pg.go`：实现 Eino `CheckPointStore`，有 DSN 走 PostgreSQL（`engine_checkpoints`），否则写 `<dataDir>/_engine/checkpoints` 原子文件。

运行时门禁：`TestGoldenSchedulingParity` 用 ledger 快照驱动唯一运行时，断言 M5 认领顺序、工具序列、收工语义、turn 数与线上 tools 数组；`TestRuntimeToolsNodeConfigPinsVerifierSerialExecution` 钉住 `ExecuteSequentially=true`。

### 4.6 多 Agent 执行 — `internal/tools/agentsgraph`

一次完整评估 = 一个 coordinator + 至多三个假设驱动的 specialist。侦察后，coordinator 把观测面切成互不重叠的角色（典型为 授权/业务逻辑、注入/服务端行为、源码/数据流或客户端/API），自己继续工作，并在收尾前收集全部子结果。

每个根扫描拥有一个 `agentsgraph.Graph`：runner、worker 信号量、取消 context、状态、部分证据与最终结果全部 scan-scoped。后代共享该图但不得替换或停止它，因此并发扫描不会串线。`spawn_agent`、`check_agent`、`wait_agent` 恒定注册；子 Agent 运行中或结果未收集时 finish gate 会阻塞。图还持有共享资源账本：时长从根开始计时，token 跨 LLM client 聚合，Agent 迭代/工具调用对扫描级上限做原子预留。

### 4.7 Hypothesis/Evidence Ledger — `internal/scanctx`（`ledger.go`）

Ledger 是扫描的持久“全局利用上下文”：以攻击假设为节点、证据为边的类型化图。它挂在共享 `ScanContext` 上（coordinator 与所有 specialist 读写同一图），按规范化 class/endpoint/parameter/role 去重，内存有界，并原子持久化到 `<scanDir>/ledger.json`，可跨重启/恢复。

一条 hypothesis 携带：身份、前置条件、baseline/control、置信度、生命周期（`queued` / `testing` / `proven` / `rejected` / `blocked` / `exhausted`）、归属 specialist、next-best action，以及 append-only 证据。已确认 finding 以报告 ID 引用而不复制。

Ledger 驱动编排而非旁置：

- 侦察产出 plan 后，`hookLedgerSeed` 按候选漏洞类确定性地播下 hypothesis。
- 委派由 ledger 驱动：三个确定性 specialist profile，各自带显式证据契约（baseline + 具体证明；盲类要求带外回调；XSS/DOM 要求浏览器确认执行）与停止规则。
- Agent 通过 `record_hypothesis`、`add_hypothesis_evidence`、`update_hypothesis`、`read_ledger` 读写。
- precision finish-gate：hypothesis 标记 `proven` 却没有关联 finding 时拒绝收工（有界，不会死锁），强制 verify-by-execution。

### 4.8 Tooling — `internal/tools`

`registry.go` 是呈现给模型的工具面，每个子包是一项能力：

- `terminal`：沙箱化 shell 执行（CLI 安全工具的主力）；
- `browser` + `pageagent`：无头浏览器自动化（JS 重应用、认证流程）；
- `httpclient`：直连 HTTP 请求；
- `websearch` / `codesearch` / `fileedit` / `notes` / `python`：研究/分析辅助；
- `search_info`（由 `internal/agent` 注册）：对可选 InfoItem 冷路径的只读区域/关联/语境激活，无判定角色；
- `reporting`：`report_vulnerability` 工具 + findings 存储；
- `agentsgraph`：子 Agent 图；
- `proxy` / `oob` / `finish` / `iolimit` / `skills`：代理、带外回调、终止、IO 限制、方法论语料；
- `mcp`：操作者注册的 MCP server（stdio + streamable HTTP），桥接为 `mcp_<server>_<tool>` 的普通注册表工具，继承完整执行链（熔断、审批门、守卫），永不绕过 scope dial/request 检查；**verifier 注册表保持 MCP-free**。per-scan `Manager` 持有会话（无包级全局）；每次调用审计到领域 journal（`skill_load` / `agent_delegate` / `mcp_call`），并尽力投影到 `<dataDir>/_invocations/` 的跨扫描 JSONL。

### 4.9 Findings 与验证 — `internal/tools/reporting` + `internal/reporting`

每个上报漏洞都要过闸门（方法合法、强制利用证明、误报与声明一致性校验、去重、severity/CVSS 归一化），然后交给报告 Agent 的独立 Verifier。验证回调是 registry-local：并行 hunter 在阻塞于自己的报告时使用自己的空闲 LLM client，不会串用兄弟 Agent 的 client。

每个 finding 恰好携带一个验证标签：

- `verified`：Verifier 独立复现；
- `exploit-proven`：Verifier 不确定或缺失，但 finding 自带具体利用结果（命令输出、被拖取数据、OOB 回调…）；
- `needs-manual-verification`：保留但尚未具体证明，**不得丢弃**。

`internal/reporting` 从持久化 findings 渲染品牌 PDF。

### 4.10 信息模型、领域审计与冷路径

CORE v2.1 以**增量层**实现，不替换扫描状态，也不替换报告/验证权威链：

- `internal/infoitem`：InfoItem 模型（七维坐标、稳定 ID、observed/inferred、supports、relations）与有界的区域/关联/语境 `Reader` 契约。
- `internal/domainjournal`：per-scan `domain_journal.jsonl`，单调 `seq`、append 时 fsync、尾行修复、重复/缺口/乱序投影、非阻塞 append 观察者（用于冷路径唤醒）。
- `internal/storage.AppendSync`：领域 journal 共用的 append+fsync 原语。
- `internal/infoitem/postgres`：可选 PG 副本，承载 InfoItem、关系边、审计记录与连续同步水位；实现三类激活查询，永不参与判定。
- `internal/jobs`：钉住 River v0.51.2 的 client/workers；job 只携带有界冷路径 journal 区间，不拥有扫描生命周期、HITL、agentsgraph 或 SSE 工作。
- `internal/coldpath`：组合 PostgreSQL、River、低延迟 journal 观察者与周期性 reconciler（比对本地 journal 与 applied 水位并重投缺失区间）。
- `scanctx.Ledger` 把 Evidence 以 `evidence:<scan_id>:<evidence_id>` 投影进 journal（进而冷路径）；Evidence 保留既有 `Kind`，另有独立 `Nature`，缺失/历史值归一为 `inferred`。
- `scanctx` 在提交前记录 hypothesis 变更（record/claim/assign/status/evidence/attempt/VOI/reset/merge），并带显式 actor。
- `search_info` 是面向 Agent 的只读激活消费者：每个 Agent 都注册，无 DSN 时返回解释性结果，永不参与 finding 接受。
- 两个引擎侧 M4 消费者注记 ledger 工具（`internal/agent/ledger_cross_scan.go`）：区域 = `read_ledger(filter=schedulable)` 的逐条同端点跨扫描注记；关联 = `claim_next_hypothesis` 的同主体跨扫描段落 + 以本扫描条目为根的当前扫描边遍历（关系边是 per-scan 的，跨扫描边图属宪章 §13 研究项）。两者只影响输出、按 per-call 查询预算封顶、无 reader 时退化为原输出；reader 错误与 §6.4 同步缺口以可见注记呈现（绝不静默）。
- 两个只读 HTTP 视图暴露审计面：`GET /api/scans/{id}/audit`（认领历史、重复/缺口/乱序检测、显式 `incomplete`）与 `GET /api/scans/{id}/coldpath`（`pending`/`applied`/`gap`/`failed`，无 DSN 时为 `disabled` 而非 `failed`）。两者位于 `web/scan_audit.go`，投影永不改 ledger、永不参与判定。

热路径变更仍在内存 + `ledger.json`；journal 是持久领域审计；PostgreSQL 是可读副本、可以缺席。存在 `XALGORIX_DATABASE_URL` 时启用启动迁移、River 与对账；PG 错误只会禁用冷路径，永不为扫描设门。

InfoItem `Reader` 是进程级全局（它是基础设施而非 per-scan 状态），但每次查询仍按调用方当前 `scan_id` 收窄。

### 4.11 平台层：Projects / Tasks / Runs — `internal/platform`

Project → Task → Run 模型是**组织**层面的冷路径 system-of-record，绝不是第二扫描状态权威。

- `internal/platform`：`Project`/`Task`/`Run` 模型、`Repository` 接口与两个实现（`pgxpool` 的 PostgreSQL 版、`<dataDir>/_platform/platform.json` 的文件快照版）。所有 list/filter 查询都带显式 `projectID`（D7 行级隔离）；平台 SQL 只存在于 `platform/postgres.go` 与 `platform/postgres_findings.go`。显式 `project_id` 但未装配存储时 fail-closed 返回 503，绝不返回未过滤的跨项目视图。
- **Run 是挂载记录，不是状态**：只存 `task_id`、`instance_id`（唯一）、`legacy_scan_id`、`mode`、`trigger`、`created_at`；状态/起止时间/漏洞数/token 数在读取时从 `findAllScanSummaries` 投影，`scan.json` 仍是唯一权威。
- **降级**：无 DSN → 文件存储 + 完整平台 API + 隐式 `default` 项目（固定 ID，不可删除）；有 DSN 但 PG 不可达 → store 为 nil，平台端点 503，扫描不受影响。
- `POST /api/tasks/{id}/runs` 复用 `/api/scan` 的完整派发序列与派发前守卫；目标必须落在项目 scope 内（精确、域后缀或显式 `*.domain`；`allow_ports` 覆盖显式与 HTTP/HTTPS 默认端口）。
- 任务 `context_paths` 是持久 Context 附件，经 `attacksurface.LoadFromPaths` 合并。
- Legacy 导入（启动时、幂等）：既有扫描挂载为 `default` 项目下 “imported” 任务的 run；既有 schedule 变为带 `schedule` 属性的 task；原始扫描目录从不修改（整树内容+mtime 哈希测试钉住）。
- Task findings 是已接受 `scan.json` findings 的冷路径投影：有 DSN 时 `run_findings` 每 `(run_id, dedup_key)` 一行，读取用 `DISTINCT ON`（最新 run 胜出）；无 DSN 时 Web 层逐 run 分页并施加同样的确定性去重。投影永不参与接受或验证。
- 既有端点接受可选 `project_id` 查询参数（`/api/instances`、`/api/findings`、`/api/findings/summary`、`/api/scans`）；不带该参数时行为逐字节不变。
- **Scheduler→Run 闭环**：tick 与手动触发都只读解析 schedule task，并按同一 `validateTargetsInScope` 规则执行项目 scope 校验。空项目 scope（默认导入态）仍以 schedule 自身 targets 作为授权来源。**任何意外状态都是 skip 而非 bypass**：缺任务、缺项目或瞬时存储错误只会跳过本周期或返回 400/503，绝不把授权放宽成“schedule 说什么就是什么”；跳过周期仍推进 `LastRun`/`NextRun`（避免 30s tick 重试风暴）。`schedulesMu` 只覆盖到期快照与时间推进，校验/挂载/派发都在无锁快照上完成。

**任务目标（`internal/objective` + `platform.Objective`）**：

- `platform.Objective`（`{kind, statement, criteria[]}`）是挂在 Task 上的结构化目标。PG 中位于 1:1 扩展表 `task_objectives`；无 DSN 时持久化在任务快照内。**缺失 objective 表示“未结构化”，绝不推断**。
- `internal/objective` 是**纯评估器**：读取信号快照（已接受 findings + ledger hypotheses + plan 计数），返回每个判据的 `met / unmet / undetermined / manual_pending` 与仅用于展示的达成度。它从不写回、从不重标 finding、从不阻塞扫描。
- 运行时装配：objective 作为独立 prompt 段落（`## OBJECTIVE (operator-stated)`）、作为扫描领域 journal 中的 **human-sourced observed InfoItem**（`domainjournal.OpRecordObjective`）、以及 `agent.ObjectiveSpec` 注入引擎——引擎侧只**标记**服务于 finding 类判据的计划任务（`Task.ObjectiveRef`），不增删/重排任务，也不改覆盖分母，因而金样本调度一致性不受影响。
- `GET /api/tasks/{id}/objective` 诚实呈现目标状态：判据状态及其证据引用、最近一次 run 的循环位置**及其推导依据**、缺口与 VOI 加权可调度池；持久化事实不足以支撑位置时返回 `Unknown`。

### 4.12 HITL 审批与 guidance — `internal/approval`、`internal/web/approvals_gate.go`

- `internal/approval`：纯分类器——工具类别（`command`/`http`/无）、只读侦察白名单（“存疑即挂起”：含 `$()`、未加引号重定向、`system(`/`eval(`、内联 `PATH=`/`LD_PRELOAD=` 赋值或 nmap 输出 flag 的片段一律不进白名单）、`AssessHTTP`（写方法带管理路径/破坏性载荷/认证变更特征即挂起；GET/HEAD/OPTIONS 通过并在既有 tool_call 事件留审计痕迹，不产生审批行）。
- 生产扫描审批使用 Eino 的 interrupt/resume 包装（`engine.ApprovalTool`）位于工具端点之前；`tools.Registry.SetApprovalGate` 作为直连注册表消费者的纵深防御缝，在熔断/参数检查之后、`tool.Execute` 之前咨询。拒绝返回 `Result{Success:false}` 且不触碰 `RecordFailure`。**verifier 注册表豁免**（审批停顿会与 15 分钟看门狗竞争，且门的同步阻塞恰好保持验证时序不变量）。
- `web.ApprovalService` 持有运行时状态：per-run 待审槽位队列（默认 1，溢出 FIFO 等待）、超时即拒绝定时器（`XALGORIX_APPROVAL_TIMEOUT`，默认 600s）、scanctx 取消（stop/pause/delete 把 pending 翻为 `cancelled`）、启动对账、存储调用超时（3s → fail-open）。审批载荷在持久化、WS 发送与 API 响应**之前**经 Agent 密钥注册表 + header 名掩码做值脱敏。
- 存储：`platform.Repository` 的 approvals/guidance 段，双实现（`0003_approvals_guidance.sql` + `_platform/platform.json`）；列表/摘要响应中的 `pending_total` 恒为跨项目（D7 唯一例外，供 OpsPulse）。WS 事件只由 web 层发出。
- Guidance：`POST /api/tasks/{id}/guidance` 经 `Agent.SendMessage` 注入运行中 Agent（`status=injected`），否则持久化为 `queued`，在下一次会话启动时并入 instruction。
- `XALGORIX_APPROVAL_MODE` 默认 **off**（无头定时扫描不得卡在待审上）；`enforce` 开启管线。

### 4.13 流量代理 — `internal/trafficproxy`（R2b）

单进程共享 loopback MITM 代理，默认开启（`XALGORIX_TRAFFICPROXY=off` 逐字节恢复 R2b 前行为）。

- `internal/proxy` 仍是纯上游池（解析/轮换/共享 client）；`internal/trafficproxy` 持有 loopback 监听、CA（stdlib ECDSA P-256，持久化于 `XALGORIX_CA_DIR` 默认 `~/.xalgorix/ca`，0600/0700，损坏 CA 对拦截 fail-closed 且绝不重签）、MITM CONNECT 路径与观测汇。
- 消费者：`httpclient.buildClient` + `send_request` 经 `trafficproxy.LocalURL` 路由并信任 CA；浏览器指向 `LocalURL`；websearch provider 走同一 loopback 路由（系统级，无 run token）；终端环境与 curl/wget 包装（`--cacert`）以及 python 的 `REQUESTS_CA_BUNDLE`/`SSL_CERT_FILE` 携带 CA。关闭模式保留全部遗留路由。WebSocket/upgrade 请求在隧道内回落为原样透传（仅以 `upgrade_passthrough` 元数据被观测）。
- 归因：`web.executeScanSession` 铸造 per-session run token。代理在两处、在 hop-by-hop 剥离**之前**解析 `Proxy-Authorization`：Go 工具传输的 `Bearer`，以及 curl/wget/python 的 `Basic`。Basic 查不到注册项时保持未归因，而非冒用某个 Run。已归因流量用该扫描的 scopeguard 配置覆盖进程 dial-guard 基线。
- 子进程审批：Basic 归因请求未经过工具层门，代理可通过 `web.proxyHighRiskGate` / `trafficproxy.SetHighRiskGate` 复用同一 `ApprovalService` 槽位队列、超时拒绝、WS 事件与脱敏；Bearer 与未归因流量永不二次门控。
- 观测：请求/响应摘要经 `OpObserveTraffic` 成为所属 run 领域 journal 中的 observed InfoItem，由请求路径之外的有界单写汇写入（溢出丢弃并计数）。**header 值永不进入观测，只留名字**；敏感查询值掩码。无 DSN 时 journal 照写，只是不发生 PG 投影。
- 安全链：代理不接管 scope（第一环仍在代理之前）；操作者机器 dial guard（第三环）移入代理的直连上游拨号。

### 4.14 工具执行、安全文件与出网

- `tools.Registry.ExecuteContext` 是唯一执行路径（`Execute` 只是 `context.Background()` 包装）。`RegistryTool` 在有界 goroutine 中运行注册表调用，带 per-tool 硬上限 + 30s 宽限，超时强制返回，非合作工具无法阻塞 ADK 工具节点。终端/Python 的取消或超时返回**失败**的工具结果而不是 `Success=true`。
- `internal/safefile` 取代 fileedit 中“先检查路径再重开路径”的写法：Linux 上以 `openat(O_NOFOLLOW)` 逐段打开，写用同目录临时文件 + `renameat`，独占创建用 `linkat`，目录项经单独只读 fd fsync 且 fsync 失败会返回；非 Linux 返回 `ErrNotSupported` 而非静默回退到不安全的重新打开。
- `internal/egress` 按 `ScanContext.ID` 记录 per-scan 边界。默认 `proxy` 模式是**路由姿态而非已证明的 OS 边界**：`Status.Enforced` 保持 false。Linux `enforce` 模式要求 root + `nft`，安装独立 `inet xalgorix_egress_<pid>` 表（仅允许专用 UID 访问代理端口，其余 DROP），并在启动前把扫描工作区转交专用 UID/GID；终端/Python 经 `SysProcAttr.Credential` 降权；浏览器无法在 rod 下换 UID，故在 enforce 模式 fail-closed。
- `internal/platformutil`：进程组（`Setpgid` + 负 PID SIGKILL）、磁盘可用量、非阻塞文件锁、`O_NOFOLLOW` 读——Windows 构建目标靠它成立。

### 4.15 Providers / LLM / Auth

- `internal/llm`：模型**路由**（模型名 → provider 端点）、多 provider **KeyStore**、chat client。
- `internal/providers`：编译期内置的 provider 目录。
- `internal/auth`：凭据 profile + OAuth 驱动（PKCE、device-code、setup-token、CLI 复用）。

引擎与模型无关：LLM、API base 与 key 均在运行时配置，没有硬编码 provider。

### 4.16 扫描模式

| 模式 | 说明 |
|---|---|
| `single` | 单个 URL/host，完整漏洞测试 |
| `dast` | 针对特定应用的深度爬取：发现 → 参数挖掘 → 漏洞测试 |
| `wildcard` | 子域枚举（被动 + 主动）→ 逐子域扫描 |

多目标经扫描队列处理并支持恢复。

---

## 5. 数据流

```text
target + instruction
   → web：创建扫描实例（独立 goroutine + scanctx）
   → coordinator：侦察 → 分解假设 → 并行 specialist agents
   → 每个 agent：reason → tool call → observe（事件经 SSE 实时流出）
   → ledger 变更：domain_journal.jsonl append + fsync，然后 ledger.json
   → reporting：过闸门 + 已验证的 findings 持久化到扫描记录
   → storage：每个扫描目录原子写 scan.json
   → 可选冷路径：journal 观察者/reconciler → River → PostgreSQL
   → search_info：有界的区域/关联/语境读取（无判定角色）
   → report.go / internal/reporting：按需渲染品牌 PDF
   → notify.go：Discord/Telegram 生命周期与 finding 通知（完成摘要需显式开启）
```

---

## 6. 持久化、隔离与冷路径

- **扫描隔离**：一切 per-scan 状态（vulns/notes/terminal/browser/ledger/coverage/tokens/toolArchive）经 `scanctx.ScanContext` 以 contextID 路由到各工具包的 store。禁止包级全局的可变扫描状态；`agentsgraph.Graph` 必须挂在 scanctx 上。
- **原子写**：`internal/storage.WriteAtomic`（O_EXCL + fsync + rename + 目录 fsync），扫描权威文件统一走它，永不产生撕裂的 `scan.json`。
- **扫描记录**：每个扫描目录一个 `scan.json`（元数据、事件、findings）；完成记录不可变并缓存。
- **领域审计**：每个扫描写 `domain_journal.jsonl`（持久领域审计）与 `ledger.json`（当前 hypothesis/evidence 状态）。journal 不是状态权威，且不由尽力而为的遥测重建。
- **冷路径**：配置 PostgreSQL 时存储 InfoItem/边/审计投影与同步水位；`(scan_id, seq)` 使重放幂等，reconciler 在崩溃后补缺口。无 DSN 即纯文件模式。
- **密钥边界**：API 永不回传密钥，只回 `*_configured` 布尔；机器令牌走 SHA-256 摘要 + 常数时间比较。

---

## 7. 安全链（不可绕过）

改工具代码不得绕过任何一环：

1. `agent_guard.shouldBlockForOutOfScope` → `scopeguard.IsLocalOrListener`：**无条件**拦截指向操作者机器（loopback/本机网卡/仪表盘监听地址）的调用，含 IPv4 变体写法与 IDNA 混淆；
2. `RequestGuard`（`tools.GuardRedirects`）：重定向每一跳复查；
3. `DialGuard`（`tools.GuardDials`，挂在 `net.Dialer.Control`）：DNS 解析后的真实 IP 复查（防 DNS rebinding）；
4. `sandbox.CheckResolve` / `CheckRead`：所有文件系统读写的路径策略（读黑名单 `~/.ssh`、`/etc/shadow` 等优先于白名单）；
5. 代理 fail-closed：`ProxyRequired` 时工具不得回落直连；
6. 密钥环境变量经 `tools.SanitEnv` / `SecretEnvKey` 从 terminal/python 子进程剥离。

补充约束：Verifier 只使用所属 Agent 的 client 与受限工具面，所属 Agent 在验证期间保持阻塞，不得跨 Agent 串用、不得并发化同一 Agent 的验证；`report_vulnerability` 的门顺序（dedup → fabrication → method → proof → CVSS 复算 → verifier → severity cap）不可重排；事件流单向（工具 → per-scan channel → `processEvent` → SSE），工具代码不得直接 broadcast 或写 HTTP 响应。

---

## 8. 配置

读取自 `~/.xalgorix.env`（以及进程环境）：

| 变量 | 用途 |
|---|---|
| `XALGORIX_LLM` | provider-native 模型 ID |
| `XALGORIX_LLM_PROVIDER` | 仪表盘选择的 provider（目录路由） |
| `XALGORIX_API_KEY` / `XALGORIX_API_BASE` | LLM 凭据 / 自定义端点 |
| `XALGORIX_BIND` | 仪表盘监听地址，默认 `127.0.0.1`（端口默认 `9137`） |
| `XALGORIX_PASSWORD` / `XALGORIX_PASSWORD_HASH` | 仪表盘认证 |
| `XALGORIX_DATA_DIR` | 扫描记录存放目录 |
| `XALGORIX_DATABASE_URL` | 可选 PostgreSQL 冷路径；空 = 纯文件模式 |
| `XALGORIX_COLDPATH_RECONCILE_SECONDS` | journal→PG 缺口对账间隔，默认 30 |
| `XALGORIX_VOI_WEIGHT` | 确定性 VOI 排序权重，钳制到 `[0, 0.30]`，默认 0 |
| `XALGORIX_MAX_ITERATIONS` / `_MAX_DURATION` / `_MAX_TOKENS` / `_MAX_TOOL_CALLS` | per-scan 预算 |
| `XALGORIX_MAX_NON_PROGRESS` | 无有效评估证据的活跃秒数上限，默认 3600，0 关闭 |
| `XALGORIX_MAX_INSTANCES` | 显式并发扫描容量；未设置时由实时 RAM 余量推导 |
| `XALGORIX_SCAN_MEMORY_BUDGET_MB` | 每个新准入扫描的估算 RAM（未设置时自动缩放） |
| `XALGORIX_RATE_LIMIT_REQUESTS` / `_RATE_LIMIT_WINDOW` / `_RATE_RPS` / `_RATE_BURST` | 请求节流 |
| `XALGORIX_PROXY_URL` / `_PROXY_FILE` / `_PROXY_ROTATION` | 上游代理 |
| `XALGORIX_OOB_*` / `XALGORIX_INTERACTSH_*` | 带外回调配置 |
| `XALGORIX_DISCORD_WEBHOOK` / `_DISCORD_MIN_SEVERITY` | Discord 通知 |
| `XALGORIX_TELEGRAM_BOT_TOKEN` / `_CHAT_ID` / `_MIN_SEVERITY` | Telegram 通知 |
| `XALGORIX_NOTIFY_SCAN_COMPLETE` | 选择接收扫描结束摘要（默认 `false`；逐漏洞告警不受影响） |
| `XALGORIX_TRAFFICPROXY` | 共享 loopback MITM 代理开关，默认开启 |
| `XALGORIX_CA_DIR` | MITM CA 目录，默认 `~/.xalgorix/ca` |
| `XALGORIX_APPROVAL_MODE` | `off`（默认）/ `enforce` |
| `XALGORIX_APPROVAL_TIMEOUT` | 审批超时秒数，默认 600（超时即拒绝） |
| `XALGORIX_APPROVAL_ALLOWLIST` | 操作者额外白名单 |
| `XALGORIX_API_TOKEN` / `XALGORIX_API_TOKENS` | 机器令牌（仅 `/api/*` 与事件流；SHA-256 摘要 + 常数时间比较） |

**准入语义**：设置 `XALGORIX_MAX_INSTANCES` 时按该总量准入，不再用自适应 RAM 估算削减；未设置时把 RAM 推导的槽位数视为剩余余量，再加上已在运行的实例得到总容量；最近 30 秒内准入的扫描会预留一个余量槽位，避免并发等待者复用同一快照。磁盘临界满仍会阻止新扫描。

---

## 9. 构建与运行

**环境要求**：Go 1.26+、Node.js（构建前端）。

```bash
git clone https://github.com/c1ayooo/xalgorix.git
cd xalgorix
make build                 # npm ci + 构建 webui → internal/web/static，再 go build
sudo install -m 755 build/xalgorix /usr/local/bin/xalgorix
xalgorix --setup           # 交互式配置 provider / 模型 / 密钥
xalgorix --web             # 启动仪表盘：http://127.0.0.1:9137
```

> 模块路径仍为 `github.com/xalgord/xalgorix/v4`（沿用上游 module 声明），因此 `go install` 请按该路径或直接克隆本仓 `make build`；不要使用与本仓 remote 不一致的 `go install` 路径。

**质量门**：

```bash
cd webui && npm run typecheck   # 前端唯一门（tsc -b）
make lint                       # golangci-lint（版本钉在 Makefile）
go test ./... -race             # 后端
make webui-dev                  # 前端独立开发（Vite + mock-backend，8787 端口）
```

**注意**：`make build` 通过 `//go:embed` 内嵌前端产物——只改 Go 不重跑 `make webui` 会嵌入旧 UI；改了前端必须重建。版本号经 `-ldflags "-X main.version=..."` 注入。

**Docker**：镜像预装大量攻击性工具，且保留 apt/go/cargo/pipx/npm 以便运行期自动安装缺失工具。容器内以 root 运行、`--privileged` 是既定运行姿态（Docker 默认 seccomp/能力集会破坏 iptables、ARP/MITM、tun/tap、ptrace、masscan 调优等），不是需要“修复”的问题。永远不要把仪表盘无认证地暴露到公网。

---

## 10. 目录结构

```text
cmd/xalgorix/            二进制入口（web 模式 / scan 模式 / OS 服务）
internal/
  agent/                 自主循环、prompt 装配、守卫、hook、Verifier、授权矩阵
  engine/                唯一扫描运行时（Eino ADK）+ 工具包装 + 审批中断 + checkpoint
  tools/                 工具注册表与全部能力
    agentsgraph/         子 Agent 图（scan-scoped）
    reporting/           report_vulnerability 报告门
    terminal/ browser/ httpclient/ python/ proxy/ oob/ mcp/ ...
  methodology/           22 阶段唯一规范定义
  scanctx/               per-scan 上下文、ledger
  domainjournal/         领域审计 journal（seq/fsync/缺口投影）
  infoitem/              InfoItem 模型 + 三类激活 Reader
    postgres/            InfoItem/边/审计 PG 副本与迁移
  coldpath/              PG + River + 观察者 + reconciler 组装
  jobs/                  钉住版本的 River client/workers
  platform/              Project/Task/Run 存储（PG + 文件双实现）
  objective/             目标判据纯评估器
  approval/              只读侦察/HTTP 风险纯分类器
  trafficproxy/          loopback MITM 代理、CA、观测汇
  proxy/                 上游代理池
  reporting/             findings 存储 + PDF 渲染
  storage/               原子写（WriteAtomic / AppendSync）
  sandbox/ scopeguard/ safefile/ egress/   安全与出网
  platformutil/          进程组/磁盘/文件锁等平台差异
  llm/ providers/ auth/  模型路由、KeyStore、provider 目录、OAuth
  web/                   本地 API + SSE + 仪表盘托管 + 编排 + 持久化
  tui/ config/ resources/ ratelimit/ attacksurface/ oob/ har/ ...
webui/                   React 仪表盘（编译进 internal/web/static）
docs/                    CORE_PHILOSOPHY / ARCHITECTURE / REDESIGN / API_CONTRACT ...
```

---

## 11. 落地顺序与验收里程碑

每一步独立可回滚；前一步验收不过，不进入下一步。工期是排期参考，不是验收标准。

| 步骤 | 内容 | 必须通过的验收 |
|---|---|---|
| **P0** | 稳定现有基线 | 修复全局停止状态污染、工具 Error/Success 与熔断语义、ledger 快照写序、agentsgraph 持久化失败回滚、审计丢失风险及 Verifier 实际 deadline；现有安全与报告回归全过 |
| **M0** | Evidence 认识类型 | 保留 `kind`；新增 `nature`、可选 `supports`/`info_item_id`；缺省按 inferred；inferred 支持关系可校验；`finding_ref` 与既有证明门测试通过 |
| **M1** | Hypothesis 前置条件 | 缺少 preconditions 时 `record_hypothesis` 返回可纠正提示；历史/未知值不伪装成已满足；Obligation 与收工门回归通过 |
| **M2** | 完整审计记录与只读投影 | 自动与模型认领均有持久化记录；重复/乱序/缺口可识别；可从记录还原认领历史；投影不参与判定，缺口显示为 incomplete |
| **M3** | InfoItem 冷路径落 PG | 无 DSN 全核心测试通过；幂等重投通过；杀死进程模拟“JSON 已写未入队”后 reconciler 能追平；`pending/applied/gap/failed` 可查询 |
| **M4** | 激活入口接真实消费者 | 区域、关联、语境分别至少一个消费者有契约测试；关联跳数/预算有上限；结果携带来源与截断标志；无 PG 时热路径不依赖跨扫描查询 |
| **M5** | 确定性 VOI 加权 | 不可变快照金样本排序稳定；`w=0` 完全保持旧排序；边界值与缺失值有测试；ClaimNext 并发认领不重复 |

**M3 参考性能基准**：本地单实例 PostgreSQL、100,000 条 InfoItem、300,000 条关系边、16 并发只读查询、单次最多返回 100 条、只计 SQL 与结果装配（不含 LLM 坐标补全），预热后按主体+时间过滤的 p95 ≤ 200ms；同时记录 p50/p95/p99、执行计划与截断结果。环境变化时重建基线，不做跨环境绝对数值比较。

**变更隔离**：P0 的正确性修复与 M0–M5 的架构变更分开提交、分开回归；不得借信息模型改造顺带重排报告门、扩大 Verifier 工具面或改变扫描生命周期。

---

## 12. 本期明确不做

- Neo4j / MongoDB / Redis 等第二数据库；
- 全局搜索树、MCTS rollout、POMDP belief 求解；
- 用 InfoItem 置信度推翻 Verifier 或直接生成 finding；
- 把 Hypothesis 当 Intent、把引擎义务当 Hint；
- 复用 `Evidence.Kind` 表达 observed/inferred；
- 跨存储分布式事务；
- 用 River 接管扫描生命周期或 HITL；
- 任何让“没有 PG 就跑不起来”的改动；
- 以“点亮”为名禁止 ledger/surface/reporting 的确定性直查；
- 把可丢弃的实时遥测当成完整审计记录；
- 在 M0–M5 中同时实施项目/企业/资产/审批平台；
- 在没有具体相似度消费者前强制引入 pgvector；
- 借架构改造并发化同一 Agent 的 Verifier 或重排报告闸门。

---

## 13. 术语表

- **InfoItem**：信息模型的全局实例，七维坐标、唯一 ID、observed/inferred 类型及来源关系。
- **Observation**：原始请求、响应、工具输出或人工原始输入，不等于对它的解释。
- **Evidence**：信息在某 hypothesis 下的投影视角；`kind` 表示用途，`nature` 表示 observed/inferred。
- **Belief / Hypothesis**：关于世界的待证主张，带前置条件、baseline、置信度与六态生命周期。
- **Operation**：PlanTask 或 tool call 等作用于状态的动作。
- **Obligation**：引擎对 Agent 的硬约束，来自阶段义务、覆盖收敛、收工闸门与 hook 拦截。
- **Verdict**：Verifier 三态或引擎确定性验证结果；finding 标签只能由报告管线给出。
- **区域 / 关联 / 语境点亮**：三类有明确消费者、预算与来源信息的确定性检索入口。
- **领域审计记录**：状态变更边界产生的持久化记录，不使用可静默丢弃的实时遥测语义。
- **审计投影**：由领域审计记录还原的只读历史视图，不参与判定，必须能显示缺口。
- **source seq / watermark**：每扫描单调序号与 PG 已应用的连续位置，用于幂等与崩溃追赶。
- **reconciler**：比较文件源与 PG 水位并补投缺口的恢复机制，与 River 正常异步处理互补。
- **VOI**：信息决策价值，仅作为受限、确定性的排序加权项，不改变验证权威或 Obligation。

---

## 14. 授权与合规

本仓库本身是已授权渗透测试工具，Apache-2.0 许可。使用时仍须遵守：

- 扫描目标只能来自你显式提供的 target/instruction；
- 不得引导或允许工具指向 `scopeguard` 判定为操作者机器的地址；
- 只对你拥有或已获得明确书面授权的系统使用；
- 永远不要把仪表盘无认证地暴露到公网。

---

## 15. 来源与致谢

本仓库是 **[c1ayooo/xalgorix](https://github.com/c1ayooo/xalgorix)**，在上游开源项目 **Xalgorix** 的基础上继续开发。

- **原作者**：xalgord（[github.com/xalgord](https://github.com/xalgord)）
- **上游仓库**：<https://github.com/xalgord/xalgorix>（镜像 / 组织地址：<https://github.com/xalgorix/xalgorix>）
- **许可**：Apache License 2.0（见 [`LICENSE`](LICENSE)）
- **模块路径**：`github.com/xalgord/xalgorix/v4`（沿用上游声明，未随 fork 更改）

上游项目完成了核心引擎、22 阶段方法论、ledger、Verifier 与报告门、仪表盘等绝大部分工作；本仓在其之上进行二次开发与架构演进（`docs/CORE_PHILOSOPHY.md` v2.1 信息驱动循环、domain journal/冷路径、平台层与 HITL 等）。所有原始版权与许可声明保留不变。

如果你要引用、转载或二次分发本项目，请同时保留上游项目的署名与 Apache-2.0 许可声明，这是对原作者最基本的致意。

---

<p align="center"><sub>Built on the shoulders of <a href="https://github.com/xalgord/xalgorix">Xalgorix</a> by xalgord. 感谢原作者的开源贡献。</sub></p>

