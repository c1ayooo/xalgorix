# Planner 观测、纠偏与停滞治理（轻量设计草案）

> **状态**：草案，未实现。本文只定义目标行为、判定口径与实施边界，不改动当前代码。
>
> **定位**：替代“execution profile”式的场景枚举。场景差异由**任务自带的两段提示词**表达；planner 只做规划与纠偏；安全与状态一致性仍由代码不变量保证。
>
> **边界**：图/信息空间的查询能力由 `docs/INFO_AS_GRAPH_SPEC.md` 定义（另一会话维护）。本文不定义存储、读视图、迁移或检索算法，只定义 planner 如何消费这些查询、如何观测 worker、何时纠正或终止。

---

## 0. 结论

1. **不引入 execution profile。** 不用全局枚举区分 CTF / 渗透；任务的两段提示词（Goal、Instruction）就是场景声明。
2. **planner 不执行。** planner 只有规划、检索、steer、kill、增删 intent 的能力；工具面从结构上禁止执行与报告。
3. **planner 与主 agent 共享只读全局视图。** 通过 `INFO_AS_GRAPH_SPEC` 的确定性查询看 intent、belief、fact、finding、asset、coverage、worker 状态与偏差，而不是只看计数器。
4. **提示词管意图，代码管边界。** 目标、优先级、时限、停止条件由提示词表达；lane 工具面、具体 target 授权、worker 上限、超时结算、scope 拦截由代码强制。
5. **4 小时是上限，不是预期。** 是否继续由“有没有信息层进展”决定；停滞时 planner 先 steer，再 kill，并留下原因与半成品。
6. **planner 自身不空转。** 规划轮事件驱动；调用 LLM 前先做廉价判断；连续无决策时指数退避。
7. **主 agent 也是决策入口，不是事件流显示器。** 它拥有与 planner 同级的规划/纠偏动作（增删 intent、steer、kill、调整优先级、决定收工），作为人工接管入口；但它读的是图与投影摘要，不订阅、不展示原始事件流。人工决策优先于 planner。

---

## 1. 任务提示词：两段，按 lane 路由

现有任务模型已有 `Goal` 与 `Instruction`，不新增字段。

### 1.1 职责

| 提示词 | 回答的问题 | 消费者 |
|---|---|---|
| `Goal` | 什么算成功、优先级、终止条件、可接受的产出 | planner；主 agent；所有 worker（只读） |
| `Instruction` | 范围、凭据、平台操作、时间/并发限制、禁止项、交付物 | planner / 主 agent 全文；worker 按 lane 取子集 |

优先级：**Goal > Instruction > planner 推断**。planner 不得用推断覆盖任务原文；冲突时保留冲突并记录 deviation。

### 1.2 lane 小节约定

`Instruction` 使用固定小节，渲染器确定性切分，不让模型自行摘取：

```markdown
## Goal
## Rules            # 所有 lane 共享：范围、禁止项、凭据、时间
## Control-plane    # 只有 control_plane_api lane 可见
## Execution        # 只有 container/web/host 执行 lane 可见
## Analysis         # 只有 analysis lane 可见
```

- planner 读全文；
- worker 只拿 `Goal + Rules + 自己 lane 的小节`；
- 没有对应小节时，worker 只拿 `Goal + Rules`；
- 控制面操作说明（start/close/submit）不得进入执行/分析 lane 的 prompt。

这条是 run5 的直接教训：分析 lane 拿到了整段平台操作手册，于是自己去 start/attack/submit。仅靠“请勿越界”的提示词不够，必须从输入上切干净。

### 1.3 与工具面的关系

提示词描述意图，工具面执行边界。两者必须一致：

- `Control-plane` 小节 → `SurfaceOrchestrator`（HTTP + notes）；
- `Execution` 小节 → `SurfaceWorker`（执行工具）；
- `Analysis` 小节 → `SurfaceAnalysis`（notes/ledger/研究，无 terminal/HTTP/browser）。

提示词里写了但工具面没有的能力，worker 做不到；工具面有但提示词没授权的目标，由 target 授权与 scope 拦截。

---

## 2. planner 的职责与观测输入

### 2.1 职责

- 把 Goal/Instruction 分解成可调度 intent；
- 维护 intent 契约（target、target_kind、timebox、precondition、yield、objective_ref）；
- 观察执行偏差，做四类动作：`add_intent`、`retire_intent`、`steer_work`、`kill_work`；
- 决定“继续、换路、收工”，但不执行、不报告 finding、不伪造 flag。

### 2.2 观测输入（有界）

每轮只取摘要与结构化信号，不读原始 transcript：

- 图 counters：open/running/done/blocked/exhausted；
- intent 列表：状态、owner、lease、target、target_kind、objective_ref、`last_progress_at`、`stall_signals`；
- worker 列表：lane、unit、运行时长、最近进展时间、当前工具是否活跃；
- 近期 notes 摘要、deviations、ledger 新增 evidence/hypothesis；
- coverage 缺口、asset 新增；
- scope 违规、lane 越界、重复 target、重复失败签名；
- 提交/收工状态（仅 benchmark 类任务有）。

具体查询接口由 `INFO_AS_GRAPH_SPEC` 提供；本文不新增另一套检索。

### 2.3 纠偏动作的约束

- 每轮最多新增 N 个 intent（N = worker 数）；
- 每轮最多 steer / kill 各若干次，避免 planner 抖动；
- `kill_work` 必须带 `reason_code` 与证据引用；
- 引擎侧校验 kill 合法性（见 §4）；
- 人工 steer/kill 优先于 planner。

### 2.4 主 agent：人工决策入口

主 agent 不是“看事件流的聊天窗口”，而是与 planner 同级的决策者。两者共享同一套决策动作，区别只在触发来源：

| 能力 | planner（自主） | 主 agent（人工） |
|---|---|---|
| 读图 / 读 intent / 读 worker / 读 coverage | 有 | 有 |
| 新增 / retire intent | 有（受每轮上限） | 有（人工确认） |
| steer worker | 有 | 有 |
| kill worker | 有（引擎校验） | 有（人工优先，仍需 reason） |
| 调整优先级 / 标记方向耗尽 | 有 | 有 |
| 决定收工 / 继续 | 建议 | 最终决定 |
| 执行工具、报告 finding | 禁止 | 禁止 |

输入与展示原则：

1. **读状态权威与投影，不读事件流。** 主 agent 使用图、intent/worker 投影、notes/deviations/coverage 摘要；`run_events` / SSE 只作审计与观测，不作为聊天上下文或决策依据。
2. **决策收件箱，而非滚动日志。** 界面展示“需要人决定的少数事项”：停滞待 steer、待 kill、被阻塞方向、偏离、越界、planner 提案、可续跑半成品；不展示 token 流和每条工具事件。
3. **人工动作有幂等键与 actor。** 每次决策记录 actor=human、时间、目标 unit、动作、理由；引擎按当前状态做 CAS，过期动作返回冲突而不是覆盖 planner 的新状态。
4. **接管与交还。** 人工可以直接决策；需要连续人工主导时，可暂停 planner 的自主规划（`planner pause`），处理完再恢复。暂停期间图仍可读、worker 仍按已发决策执行。
5. **人工不能绕过硬边界。** 人工 kill/steer/add intent 仍受 lane 工具面、target 授权、scope、并发上限与报告门约束；人工决定只改变“做什么”，不改变“允许怎么做”。

冲突处理：

- 人工决策 > planner 决策 > 默认策略；
- 同一 unit 的并发决策以图状态 CAS 为准，后到的冲突动作返回“已被处理”，不静默覆盖；
- planner 每轮读取人工决策记录，把人工意图当环境事实，不重复提案。

---

## 3. 进展账本：什么算“有进展”

只统计**信息层增量**。以下不算进展：token 数、assistant 输出、工具调用次数、心跳、重复读取同一内容。

### 3.1 进展信号

- 新增 evidence / hypothesis / finding；
- 新增 InfoItem（observed 或带支持的 inferred）；
- note 内容发生实质变化（非重复写入）；
- coverage 新增端点/参数/资产；
- 新 asset 或资产关系；
- 成功提交/得分（仅 benchmark 类任务）；
- 生成了后续 intent 所需的确定性产出（如 container_addr、flag、凭据、可用会话）。

### 3.2 每个 intent 维护

```text
last_progress_at      最近一次信息层增量的时间
progress_events       本次运行累计的增量事件（有界计数）
stall_signals         连续无进展工具调用、重复错误签名、重复 target、lane/scope 违规
active_tool           当前是否有合法长工具在跑（带自身超时）
```

这些是**投影/观测数据**，不是新的状态权威；intent 生命周期仍以图与 `ConcludeIntent` 为准。

### 3.3 默认停滞阈值

可按 intent 合同覆盖（`stall_after_s`），不引入 profile：

| lane | 软停滞（进入观察） | 先 steer | 硬停滞（允许 kill） | 兜底结算 |
|---|---|---|---|---|
| control_plane_api | 2 min | 3 min | 5 min | 8 min |
| execution（container/web/host） | 10 min | 15 min | 25 min | 30 min |
| analysis | 8 min | 10 min | 15 min | 20 min |

规则：

1. 先 steer，给 5–10 分钟观察窗；一次纠偏无效才允许 kill。
2. 合法长工具在跑时不算停滞；工具返回后仍无增量，停滞计数继续。
3. 有进展的 worker 不允许 kill；引擎校验，不依赖 planner 自觉。
4. kill 走优雅收尾：停止信号 → 落盘 notes/证据/checkpoint → 再强杀；结算带 `partial_result` 与 `resumable`。
5. 4 小时到点仍无进展，引擎直接结算，不必等 planner。

---

## 4. kill 的决策权与边界

### 4.1 planner 或主 agent 决定，引擎校验

`kill_work` 可以由 planner 自主发起，也可以由主 agent 作为人工决策发起；两者使用同一套参数与校验。人工决策优先，但仍需提供理由与证据。

调用方必须提供：

```json
{
  "unit_id": "container-attack:c-09",
  "reason_code": "stagnant | repeated_failure | lane_violation | scope_violation | duplicate | superseded",
  "evidence": ["note:...", "intent:...", "event:..."],
  "steer_attempted": true
}
```

引擎拒绝以下 kill：

- worker 未达到最小运行时（默认 5 分钟）；
- 没有确定性 trigger；
- 最近一个进展窗口内仍有信息增量；
- `steer_attempted=false` 且 reason 不是硬违规（lane/scope）。

### 4.2 kill 之后

- intent 结算为 `exhausted` 或 `blocked`，必须带原因；
- 保留 `partial_result` 与 checkpoint 引用；
- planner 不应立刻用同一脚本重试；应换路（新 intent、新假设、新入口）或明确记录“此路已尽”；
- 同一 target 的重复失败达到上限后，retire 该方向，而不是无限重派。

### 4.3 为什么不是纯 LLM 判断

LLM 可以判断“这条路是否值得继续”，但不能可靠判断“有没有进展”和“是否越界”。前者由进展账本给事实，后者由图/scope 给事实；planner 只做取舍。

---

## 5. 防止 planner 自身空转

### 5.1 状态机

```text
planning ──> executing ──> draining ──> finished
    ▲             │             │
    └── blocked <─┴─────────────┘
```

- `planning`：可以调 LLM 规划；
- `executing`：有 worker 在跑且有进展，不调 LLM，只等 settle/偏差信号；
- `draining`：没有可派工作，等最后几个 worker，不调 LLM；
- `blocked`：确定性原因堵住（无 target、scope 拒绝、依赖缺失），升级给主 agent 或收工闸门，不循环重试；
- `finished`：Goal/收工闸门满足。

### 5.2 规划触发条件

只有以下事件触发一轮规划：

1. frontier 低于低水位（无 open intent 或少于 worker 数）；
2. worker settle；
3. 确定性偏差信号（停滞、越界、重复失败、scope 违规）；
4. 人工消息 / steer；
5. 慢速安全网定时器（5–10 分钟），仅兜底。

### 5.3 调用 LLM 前的廉价判断

不调模型，先回答：

- frontier 是否已高于高水位？→ 直接返回；
- 所有 worker 在跑且无停滞信号？→ 直接返回，等 settle；
- 没有 open intent 且没有可行动作？→ 进入 draining，不调模型；
- 同一 trigger 是否在合并窗口内出现过？→ 合并，不重复规划。

### 5.4 防抖与退避

- 2 秒内的多次 settle 合并为一轮；
- 每轮动作数有上限；
- 连续 K 轮没有新增/退休/纠偏 → 间隔 1 min → 5 min → 15 min；
- 任何 settle、新证据、人工消息立即复位；
- planner 长时间阻塞在无槽位时，不轮询 LLM；由 worker settle 唤醒。

### 5.5 4 小时预算的定位

- 4 小时是单 worker 的上限，不是 planner 的等待时间；
- 实际终止通常由停滞阈值触发，远早于 4 小时；
- 若持续有进展，planner 可显式续期，并记录续期理由；
- 若 4 小时到点仍无进展，引擎兜底结算，不依赖 planner 在线。

### 5.6 人工接管时的 planner 行为

主 agent 是人工决策入口，可以在任意时刻介入。为避免人工与 planner 互相打架：

1. **可暂停自主规划。** `planner pause` 停止新的规划轮，但不影响已发 worker 的执行与已有 intent；`resume` 后 planner 先读人工决策记录再规划。
2. **人工决策即图变更。** 增删 intent、steer、kill、改优先级都走与 planner 相同的图/引擎接口，带 actor、时间、理由。
3. **冲突以 CAS 为准。** 人工和 planner 同时操作同一 unit 时，后到者收到状态冲突，不静默覆盖；人工动作不需要“赢过”已经完成的图事务，但人工可以基于最新状态重新发起。
4. **不做事件流回放。** 主 agent 不读取 `run_events` 作为上下文；它看决策收件箱与图投影。事件流只用于审计与故障排查。
5. **暂停不是安全豁免。** 人工接管期间，lane 工具面、scope、target 授权、并发上限仍由引擎强制。

---

## 6. 与 benchmark 行为的关系

不引入 profile 后，跑分专用行为按以下方式处理：

| 现有行为 | 处理 |
|---|---|
| `platform_finished` 取消整场 | 降级为“停止提交类 intent”的信号；扫描继续，由 planner 决定是否收工 |
| `container_addr` 自动物化 | 泛化为 handoff 物化：任何笔记发布范围内的具体 `host:port`，为未解决项派生执行 intent（按 worker 数封顶） |
| CTF 版 planner prompt | 从系统 prompt 移除；平台 API、token、flag 规则全部写在任务 `Instruction` |
| 60m/30m 场景常量 | 移除；默认上限给足，planner 按 Instruction 的时限设置每 intent `timebox_s`，保留 5 分钟安全下限 |
| 精确 N 背压 | 保留为用户明确要求的行为；若后续发现流水线间隙，可作为可调参数，不引入 profile |

---

## 7. 不可配置的硬不变量

任何提示词、任何 lane、任何任务都不能关闭：

1. lane 工具面：编排 lane 无执行工具；分析 lane 无 terminal/HTTP/browser；执行 lane 无 planner/全局控制工具；
2. 网络执行必须有具体 target；symbolic target（`task`、`task:*`）不构成授权；
3. 项目 Scope / Fence / DenyTargets 优先于 planner 意图；
4. worker 并发上限 = 设置值；
5. 超时必须先优雅收尾、再停、再结算；不允许“intent 已结束、worker 还在执行”；
6. finding 只能经报告门产生；planner、主 agent 与 worker 都不得伪造 flag/finding；
7. 主 agent 拥有决策能力但没有执行面：不能直接调用工具、不能报告 finding、不能把事件流当作决策输入；
8. 人工决策不改变“允许怎么做”：kill/steer/add intent 仍受 lane、target 授权、scope、并发上限约束；
9. 密钥不回传、子进程剥离、六环安全链不变。

---

## 8. 验收口径

### 8.1 行为验收

- planner 在 `executing` / `draining` 且无 trigger 时不调用 LLM；
- 连续无决策时规划间隔按退避增长，触发后复位；
- 停滞 worker 先收到 steer，观察窗内无进展才被 kill；
- 有信息增量的 worker 不会被 kill；
- 每次 kill 都有 `reason_code`、证据引用、partial 结果；
- worker prompt 只包含自己 lane 的小节；
- analysis lane 在 tool 层无法调用 terminal/HTTP/browser；
- benchmark 任务结束后只停止提交类 intent，不取消整场扫描（除非任务 Instruction 明确要求收工）；
- 主 agent 可以在不订阅事件流的情况下看到决策收件箱，并执行 add/retire/steer/kill/优先级/收工决定；
- 人工决策与 planner 冲突时，后到动作返回状态冲突，不静默覆盖；
- `planner pause` 期间不产生新的规划轮，已有 worker 与 intent 不受影响，`resume` 后能读到人工决策。

### 8.2 观测口径

- 每轮 sample 输出：planner 状态、trigger、LLM 调用次数、退避间隔；
- 每个 intent：`last_progress_at`、停滞时长、steer 次数、kill 原因；
- 每次 kill：原因、证据、partial 引用、是否可续跑、发起者（planner/human）；
- 每次人工决策：actor、unit、动作、理由、结果（applied/conflict/rejected）；
- 误杀率：被 kill 后 10 分钟内同一 target 出现新进展的次数（目标为 0）。

### 8.3 回归口径

- benchmark 任务使用显式标注的提示词运行，分数只作参考；
- 设计一致性优先：无孤儿 worker、无 lane 越界、无 scope 越界、done intent 有 yield、finding 有 evidence；
- 渗透任务：scope 内可发现、scope 外被拒、HITL/Fence 正常、报告可生成。

---

## 9. 实施顺序（建议）

1. **提示词 lane 路由**：`Goal/Rules/Control-plane/Execution/Analysis` 渲染 + 工具面一致性；不改图与存储。
2. **进展账本**：由现有 note/ledger/coverage/asset 事件更新 intent 进展投影；不新增状态权威。
3. **planner 状态机与触发**：事件驱动、廉价前置判断、合并、退避；先不引入新查询，使用现有 overview。
4. **kill 校验与优雅收尾**：reason code、steer 前置、grace、partial。
5. **主 agent 决策入口**：与 planner 共用决策接口；决策收件箱、actor/CAS、planner pause/resume；不接事件流作为输入。
6. **接入 `INFO_AS_GRAPH_SPEC` 查询**：等并行会话的 `Region/Associate/ContextSlice` API 定型后，通过接口接入 planner 与主 agent，不并发修改其检索实现。
7. **benchmark 行为降级**：`platform_finished` 只停提交；handoff 物化泛化；CTF prompt 迁入任务 Instruction。

---

## 10. 与并行会话的边界

`docs/INFO_AS_GRAPH_SPEC.md` 拥有：

- `internal/infoitem/**`、信息空间读视图与迁移；
- `internal/graph` 的读接口扩展；
- `Region/Associate/ContextSlice` 查询与 `search_info`；
- planner 现有“词法召回”缺口的替换实现。

本文只定义 planner 的策略与观测口径。落地时：

- 不修改上述文件；
- planner 的查询接入通过一个小接口完成，等对方 API 定型；
- `internal/orchestrator/planner.go` 的 overview/prior-work 段如需改动，先对齐文件归属，避免两端同时编辑。

---

## 11. 待定问题

1. `last_progress_at` 的写入方：由引擎在既有 note/ledger/coverage 事件处更新，还是由信息空间读视图派生？倾向引擎事件更新，避免新增写权威。
2. 停滞阈值是否允许任务 Instruction 用结构化小块覆盖（如 `stall_after_s`），还是只靠 planner 按自然语言设置？
3. 合法长工具的“活跃”判定：按工具自身超时，还是要求工具输出有进度标记？
4. kill 后的续跑：新建 intent 引用 partial，还是允许同一 intent 重新认领？倾向新建 follow-up intent，保留原 intent 的 exhausted 与 partial。
5. 主 agent UI 如何展示“停滞中 / 已 steer / 待 kill / 可续跑”，避免人工与 planner 同时操作同一 unit。
6. 人工与 planner 的仲裁细则：人工默认是否自动暂停 planner，还是只在同一 unit 冲突时暂停？倾向前者可选、后者强制。
7. 决策收件箱的保留策略：人工未处理的事项保留多久、是否跨 run 延续、是否需要“稍后处理”状态。
8. 主 agent 的图查询深度：默认返回摘要还是允许按需下钻 detail；如何避免把大段证据拉进聊天上下文。

---

## 12. 实施状态（2026-11-24）

已实现：

| 项 | 实现 |
|---|---|
| 提示词 lane 路由 | `internal/orchestrator/taskbrief.go`：`## Goal/Rules/Control-plane/Execution/Analysis`（含中文别名、fence 保护、无标题向后兼容），worker 只拿 Goal + Rules + 本 lane 小节 |
| planner 廉价门控 | `Planner.round()` 先查 `Repo.Counts`；frontier 满、drained、无 trigger 时不调用模型；首轮失败会重试而不是永久 drained |
| planner 退避 | 1m→2m→5m→15m；settle/stall/实际动作复位；notify 2s 合并 |
| planner 决策工具 | `steer_work`、`kill_work`（封闭 reason_code）已加入 tool 定义与执行；`ScanSession` 已接 `SteerWorker` / `KillWorkerWithReason` |
| 主 agent 决策入口 | main chat 新增 `steer_work` / `kill_work` / `retire_intent`；`retire_intent` 仅 open→deleted，CAS，actor=human；不接事件流 |
| 进展账本 | `internal/scanctx/progress.go`：Touch/MarkProgress/AgentTimes/Snapshot/BindUnit；工具结果统一打点，只有信息层 mutation 计进展 |
| 停滞投影 | `Pool.StallReport()` + 15s 扫描，新停滞触发 `Planner.Stall()`；planner overview 渲染 bounded `STALLED WORKERS` |
| kill 校验 | `KillWorkerWithReason`：stagnant 且 2 分钟内有进展则拒绝；其它 reason 放行 |
| 优雅超时 | `Config.WindDown`（默认 30s）：steer → 等待完成 → 未完成才 kill；grace 内完成则正常 conclude |
| platform_finished 降级 | hook 不再取消扫描，只停当前提交 worker；planner 拒绝新的 `/submit` `/start` intent；容器物化不再因该 marker 停止 |
| benchmark prompt 隔离 | 仅当配置了 ControlPlaneHosts 时附加平台/挑战规则；engagement 提示词不含 control_plane/container/challenge 词，READY CONTAINER TARGETS 也不渲染 |
| 决策收件箱 API | `GET/POST /api/scans/{id}/decisions`：stalled（Pool.StallReport）、blocked/open（图）、deviations（审计）；动作 steer/kill/retire/reprioritize 路由到 live agent；不订阅事件流 |
| 决策收件箱 UI | 探索图内“决策”面板：stalled 可 steer/kill，blocked/open 可 retire/reprioritize；15s 轮询，仅运行中 |
| reprioritize | main chat 与决策 API 均支持；`Repository.UpdateNode` 改优先级，不改状态，actor=human |
| Scope/Asset v1 | 仅当项目 `Fence.Enforce=true` 时启用；支持精确域/后缀/`*.domain`/IPv4/CIDR、端口白名单、DenyTargets；http_request 与 terminal/python 的 host 目标硬拦截；`target_kind=scope` 需在授权范围内 |

验证：`go build ./...`；`go test` 全绿（orchestrator/runtime/config/graph/scanctx/agent/web）；orchestrator 与 scanctx 的 `-race` 绿。

尚未实现/已知限制：

- Scope v1 只检查初始目标：HTTP 重定向每一跳、browser_action/page_agent 尚未纳入范围校验；裸命令里的无管理 TLD 主机为兼容 Python 标识符而跳过（URL/显式 host:port 仍检查）。
- Scope/CIDR/wildcard intent 校验通过，但不会自动物化成具体 host intent（发现→派生仍需 planner 或后续 handoff 物化）。
- 停滞阈值按任务 Instruction 的结构化覆盖；当前按 lane 固定默认值。
- 长工具“即将超时”的主动提示；当前用 last touch 近似 `ActiveTool`。
- 决策收件箱暂不展示待确认的 main-chat intent 提案（提案仍在 chat 流程内确认）。
