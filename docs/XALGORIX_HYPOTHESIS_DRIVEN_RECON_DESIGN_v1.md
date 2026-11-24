# Xalgorix 假设驱动探索与渐进式侦察改造设计

**版本：** v1.0  
**日期：** 2026-11-24  
**适用对象：** 基于 `xalgorix/xalgorix` 上游项目进行二次开发的代码库  
**设计性质：** 架构改造提案；不代表当前代码已经实现  
**核心目标：** 保留 Xalgorix 原生 LLM 自主推理与假设验证能力，避免系统退化为“固定阶段 + 大范围扫描 + 工具执行”的 AI Scanner。

---

## 0. 执行摘要

本设计解决的不是“怎样让扫描器扫得更慢”，而是一个更根本的问题：**为什么一个以 LLM 自主推理为核心的渗透 Agent，会在任务开始时倾向于直接执行目录枚举、全端口探测等大范围发现动作，而不是先理解目标、利用已有信息形成假设，再选择有依据的验证行动？**

上游项目的架构文档将 Agent Core 描述为 `reason → call tool → observe → repeat`，并将 22 阶段方法论描述为指导 Agent 的知识与提示。但当前上游 `internal/agent/planner.go` 同时存在引擎预先构建计划的路径：它会加入 Recon 任务，并在完整方法论包含 Phase 3 时加入 Directory & File Discovery；当已有 endpoint 时，任务说明虽要求有界、一次性的非 API 路径发现，但任务本身仍可能在扫描早期被自动创建。因此，“Agent Core 是自主循环”与“系统启动时自动派发侦察计划”可以同时存在，两者之间的张力正是本次要处理的对象。

用户反馈的“启动就扫目录、扫全端口、很容易被封禁”必须拆成两类证据：

1. **目录发现自动进入计划：** 上游 `planner.go` 的公开代码检索结果可以直接证明该行为在特定 phase / route-discovery 条件下会发生。
2. **全端口探测：** 当前已提供的上游 planner 片段不足以证明它必然由 `planner.go` 无条件创建。需要在用户实际 fork 的运行日志中追踪具体工具调用、参数、调用来源和启用配置，确认它来自系统提示、Recon skill、LLM 自主工具调用、自动计划，还是扫描模式/深度配置。

本设计的核心裁决如下：

- **保留 LLM 的自由推理权。** 不要求每一次思考都写成 Hypothesis，也不把 LLM 限制为从漏洞类别清单中选题。
- **保留 Hypothesis 作为正式、可追溯的认知对象。** 只有形成可检验主张、明确依据/前置条件和可能的反证条件时，才进入 Hypothesis Ledger。
- **方法论是知识地图和覆盖观察面，不是自动执行清单。** 22 个 phase 不应等价于 22 组必须启动的扫描任务。
- **Recon 是渐进式信息获取，不是固定扫描套餐。** 优先使用用户提供的信息和低影响观察；主动发现必须由明确的信息缺口、目标适用性和授权预算共同支撑。
- **Coverage 是提醒，不是命令。** 未覆盖不等于必须立刻扫描；覆盖缺口应交给 Agent 解释其价值，再形成假设或有依据的 Intent。
- **速率控制必须覆盖所有执行面。** HTTP、Browser、Terminal 中的网络工具和子 Agent 共享同一 Engagement Policy 与预算；不能只限制一个 HTTP client。
- **尊重目标方限速与停止信号。** 遇到 429、Retry-After、明确拒绝、账户锁定、测试窗口结束等情况应退避、暂停或停止；不得通过代理轮换、身份轮换或其他方式规避目标方封锁。
- **Benchmark 是验证手段，不是主目标。** Benchmark Adapter 可以提供目标清单、阶段约束和结果对账，但不得改变通用渗透 Agent 的认知与行动原则。

本设计不推翻现有 CORE v3.x、PG 权威、双图、InfoItem、Evidence、Intent 契约、Verifier、报告门或 HITL。重点是调整这些机制在决策链中的角色与优先级。

## 1. 上游基线与本地二开基线

### 1.1 上游 Xalgorix 的公开基线

参考：

- `https://github.com/xalgorix/xalgorix/blob/main/docs/ARCHITECTURE.md`
- `https://github.com/xalgorix/xalgorix/blob/main/internal/agent/planner.go`
- `https://github.com/xalgorix/xalgorix/blob/main/README.md`

上游架构文档描述的 Agent Core 是一个 LLM 循环：构造包含 22 阶段方法论的上下文，将对话发送给模型，执行模型请求的工具调用，将工具结果反馈给模型，直至目标完成或预算耗尽。上游架构还描述了共享 Hypothesis/Evidence Ledger、协调器和 hypothesis-driven specialists；这些内容说明原项目并非传统签名扫描器。

但上游 Planner 同时有结构化计划构建逻辑。公开的 `planner.go` 检索结果可见：

- Recon 作为技术前置任务加入计划；
- 完整方法论或 Phase 3 选择可能使 Directory & File Discovery 任务进入计划；
- 在已知端点的情况下，任务说明要求进行有界的非 API 路径发现，而不是对已知 API 面做广泛爬取；
- Planner 将任务作为依赖跟踪的 Task graph 提供给 Agent。

因此，不能只根据架构图中的 `reason → tool → observe` 推断启动阶段一定由 LLM 完全自主决定。必须同时检查 Planner 的自动任务构建、Prompt、Skill、Hook、Scan Mode 和运行参数。

### 1.2 用户本地 fork 的已知基线

根据用户提供的文档，以下内容属于本地二开设计/实现基线，不应误称为上游原生实现：

- `CORE_PHILOSOPHY.md` v3.x：State / Operation / Obligation、七环循环、PG 持久态、双图、InfoItem、Evidence、Hypothesis、Intent 权威、HITL 和安全不变量。
- `INFO_AS_GRAPH_SPEC.md`：探索图作为信息聚合层，InfoItem 作为原子信息层，统一 `info_space_items` / `info_space_edges` 读视图；IG0–IG7 已落地，IG6b–IG6d 等条目状态以文件最新记录为准。
- `INFO_GRAPH_CONTRACT.md`：Intent claim/preconditions/lease/ConcludeIntent、证据层级、上下文分层、运行期合规和审计。
- `CORE_LOOP_AUDIT.md` / `CORE_LOOP_OPTIMIZATION.md`：记录过 Hypothesis 积累但未被消费、CTF 单代理分支关闭委派消费、VOI 默认未激活等运行问题；后续已增加 `hookBeliefPoolBrief`、CTF 委派开关等整改。
- `BENCHMARK_OPTIMIZATION.md` / `BENCHMARK_ROUND5_ANALYSIS.md`：记录过 lane 混用、容器槽位空耗、错误提交、Worker 取消与多阶段任务问题，以及之后已完成/未完成的整改。
- 最早的“信息核心建模改造实施文档”：提出 Information Item 七维坐标、区域点亮、关联点亮、语境切片，以及 Information → Fact → Intent 的闭环。

**重要边界：** 上游当前 `main` 分支文档与用户本地 fork 不是同一个代码基线。代码 Agent 必须以本地 checkout 的实际源码、提交版本和测试为准；本文中的上游说明用于定位设计来源，不可代替本地代码审计。

## 2. 问题定义：从 Scanner-first 转回 Hypothesis-driven

### 2.1 Scanner-first 的典型行为

```text
目标 / instruction
      ↓
自动 Recon
      ↓
目录枚举 / 端口探测 / 技术指纹
      ↓
按 endpoint × vulnerability class 展开任务
      ↓
大量工具调用
      ↓
结果进入 notes / InfoItem
      ↓
报告候选
```

这种模式的问题不在于工具本身，而在于**工具调用先于对目标的具体理解**：

- 没有先利用用户提供的架构、域名、API 文档、账号、角色和历史情报；
- 没有判断当前发现动作是否能改变下一步决策；
- 对“未知”采用“扩大扫描范围”作为默认响应；
- 目录发现与端口探测可能成为启动阶段的固定工作，而不是由证据缺口触发；
- 对工具产生的大量结果，未必形成明确的 Hypothesis、反证条件和后续 Intent；
- 大量请求会增加被限速、封禁、触发 WAF/IDS、账户锁定或造成目标负担的概率。

### 2.2 Hypothesis-driven 的目标行为

```text
Engagement Goal + Authorization / Rules of Engagement
                    ↓
         现有信息与上下文装配
                    ↓
       LLM 自主理解目标并提出解释
                    ↓
     （有必要时）形成可检验 Hypothesis
                    ↓
       检索支持、反驳与历史信息
                    ↓
        比较多种可能解释与缺口
                    ↓
    选择最有信息价值且符合授权预算的行动
                    ↓
        通过 Intent Contract 执行
                    ↓
       观察结果 / 新 Evidence / 新 InfoItem
                    ↓
    支持 / 反驳 / 修正 / 暂缓 Hypothesis
                    ↓
             下一轮自主推理
```

这并不是“每一步都必须先 record_hypothesis”。探索初期可能只有 Goal 和少量观察，Agent 可以先进行必要的低影响信息获取；当它形成了可以被证伪的主张后，再将其正式记录。核心区别是：主动探测的强度和范围不能仅由“还没有扫过”决定，而应由目标理解、证据缺口、假设价值与授权规则共同决定。

## 3. 设计目标与非目标

### 3.1 设计目标

1. 恢复 LLM 的主动假设生成、替代解释、反证思考和路径调整能力。
2. 让 Hypothesis 成为可被消费的认知状态，而不是为了满足流程而批量生成的条目。
3. 让 Information Space 为推理提供上下文、历史、冲突与来源，而不是仅提供检索列表。
4. 把 22 阶段方法论从强制扫描队列重新定位为知识和覆盖模型。
5. 使 Recon 从宽范围默认扫描转为逐级、按证据触发的信息获取。
6. 对主动请求建立 Engagement 级统一预算、速率、并发和停止规则。
7. 让每个重要 Intent 能回答：它验证什么、依赖什么、预期得到什么信息、如何判断继续/停止。
8. 保留复杂任务和多 Agent 协作能力，但委派必须由可区分的假设/专业工作产生。
9. 通过运行遥测验证信息是否改变了推理和行动，而不仅是统计查询次数。

### 3.2 非目标

- 不把 Xalgorix 改成固定漏洞扫描器或确定性攻击脚本引擎。
- 不废除 22 阶段方法论、Coverage、Verifier 或 Finding 报告门。
- 不要求每一次思考、每个工具调用都创建 Hypothesis。
- 不要求每轮机械执行 Region → Association → ContextSlice 三类点亮。
- 不新增第二套 Goal / Intent / Belief 权威。
- 不通过代理轮换、IP 轮换、伪装身份或规避封锁来提高扫描持续性。
- 不将 Benchmark 分数、Flag 或平台提交状态提升为通用安全结论。
- 本期不引入 A*、MCTS、POMDP、pgvector 或新的数据库。

## 4. 核心架构裁决

### 4.1 保留现有对象，不再增加顶层原语

| 现有对象 | 本设计中的职责 | 必须避免的退化 |
|---|---|---|
| Goal | 操作者定义的授权任务与完成判据 | 不等于扫描列表 |
| Hint / Obligation | 授权约束、工作规则、收敛/收工义务 | 不代替 Agent 思考 |
| InfoItem | 观察、原始输出或有出处的推断实例 | 不直接决定漏洞成立 |
| Belief / Hypothesis | 对目标行为/弱点/攻击路径的可检验主张 | 不批量充当 endpoint×class 清单 |
| Evidence | 某条信息对某个 Hypothesis 的支持、反驳或控制作用 | 不与 InfoItem 混为一份真相 |
| Intent | 为检验假设、减少关键不确定性或满足明确前置条件而执行的行动 | 不默认等价于一个 phase 扫描任务 |
| Fact / Finding | 经既有权威路径确认的事实/安全发现 | 不由模型自述或信息置信度生成 |
| Verdict | Verifier 或引擎确定性验证结论 | 不由 Planner 的完成状态替代 |

### 4.2 自主推理与确定性控制的边界

**LLM 负责：**

- 解释观察结果；
- 生成一个或多个竞争性假设；
- 提出替代解释和可能的反证；
- 判断需要哪类信息来区分假设；
- 选择探索方向并说明理由；
- 根据新结果修正原有理解；
- 判断是否值得继续深入某个分支。

**引擎负责：**

- 授权范围、目标身份、测试窗口与安全策略；
- Intent 合法性、前置条件、owner、lease 和原子结论；
- 请求速率、并发、预算、停机与停止信号；
- Hypothesis 状态迁移、Evidence 追加和去重；
- Verifier、报告门、Finding 权威；
- 可审计性、隔离和持久化一致性。

**Planner 不再负责：**

- 把完整 phase 集合机械展开成大量扫描任务；
- 在缺少目标证据时自动创建大范围目录/端口发现任务；
- 用 coverage 缺口替代对目标的推理；
- 将 `queued` Hypothesis 数量当成探索进度。

Planner 仍可把 LLM 提出的方向整理成有依赖关系的 Intent 图，并执行确定性验证；但规划内容必须尽量有明确决策依据。

### 4.3 方法论的重新定位

22 阶段保留为**方法知识库、适用性目录和覆盖观察视图**，而不是默认的强制执行队列。

对每个 Phase / vulnerability class，系统只需维护或推导以下适用状态：

- `unknown`：尚未有足够信息判断是否适用；
- `applicable`：有上下文或证据表明值得考虑；
- `hypothesized`：已形成对应可检验假设；
- `planned`：存在有依据的 Intent；
- `testing`：正在执行；
- `assessed`：已完成有证据的评估；
- `not_applicable`：有依据地判定不适用；
- `deferred`：暂缓，记录原因；
- `blocked`：授权、资源、环境或目标方限制导致不能继续。

这些是覆盖/适用性读视图的状态建议，不要求新增一个平行权威状态机。能从现有 Goal、Belief、Intent、Evidence、coverage 和审计数据推导的，应优先推导。

**关键规则：** `unknown` 不自动意味着 `applicable`；`applicable` 不自动意味着必须执行扫描；`not tested` 不自动意味着应当扩大请求范围。

## 5. 渐进式 Recon 设计

### 5.1 Recon 的新定义

Recon 的任务是逐步建立可供推理的目标模型，而不是在启动时最大化收集所有可能端口、路径和指纹。

建议拆为四个决策层次：

1. **已有信息优先：** 目标 URL、用户给定域名/IP、API Spec、源代码、架构说明、登录账号、角色、历史扫描、已有资产图、操作者备注。
2. **低影响观察：** 从已授权入口及明确允许的数据源提取基本服务信息、页面/接口引用、公开文档、robots/sitemap（如适用）、应用返回的链接和已知入口，不以宽范围扫描作为默认。
3. **假设驱动的定向发现：** 只有在信息缺口会改变某个重要判断或行动时，才提出有界的发现 Intent；每个 Intent 需写明目标、证据依据、限制范围、请求预算和完成判据。
4. **深度主动测试：** 只有在 Rules of Engagement 明确允许、目标/资产范围明确、测试窗口允许且存在合理依据时，才进入更高影响的主动探测。需要操作者确认的动作仍走现有 HITL。

### 5.2 启动阶段必须先建立 Engagement Policy

在第一次主动网络探测前，确认并持久化本次 Engagement 的授权和测试规则。优先复用项目/任务/Hint/Objective/Scope 现有结构，不另建一套权限系统。

至少明确：

- 授权目标：域名、IP、应用、环境、租户、允许的子域范围；
- 排除项：第三方托管、共享基础设施、生产敏感区域、DoS/压力测试等不允许项目；
- 允许的测试类型和工具类别；
- 允许的主动探测强度、请求速率、并发与总请求/时间预算；
- 允许的测试窗口及时区；
- 测试账号、角色、匿名身份及使用约束；
- 目标方给出的限速、维护窗口和停止要求；
- 遇到 429、Retry-After、明确拒绝、账户锁定、服务异常时的处理规则；
- 是否允许端口发现、目录发现、主动爬取以及其他可能显著增加请求量的动作。

如果关键信息缺失：系统可以继续做不越界的本地分析和已有资料整理，但不得自行推定“完整扫描”已获授权。尤其不能因为默认 full methodology 就推定全端口扫描、广泛路径字典探测或高并发探测已获授权。

### 5.3 目录发现的触发条件

目录/内容发现不应仅因为默认启用了 Phase 3 而自动成为启动任务。

应满足以下条件之一：

- 操作者明确要求执行目录/内容发现，并且 RoE 允许；
- 已有观察指出存在具体的路由/内容缺口，例如发现应用有管理功能但路由清单不完整；
- 某个 Hypothesis 的验证确实依赖发现特定类别的资源，并能说明该信息如何影响后续决策。

不满足条件时：

- 只把 Phase 3 标为 `unknown` / `deferred` / `not yet justified`；
- 不自动创建 `dirbust` Intent；
- Planner 可以向 LLM 展示“当前已知路由和仍未知的部分”，由 LLM 判断是否需要提出发现假设；
- 即使获准执行，也必须有预算、停止条件和结果审查，而不是无界枚举。

如果已经有完整 API inventory，不能因为 Phase 3 存在就对 API 路径再做通用目录扫描；需要把 API 路由与非 API 内容发现区分开。

### 5.4 端口发现的触发条件

端口发现应作为独立、明确受授权控制的能力，而不是隐含在“Recon”中的默认副作用。

触发前至少确认：

- 目标是明确授权的主机/IP，而不是仅有一个 Web URL；
- 授权规则明确包含对应主机和端口范围；
- 扫描时间、速率、并发和工具参数符合 RoE；
- 该信息对当前任务有实际价值，而不是为了完成“扫过”指标；
- 若目标方或操作者要求停止/限速，执行面能够立刻遵守。

对于只给定 HTTPS 应用入口的 Web/API Engagement，不应自动把它解释为对同一 IP 全端口枚举的授权。需要端口信息时，优先使用操作者已有清单、配置资料或已授权的服务信息；主动端口发现由明确策略开启。

本设计不规定任何绕过限速或封禁的方法。代理轮换、来源 IP 轮换、身份切换和分布式并发不得被用来规避目标方的限制。

## 6. Hypothesis：从批量假设种子转向可检验主张

### 6.1 当前风险

本地 CORE 文档将 Hypothesis 定义为 Belief 的权威对象，具有 preconditions、baseline、confidence、VOI、六态生命周期和 Evidence。早期审计还记录了 `hookLedgerSeed` 按候选漏洞类别产生假设、随后大量假设 queued 无人认领的情况。

这种做法有利于避免完全没有结构化信念，但如果对每个 endpoint × vulnerability class 系统性生成 Hypothesis，Ledger 就容易变成另一种扫描矩阵：条目数量很多，却没有体现 Agent 对特定目标的理解。

### 6.2 新规则：Hypothesis 必须表达可区分的判断

正式 Hypothesis 应包含：

- **Claim：** 关于目标行为、信任边界、访问控制、数据流或安全属性的具体主张；
- **Subject：** 目标资产/应用/接口/身份/业务对象，引用稳定资产身份；
- **Evidence refs：** 已知支持信息与反对信息；早期假设可为空，但要明确标记为低证据基础；
- **Preconditions：** 已满足、未知、缺失或不适用，不得把未知伪装成满足；
- **Baseline / Control：** 如何区分正常行为与异常行为；
- **Falsification condition：** 什么观察会反驳该假设；
- **Next discriminating observation：** 最能区分当前假设与替代解释的下一条信息；
- **Confidence：** 对该主张成立程度的判断，不等于 InfoItem 坐标置信度，也不等于 Finding 标签；
- **VOI：** 该假设若被验证/反驳，预期如何改变后续决策；缺失时按 CORE 既定规则处理。

### 6.3 区分三种“候选”

避免把所有未覆盖项都叫 Hypothesis：

1. **Coverage opportunity：** 方法论/覆盖视图指出某区域尚未评估。这不是 Belief，也不能自动派发扫描。
2. **Reasoning candidate：** LLM 根据目标信息提出的待讨论方向，可以留在当前推理上下文，不必立即持久化。
3. **Formal Hypothesis：** 具备可检验主张、目标、前置条件和判伪方向，进入现有 Belief/Hypothesis Ledger。

Coverage opportunity 可以提示 LLM 思考；Reasoning candidate 由 LLM 自行比较；只有 Formal Hypothesis 才进入 claim 生命周期。

### 6.4 假设来源与质量观测

沿用现有 Hypothesis 对象，在允许的 payload / 现有字段内记录 provenance：

- `agent_generated`：LLM 根据观察主动提出；
- `evidence_derived`：由特定 Observation/Evidence 触发；
- `coverage_prompted`：由 Coverage opportunity 提醒后，经 LLM 实质推理形成；
- `human_guided`：操作者指导形成；
- `engine_seeded`：旧机制自动种子，仅作为待审查候选，不应不经模型判断就变成可执行工作。

新增 provenance 前先检查现有 schema 是否已有可复用字段；不要为来源标签创建新的权威表。

应记录：Hypothesis 被提出的比例、获得 claim 的比例、完成一次有效验证的比例、被支持/反驳/阻塞/耗尽的比例，以及它产生的新发现和新信息。不能把“Hypothesis 数量多”作为成功指标。

## 7. 信息空间如何服务自主推理

### 7.1 保留现有“信息即图”模型

保留：

- Exploration Graph 聚合层；
- InfoItem 原子信息层；
- Evidence 作为信息在某一 Hypothesis 下的角色投影；
- `info_space_items` / `info_space_edges` 统一只读视图；
- `run_id` 归一化；
- Region / Association / ContextSlice；
- L0 / L1 / L2 上下文分层；
- inferred 必须有 supports；
- InfoItem 和图节点都不越权决定 Finding 或 Verdict。

### 7.2 点亮不应变成每轮固定仪式

不同推理问题选择不同的信息入口：

- **Region：** 当前目标/主体有哪些相关 Observation、Hypothesis、Fact、Intent；
- **Association：** 当前信息与哪些支持、反驳、同会话或同主体信息关联；
- **ContextSlice：** 对某资产、版本、身份或业务流程进行完整上下文理解；
- **read_info_item：** 在模型确实需要原始细节时按 ID 显式读取。

禁止机械地让每一轮都执行三种查询。查询预算应由当前推理问题决定，结果需保留来源、nature、时间、截断和 incomplete 标记。

### 7.3 Information-to-Decision Trace

每当信息被用于关键判断时，记录可追踪关系：

```text
InfoItem(s)
   ├── supports ───────→ Hypothesis H1
   ├── contradicts ────→ Hypothesis H2
   └── contextualizes ─→ Asset / Context

Hypothesis H1
   └── motivates ──────→ Intent I1

Intent I1
   └── yields ─────────→ Observation / Fact / Finding reference

Outcome
   └── updates ────────→ Hypothesis H1
```

关系名称应优先映射到现有支持/矛盾/工作关系；只有确实无法表达时才新增关系类型。不要让审计事件成为状态权威。

最重要的运行指标不是“search_info 调用了多少次”，而是：

- 被读取的 InfoItem 有多少引用到 Hypothesis；
- 哪些 Hypothesis 因新信息改变状态；
- 哪些 Intent 因支持/反驳证据而新增、改写、阻塞或取消；
- 哪些重复测试因历史信息而避免；
- 哪些信息缺口最终改变了行动选择。

## 8. Intent 与 Planner 的改造

### 8.1 Intent 的决策依据

每个主动测试 Intent 应尽量关联至少一个：

- 正式 Hypothesis；
- 结构化 Goal criterion；
- 已有 Observation 导出的明确信息缺口；
- 操作者明确要求的任务。

Intent 应说明：

- `decision_basis_refs`：依据的 Goal / Hypothesis / InfoItem / Evidence ID；
- `question`：本次执行希望回答的问题；
- `expected_observation`：期望取得什么信息，而不是预设结论；
- `falsification_or_stop_condition`：哪些结果会停止该方向；
- `scope_ref`：具体授权范围；
- `budget_ref`：适用的速率、并发和时间预算；
- `timebox`：有界执行时间。

这些内容优先使用现有 Intent contract/payload，不要直接扩展为另一套 Intent 生命周期。

对低影响的必要信息获取，可以由 Goal/Objective 和 Engagement Policy 提供依据，不要求硬造一个 Hypothesis。

### 8.2 Planner 从“扫描任务生成器”转为“假设验证计划组织者”

Planner 的工作：

1. 接收 LLM 的目标理解、候选假设和计划提案；
2. 检查已有 Goal、InfoItem、Belief、Fact、Intent 和覆盖状态；
3. 去除重复方向，识别缺失前置条件；
4. 将值得验证的假设整理成有依赖关系的 Intent；
5. 检查 Intent 是否有范围、预算和完成判据；
6. 将无依据的广泛发现任务留为建议或拒绝创建；
7. 把新执行结果反馈给下一轮 Agent 推理。

Planner 不应仅因 phase 未覆盖就自动创建端口扫描、目录字典或 endpoint × vulnerability class 的笛卡尔积任务。

### 8.3 Planner 与原生 Agent Reasoning 的分工

- Main Agent / Coordinator：保留目标理解、假设生成、替代解释、行动取舍和结果综合的主导职责。
- Planner：把模型提出的方向结构化、验证依赖、预算和范围，不替模型决定所有探索方向。
- Worker / Specialist：承担边界明确的验证任务，返回证据与不确定性，不只是执行固定类别扫描。
- Verifier：独立复核候选发现，保持现有权威和工具面约束。

如果本地 `planner_worker` 拓扑继续作为默认执行拓扑，应调整它的职责边界，而不是直接恢复旧版固定 coordinator 波次。由主 Agent 提案、操作者确认、Planner 结构化、Worker 执行的现有 HITL 流程继续保留。

### 8.4 Specialists 的触发条件

委派不应只因为“有很多端点/漏洞类别”就启动。

适合委派的情况：

- 已有可区分的多个假设或专业问题；
- 子任务有互不冲突的资产/身份/范围；
- 每个 specialist 的输入证据、工具权限、输出契约和预算清楚；
- 任务结果可回收到同一个 run 的信息与信念上下文。

不适合委派的情况：

- 目标尚未理解、没有可区分的问题；
- 多个 worker 会重复探测同一端点或共享身份；
- 为了“并行”而复制相同扫描任务；
- 目标方已经限速、拒绝或要求暂停。

## 9. 速率、请求预算与停止规则

### 9.1 从工具局部限速升级为 Engagement 级约束

上游 README/配置说明了请求速率相关配置，但本地 fork 必须审计这些限制是否覆盖所有可执行路径。不能假定 HTTP client 的 rate limiter 自动管住 Browser、Terminal 中的命令行扫描器和多个 Worker 的并发请求。

建议建立一个统一的执行策略服务（内部策略组件，不是新的领域权威对象）：

- 每个 Engagement / Run 的总请求预算；
- 每个 origin / host 的速率和并发上限；
- HTTP、Browser、Terminal 网络工具共享预算；
- Worker 和子 Agent 共享父 Run 的总预算；
- 工具进程启动前校验策略；
- 工具执行中可取消、暂停或结束；
- 记录实际请求数、速率、并发、响应码、超时与停止原因。

不得只靠 Prompt 告诉 LLM“慢一点”。约束必须在工具调用或网络执行层强制生效。

### 9.2 面向目标方反馈的自适应退避

这是为了尊重目标方限制、降低误伤和不必要流量，不是用于绕过封禁。

策略至少应识别：

- `429` 和 `Retry-After`：按目标明确给出的时间退避；
- 连续 `503`、连接超时、连接重置或明显服务异常：暂停当前主动测试，降低后续请求或等待操作者确认；
- 明确的拒绝/封禁提示、账户锁定、WAF/IDS 告警或操作者停止：停止相关主动测试，不通过更换来源继续；
- 测试窗口结束或授权失效：停止所有相关主动执行；
- 不同 origin 之间不得共享错误的限速桶，也不得通过并发 Worker 绕过同一目标的总预算。

系统应区分“目标暂时限速”“服务异常”“授权拒绝”“工具本身失败”，并将其作为 Observation/InfoItem 或执行审计记录保存。

### 9.3 风险分级与确认门

将主动动作按影响范围分成内部策略级别，例如：

- `observe`：读取已经提供的信息或不产生额外目标流量的本地分析；
- `low_impact`：小范围、低并发、预算内的必要验证；
- `active_discovery`：目录、服务等主动发现，需要明确授权和显式预算；
- `high_impact`：可能影响可用性、触发账户锁定、写入状态或对业务产生副作用的测试，必须满足更严格的确认条件。

具体哪些工具/参数属于哪个级别，应由本地代码和工具语义审定，不靠模型自行分类。高影响动作不得仅凭 LLM 自述“已授权”放行。

## 10. 运行模式与策略

建议将方法论深度与主动扫描强度拆开，不再让 `full methodology` 隐式代表“大范围网络发现”。

| 维度 | 建议模式 | 语义 |
|---|---|---|
| 方法论 | `focused` / `full` | 控制 Agent 可参考的知识与评估范围 |
| 信息获取 | `provided_first` / `progressive` | 优先用已有信息，按缺口逐步获取 |
| 主动发现 | `disabled` / `explicit` / `evidence_gated` | 控制目录/端口等发现能力何时允许 |
| 测试深度 | `standard` / `deep` | 控制单个有依据方向的验证深度，不自动扩大授权范围 |

这是设计上的策略维度，不代表必须新增四个用户环境变量。应先检查现有 UI、配置、任务字段和 RoE 能否承载，避免配置爆炸。默认策略应偏向 `provided_first + progressive + explicit/evidence_gated`；对端口范围、速率和并发没有明确授权时，不运行宽范围主动发现。

## 11. 扫描模式如何保留而不主导通用推理

上游支持 single、DAST、wildcard 等模式。本地二开可以保留这些入口，但应明确它们是不同的任务上下文，而不是不同的“自主性等级”。

- **Single Web/API：** 默认把已给定 URL/Host 视为应用入口；优先分析页面、API、认证、角色、业务流程与已有资产信息。不要自动推导出同一 IP 的所有端口均已授权。
- **DAST：** 以浏览器/应用交互和用户流程为中心；Crawler 的输出是观察，不是最终攻击计划。已知页面/路由应优先用于形成具体假设。
- **Wildcard / Multi：** 被发现的子域必须先进入授权范围复核/资产身份解析，再决定是否创建独立 Run；不能把 DNS 发现等同于对任意新资产的测试授权。
- **Code / Whitebox：** 利用源代码、配置和依赖信息形成数据流、权限边界、危险调用和业务假设，减少对宽范围黑盒扫描的依赖。
- **Benchmark：** 由 Adapter 提供题目、环境、工具入口和得分结果；通用 Agent Core 不因为题目标签而改变认知权威。

## 12. Benchmark 与真实渗透的边界

Benchmark 是验证系统架构的实验夹具，不应定义通用渗透能力。

- `ChallengeResult` / 平台得分状态与 `Finding` 分开；平台 `correct` 不能自动制造 Security Finding。
- Flag 或 challenge artifact 只有在符合安全报告契约时才被引用，不改变 Verifier 和 `report_vulnerability` 的权威。
- Benchmark 的题目清单可以影响 Goal / Objective，但不能把整个系统的任务策略改成“优先得分”。
- Benchmark 专用 lane、提交保护和 challenge 生命周期由 Adapter 承载；通用 InfoItem、Hypothesis、Evidence 和 Intent 不依赖平台题型。
- Benchmark 运行可以验证 Hypothesis 是否真正被生成、claim、测试和更新；不能把 claim 次数本身作为“自主性”唯一指标。

## 13. 数据与可观测性：证明系统不再是 Scanner

每次 Run 应能回答以下问题：

### 13.1 起始阶段

- Agent 是否先读取了用户提供的上下文/已有资产/历史信息？
- 主动扫描类任务是谁创建的：系统自动计划、LLM、操作者还是 skill/hook？
- 自动创建目录/端口任务时，引用了哪条授权和哪条信息缺口？
- 第一批工具调用的范围、速率、并发、预算与停止条件是什么？

### 13.2 假设与决策

- 本轮形成了哪些正式 Hypothesis？来源是什么？
- 哪些假设由新观察触发，而不是由漏洞类别矩阵机械生成？
- Agent 是否考虑过替代解释和反证条件？
- 哪些 Information 被读取，并真正改变了假设或行动？
- 当前 Intent 对应哪个 Goal、Hypothesis 或信息缺口？
- 有没有仅因 phase 未覆盖而生成的无依据 Intent？

### 13.3 目标响应与资源

- 各 origin / host 的请求量、速率和并发是多少？
- 429 / Retry-After / 503 / 超时 / 拒绝响应出现后，实际采取了什么动作？
- 是否存在 Worker 通过另一工具面绕过同一 Engagement 预算？
- 哪些任务因目标限速或服务异常暂停？是否错误地继续主动请求？

### 13.4 决策效果

- Hypothesis 的支持/反驳/不确定状态变化；
- 每个 Intent 预期观察与实际观察是否一致；
- 因新信息而取消/修改/新增了哪些 Intent；
- 有多少重复探测被历史信息阻止；
- 发现结果是否通过既有 Verifier / report gate。

建议增加只读审计投影或扩展已有 domain audit，不增加新的决策权威。事件和投影只用于审计与度量，不参与状态判定。

## 14. 与现有 CORE / 信息图契约的兼容关系

### 14.1 必须保持不变

- PostgreSQL 是唯一持久态；Explorer Graph / Asset Graph 的权威分工不变；
- `run_id` 是跨聚合层和原子层的规范关联键；
- Exploration Graph 节点只存权威引用与摘要，不复制正文；
- InfoItem 的 observed/inferred 不混层；inferred 必须有 supports 才能进入证明/判定；
- Evidence `kind` 与 `nature` 是不同维度；
- Belief / Hypothesis 的生命周期由现有 authority 管理；
- Intent 的 claim、owner、lease、preconditions、confirmation 和 ConcludeIntent 不变；
- Finding 接受和标签只由 `report_vulnerability` 报告门产生；
- goal 达成仍由 `objective.Evaluate` 确定性判断；
- HITL、Scope Guard、Verifier、预算、六环安全链不变；
- Event / Activity / Graph projection 不是状态权威；
- 不新增第二套任务/假设状态机。

### 14.2 需要调整的机制

1. **自动 plan 构建：** Recon 可保留为必要、有限的 prerequisite，但目录发现不应由 full phase 默认自动变成启动扫描；端口发现必须有独立授权和预算条件。
2. **coverage epoch：** 保留覆盖观察，但将未覆盖项默认作为“待判断”而不是自动任务。
3. **GapScanner：** 保持只读、advisory；输出应包含形成 gap 的证据引用和“为什么该 gap 会影响某个判断”。
4. **Hypothesis seeding：** 对旧的批量 seed 机制做 provenance 审计。缺乏具体 claim/证据/可区分验证路径的 coverage seed 不应直接进入活动 claim 池。
5. **BeliefPoolBrief：** 不只展示 Top-N，还应展示为什么优先、支持/反证摘要、已知前置条件和需要区分的替代假设；不得把 brief 变成固定任务清单。
6. **VOI：** 现有受限 VOI 排序保留；VOI 应表示对决策的预期影响，而不是简单等同于“扫描后得分收益”。
7. **Intent 级 VOI：** 现有 IG4 实现保留，但需验证它不会使 phase 基线长期压过证据更强的新假设；不允许擅自突破 CORE 的权重上限。
8. **上下文压缩：** 保留 digest 有出处、不可递归摘要和权威快照规则；压缩不能把 coverage 候选伪装成已观察事实或已测试项。
9. **多 Agent：** 保留 scan-scoped agentsgraph / pool / lease；specialist 必须围绕可区分假设和不重叠任务分工，不以端点数量本身触发重复扫描。

## 15. 实施路线

所有批次独立可运行、可测试、可回滚。先修决策路径，不要先重写存储或增加新算法。

### R0 — 运行行为归因（只审计，不改代码）

目标：确认“启动即扫目录/全端口”的真实来源。

必须追踪：

- 本地 fork 的 commit / tag / 上游基线；
- 启动时生成的 Plan/Intent；
- 首 20 次 tool call 的时间、tool name、完整参数摘要、目标、调用方；
- `planner.go` 创建的任务与 LLM 自主 tool call 的区别；
- 22 阶段 Prompt、Phase Playbook、Recon Skill、Hook nudge；
- ScanDepth / active intensity / phase selection / mode / user instruction；
- HTTP、Browser、Terminal 和外部扫描工具各自的请求限速路径；
- 是否存在跨 Worker 共用预算缺失。

特别要求：不要仅凭 shell 命令文本判断“全端口扫描由 Planner 自动创建”；必须用事件、计划节点和调用链证明来源。

交付：启动行为时间线、自动任务来源表、请求预算覆盖矩阵、最小根因报告。

### R1 — Progressive Recon 策略（最小行为变更）

目标：减少无依据的启动扫描。

- 将目录发现从“完整方法论默认任务”调整为显式选择或 evidence-gated gap；
- 端口发现独立受 RoE 授权和预算控制；
- 对仅提供 Web URL 的单目标任务，不隐式推断所有端口已授权；
- 先装配操作者提供的信息与资产上下文；
- 把“需要更多信息”交给 LLM 解释，输出可审查的 discovery proposal；
- 未满足授权或预算条件时不创建主动发现 Intent。

验收：默认普通 Web/API Engagement 不会仅因 `full methodology` 就自动启动宽范围目录/端口发现；已有完整端点清单时不会重复做通用 API 路径枚举；每个主动发现 Intent 有范围、理由、预算和停止条件。

### R2 — Hypothesis 质量与消费路径

目标：正式 Hypothesis 从覆盖矩阵副产物变为可检验信念。

- 审计 engine-seeded hypotheses 的质量和 claim 情况；
- 区分 Coverage opportunity 与 Formal Hypothesis；
- LLM 可生成多个候选解释，但正式入 Ledger 前需结构化；
- 提供 supports / contradicts / baseline / falsification / next discriminating observation；
- claim 选择仍用确定性排序和现有 VOI 约束；
- `update_hypothesis` 必须处理 proven / rejected / blocked / exhausted / queued 等现有状态语义，不创造新终态。

验收：Hypothesis 的新增有来源；无依据 coverage seed 不占据可调度池；模型能根据反证修订或拒绝假设；Ledger 状态变化有完整审计；Verifier 和报告门没有改变。

### R3 — Information-to-Decision Trace

目标：证明信息空间真的影响推理与行动。

- 记录关键 InfoItem 的读取与引用；
- 记录 Hypothesis 更新引用的 Evidence/InfoItem；
- 记录 Intent 的 decision basis refs；
- 记录执行结果对 Hypothesis / Intent 的影响；
- 加入“重复动作被历史信息抑制”的可验证事件。

验收：给定一条新反证，可以沿 ID 找到被更新的假设、取消/调整的 Intent 和后续结果；不新增第二套权威。

### R4 — Engagement-wide Rate / Budget Policy

目标：工具层统一遵守授权方的速率与停止要求。

- 审计所有可产生目标网络流量的工具面；
- 对 HTTP、Browser、Terminal/外部工具和多 Worker 共享总预算；
- 遇到 Retry-After / 明确拒绝 / 服务异常时执行退避或停止；
- 阻止多个 Worker 通过独立 limiter 叠加超过目标总预算；
- 让停止原因进入 Intent/Run 审计。

验收：模拟 429 + Retry-After 时，所有相关执行路径都遵守暂停；操作者停止、授权窗口关闭或明确拒绝后无后续主动请求；无工具通道可绕过 Engagement 级策略。

### R5 — 多 Agent 与专项能力的 hypothesis-driven 化

目标：保留原生 specialists 能力，而非退化为固定扫描分片。

- 根据假设与专业问题创建互不重叠的 specialist work；
- 每个子任务携带必要证据引用、scope、预算、交付契约；
- 子 Agent 产出的结果返回统一 Information/Hypothesis 上下文；
- 对 B 类多阶段、F1/F2 二进制等任务，先建立能力/工具就绪判定，不因工具缺失反复空转。

验收：委派任务不是简单复制 endpoint × class；子 Agent 的产出可追踪到父 Goal/Hypothesis；工具缺失有明确能力阻塞原因。

### R6 — 对照实验与回归

目标：验证自主推理能力是否恢复，而不仅是请求减少。

使用固定、授权、可复现的测试应用/靶场，比较旧版与新版：

- 启动阶段请求数与范围；
- 首次形成可检验 Hypothesis 的时间；
- 假设有证据引用比例；
- Hypothesis 被支持/反驳/修正的比例；
- 信息读取后改变 Intent 的比例；
- 重复探测比例；
- 429/拒绝/服务异常的发生与处置；
- 真正验证的安全发现数量与证据质量；
- Token、工具调用和总测试时间。

Benchmark 分数只是一个结果指标，不能单独决定优化是否成功。

## 16. 关键验收用例

1. **Web URL 单入口：** 仅提供授权 Web URL，默认不得推断其 IP 的所有 TCP 端口均获授权。
2. **完整 API 清单：** 已提供 API Spec 和 endpoint inventory，Agent 应优先理解和形成业务/权限/输入处理假设，不得仅因 Phase 3 存在而重复宽范围目录枚举。
3. **缺少端点信息：** Agent 可以提出低影响的信息获取建议；主动目录发现必须有 RoE 授权、明确预算和完成条件。
4. **显式目录发现授权：** 即使操作者选择目录发现，也必须有界、有速率、有停止条件，执行后审查结果再决定是否进一步探索。
5. **端口发现未授权：** 明确拒绝创建端口发现 Intent；不得以 Recon prerequisite 名义绕过。
6. **Hypothesis 反证：** 新 Observation 反驳当前假设时，Agent 应能更新或拒绝原假设，而非继续按原扫描列表执行。
7. **多个竞争假设：** Agent 能解释为什么先验证其中一个，并说明延后其他假设的原因；排序保持确定性可复放。
8. **429 / Retry-After：** 统一执行策略触发退避；Browser/Terminal/Worker 不得绕过。
9. **明确拒绝或停止：** 停止相关主动测试，不得通过代理轮换或更换执行身份继续。
10. **跨 Worker：** 父子 Worker 共用同一 Engagement 预算，Evidence 和 Intent 归属可追踪。
11. **上下文压缩：** digest 只引用真实权威 ID，不把 coverage opportunity 转成“已观察”或“已验证”。
12. **Verifier / Finding：** 以上调整不得改变 Verifier、`report_vulnerability` 和 Finding 标签权威。

## 17. 运行指标与判定标准

不以“扫描请求越少越好”或“Hypothesis 越多越好”作为单一目标。建议使用以下指标组合：

| 维度 | 指标 | 期望变化 |
|---|---|---|
| 启动质量 | 首个主动工具调用前是否装配已有信息、授权和目标上下文 | 完整度提升 |
| Recon 效率 | 每条有效新信息所需的目标请求数 | 降低，但不能靠漏测制造好看指标 |
| 假设质量 | 有 claim、preconditions、evidence refs、falsification 的 Hypothesis 比例 | 提升 |
| 假设消费 | Formal Hypothesis 被 claim 并进入验证的比例 | 提升 |
| 认知反馈 | 新信息导致 Hypothesis / Intent 改变的比例 | 提升 |
| 探索自主性 | 由具体观察触发的新假设占比 | 提升 |
| 方法论健康度 | 不适用/已评估/暂缓/阻塞状态有依据的比例 | 提升 |
| 请求纪律 | 超 RoE 请求数、停止信号后请求数 | 必须为 0 |
| 稳定性 | 重复动作、无进展工具循环、无理由取消 | 降低 |
| 结果质量 | 可复核 Finding、独立验证通过率、证据完整度 | 提升 |
| 成本 | Tokens、工具调用、运行时间与请求量 | 在不牺牲有效覆盖前提下优化 |

### 17.1 建议新增的审计字段/事件（先复用已有审计机制）

- `decision_basis_refs`；
- `hypothesis_origin`；
- `discovery_reason`；
- `engagement_policy_ref`；
- `active_request_budget` / `budget_consumed`；
- `rate_limit_event`（含 status / Retry-After / action）；
- `stop_reason`；
- `information_read_refs`；
- `intent_changed_by_evidence`。

以上是逻辑字段建议。实现前先检查现有 PG 审计表、Intent contract、domain journal 和 InfoItem 能否承载，禁止为每项都新增表。

## 18. 风险与回滚策略

- **风险：** 将主动发现设为证据触发后，可能漏掉真正未知但重要的攻击面。  
  **缓解：** 保留操作者可显式开启的深度发现；在结束前展示尚未评估的适用领域与原因，让 Agent 解释是否值得追加测试。
- **风险：** Hypothesis 质量门槛太高，导致 Agent 不愿记录早期推测。  
  **缓解：** 允许 reasoning candidate 留在短期上下文；正式 Hypothesis 可以明确标记低证据基础，但必须承认未知和可证伪条件。
- **风险：** Coverage 权重降低后，部分方法论领域长期被忽略。  
  **缓解：** 将 coverage 作为定期反思提醒，并允许有依据地 `not_applicable` / `deferred` / `blocked`；不把它直接转换成扫描命令。
- **风险：** 全局速率限制实施不完整，某些工具绕过策略。  
  **缓解：** 把所有产生目标流量的执行入口列成矩阵，未纳入策略的工具不得用于自动主动测试。
- **风险：** 同一目标多 Worker 速率叠加。  
  **缓解：** 父 Run 统一预算、每 origin 限速和并发；子 Worker 只申请预算，不自行创建新 limiter。
- **回滚：** 每个 R0–R6 独立提交；R1 可以回滚 Planner 任务生成调整，但不能回滚 RoE / Scope / Stop 安全边界；R2/R3 可关闭新的提示/读模型消费，但不能删除历史证据；R4 的安全策略不得降级为无约束执行。

## 19. 给代码 Agent 的实施纪律

代码 Agent 必须遵守：

1. 以用户本地 fork 的 commit 和源码为事实，不把上游 main 的当前实现当作本地现状。
2. 先完成 R0，只做行为来源审计，不得同时大规模修改代码。
3. 逐条追踪“首批目录/端口扫描”来自 Planner、Prompt、Skill、Hook、LLM tool call 还是模式配置。
4. 每条结论必须提供文件、函数、调用链、测试或运行日志证据。
5. 不把上游 `reason → tool → observe` 架构图当作“运行时一定自主”的证明。
6. 不把本地 CORE 的 coverage/phase 机制当作原项目不可改变的上游语义；但改变前必须提出兼容方案。
7. 不新增第二套 Goal/Hypothesis/Intent 权威，不绕过 `ConcludeIntent`、HITL、Scope Guard、Verifier 或报告门。
8. 不通过 Prompt 单独解决速率和停止规则；必须在执行层强制。
9. 不实现代理/IP/身份轮换等规避目标方封禁的机制。
10. 每个里程碑必须有可运行测试和明确回滚点；未运行的测试标记为未验证。
11. 对“全端口扫描”必须先用运行记录证明工具、参数和触发来源，不可仅凭经验归因。
12. 优化成功的标准是：Agent 更会理解目标、形成和修正假设、以较少且有根据的请求获得更有价值的信息，并产出更可靠的安全结论，而不是仅仅扫描次数减少。

## 20. 最终设计结论

Xalgorix 不应该在“纯 LLM 自由行动”和“固定扫描工作流”之间二选一。

更合理的架构是：

- LLM 保留目标理解、主动假设、替代解释和探索策略的主导权；
- Information Space 提供有出处、可关联、可追溯的认知材料；
- Hypothesis Ledger 将重要推测转化为可检验、可反驳的长期信念；
- Intent / Planner 将被选择的验证方向变成有界、可审计的执行；
- 22 阶段 Coverage 提醒遗漏，不强制批量扫描；
- Engagement Policy 约束范围、速率、并发、预算和停止信号；
- Verifier 和报告门保持独立权威。

最终的核心循环应是：

```text
目标与授权
   ↓
理解已有信息
   ↓
LLM 自主推理
   ↓
形成 / 比较 / 修正 Hypothesis
   ↓
检索支持与反证
   ↓
选择一个有依据且符合规则的行动
   ↓
Intent Contract + Engagement Budget
   ↓
执行并观察
   ↓
Evidence / InfoItem 更新
   ↓
Hypothesis 状态与目标理解更新
   ↺
```

**最终原则：不要让系统因为“还有东西没扫描”就继续扫描；要让系统因为“有一个值得验证、且在授权范围内的问题”才采取下一步行动。**

---

## 附录 A：与用户现有文档的映射

| 本设计章节 | 主要依据 | 关系 |
|---|---|---|
| §1 上游与本地基线 | 上游 `docs/ARCHITECTURE.md` / `planner.go`；本地 `CORE_PHILOSOPHY.md` | 区分 upstream 与 fork |
| §2–4 Scanner-first → Hypothesis-driven | `CORE_PHILOSOPHY.md` 七环与对象映射 | 调整决策优先级，不新增权威 |
| §5 Progressive Recon | 原始信息核心建模文档；`BENCHMARK_OPTIMIZATION.md` 的 lane/任务损耗教训 | 从固定扫描改为缺口驱动 |
| §6 Hypothesis 质量 | `CORE_LOOP_AUDIT.md`、`CORE_LOOP_OPTIMIZATION.md` | 解决 queued 积压与批量 seed 风险 |
| §7 Information Space | `INFO_AS_GRAPH_SPEC.md`、原始信息核心建模文档 | 让信息真正影响决策 |
| §8 Intent / Planner | `CORE_PHILOSOPHY.md`、`INFO_GRAPH_CONTRACT.md`、`INFO_AS_GRAPH_SPEC.md` | 保留权威，改变计划来源 |
| §9 Request policy | 上游 Architecture / README 的 rate policy；本地 benchmark 工具/lane 问题 | 执行层统一预算 |
| §12 Benchmark boundary | `BENCHMARK_OPTIMIZATION.md`、`BENCHMARK_ROUND5_ANALYSIS.md` | Benchmark 仅作验证 |
| §13–17 运行验收 | `CORE_LOOP_AUDIT.md`、`INFO_GRAPH_CONTRACT.md`、两次跑分记录 | 从结构验收走向运行验收 |

## 附录 B：实施前必须核查的代码位置

以下是审查入口，不代表本文已经确认本地 fork 每个文件均与上游一致：

- 上游 `internal/agent/planner.go`：`buildEnginePlan`、Recon task、`dirbustWanted`、`selectionNeedsRouteDiscovery`。
- `internal/agent/agent_prompt.go`：22 阶段、Recon 契约、Phase playbook 注入和关闭指令。
- `internal/agent/agent_guard.go`：scope / phase / passive recon guard。
- `internal/agent/agent_ratepolicy.go`：instruction 中速率策略的解析与执行连接。
- `internal/agent/hooks.go`：plan nudge、coverage obligation、deep recon、finish gate 和 belief pool brief。
- `internal/scanctx/ledger.go` 及 ledger tools/hooks：Hypothesis seed、去重、claim、状态变更和 Evidence。
- `internal/orchestrator` / 本地 `planner_worker`：Intent 提案、apply、排序、Worker dispatch 和共享预算。
- `internal/tools/httpclient`、`internal/tools/browser`、`internal/tools/terminal` 及外部工具包装：检查每条网络流量路径是否遵守 Engagement 级预算。
- 本地 `internal/graph`、`internal/infospace`、`internal/infoitem`：核查信息读取与 Hypothesis/Intent 决策链的关联。

本地实现位置应由代码 Agent 在 R0 阶段重新确认，并输出 commit SHA、函数位置和实际调用链。
