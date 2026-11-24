# 面向授权渗透的持续态势理解与证据驱动探索架构

> **版本**：v1.0（设计提案）  
> **日期**：2026-11-24  
> **定位**：在现有 Xalgorix CORE v3.4、信息即图 IG0–IG7、信息图契约 R8.1–R8.4 基础上的架构优化提案。  
> **核心目标**：提升系统在授权范围内开展持续、可解释、可复核的渗透测试的能力，而不是把系统改造成 Benchmark 刷分器。  
> **验证原则**：Benchmark 是受控实验场，用于检验架构假设、暴露能力边界和观察回归；它不是系统的领域模型，也不是唯一成功指标。

---

## 0. 执行摘要

本设计不推翻现有信息驱动的渗透循环，也不另建一套图、账本、调度器或持久化体系。现有核心抽象已经覆盖了相当多必要能力：

- `Project / Task / Run` 提供项目、任务与运行边界；
- PostgreSQL 是状态与面板数据的唯一持久态；
- 探索图承载 `Goal / Intent / Belief / Fact / Finding / Hint / Digest` 等聚合对象；
- `InfoItem` 是一次观测或推导的原子信息，`Evidence` 是它在某个 Hypothesis 下的证据角色；
- `info_space_items / info_space_edges` 将聚合层和原子层统一为只读信息空间；
- `ConcludeIntent`、claim 谓词、前置条件、确认门和报告门维持权威边界；
- Planner / Worker、工具面裁剪、Verifier、HITL 和审计机制构成执行与判定基础。

因此，当前主要矛盾不宜概括成“信息模型不够复杂”。更准确的判断是：**信息表示和局部执行契约已较完整，但从持续态势理解到跨阶段任务组织、全局探索决策、执行反馈和最终安全评估之间，仍缺少一套统一的任务推进策略与运行效果度量。**

本方案建议把优化重心放在五个方面：

1. **任务目标与授权边界分离且贯穿全程**：每项行动都必须服务于明确的 Engagement Goal，并受不可绕过的 Scope / Rules of Engagement 约束。
2. **从局部 Intent 执行升级为跨阶段 Goal 工作流**：解决资产身份、阶段依赖、凭据/工件引用和多 Worker 协作的连续性问题。
3. **增加任务组合级探索决策**：决定当前应推进哪个目标、哪条假设或哪条攻击面验证线，而不是只对已有 Intent 做局部排序。
4. **建立多类型进展与停滞判断**：不能只用 InfoItem 增量代表所有任务进展，也不能把工具调用次数当作有效进展。
5. **测量信息是否真正改变了决策**：从“信息空间查询已经接通”推进到“信息复用、假设更新、行动调整和结果改善可以被验证”。

Benchmark 适配器只负责把题目、容器、平台状态和外部得分映射到测试 Goal 与外部评估记录。它不应成为核心模型里的 `Challenge` 优先级系统，也不能把平台 `correct=true` 伪装成 `Finding`。

---

## 1. 设计目标、非目标与成功定义

### 1.1 首要目标：授权渗透任务的可解释推进

系统的首要职责是帮助操作者在明确授权范围内，对目标环境形成持续、可更新、可追溯的安全态势认知，并依据证据选择下一步合理行动。

一次高质量的渗透测试运行，至少应能回答：

- 当前已识别哪些资产、入口、服务、身份和信任边界？
- 哪些是直接观测，哪些是推导判断？来源、时间和适用情境是什么？
- 当前有哪些安全假设，哪些得到支持、反驳或仍未知？
- 目标环境的哪些部分已覆盖，哪些尚未覆盖，为什么？
- 哪些验证路径最值得继续，哪些路径已经达到停止条件？
- 某项行动是否在授权范围、工具权限、速率和影响约束之内？
- 最终结论能否由独立证据、复测结果和报告门支撑？
- 当运行结束时，未完成项、限制、未知和计划偏离是否都被如实说明？

系统的优化目标应是提高**有效安全发现与评估质量**，而不是单纯最大化工具调用数、Hypothesis 数量、图节点数量、Worker 利用率或 Benchmark 得分。

### 1.2 次要目标：改善有限预算下的探索效率

有限预算是现实约束。系统需要在时间、并发、工具能力、环境稳定性和操作者审批之间做选择。但资源效率必须服务于渗透任务目标：

- 减少重复探测与无效重试；
- 更早发现重要攻击面和关键证据缺口；
- 在合理时间内推进高价值验证路径；
- 不让单个长期无进展任务吞噬整个运行预算；
- 保留对未知区域和不同技术面的探索机会；
- 在关键决策前保留充分的证据与人工确认机会。

### 1.3 Benchmark 的定位

Benchmark 是**验证适配器与测试场景**，不是系统的领域中心。

它可以验证：

- 信息是否被记录和复用；
- 不同 Worker lane 是否正确隔离；
- 长链任务是否能够跨阶段推进；
- 有限执行槽位是否能合理轮转；
- 系统是否能从停滞、错误和环境限制中恢复；
- 已验证结果是否能正确映射到外部评测结果。

Benchmark 分数只能作为一项结果指标。一次跑分失败必须区分：没有获得执行机会、工具或环境不具备能力、模型没有提出有效假设、验证链失败、执行中断、最终结果没有被正确记录。不能把“未进场”直接解释为“不会做”，也不能把“局部漏洞成立”直接解释为“整个任务成功”。

### 1.4 非目标

本期不做：

- 将 Xalgorix 改造成只围绕 TSec Challenge / Flag / 分数工作的专用系统；
- 用 Benchmark 题目分值替代真实渗透测试的风险、影响和授权约束；
- 用单一“成功概率”或 LLM 自评来决定漏洞成立与否；
- 通过 InfoItem 置信度、VOI 或图关系绕过 Verifier、`report_vulnerability` 或确定性目标判定；
- 引入第二数据库、Neo4j、MongoDB、Redis、pgvector；
- 本期引入 A*、MCTS、POMDP、POMCP、DESPOT；
- 以“攻击路径”名义把 `supports`、`references` 等证据边重解释为攻击步骤；
- 为了增加并发而放宽授权范围、scope guard、HITL 或工具面边界；
- 把平台成功提交直接转换成已验证的安全 Finding。

---

## 2. 对现有架构的判断

### 2.1 应保留的基础

下列设计继续作为权威基线，不在本提案中重做：

| 现有能力 | 当前权威 / 机制 | 本提案处理方式 |
|---|---|---|
| 主循环 | Observation → Belief → Obligation → Operation → Evidence → Verdict → Close | 保留七环，强化环间消费与推进策略 |
| Intent | PG `exploration_nodes(kind='intent')` | 保留状态、owner、lease、contract 权威 |
| Belief | PG `exploration_nodes(kind='belief')`；Evidence 明细在 ledger | 保留 Hypothesis 生命周期和 claim 谓词 |
| Fact / Finding | 对应图节点；Finding 只由报告门写入 | 保留字段级权威 |
| InfoItem | 原子信息层，来源、坐标、时间、nature、supports | 保留 observed / inferred 区分与可追溯性 |
| Evidence | Hypothesis 下的证据角色投影 | 保留 append-only、类型维度和上限 |
| 信息空间 | `info_space_items / info_space_edges` | 继续作为统一只读查询入口 |
| 激活机制 | Region / Association / ContextSlice | 保留查询语义、预算、上限和 incomplete 标记 |
| 规划与排序 | 确定性基线 + 受限 VOI（`w<=0.30`） | 保留；增加决策效果观测，不直接放大权重 |
| Intent 结束 | `ConcludeIntent` / `ConcludePlanned` | 保留事务性结论和 yields 约束 |
| Finding 判定 | Verifier + `report_vulnerability` 门管线 | 冻结权威与闸门顺序 |
| 状态存储 | PostgreSQL 唯一持久态 | 保留，不引入第二数据库 |
| 异步处理 | River 仅用于异步补全 / 冷路径 | 不接管主扫描生命周期 |
| 安全边界 | Scope → redirects → dial → sandbox → proxy fail-closed → 环境剥离等安全链 | 只允许收紧，不允许以优化名义绕过 |

### 2.2 已知的运行问题与实现状态必须分开

两次 Benchmark 复盘揭示了多个真实问题，但截至 2026-11-24，部分已在代码中整改，不能在新路线图中重复当作未完成项目。

| 复盘发现 | 复盘中的代码状态 | 本提案中的处理 |
|---|---|---|
| 假 Flag / 重复提交 / 清单外题号 | 已增加提交来源守卫、格式/重复/频率约束 | 下一轮做运行验收；不重复做 Prompt-only 约束 |
| 控制面与攻击执行 lane 混用 | 已收紧控制面工具面和目标主机访问边界 | 增加持续遥测验证，确认没有 lane 泄漏 |
| 进展停滞后长期占用容器 | 已实现进度守卫、typed kill、owner 围栏、退出确认和 close intent | 将进展信号升级为多类型，不简单依赖 InfoItem 增量 |
| 容器槽位调度和背压 | 已有派生攻击 intent、并发上限、生产侧背压和 worker 预算 | 不再设计 Benchmark 专属“得分率调度器”；统一到任务/目标推进管理 |
| 首轮 Planner 上下文丢失、Planner JSON 类型错误、占位符 target | 第四轮复盘标记为已修 | 继续做 Planner 输入/输出契约回归 |
| Docker 到靶场网络路由 | 第四轮复盘后以配置模式整改 | 明确区分 Benchmark 网络姿态与真实项目默认姿态 |
| 二进制工具链不足 | 第五轮复盘仍标记未落地 | 作为技术能力矩阵与专项工具链工作项 |
| 多阶段任务、容器实例身份、凭据跨 Intent 传递 | 第五轮复盘仍标记未落地 | 作为本提案的重点架构工作项 |
| 得分结果与 Finding 对齐 | 仍未落地；且不能把得分直接伪装为 Finding | 增加独立外部评估结果引用，不改变 Finding 权威 |
| 题目覆盖义务 | 仍未成为引擎级对象 | 转化为通用 Goal/coverage obligation；Benchmark 适配器仅提供测试 Goal |
| 信息图是否真正改善决策 | IG0–IG7 已接通查询与部分消费者，但缺少运行效果因果指标 | 增加信息消费与决策影响遥测 |

### 2.3 当前核心缺口的重新表述

本提案不把问题描述为“没有全局调度器”或“信息图没有消费者”。更准确的缺口是：

1. **局部执行契约不等于跨阶段任务连续性**：Intent 能正确 claim / conclude，不代表多个 Intent 已经围绕同一个长期目标持续推进。
2. **信息可查询不等于信息被有效利用**：Planner 和 Worker 能读取信息空间，但运行指标尚不能回答这些读取是否改变了 Hypothesis、Intent 或最终判断。
3. **进度守卫不等于通用进展理解**：当前停滞回收以信息层增量为重要信号，复杂分析任务的进展可能先体现在资产、假设、分析工件或验证状态中。
4. **局部 VOI 不等于完整的任务决策**：现有 VOI 仅作为受限排序项，尚不能单独表示任务风险、覆盖缺口、阶段依赖、资源成本和行动影响。
5. **任务终态不等于 Engagement 完成**：某个 Finding、某个 Intent 或某次 Benchmark 提交完成，都不代表整个授权任务达到目标。
6. **工具覆盖不等于方法覆盖**：二进制分析、多阶段验证、凭据与资产连续性需要对应的工具能力、任务组织与安全边界共同支持。

---
## 3. 目标架构：持续态势理解与证据驱动探索

### 3.1 总体结构

```text
┌──────────────────────────────────────────────────────────────────┐
│                    Engagement / Operator Layer                    │
│  授权目标 · Scope · Rules of Engagement · 时间窗 · 人工确认 · 目标  │
└──────────────────────────────┬───────────────────────────────────┘
                               │
┌──────────────────────────────▼───────────────────────────────────┐
│                 Mission & Goal Orchestration（增强）               │
│  Root Goal · 子目标 · 覆盖义务 · 阶段依赖 · 任务组合 · 完成判据       │
└──────────────────────────────┬───────────────────────────────────┘
                               │
┌──────────────────────────────▼───────────────────────────────────┐
│             Exploration Decision / Portfolio Controller            │
│  Gap 评估 · 假设选择 · 行动价值 · 进展/停滞 · 预算与资源 · 轮转       │
│  LLM 提议候选；引擎执行硬约束、确定性排序、状态迁移和预算扣减        │
└──────────────────────────────┬───────────────────────────────────┘
                               │
┌──────────────────────────────▼───────────────────────────────────┐
│                  Planner / Intent / Worker Pool                    │
│  规划契约 · preconditions · yields · lease · timebox · typed lane  │
│  控制面 / 侦察分析 / 验证执行 / 专项分析 / 报告验证                    │
└──────────────────────────────┬───────────────────────────────────┘
                               │
┌──────────────────────────────▼───────────────────────────────────┐
│                    Information & Belief Core                       │
│  Asset Graph · Exploration Graph · InfoItem · Evidence · Hypothesis │
│  Region / Association / ContextSlice · L0/L1/L2 · Digest             │
└──────────────────────────────┬───────────────────────────────────┘
                               │
┌──────────────────────────────▼───────────────────────────────────┐
│                Verification / Reporting / Close                     │
│  独立验证 · report_vulnerability · Finding · Objective Evaluate    │
│  目标处置 · 计划偏离 · 未知项 · 可复核报告                           │
└──────────────────────────────────────────────────────────────────┘

所有写路径受统一授权与安全链约束；所有状态权威仍落在现有 PG / ledger / gate。
```

### 3.2 分层职责

#### L0 — Engagement Governance（授权与操作者治理）

回答：**系统被允许对什么做什么？**

职责：

- 管理项目、任务、Run、授权 Scope、允许的操作类型、时间窗和限制条件；
- 将操作者的目标、禁区、例外和风险容忍度转成结构化约束；
- 对高影响、破坏性、越权边界附近或不可逆操作触发既有 HITL；
- 处理操作者停止、外部任务截止、撤销授权和恢复；
- 向所有 Planner、Worker、MCP 工具和子任务传播同一授权上下文。

约束：Scope 与 Rules of Engagement 是硬边界，不是排序分数。LLM、VOI、Goal、InfoItem 和 Worker 均不能扩大授权。授权变更必须由有权操作者入口写入，产生审计记录。

#### L1 — Asset & Surface Model（资产与攻击面）

回答：**目标环境由什么组成，资产之间有哪些已知关系？**

复用现有 `assets` 资产图和探索图，不另建第二张持久图。重点补齐稳定身份和时间绑定：

- `asset_id`：环境中的逻辑资产身份；
- `locator`：IP、hostname、URL、端口、容器地址、文件路径等可变化定位符；
- `instance_id`：一次容器 / 部署 / 会话实例身份；
- `valid_from / valid_until / observed_at`：定位符和属性的时间范围；
- `scope_ref`：该资产在当前 Engagement / Task / Run 下的授权引用；
- `source_info_ids`：资产属性由哪些 Observation 支撑。

IP 地址不能充当永久资产身份；同一个逻辑资产可以有多个随时间变化的 locator，一个 locator 也可能在不同时间指向不同实例。跨阶段任务应引用逻辑身份或实例身份，不应仅靠旧 IP 字符串续接。

资产关系只能记录实际观测或明确来源的关系。不得仅凭文本相似或 `supports` 证据边推导网络可达、信任关系或攻击路径。

#### L2 — Information Space（信息空间）

回答：**我们实际观察到什么，哪些上下文可能相关？**

保持当前双层模型：

- 聚合层：`exploration_nodes / exploration_edges`；
- 原子层：`info_items / info_item_edges`；
- 统一只读视图：`info_space_items / info_space_edges`；
- Evidence 是原子信息在某条 Hypothesis 下的角色视图。

保留 `run_id` 作为跨层规范键、Region / Association / ContextSlice 的确定性与有界语义，以及 L0 / L1 / L2 上下文分层。查询结果必须携带 source、nature、截断和同步缺口状态。

信息模型不承担以下职责：

- 判定漏洞是否成立；
- 直接改变 Intent 生命周期；
- 自动将推导信息升级为 observed；
- 把信息关系边解释成执行依赖或攻击路径。

#### L3 — Belief & Evidence（信念与证据）

回答：**基于已有观测，我们有哪些待验证主张？**

继续使用 `belief` 节点与 ledger Hypothesis，不新增一个与 Hypothesis 重复的 `Claim` 权威对象。

每个 Hypothesis 应有：

- 明确的目标资产 / endpoint / 漏洞类 / 适用角色或情境；
- 显式 preconditions；未知条件必须写明 `unknown`，不得伪装为满足；
- baseline / control / probe / exploit / artifact / finding_ref 等 Evidence 角色；
- observed / inferred 独立 nature；
- 支持、反驳、未知和冲突证据的引用；
- 状态、owner、lease、复核记录和关闭理由。

Hypothesis 不是 Intent：前者描述“需要验证的主张”，后者描述“要执行的行动”。Hypothesis proven 也不等于 Finding accepted；Finding 仍只能通过既有报告门管线形成。

#### L4 — Goal & Workflow（长期目标与阶段工作流）

回答：**为了完成本次 Engagement 的目标，需要完成哪些相互依赖的工作？**

复用现有 Goal / Intent / `depends_on` / `yields` / `preconditions`，强化跨 Intent 的连续性，而不是增加新的平行工作流数据库。

目标分为：

1. **Engagement Root Goal**：本次授权评估要回答的顶层问题；
2. **Coverage Goal**：需要覆盖的资产、入口、技术面或测试范围；
3. **Investigation Goal**：某个资产 / 风险 / 假设需要查明的问题；
4. **Validation Goal**：需要取得独立证据确认或排除的主张；
5. **Impact Goal**：在授权与安全约束下评估可能影响；
6. **Reporting Goal**：形成可复核结论、限制说明和未解决事项。

Goal 的状态必须由确定性判据或操作者判断推进。LLM 可以提出子目标，但不得自行宣称目标达成。`objective.Evaluate` 仍是 Goal 达成判定权威。

#### L5 — Exploration Decision Controller（探索决策控制器，新增重点）

回答：**当前最值得推进的目标、假设或行动是什么，投入多少资源，何时复核？**

这不是一个“Benchmark 得分调度器”，而是整个渗透任务的全局探索决策层。它读取：

- Engagement Goal 及其完成判据；
- Scope 与操作者约束；
- Asset / Surface 覆盖状态；
- 相关 InfoItem、Evidence、Hypothesis 和 Finding；
- open / running / blocked / exhausted Intent；
- 各 Worker lane 的能力、预算、占用和健康状态；
- 当前任务的时间预算、速率限制、环境稳定性和人工审批状态。

它输出：

- 下一轮要推进的 Goal / Gap / Hypothesis；
- 候选 Intent 的优先级建议；
- 建议采用的 Worker lane / 工具能力类别；
- 执行预算和复核点；
- 暂停、轮转、继续、降级或请求人工判断的原因。

引擎必须执行授权、前置条件、并发、预算、状态迁移和报告门等硬约束。LLM 只提出候选、解释观察或给出待校准的估计，不能直接越权分配资源或写入确定性判定。

#### L6 — Planner / Worker / Tool Surfaces（规划与执行）

回答：**如何安全、可复现地执行一个明确的 Intent？**

继续使用 `planner_worker` 默认拓扑和现有 Worker 池。按职责区分工具面，而不是只靠 Prompt 声明：

- `control_plane`：项目 / 任务 / Run / 靶场生命周期 / 只读协调；不得执行目标攻击动作；
- `recon_analysis`：授权范围内的资产发现、服务识别、资料分析和离线研究；
- `validation_execution`：在明确 scope、预算和工具许可下执行验证动作；
- `specialist_analysis`：二进制、固件、云、身份、协议等专项分析；
- `verification_reporting`：独立验证与报告门相关工作，遵守所属 Agent 的 verifier/client 隔离。

lane 是能力与权限的执行边界，不是不同的事实权威。所有 lane 写入同一 Run 的对象时，必须遵守同一来源、时间、run_id、owner 和审计契约。

#### L7 — Verification / Finding / Close（验证、报告与收工）

回答：**哪些结论已经足够可靠，哪些仍未知，任务是否真正达到目标？**

- 保留现有 Verifier 三态和 `report_vulnerability` 闸门顺序；
- Finding 接受状态、验证标签和严重性仍由报告管线决定；
- Goal 达成由确定性 `objective.Evaluate` 或操作者按规则判断；
- 收工时输出已覆盖、已验证、已排除、受限、未测试和未解决项；
- 未完成目标不能因为“找到了一个 Finding”而隐式关闭。

---

## 4. 统一的任务推进模型

### 4.1 不改变七环，强化环间的反馈

```text
Observation
   ↓ 规范化来源、时间、主体、情境和授权引用
Information Space
   ↓ Region / Association / ContextSlice 有界激活
Belief / Hypothesis
   ↓ 检查支持、反驳、未知、前置条件
Obligation / Goal Gap
   ↓ 识别必须完成的工作与剩余缺口
Decision Controller
   ↓ 选择目标 / 假设 / 行动 / 预算 / lane
Operation / Intent
   ↓ Claim → 执行 → Progress Signals
Evidence
   ↓ append-only，observed / inferred 分离
Verdict
   ↓ Verifier / 确定性验证 / report gate
Goal Update
   ↓ 未完成则重算缺口；完成则进入报告与收工
```

核心变化不是在每一轮机械调用三种点亮，而是确保每次决策都能说明：使用了哪些相关信息、解决了哪个缺口、为什么选择此行动、执行后获得了什么新证据。

### 4.2 统一的候选行动来源

候选行动不能只来自 LLM 新写的一份计划，也不能只来自 GapScanner。应从以下来源合并：

1. 已存在但尚未完成的 Intent；
2. 满足条件、可调度的 Hypothesis；
3. Coverage / Surface 发现的确定性缺口；
4. 验证失败、矛盾证据或新资产触发的后续检查；
5. 操作者明确要求的 Goal / Hint；
6. 历史 Run 中可授权复用的经验线索（只提供引用和候选，不复制为当前事实）。

候选合并后统一做去重、授权检查、依赖检查、预算检查和确定性排序。GapScanner 仍是只读候选生成器，不直接写 Intent；Planner 仍负责提出符合契约的计划；Intent 仍需经过现有 `apply`、claim 和 conclude 路径。

### 4.3 决策排序：先硬约束，再软价值

不建议以单一加权公式直接决定一切。采用两阶段策略：

**第一阶段：硬约束过滤**

- 不在授权 Scope 内：拒绝；
- 所需权限或人工确认未满足：阻塞 / 请求确认；
- 前置条件不成立：不允许 claim；
- 重复或等价 Intent 已在运行：合并、等待或抑制；
- 资源、时间、速率或工具面不满足：延后或明确标记不可执行；
- 目标已达成或已被证伪：关闭相关候选。

**第二阶段：软价值排序**

优先考虑：

1. 对 Engagement Goal 的相关程度；
2. 当前风险与潜在影响是否重要；
3. 是否能弥补关键覆盖缺口；
4. 是否能确认 / 反驳一个关键 Hypothesis；
5. 是否可能减少重大不确定性或改变后续行动；
6. 是否复用已有证据或揭示新资产关系；
7. 可执行性、预计资源成本和当前环境稳定性；
8. 最近是否已经尝试过等价动作，是否已出现停滞信号。

现有 VOI 作为其中一个有界因子，不直接成为任务价值的全部。`w<=0.30`、缺失值处理、确定性 tie-break 和 `w=0` 兼容性继续保留。未完成离线校准前，不得为了 Benchmark 分数或单次跑分结果提高 VOI 权重。

### 4.4 Decision Trace（决策依据记录）

建议为每次有实质影响的调度决策增加可查询的决策追踪记录。它是**审计/解释投影，不是新的状态权威**，可先复用现有 journal / audit 机制，不急于增加新表。

最小字段：

- `decision_id`、`run_id`、`goal_id`；
- 被评估候选的 Intent / Hypothesis / Gap ID；
- 被引用的信息和证据 ID；
- 硬约束过滤结果及原因；
- 排序特征快照（相关性、缺口、VOI、成本、风险、进展状态）；
- 最终选择、被延后的候选和原因；
- 预算 / lane / timebox；
- 执行后的 outcome 引用。

禁止只记录模型的一段自然语言理由。需要保留可核对的对象引用、结构化约束结果和稳定快照，便于事后判断排序是否合理。

---
## 5. 多阶段渗透与资产连续性

### 5.1 以 Goal DAG 组织长期工作

多阶段渗透不应该被压缩成一个超长 Intent，也不应该让每个 Intent 自己维护一份无法关联的局部上下文。建议采用 Goal DAG：

```text
Engagement Root Goal
   ├── Asset / Surface Coverage Goals
   ├── Investigation Goals
   │      ├── Hypothesis A
   │      └── Hypothesis B
   ├── Validation Goals
   ├── Impact Assessment Goals
   └── Reporting / Closure Goals
```

每个阶段由一个或多个 Intent 完成。Intent 的 `preconditions` 表达真实前置条件，`yields` 表达预期产出类型，`depends_on` 表达工作依赖。一个 Goal 可跨多个 Run / sibling context，但跨 Run 关联仍受现有权限和只读查询限制；本期不放开跨 Run 任意关系边。

### 5.2 资产身份与 locator 分离

建议把第五轮复盘中提出的“题号 ↔ 容器实例 ↔ 地址 ↔ 时间”映射抽象为通用资产身份机制：

- 逻辑资产身份稳定；
- 地址、域名、端口、容器 IP 是带有效时间的 locator；
- 容器 / 云实例 / 部署版本使用独立 instance identity；
- 所有 Observation 指向逻辑资产或实例，不以可能复用的 IP 作为唯一 subject；
- Scope 校验使用当前授权资源映射，不靠历史 IP 字符串推断；
- 资产身份变更和 locator 复用必须保留时间线，不覆盖历史观测。

这项工作必须与 scope guard 一起设计和测试，不能只改 InfoItem `subject` 字段而不改变授权判定。

### 5.3 凭据、令牌和敏感产物的跨阶段传递

跨 Intent 的凭据连续性需要解决，但不应把秘密写入图节点、InfoItem 摘要、Prompt、普通日志或报告。

建议：

- 图 / InfoItem / Intent 只保存不可逆引用（credential handle / artifact ref）和必要元数据；
- 秘密内容保存在受控秘密存储或现有安全工件机制中；
- 读取需基于 Run、Project、Scope 和 Worker lane 授权；
- 访问记录写审计，但不记录秘密正文；
- 过期、撤销、轮换和权限变化必须有明确状态；
- Worker 完成或被取消不应导致引用丢失，也不能让新 Worker 自动获得超出任务授权的秘密。

这是多阶段工作的基础设施，不是专为某个 B 组题目增加的特殊逻辑。

### 5.4 Attack Path 表达边界

攻击路径视图可以作为**派生读模型**，帮助操作者理解资产、信任边界、已证实暴露和阶段间依赖，但不能把所有图边都解释为攻击路径：

- `supports`：证据支持关系，不代表网络可达；
- `references`：信息引用，不代表权限或信任关系；
- `same_subject / same_session`：上下文关联，不代表攻击步骤；
- `depends_on / yields / spawns`：工作流关系；
- 资产拓扑关系必须由明确观测或来源记录支撑。

如果要呈现攻击路径，应由受约束的关系类型、已验证资产关系和明确的 Intent / Goal 依赖共同构建，并标注每一段的证据状态：Observed、Inferred、Validated、Unknown。不要新增一个含义模糊的通用 `pivot` 边来掩盖证据缺失。

---

## 6. 进展、停滞与资源管理

### 6.1 从“InfoItem 增量”升级为“多类型有效进展”

现有 `watchProgress` 以信息层增量作为重要停滞信号，已经解决部分长期占槽问题。但不同任务类型的有效进展不一定立即产生新的 InfoItem。建议定义统一 `ProgressSnapshot` 读模型，聚合以下进展信号：

| 信号 | 例子 | 需要的证据 |
|---|---|---|
| Observation | 新服务响应、页面、协议行为 | 新 InfoItem / 原始观测引用 |
| Asset | 新资产、服务、身份或实例关系 | 资产图节点 / 关系与来源 |
| Belief | Hypothesis 新增、状态变化、明确排除 | Belief ID + 生命周期变化 |
| Evidence | 新基线、控制组、验证证据、矛盾证据 | Evidence / InfoItem 引用 |
| Analysis Artifact | 新二进制分析、反编译、配置解析或离线产物 | Artifact ID + 来源 + 哈希 / 版本 |
| Coverage | 新增已探测或已验证的 coverage cell | Coverage epoch 差异 |
| Workflow | 阶段 Goal 完成、Intent 依赖解除 | Goal / Intent 状态变化 |
| Finding | 通过报告门的新 Finding | Finding ID |
| Negative Result | 有效地排除一种假设或一条路径 | 明确 baseline / 方法 / 环境 / 结论引用 |

不能把纯日志、重复输出、无变化的重试或模型声称“已经分析”计作有效进展。

### 6.2 进展评估必须感知任务类型

不同任务使用不同的进展窗口和信号权重：

- Web / API 验证：新响应差异、端点覆盖、认证状态变化、有效控制组；
- 网络 / 协议分析：新主机、服务、协议状态、拓扑或路由证据；
- 二进制 / 固件分析：新符号、调用关系、反汇编 / 反编译产物、可复现分析结论；
- 身份 / 云配置评估：新主体、权限关系、策略差异和可验证配置证据；
- 多阶段任务：阶段依赖解除、可授权的新资产 / 新身份引用、目标状态推进。

进展守卫应使用任务 profile 决定“什么算进展”和多久复核一次，而不是用单个全局阈值判断所有任务。现有 30 分钟 execution lane 合同下限、worker timebox、typed kill、owner 围栏和退出确认继续有效；任何修改都必须通过单独 ADR 和回归，不允许因某轮 Benchmark 节奏擅自缩短既有合同。

### 6.3 循环与重复动作检测

为每次有副作用或成本较高的动作形成稳定执行指纹：

`fingerprint = hash(intent_kind, target_identity, method, normalized_parameters, relevant_input_refs)`

当同一指纹在相同状态快照下重复且没有有效进展时：

1. 先读取上次结果和失败原因；
2. 检查是否有新的前置条件、信息或环境状态；
3. 没有新输入时，禁止无意义重试或要求 Planner 明确提出变化；
4. 超过重试预算后转为 blocked / exhausted，并记录原因；
5. 若环境变化或操作者明确要求，可在新证据和新预算下重新开放。

该机制应适用于重复请求、重复探测、重复分析和重复创建 Intent，不只限于 Benchmark submit。

### 6.4 资源决策不是“永远优先最高分”

真实渗透任务的价值来自风险、影响、授权目标、覆盖缺口和证据质量，不能用 Benchmark 分值作为通用排序。

建议在硬约束之后采用多目标排序：

- 任务目标相关性；
- 风险严重性与潜在影响；
- 资产暴露与可达性证据；
- 覆盖缺口的重要程度；
- 可能获得的决策性信息；
- 任务可行性与资源成本；
- 当前任务是否停滞、是否有新的结果空间；
- 是否需要保留探索预算以发现未知区域。

排序需有稳定 tie-break，输出决策依据。对高风险、破坏性或可能影响生产的操作，不允许通过高价值分数跳过 HITL 或安全策略。

---

## 7. 信息空间的真正价值：建立“信息 → 决策 → 结果”的可测链

### 7.1 当前能力与待证明事项

按现有规格，IG0–IG7 已落地：统一读视图、三类激活、Planner 消费、聚合与原子链接、Intent 级 VOI、Gap 候选、上下文压缩和信息热力图均有对应实现与测试。R8.1–R8.4 也已包含 Intent 契约、结论事务、上下文分层、注入硬化、缺口召回、审计和韧性机制。

因此，下一步不应只是增加查询数量，而要验证以下问题：

- 哪些决策真正读取了信息空间？
- 哪些被读取的信息进入了某条 Hypothesis / Evidence？
- 哪些证据使已有 Hypothesis 被支持、反驳或关闭？
- 哪些结果改变、创建、取消或重排了 Intent？
- 哪些历史信息避免了重复动作？
- 哪些信息虽然被检索，但最终没有对决策产生影响？为什么？
- 信息缺口、冷路径 incomplete 或查询截断，是否影响了 Planner 的判断？

### 7.2 建议新增的消费遥测

不要求每个 Worker 每轮机械点亮三次；只需记录实际发生的消费因果链。

```text
info_query
   ├── query_id / query_type / filters / layer / run_id
   ├── returned_ids / truncated / incomplete / source
   ↓
info_read
   ├── info_item_id / detail_layer / source
   ↓
belief_change
   ├── hypothesis_id / old_state / new_state / evidence_refs
   ↓
decision_change
   ├── goal_id / candidate_ids / chosen_intent_id / reason_codes
   ↓
execution_outcome
   ├── intent_id / progress_refs / result_state / cost / elapsed
   ↓
verdict_or_goal_update
   └── finding_id / objective_state / unresolved_gaps
```

遥测是审计投影，不是决策权威。结构化引用必须可验证；不要依赖 LLM 自述“我使用了某条信息”。能通过实际读 API、Evidence 变更和 Intent 更新关联的，以这些权威写路径为准。

### 7.3 核心指标

| 指标 | 含义 | 价值 |
|---|---|---|
| Information Read-to-Use | 读取的信息中，被 Hypothesis / Intent 引用的比例 | 判断检索是否被消费 |
| Evidence-to-Belief Transition | 新证据引起 Belief 状态变化的比例 | 判断证据是否有效 |
| Decision Change Rate | 信息读取后创建 / 重排 / 取消 Intent 的比例 | 判断信息是否改变行动 |
| Duplicate Action Rate | 相同目标和输入下无新增证据的重复动作比例 | 衡量无效探索 |
| Dead-end Revisit Rate | 已明确排除的路径被再次无理由尝试的比例 | 衡量历史复用 |
| Coverage Progress | 目标资产 / 技术面 / 阶段的覆盖推进情况 | 衡量态势理解完整性 |
| Evidence Provenance Rate | 结论有来源引用的比例 | 衡量可辩护性 |
| Goal Closure Quality | Goal 关闭时是否有确定性判据和所需证据 | 衡量任务完成质量 |

不要追求 Information Read-to-Use 越高越好：有些信息读取后合理地被判定为无关。应把“未使用原因”结构化分类，而不是逼迫模型消费所有检索结果。

---
## 8. 工具能力与专业化执行

### 8.1 能力矩阵而不是单一 Worker Profile

当前通用 Worker 能执行多个任务，但专项任务需要不同工具能力。建议维护受版本管理的 `CapabilityProfile`，用于调度候选与执行前校验。

每个 Profile 至少描述：

- 任务类型 / 分析领域；
- 可用工具和工具版本；
- 运行环境约束（网络、文件、架构、权限）；
- 输入 / 输出 Artifact 类型；
- 允许的操作类别和 Scope 约束；
- 资源需求、超时和并发上限；
- 结果如何产生 InfoItem、Evidence 或 Analysis Artifact。

Profile 只能说明“具备什么能力”，不能放宽授权和工具安全链。

### 8.2 优先补齐的能力面

基于第五轮复盘，当前明确需要单独验证的是：

- **二进制 / 固件分析**：工具链完整性、架构识别、反汇编 / 反编译、调试、工件引用与可复现分析；
- **多阶段任务**：跨阶段资产身份、身份/凭据引用、阶段 Goal 依赖、容器实例连续性；
- **后利用评估**：在授权规则内，判断已验证访问能力能否支持预定影响评估；不把单点 RCE 自动等同于整个 Engagement 完成；
- **云与身份评估**：主体、权限、策略、资源之间关系的可追溯建模。

补工具时必须同时完成容器镜像验收、运行时可用性测试、Scope 约束回归、产物归档和信息消费测试。仅确认某个二进制“安装成功”不算能力完成。

### 8.3 Worker lane 与能力解耦

Lane 定义权限边界，CapabilityProfile 定义能力。两者不能混为一体：

- 一个 lane 可承载多个 profile，但不能继承超出 lane 的权限；
- 一个 profile 可被多个 Worker 使用，但每个 Worker 仍受自己的 scope、owner、budget 和 lease 约束；
- control-plane lane 的工具面不能通过任务指令获得目标执行权限；
- offline analysis profile 不应拥有网络访问能力，除非任务和授权明确允许；
- Verifier 继续保持所属 Agent 的 client 和受限工具面隔离。

---

## 9. 报告、外部评估与 Benchmark Adapter

### 9.1 安全 Finding 与外部评估结果必须分离

建议严格区分：

**Security Finding**

- 由 `report_vulnerability` 门管线接受；
- 有漏洞类别、受影响资产、证据、验证标签和严重性；
- 遵循现有 Verifier 与报告规则；
- 是渗透测试报告的安全结论。

**Evaluation Result（外部评估结果）**

- 由 Benchmark 平台 / 测试 Harness 返回；
- 记录外部题目 ID、提交状态、得分、提交时间和外部响应引用；
- 可以引用相关 Goal、Intent、Observation 或 Finding；
- 不得自动创建、接受或改变 Security Finding；
- 平台得分与报告结论不一致时，显示为待对账，而不是静默合并。

### 9.2 Benchmark Adapter 责任

Benchmark Adapter 负责：

1. 读取题目清单与题目约束；
2. 将每个测试任务映射到既有 Goal / Coverage Goal；
3. 提供平台特有的容器、控制面 API、外部状态和评估结果；
4. 为测试任务提供有限时间 / 容器资源约束；
5. 将外部评测结果写入独立的 Evaluation Result 读模型；
6. 产出可与 Run、Goal、Intent 和 Finding 对照的测试报告。

它不负责定义真实渗透任务的全局价值函数。离开 Benchmark 后，核心系统仍然能围绕授权 Engagement Goal 运作。

### 9.3 测试结果解释规则

每个失败必须至少归入以下之一：

- `not_scheduled`：没有获得执行机会；
- `blocked_by_scope_or_policy`：被授权 / 安全边界阻止；
- `environment_unavailable`：网络、容器、目标或平台环境不可用；
- `capability_missing`：当前工具 / profile 不具备必要能力；
- `hypothesis_not_generated`：未形成有价值的待验证主张；
- `execution_failed`：执行动作失败；
- `evidence_insufficient`：没有得到足够证据；
- `verification_failed`：独立验证未通过；
- `result_not_reconciled`：外部结果与内部状态尚未对账；
- `time_or_budget_exhausted`：预算到期；
- `operator_stopped`：操作者停止。

这能防止把所有失败都归结为“模型能力不足”，也避免因单轮 Benchmark 的特殊约束改变通用渗透架构。

---

## 10. 安全、不变量与故障处理

本提案强化而不改变当前安全边界。

### 10.1 必须继续成立的不变量

1. 所有资产和操作都必须落在明确授权范围内；
2. Scope guard、redirect、dial、sandbox、proxy fail-closed 和环境剥离不能被 Planner / Worker / MCP 绕过；
3. 高影响操作的确认要求由操作者端点写入，不提供模型可调用的 force / any 旁路；
4. InfoItem 的 inferred 必须有 supports，不能升级为 observed；
5. Finding 接受与标签只由报告门管线产生；
6. Intent 生命周期只经图仓库合法迁移和 `ConcludeIntent`；
7. Goal 达成由确定性评估器或明确的人工判据决定；
8. worker owner、lease、timebox、typed kill 和退出确认继续有效；
9. 事件、activity、热力图和决策追踪都是审计 / 读模型，不取代状态权威；
10. PG 继续是唯一持久态，不引入跨库分布式事务。

### 10.2 失败必须可恢复且不伪装成功

- PG 不可用：Planner / Worker 退避，Run 进入可恢复 pause；
- InfoItem 冷路径 lag：返回 incomplete / gap reason，不把不完整结果当成空结果；
- Worker 取消：记录 typed reason、owner、lease 和退出确认；
- 目标环境变化：重新校验 asset identity、locator 和 scope，不复用过期定位符；
- 证据冲突：并存并关联，不静默覆盖；
- 任务超时：保留最后有效进展、未完成假设和失败原因；
- 人工停止：立即停止受影响的执行，并保留审计状态。

---

## 11. 运行验收：从代码正确转向渗透能力正确

### 11.1 四层验收模型

| 层级 | 要证明什么 | 验收方式 |
|---|---|---|
| C1 结构正确 | 图、ledger、Intent、Evidence 和投影一致 | 单测、集成测试、`/graph/validate` |
| C2 执行正确 | lane、scope、lease、预算、终止和工具面正确 | 工具守卫测试、故障注入、运行日志 |
| C3 认知正确 | 信息被恰当地解释、假设能更新、证据可追溯 | 证据金样本、冲突样本、盲测案例 |
| C4 任务有效 | 系统能完成授权目标并给出可复核结果 | 端到端实验、人工复核、跨场景对比 |

C1 通过不代表 C4 通过。`/graph/validate` 只能证明图和权威投影的一致性，不证明漏洞发现质量或任务目标完成质量。

### 11.2 主要指标

**授权与安全指标**

- 未授权工具调用：0；
- scope 越界：0；
- 确认门绕过：0；
- 高风险操作缺少确认：0；
- 敏感凭据进入普通日志 / 图摘要 / 非授权 Prompt：0。

**信息与证据指标**

- accepted Finding 的 Evidence 来源完整率；
- inferred 信息具有非空 supports 的比例；
- 关键结论的独立复测覆盖率；
- 过期信息被错误当作当前事实的比例；
- 信息检索 incomplete / truncated 的显式处理率；
- 信息读取到 Belief / Intent 变化的可追溯率。

**渗透任务指标**

- 目标资产与攻击面覆盖率（附未覆盖原因）；
- 高价值假设验证 / 排除率；
- 有效 Finding 数与复核通过率；
- 重复动作率、无进展资源消耗比例；
- 从首次有效观察到首个可验证结论的时间；
- 多阶段 Goal 连续完成率；
- 运行结束时未解决风险与限制的完整披露率。

**资源与运行指标**

- Worker slot 有效工作时间占比；
- 预算在各目标 / 任务 / lane 之间的分布；
- 因 owner / lease / environment / scope 导致的阻塞分类；
- Worker 取消后确认退出的比例；
- Planner 候选膨胀、重复 Intent 和失败重试次数；
- 单个任务长期占用资源但缺少有效进展的比例。

### 11.3 Benchmark 指标仅作为适配器指标

Benchmark 仍可记录总分、完成题数、类别分布、启动数、槽位占用、错误提交和平台截止处理。但这些指标必须与上面的任务 / 认知 / 安全指标并列展示，不能单独成为系统质量的定义。

跑分复盘时至少分开报告：

- 已获得执行机会的任务表现；
- 未获得执行机会的任务及原因；
- 环境 / 工具能力未覆盖的任务；
- 执行失败、证据不足和验证失败；
- 外部评估状态与内部 Finding / Goal 状态之间的差异。

---
## 12. 实施路线

采取增量改造，每批都应有代码验收、运行验收、回滚点；不把以下里程碑描述为已实现。

### P0 — Mission Continuity（目标连续性）

目标：在不改权威对象的前提下，让长期 Engagement Goal 能跨多个 Intent 稳定推进。

工作项：

1. 审查 Goal / Intent DAG 当前是否能表达父子目标、阶段依赖、阶段完成和失败恢复；
2. 为 Goal 建立明确的 required / optional / manual 判据和未完成原因；
3. 将 Coverage Gap 与阶段缺口关联到具体 Goal；
4. 给跨 Intent 任务引入稳定资产 / 实例引用；
5. 凭据与敏感工件只跨阶段传递受控引用；
6. 设计跨阶段任务的 Run / sibling context 授权范围和审计；
7. 先用一个小型、完全授权的多阶段实验验证，不以 Benchmark 题目数作为唯一验收。

验收：

- 阶段 1 的产出能被阶段 2 通过 ID 引用；
- Worker 结束、取消或容器 IP 变化不会使逻辑资产身份丢失；
- scope guard 仍然能准确允许或拒绝当前 locator；
- 前置条件未满足时后续 Intent 不可 claim；
- 部分阶段完成不会把根 Goal 误标为 met。

### P1 — Decision Trace & Information Value（信息消费可观测）

目标：回答“信息空间究竟有没有改变决策”。

工作项：

1. 为 `search_info` / `read_info_item` 增加结构化 query/read trace；
2. 把信息读取与 Evidence、Hypothesis 变化通过 ID 关联；
3. 把 Hypothesis 变化与 Intent create / reorder / stop 关联；
4. 记录决策快照、候选、硬约束结果和选择原因；
5. 记录信息没有被使用的原因类别；
6. 在真实 run 中比较“有信息激活”与“仅基础 overview / prior work”的差异，不靠主观印象评估。

验收：

- 任一有实质影响的决策都能追溯到输入证据或明确标注“无新增信息”；
- 对信息读取导致的行动变化可以通过结构化 ID 关联；
- 同一不可变输入快照得到稳定的确定性候选与 tie-break；
- 查询 incomplete / truncated 时，Planner 不得把它当成完整空结果。

### P2 — Typed Progress & Anti-loop（任务类型感知的进展）

目标：在保留当前 20 分钟默认停滞机制与 30 分钟执行合同下限的前提下，减少误杀有效分析和无效循环。

工作项：

1. 定义 `ProgressSnapshot` 读模型；
2. 汇总 Observation / Asset / Belief / Evidence / Artifact / Coverage / Workflow / Finding 信号；
3. 为 Web、协议、二进制、多阶段等 Profile 定义进展信号权重和观察窗口；
4. 为重复副作用动作建立执行指纹和预算；
5. 区分“活动但未进展”“有分析进展但未产出 InfoItem”“环境阻塞”“真正停滞”；
6. typed kill 后继续执行 owner 围栏、退出确认与终态结算。

验收：

- 有有效 Artifact / Belief / Coverage 进展的分析 Worker 不会仅因 InfoItem 暂无增量被误杀；
- 相同状态下重复动作会被识别；
- 超时、预算、人工停止、租约失效、父 Run 取消分别有稳定终态原因；
- 回收之后资源能被其他 Goal / Intent 使用，且不会出现旧 Worker 改写新 owner 状态。

### P3 — Asset Identity & Specialist Capability（资产身份与专项能力）

目标：补齐复杂任务跨阶段所需的身份连续性和工具能力。

工作项：

1. 稳定资产 ID 与 locator / instance 时间绑定；
2. 题目容器、云资源或临时环境的逻辑身份映射；
3. 跨 Intent 凭据和 Artifact 受控引用；
4. 二进制 / 固件 / 身份 / 云安全等 CapabilityProfile；
5. 工具镜像构建、版本、执行可用性和结果归档测试；
6. 每种 Profile 对应一个授权实验基准和失败分类。

验收：

- 资产身份在地址变化后仍连续，且历史 locator 可追溯；
- 凭据访问有授权、过期和审计机制；
- 专项工具在真实 Worker 容器内可执行并生成可引用产物；
- 工具缺失能够报告为 capability_missing，而不是错误归因给模型。

### P4 — Decision Calibration（决策校准）

目标：在已有可追溯决策数据后，逐步校准 VOI / 成本 / 风险 / 可行性权重。

工作项：

1. 从真实 run 中构建“候选行动 → 后续信息 / 目标推进 / 资源成本”的数据集；
2. 区分高信息量但无决策影响、降低不确定性但没有任务价值、真正改变路线的结果；
3. 先离线回放，保持确定性与可复现；
4. 通过影子模式记录新策略排序，但暂不控制真实执行；
5. 对比旧排序与新排序的任务质量、覆盖、资源成本和安全影响；
6. 只有通过独立评估后再逐步启用。

验收：

- 新策略不能降低授权和安全指标；
- 同一输入快照的排序稳定；
- 能说明相较旧策略为何改变选择；
- 不能只用 Benchmark 分数提升作为唯一证据。

---

## 13. 推荐的验证实验矩阵

| 实验类型 | 验证重点 | 主要观察结果 |
|---|---|---|
| 单资产、单漏洞类 | 现有七环与报告门是否稳定 | 证据完整、验证可复现、重复动作率 |
| 多入口 Web 应用 | 资产 / endpoint 覆盖和历史复用 | Coverage、Hypothesis 转换、信息消费链 |
| 多阶段授权实验环境 | Goal DAG、跨 Intent、身份和 Artifact 连续性 | 阶段依赖、身份映射、终态准确性 |
| 网络 / 协议实验环境 | 多资产定位和环境变化 | locator 时效、Scope 一致性、拓扑来源 |
| 二进制 / 固件实验环境 | 专项工具与离线分析进展 | 工具可用性、分析 Artifact、误停率 |
| 长时运行实验 | 停滞、取消、恢复和资源管理 | 有效 slot 时间、typed kill、旧 owner 隔离 |
| 对抗性信息实验 | Prompt 注入、蜜罐、错误信息和矛盾证据 | observed / inferred 区分、冲突处理、验证拒绝 |
| Benchmark 回归 | 外部环境下的综合集成表现 | 分数、启动覆盖、外部状态对账、失败分类 |

每个实验都应预先定义：授权范围、初始条件、可重复步骤、基线版本、待验证架构假设、数据采集点和失败解释。单次跑分不能同时证明所有层级。

---

## 14. 设计决策与保留项

### 14.1 本提案明确建议

- 保留 CORE 七环和 State / Operation / Obligation 原语；
- 保留探索图、资产图、InfoItem 和 Evidence 的现有权威分工；
- 保留 PostgreSQL 单库、统一信息空间视图和确定性查询；
- 增强跨阶段 Goal / Intent 连续性，而不是新增平行工作流权威；
- 新增任务组合级 Exploration Decision Controller，但先从确定性规则、决策追踪和影子评估开始；
- 用多类型 ProgressSnapshot 改善停滞判断；
- 增加信息消费因果链遥测；
- Benchmark 采用 Adapter，外部评估结果独立于 Security Finding。

### 14.2 暂缓决定

- 是否新增独立的 DecisionRecord 表，还是先复用 domain journal / audit；
- 是否将多阶段任务状态提升为新的 Goal 子类型，或仅使用现有 Goal + Intent 边和 contract；
- 是否将 Attack Path 作为独立只读投影，以及其边类型的最小集合；
- 是否把 CapabilityProfile 放在编译期注册、配置文件或数据库读模型中；
- ProgressSnapshot 的具体任务 profile 与阈值；
- VOI 从 Hypothesis / Intent 层扩展到 Goal / Engagement 层的校准方法。

这些问题必须先做接口和数据流验证，再决定 schema 迁移；不能先增加一批节点类型再寻找用途。

### 14.3 不改变的冻结区

除非单独提交 ADR 并完成回归，不得修改：

- Verifier 判定语义与所属 Agent 工具面；
- `report_vulnerability` 闸门顺序、Finding 标签和严重性规则；
- HITL 确认语义；
- `ConcludeIntent`、claim 谓词、precondition / confirmation 语义；
- Evidence 的 `kind` 与 `nature` 双维度、40 条 / 4096 字符上限；
- PG 唯一持久态、事件非权威、River 仅异步补全；
- 六环安全链、扫描隔离、SSE 单向约束；
- 不引入第二数据库、pgvector、A* / MCTS / POMDP。

---

## 15. 结论

Xalgorix 的核心竞争力不应被定义为“能在 Benchmark 里提交多少个 Flag”，而应是：

**它能否在明确授权范围内，持续构建可信的目标态势，提出可检验的安全假设，利用证据选择合理行动，跨阶段组织复杂工作，在执行中保持安全与可恢复，并最终给出可复核的安全结论。**

现有 CORE / 信息即图 / R8 已经为信息、证据、Intent 生命周期和审计建立了坚实底座。本阶段真正需要补强的是：

1. 长期 Goal 如何跨 Intent 和 Worker 连续推进；
2. 多阶段资产、实例、凭据和工件如何保持身份与授权一致；
3. 决策器如何在多个探索方向之间做有依据的选择；
4. 如何识别真实进展而不是把活动误当进展；
5. 如何证明信息空间确实改善了行动和安全结论。

Benchmark 应持续用于验证这些能力，但不应反过来定义系统本身。以真实授权渗透任务为中心，以 Benchmark 作为受控实验手段，才是这套架构应当坚持的方向。

---

## 附录 A：与现有文档的关系

| 现有文档 | 本提案的关系 |
|---|---|
| `CORE_PHILOSOPHY.md` v3.4 | 上位宪章；本提案不覆盖其对象权威和安全不变量 |
| `INFO_AS_GRAPH_SPEC.md` v0.2 | 信息空间实施规格；本提案重点增加信息消费效果验证 |
| `INFO_GRAPH_CONTRACT.md` R8 | Intent、上下文、确认门和运行期合规契约；本提案继续遵守 |
| `CORE_LOOP_AUDIT.md` | 2026-11-22 历史运行审计；用于定位信念消费和运行验收问题，不作为 11-24 后当前状态的唯一依据 |
| `CORE_LOOP_OPTIMIZATION.md` | M1/M4/M5 运行兑现路径；区分已落地实现与后续运行验收 |
| `BENCHMARK_OPTIMIZATION.md` | 第四轮问题与整改记录；部分问题在后续版本已修复 |
| `BENCHMARK_ROUND5_ANALYSIS.md` | 第五轮真实运行复盘；用于识别多阶段、资产身份、专项工具与报告/外部评估分离问题 |

## 附录 B：设计依据与来源边界

本提案中的“现状”以用户提供的文档为准，尤其是各文档中明确标注的 2026-11-24 落地状态。架构层新增内容（Goal DAG 强化、Exploration Decision Controller、ProgressSnapshot、Decision Trace、CapabilityProfile、Benchmark Adapter）均为**本提案建议**，不是对现有代码已实现能力的陈述。

实现前应以代码、测试和当前运行数据再次核实，不得仅凭本设计文档覆盖实际事实。
