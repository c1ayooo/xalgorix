# Xalgorix 实现问题与设计约束审阅

日期：2026 年 11 月 24 日  
适用对象：架构负责人、后端及运行时开发、渗透能力负责人、测试人员  
审阅版本：用户提供的 `xalgorix-wt-20261124.tar.gz`  
源码归档 SHA256：`d32d9c450481349dcbbcd721953ecfa08f25f9fd06f1edc33794e289d2c57df3`

## 1 结论与整改目标

当前实现存在能够直接解释 worker 空转与 planner 纠偏失效的控制流断点。最优先的问题是 worker 没有与运行时匹配的正常完成协议，以及池满时 planner 的纠偏轮也被生产侧背压阻塞。进展计时、取消结算、信息图消费者接线进一步影响调度判断。

HITL 的工具执行前审批和 MITM 转发前审批主路径已经存在，但高危识别、策略热更新及故障放行边界不足以支持“所有高危操作都必须经过审批”的承诺。项目范围在分发、重定向、浏览器和运行中更新之间不一致。任务代理的初始安装和热切换也存在明确接线缺陷。

整改目标是恢复授权渗透任务的完整闭环：任务目标与授权范围进入系统，planner 基于可读取的实际进展决策，worker 执行并保存证据，单元能够正常完成或安全停止，报告及收工依据经过确定性校验。跑分用于检验平台能力和暴露问题，不应把固定题数、flag 或分数策略变成普通渗透任务的通用硬规则。

### 1.1 优先处理顺序

1. 修复 XALG001 和 XALG002，使 worker 能正常退出且 planner 在满池时仍能纠偏。
2. 修复进展计时、取消确认和租约丢失处理，保证 worker 生命周期与实际执行一致。
3. 统一 HITL、scope 和代理身份及热更新协议，消除不同执行通道的策略差异。
4. 补齐主 Agent 和 planner 的信息读取、图投影刷新及 objective 收工判定。
5. 使用普通渗透场景与 benchmark 共同验收覆盖、长链和工具能力。

### 1.2 问题级别与证据口径

- **P0**：阻塞核心工作闭环，应优先修复。
- **P1**：影响正确性、授权边界、审计或关键能力，应在能力评估前修复或明确裁决。
- **P2**：配置语义或可用性问题，可与相关模块整改合并处理。
- **隔离复现**：使用本快照实际函数及必要测试桩确认局部行为，不等同于整个产品端到端验收。
- **静态确认**：代码分支、调用链或缺失接线可直接确认，运行影响仍受触发条件约束。
- **待集成验证**：代码缺少所需保证，但需 PG、Docker 或真实网络环境确认具体残留或并发后果。
- **设计限制**：实现遵循现有明确规则，但规则不足以满足平台能力或安全保证，需要独立设计裁决。

清单共 26 项，其中 2 项 P0、23 项 P1、1 项 P2，包含实现缺陷、设计限制和需要集成验证的保证缺口。问题状态均为“待处理或待裁决”，本次未修改业务代码。

## 2 审阅基线与验证范围

### 2.1 设计基线

按 CORE 顶部较新的 R7 与信息即图修订理解权威边界，再对照具体实施规格；不以较旧段落覆盖较新修订。

- [CORE_PHILOSOPHY](<docs/CORE_PHILOSOPHY.md>)：七环、字段权威、R7 图权威、信息激活与收工义务。
- [INFO_AS_GRAPH_SPEC](<docs/INFO_AS_GRAPH_SPEC.md>)：统一读空间、消费者与里程碑验收。
- [INFO_GRAPH_CONTRACT](<docs/INFO_GRAPH_CONTRACT.md>)：intent 合同、claim、ConcludeIntent 与运行期不变量。
- [DUAL_GRAPH_ORCHESTRATION](<docs/DUAL_GRAPH_ORCHESTRATION.md>)：默认 planner worker 拓扑与角色工具面。
- [PLANNER_OBSERVATION_AND_CORRECTION](<docs/PLANNER_OBSERVATION_AND_CORRECTION.md>)：进展、停滞、纠偏和主 Agent 决策职责。
- [REDESIGN](<docs/REDESIGN.md>)：项目授权边界、D5 HITL 和 D10 目标义务。

### 2.2 跑分证据的适用范围

[BENCHMARK_OPTIMIZATION](<docs/BENCHMARK_OPTIMIZATION.md>) 记录第四轮 5400 分及整改；[BENCHMARK_ROUND5_ANALYSIS](<docs/BENCHMARK_ROUND5_ANALYSIS.md>) 记录第五轮 8600 分、38 道启动、32 道完成、449 次非真实 flag 提交等事实，并追加了后续整改状态。

这些是文档记录的历史运行结果，本次未对原始 PG 数据及平台 CSV 独立对账。归档包含后续整改代码，因此不能把历史问题未经复核直接认定为当前代码仍存在，也不能把“已落地”表格当作默认拓扑已完成运行验收。

### 2.3 本次已完成的验证

源码安全解包至 本地审阅目录（未入仓），未读取或创建 `.git`，未执行任何 Git 操作。

独立验证目录为审阅侧复现目录 `xalgorix-review-checks-20261124`（Windows 本地路径，不属于本仓库；下文 §13 出现的 review-checks 路径均指它）。审批纯函数和 ProgressStore 的已有测试通过；新增隔离用例确认满池纠偏阻塞、首轮非法 JSON 后停止规划、重复笔记计进展、停滞 kill 降级、worker 结束协议冲突、代理身份错配及 CIDR 判定差异。

本地 Go 为 1.22.1，项目声明 Go 1.26.0 及 toolchain 1.26.11。未运行完整模块测试、PG 集成测试、Docker 端到端测试及真实目标扫描。独立测试以仅依赖标准库的文件集运行，禁用工具链下载、依赖下载与 VCS 查询。测试中的危险命令仅作为判定器字符串输入，从未执行。

## 3 问题索引

| 编号 | 级别 | 问题 | 性质与证据 |
|---|---|---|---|
| XALG001 | P0 | Worker 缺少正常完成当前工作的协议 | 实现缺陷 隔离复现与静态调用链 |
| XALG002 | P0 | 满池背压阻塞 planner 停滞纠偏 | 实现缺陷 隔离复现 |
| XALG003 | P1 | 首轮非法 JSON 后误判 drained | 实现缺陷 隔离复现 |
| XALG004 | P1 | 进展按工具名称计数而非真实信息增量 | 实现缺陷 隔离复现与静态确认 |
| XALG005 | P1 | 长工具活跃状态与停滞回收不协调 | 实现缺陷 静态确认 |
| XALG006 | P1 | 停滞 kill 被拒后降级为普通 kill | 实现缺陷 隔离复现 |
| XALG007 | P1 | 取消后缺少执行退出确认 | 保证缺口 待 Docker 集成验证 |
| XALG008 | P1 | 租约丢失后旧 worker 继续执行 | 实现缺陷 静态确认 |
| XALG009 | P1 | 默认拓扑缺少持续图投影刷新 | 实现缺陷 静态确认 |
| XALG010 | P1 | 信息图索引 ID 无法由 planner 详情工具解析 | 消费者接线缺陷 静态确认 |
| XALG011 | P1 | 主 Agent 查询能力与全局决策职责不匹配 | 设计兑现缺口 静态确认 |
| XALG012 | P1 | 主动收工没有重新执行 required objective 判定 | 实现缺陷 静态确认 |
| XALG013 | P1 | GET 无条件放行限制高危操作审计能力 | 设计限制 判定器复现 |
| XALG014 | P1 | 编码后的危险载荷漏判 | 实现缺陷 隔离复现 |
| XALG015 | P1 | Scope baseline 规则无法覆盖提前放行请求 | 规则接线缺陷 隔离复现与静态确认 |
| XALG016 | P1 | Audit mode 热切换未同步工具审批包装 | 实现缺陷 静态确认 |
| XALG017 | P1 | Enforce 下审批基础设施故障仍可能放行 | 明确故障策略 需设计裁决 |
| XALG018 | P1 | 项目围栏未覆盖各实际网络执行路径 | 实现覆盖缺口 静态确认 |
| XALG019 | P1 | 收紧项目范围未更新运行中的 scope 副本 | 热更新缺陷 静态确认 |
| XALG020 | P1 | CIDR 在分发和运行时判定不一致 | 实现缺陷 隔离复现 |
| XALG021 | P1 | 正常任务启动可能跳过代理 pin 安装 | 实现缺陷 静态确认 |
| XALG022 | P1 | 代理热切换混用 instance ID 和 ScanContext ID | 实现缺陷 隔离复现 |
| XALG023 | P1 | 容器自动物化被历史地址复用记录压制 | 实现缺陷 静态确认 |
| XALG024 | P2 | 进展回收禁用值被默认值重新覆盖 | 配置缺陷 静态确认 |
| XALG025 | P1 | 任务 enforce 的 scope observer 仍被全局 off 压制 | 策略接线缺陷 静态确认 |
| XALG026 | P1 | Native browser 的上游选择可能绕过本地 MITM | 路由接线缺陷 静态确认 |

## 4 Planner 与 Worker 生命周期问题

### XALG001 Worker 缺少正常完成当前工作的协议

**级别与性质**：P0，实现缺陷。工具面与回复处理已隔离复现，完整结束链由静态调用关系确认。

**设计要求**：worker 完成一个已认领 intent 后应归还结果，由 scheduler 经 ConcludeIntent 结算；主 Agent 保留整场扫描的收工权。

**当前实现**：worker、analysis、orchestrator 工具面没有 finish，任务提示词也禁止调用 finish。与此同时，通用 OnModelResponse 对没有工具调用的非空回复返回 Retry，即使内容是当前工作已经完成。自动 plan 完成和目标完成路径又排除了 delegated agent。单元工作完成后缺少稳定、正常的返回协议。

**触发与影响**：任务已完成或没有可认领工作时，模型尝试提交完成说明，运行时要求继续调用工具。worker 可能继续读取、重复记录或等待预算和错误路径结束，槽位无法及时释放，完成与取消统计混杂。

**证据**：[worker 工具面](<internal/agent/tool_surface.go:64>)、[回复处理](<internal/agent/eino_observer.go:510>)、[任务收尾指令](<internal/orchestrator/pool.go:1275>)、[控制面提示词](<internal/agent/agent_prompt_orchestrator.go:78>)、[delegated 自动收尾排除](<internal/agent/liveness.go:477>)。

**修复建议**：新增单元级 complete_work 或 yield_work，携带产出引用、终态意图、未完成项、partial result 和可续跑标记。运行时收到合法单元结束后停止模型循环；scheduler 校验合同并结算。该协议不得结束根扫描，也不得直接产生 finding。

**验收标准**：执行、分析、控制面三类 worker 均能正常结束并释放槽位；正常完成不依赖 timeout、context canceled 或 no tool abort；根扫描继续运行；done intent 的产出与证据引用有效。

### XALG002 满池背压阻塞 planner 停滞纠偏

**级别与性质**：P0，实现缺陷，已隔离复现。

**当前实现**：stall 可以使 shouldPlan 返回 true，但 round 随后无条件等待 WaitForCapacity。所有槽位都忙时，planner 无法调用模型进行 steer 或 kill。applyStallGuard 为 defer，也需等待当前 round 返回。

**触发与影响**：N 个 worker 占满 N 个槽位，其中一个已经停滞。planner 收到停滞信号仍等待空槽，失去及时终止占槽 worker 的能力。独立测试确认该状态模型调用为 0，直到上下文取消才返回。

**证据**：[规划轮背压](<internal/orchestrator/planner.go:449>)、[槽位等待](<internal/orchestrator/pool.go:317>)、[生产接线](<internal/web/scan_session.go:987>)。

**修复建议**：将“观测与纠偏”和“新增工作准入”分开。满池时允许有界查询、steer、kill、retire 和优先级决策；只限制新增工作。等待容量的生产流程必须能被纠偏和人工接管信号中断。

**验收标准**：满池时注入 stall，下一决策轮可执行纠偏；无需等待其他 worker 自然结束；新增 intent 不突破独立准入上限；既有背压不会产生 intent 无界堆积。

### XALG003 首轮非法 JSON 后误判 drained

**级别与性质**：P1，实现缺陷，已隔离复现。

**当前实现**：模型响应成功即增加 modelRounds，随后才解析计划 JSON。首轮响应格式错误时没有新增 intent，但 shouldPlan 已把“模型曾返回响应”视为完成过规划，空 frontier 被置为 drained，普通 timer 不再重试。该结论以首轮未落下任何 intent 且没有新的 settle、stall 或人工触发为前提；新的外部触发仍可恢复规划。

**影响**：新任务可能在首次规划格式错误后长期没有任何工作。该行为不同于首轮模型调用失败的重试路径。

**证据**：[先计数后解析](<internal/orchestrator/planner.go:501>)、[drained 判定](<internal/orchestrator/planner.go:582>)。

**修复建议**：分别记录模型响应、有效计划解析、有效规划完成和有效空计划。格式错误或候选均无效不等于合法 drained，使用有界纠错和退避。

**验收标准**：首轮非法 JSON、合法但无效 target 的计划、模型调用失败均能恢复重试；合法空计划才进入相应的无工作处理；不得形成无上限重试。

### XALG004 进展按工具名称计数而非真实信息增量

**级别与性质**：P1，实现缺陷，重复笔记计数已隔离复现，其余归因路径静态确认。

**当前实现**：hookProgressLedger 通过 mutation 工具名称和无 error 判断进展，未核对对象是否真正变化。相同 key 和 value 的成功 add_note 可重复刷新时间；工具产生新观测及引擎自动写入 InfoItem 的路径又未统一接入 worker 进展钟。

**影响**：重复保存可以让无产出 worker 保持活跃；有新观测但尚未由模型显式落台账的 worker 可能被判停滞。

**证据**：[进展白名单和 hook](<internal/agent/progress_hooks.go:20>)、[笔记写入](<internal/tools/notes/notes.go:323>)、[进展 store](<internal/scanctx/progress.go:71>)。

**修复建议**：在真实状态变更边界发布进展，携带 unit、对象 ID、内容指纹或版本、来源和产出引用。原始输出不能一律计为进展；应以新观测被接收、有效推断带支持、覆盖变化等可验证增量为依据。

**验收标准**：相同笔记重复写不延寿；相同错误和相同证据重复回放不计新进展；新证据、资产、有效覆盖变化能归属到正确 worker；失败 mutation 不计进展。

### XALG005 长工具活跃状态与停滞回收不协调

**级别与性质**：P1，实现缺陷，静态确认，长任务实际误杀需集成验证。

**当前实现**：StallReport 的 ActiveTool 以最近一分钟是否有工具结果近似。watchProgress 不检查真实在途工具状态。默认进展回收是 20 分钟，而 Python 默认允许 30 分钟，部分 terminal 重工具允许 60 分钟。

**影响**：合法长工具在等待结果时可能被当作闲置。仅提高 worker 总预算无法解决较短停滞计时器的冲突。

**证据**：[ActiveTool 投影](<internal/orchestrator/pool.go:411>)、[回收逻辑](<internal/orchestrator/reclaim.go:118>)、[Python 时限](<internal/tools/python/python.go:84>)、[terminal 时限](<internal/tools/terminal/terminal.go:36>)。

**修复建议**：记录工具开始、结束、合法 deadline、取消状态和可选分段进度。对合法在途工具采用有界等待；工具自身超时、重复失败与模型空转分别治理。

**验收标准**：合法在途长工具不会仅因缺少 tool result 被判停滞；工具到期可终止；工具返回后若仍无有效增量，停滞治理继续生效。

### XALG006 停滞 kill 被拒后降级为普通 kill

**级别与性质**：P1，实现缺陷，已隔离复现。

**当前实现**：killStagnant 调用 KillWorkerWithReason；被拒后会回退到普通 KillWorker。普通入口使用 human 原因，不检查 stagnant 的近期进展保护。

**影响**：worker 在 steer 后保存了新证据，类型化 kill 正确拒绝，随后仍可能被普通 kill 取消；原因语义与实际决策不一致。

**证据**：[fallback](<internal/orchestrator/reclaim.go:170>)、[普通 kill 与近期进展校验](<internal/agent/worker_pool.go:133>)。

**修复建议**：区分接口不存在、基础设施失败和业务校验拒绝。业务拒绝不得降级强杀；收尾宽限期后重新读取进展。人工显式停止与自动停滞回收使用不同、不可混淆的原因。

**验收标准**：近期有效进展导致拒绝后，不调用普通 kill；接口不支持的兼容路径单测独立；所有实际终止有真实 actor、类型及触发依据。

### XALG007 取消后缺少执行退出确认

**级别与性质**：P1，生命周期保证缺口；缺少确认的代码路径可确认，实际容器进程残留待 Docker 集成验证。

**当前实现**：windDown 在发送 kill 后可以返回 timeout，由调用方结算。KillUnit 发送 context cancel，并未等待完成。Docker Exec 在 context 取消时返回 canceled，未在该分支执行容器内进程终止并等待退出。容器内 timeout 包装表达命令自身时限，不等同于任意父上下文取消的退出确认。

**影响**：图中的终态、pool 槽位释放和实际命令停止之间没有可验证的一致性保证。新的工作可能与尚未退出的执行重叠。

**证据**：[windDown](<internal/orchestrator/pool.go:940>)、[KillUnit](<internal/tools/agentsgraph/agentsgraph.go:639>)、[Docker canceled 分支](<internal/runtime/docker_driver.go:845>)、[exec stream](<internal/runtime/docker_client.go:299>)。

**修复建议**：建立 stopping 状态和退出确认，按每次 exec 记录可终止的进程或进程组标识。在共享 toolbox 中只终止该 unit 的执行，不能为回收一个 worker 停掉其他 worker 共用的容器。确认退出后再释放执行资源并结算；保留 context cause 与 partial result。

**验收标准**：Docker 和 native 均使用无害长命令验证 kill 后进程、子进程及目标请求停止；取消后无新流量；图状态和槽位释放有退出确认；不能协作取消的工具有明确终态与隔离策略。

### XALG008 租约丢失后旧 worker 继续执行

**级别与性质**：P1，实现缺陷，静态确认。

**当前实现**：续约失败仅记录日志并继续；返回 owner 已丢失时停止续约，但不停止对应 worker。ClaimNextIntent 又可以回收已过期 running intent。

**影响**：旧 worker 未停时，另一空闲 worker 可能重新认领同一 intent。后续结算冲突不能撤回已经发生的重复网络操作。

**证据**：[续约](<internal/orchestrator/pool.go:833>)、[claim 与回收](<internal/graph/pg.go:262>)、[过期回收](<internal/graph/pg.go:762>)。

**修复建议**：丢失租约时撤销旧执行权，停止并确认旧 worker 退出；续约基础设施失败采用有界暂停或退出策略，避免仍执行但不再拥有合法租约。结算校验 owner 或 lease generation。

**验收标准**：模拟 PG 短暂故障、续约失败和 owner 被替换，同一 intent 不出现两个有执行权的 worker；旧 owner 不能继续产生有效副作用或结算新 owner 的状态。

## 5 信息图与主 Agent 决策问题

### XALG009 默认拓扑缺少持续图投影刷新

**级别与性质**：P1，默认拓扑接线缺陷，静态确认。

**当前实现**：syncExplorationGraph 排除 delegated agent；原来的持续 BeforeTurn 可调用它，但默认根 Agent 进入主聊天循环，主要在退出时 defer 投影。worker 更新共享 ledger，不会自动使该投影持续刷新。

**影响**：worker ledger 与 accepted findings 对应的 belief、fact、finding，以及该投影负责的 goal 节点可能长期落后于内存状态；图上的点亮、unit 视图和合规状态不能据此宣称实时完整。intent、hint、digest 及 intent result fact 仍有其他写入者，不能据此认定整张图都不更新。ledger 直查可缓解部分读取，但没有兑现 R7 图生命周期权威的完整承诺。

**证据**：[投影入口](<internal/agent/graph_projection.go:26>)、[原 BeforeTurn](<internal/agent/eino_observer.go:128>)、[主聊天路径](<internal/agent/main_chat.go:155>)、[默认 Run 分支](<internal/agent/agent.go:2092>)。

**修复建议**：定义默认拓扑下稳定的图状态变更及投影刷新入口。区分同步生命周期权威与异步读模型投影；读模型不完整时显式标注水位，不以“读到了部分聚合对象”代替完整状态。

**验收标准**：没有人工聊天输入时，worker 新增 hypothesis、evidence、finding、资产后，相关消费者在约定时限内可读；owner 与 lease 保持一致；运行期 validate 能发现缺口；不依赖根扫描退出才补齐。

### XALG010 信息图索引 ID 无法由 planner 详情工具解析

**级别与性质**：P1，消费者接线缺陷，静态确认。

**当前实现**：planner information space 块只展示坐标和引用，聚合层 InfoID 是 exploration node 的数字 ID；提示其使用 read_info_item 取详情。Planner.readInfoItem 仅按内存 ledger 的 hypothesis ID 或 evidence ID 匹配，未解析图节点 ID。

**影响**：看到索引不等于能取得相关内容；模型可能得到 not found，改为凭摘要或 notes 决策。统一空间的查询能力存在，但读详情消费者未闭合。

**证据**：[overview 点亮](<internal/orchestrator/planner.go:1068>)、[详情读取](<internal/orchestrator/planner.go:1875>)、[聚合层 ID](<internal/infoitem/postgres/migrations/9004_info_items_instance.sql:25>)。

**修复建议**：提供带 run 和 layer 的统一引用解析器，支持节点、原子信息、ledger 及 archive 引用；执行相同的项目授权检查。索引提示必须指向实际可用的工具和 ID。

**验收标准**：overview 返回的每种引用均可按 ID 读取；跨 sibling、跨层与过期引用结果明确；错误与不存在分开；不能跨项目读取。

### XALG011 主 Agent 查询能力与全局决策职责不匹配

**级别与性质**：P1，设计兑现缺口，静态确认。

**当前实现**：主聊天使用 mainChatToolDefs，而不是通用 SurfaceMain 所有 read 前缀工具。实际工具以 intents、units、findings、hypotheses、hints 列表为主；没有完整的信息检索、笔记、证据正文、工具归档、资产、coverage、deviation 和真实 worker 进展查询。默认 20 行、最多 50 行、单次约 6000 字节，缺少 cursor 和充分的按 ID 详情读取；每次用户输入工具循环最多 6 轮。

**影响**：主 Agent 有 steer、kill 权力，却缺少判断方向、证据与近期执行状态的足够输入；同类状态超过上限后无法完整遍历。决策收件箱 UI 的能力不会自动成为主 Agent 的工具能力。

**证据**：[专用工具定义](<internal/agent/main_chat.go:28>)、[工具循环上限](<internal/agent/main_chat.go:308>)、[读上限](<internal/agent/main_chat.go:967>)、[units 投影](<internal/agent/main_chat.go:1101>)。

**修复建议**：保留有界读取，为 main 和 planner 提供统一只读决策视图，支持按 unit、target、时间、状态过滤、分页和证据详情。worker 状态包括真实 active tool、进展、重复失败、纠偏与取消依据。不得用无限扩大上下文补偿缺失接口。

**验收标准**：超过 50 个同状态 unit 时可完整分页；主 Agent 能从目标 worker 取得作出决定所需的证据与进展，不依赖原始事件流回放；读取受项目与 run 授权约束。

### XALG012 主动收工没有重新执行 required objective 判定

**级别与性质**：P1，实现缺陷，静态确认。

**当前实现**：主聊天 finish 检查未确认提案及 open、running、paused 计数，然后直接 completeWithFinish。planner_worker 的完成评估主要依据图计数，未执行原来 required objective 的完整确定性 goal gate。

**影响**：系统 frontier drained 后，主 Agent 的主动 finish 可能不再被 required 且可判定的未达成目标阻断。ConcludeIntent 的执行回执是合法产出，但不等于目标已达成或 finding 已被接受。

**证据**：[主聊天 finish](<internal/agent/main_chat.go:453>)、[完成评估](<internal/agent/completion.go:94>)、[目标判定规则](<internal/agent/goal_gate.go:101>)。

**修复建议**：区分用户显式停止、资源上限终止和系统主动宣告完成。主动完成必须经过 objective.Evaluate 及证据义务；无法达成时以 blocked 或 incomplete 如实终结。用户停止仍应立即生效。

**验收标准**：仅队列耗尽但 required criterion 未达成时，不能标记正常目标完成；manual 判据和用户显式停止按既有规则处理；所有状态保留未完成目标及证据快照。

## 6 MITM 与容器高危命令 HITL 问题

工具路径在 ApprovalTool 内执行审批中断和恢复，MITM 子进程路径在 roundTrip 前进行审批等待并在拒绝时返回 403。该主路径在代码中存在。当前审计对象是提交给工具的命令源码和可见 HTTP 明文，不是容器 execve 或目标远端命令的逐条强制审计；SSH、原始 TCP 和 WebSocket 后续帧也不因存在 MITM 而获得明文判定能力。

### XALG013 GET 无条件放行限制高危操作审计能力

**级别与性质**：P1，明确设计限制，判定器行为已复现。该项不是“实现未遵循 D5”。

**当前实现**：AssessHTTP 对 GET、HEAD、OPTIONS 先返回 Allow，不检查其携带的破坏性语义。对通过 GET 参数触发的远端命令、删除或状态改变操作，不能提供审批保证。

**证据**：[HTTP 判定](<internal/approval/approval.go:312>)、[设计 D5](<docs/REDESIGN.md:111>)。

**修复建议**：把“普通读取免人工审批”与“危险语义必须审查”分别表达。危险操作识别不能仅由 HTTP method 决定；同时避免将正常 GET 统一变成人工队列。

**验收标准**：高危 GET 进入规定处置，普通读取继续免审且留审计；工具层和 MITM 层使用同一语义；记录命中的规则、解析层与决策原因。

### XALG014 编码后的危险载荷漏判

**级别与性质**：P1，实现覆盖缺口，已隔离复现。

**当前实现**：危险特征与 RawQuery、body 文本做子串匹配，未统一进行与内容类型对应的规范化。POST query 使用百分号编码、form body 使用加号编码时，字面 rm -rf 特征可以漏判。

**影响**：同一操作因序列化形式不同得到不同审批结果。读取方法提前放行之外，写请求也存在该问题。

**证据**：[特征与查询匹配](<internal/approval/approval.go:281>)、[RawQuery 返回](<internal/approval/approval.go:356>)。

**修复建议**：按 URL、form、JSON 等类型做有界解码与结构化检查，同时保留原始字节供审计。限制解码深度和展开大小，不能改变最终发送的请求。

**验收标准**：明文、URL 编码、form 编码及合理 JSON 表达得到一致的风险结果；无关搜索词与普通文件内容不会被简单词命中误拒；解析失败按明确策略处置。

### XALG015 Scope baseline 规则无法覆盖提前放行请求

**级别与性质**：P1，规则接线缺陷，已隔离复现与静态确认。

**当前实现**：broker 只有在 Policy.Evaluate 返回 Require 后才执行 scope observer。普通 POST /upload 可被前层 Allow，但 baseline 对该路由是 deny；普通 POST /api/settings 也可能在 baseline require 之前被 Allow。MITM 的 HighRisk 入口依赖同一前层分类器。

**影响**：后续存在规则不等于实际会执行规则；baseline 的单元测试通过也不能证明生产链路覆盖。

**证据**：[broker 提前返回](<internal/web/eino_approval.go:120>)、[baseline](<internal/approval/scope_rules.go:106>)、[MITM 入口](<internal/trafficproxy/gate.go:95>)。

**修复建议**：先进行授权范围与适用策略的轻量判定，再决定是否需要人工审批；不要以“已经需要审批”为执行另一组独立策略的前提。是否继续使用通用上传 deny，需结合渗透验证能力独立裁决。

**验收标准**：从真实工具入口和代理入口测试规则，而不只直接调用 baseline；所有适用规则均可到达；普通目标的业务路由不会被误当作平台控制面。

### XALG016 Audit mode 热切换未同步工具审批包装

**级别与性质**：P1，热更新缺陷，静态确认。

**当前实现**：broker 及 ApprovalTool 在启动时按有效模式决定是否构造。任务热更新只修改 ScanContext.AuditMode，未重装或替换既有运行时的审批能力。off 变 enforce 时原来未包装的工具仍可能直接执行；enforce 变 off 时原 broker 仍使用显式 enforce policy。MITM 则在请求时读取 runAuditMode。

**影响**：同一任务的工具与请求通道使用不同版本的审批策略，界面热切换状态不能代表全部执行面状态。

**证据**：[broker 构造](<internal/web/eino_approval.go:57>)、[强制 enforce](<internal/web/eino_approval.go:117>)、[运行时包装](<internal/engine/runtime.go:174>)、[热更新字段](<internal/web/task_config_live.go:120>)。

**修复建议**：始终安装可动态读取 run policy 的稳定审批适配器，或提供原子替换机制；所有 worker、子进程代理与新 intent 使用相同 policy revision。明确已经审批和正在等待的调用如何处理。

**验收标准**：在已有 worker 上连续执行 off 到 enforce 再到 off；terminal、Python、HTTP、browser 和 MITM 结果一致；新策略对下一次适用调用生效，旧审批不能授权改变后的新操作。

### XALG017 Enforce 下审批基础设施故障仍可能放行

**级别与性质**：P1，代码明确的故障策略，需要设计裁决，不能视为偶发异常或自动改动的授权。

**当前实现**：MITM gate 对部分 adapter 错误 fail open；scan context 不存在或部分存储不可用时也可能放行。工具和代理的不同入口对故障有不同处理。相关注释仍使用平台可选及扫描不能依赖平台的旧理由，而当前部署已要求 PG。

**影响**：enforce 不能被宣传为“未获批准绝不执行”。故障及归因丢失可能改变安全语义。

**证据**：[代理错误放行](<internal/trafficproxy/gate.go:100>)、[上下文消失放行](<internal/web/proxy_gate.go:58>)、[submit 存储故障](<internal/web/approvals_gate.go:343>)。

**修复建议**：如果产品要求强制审批，高危调用在策略或审批状态不可读取时应阻断或进入可恢复等待；恢复后重新校验。observe 模式的可用性策略与 enforce 的执行保证分别规定。显式处理无法归因流量。

**验收标准**：故障注入覆盖审批创建、读取、决策写入、上下文销毁及归因失败；enforce 不发送未授权高危操作；恢复无重复执行；界面显示降级或暂停状态及原因。

### XALG025 任务 enforce 的 scope observer 仍被全局 off 压制

**级别与性质**：P1，策略接线缺陷，静态确认；需补任务覆盖全局模式的集成测试。

**当前实现**：newApprovalService 根据进程级 ApprovalMode 保存 s.policy.Mode。任务可以独立设置 enforce，broker 因而构造并按 enforce 判定，MITM 也可以读取任务的 enforce；但 observeScope 一进入就检查全局 s.policy.Mode，非 enforce 时直接 defer。

**触发与影响**：全局 off、任务 enforce 时，项目 deny、approve and remember 规则及内建 baseline 被 observer 跳过。defer 仍可能进入正常人工审批，不能将该路径描述为直接自动执行；实际问题是任务级策略没有贯穿同一审批链。

**证据**：[全局模式保存](<internal/web/approvals_gate.go:141>)、[任务有效模式创建 broker](<internal/web/eino_approval.go:62>)、[observer 全局模式门](<internal/web/scope_observer.go:49>)。

**修复建议**：向 observer 传入该 run 已解析的有效策略及版本，或从统一 policy provider 读取，不在子步骤重新使用全局开关覆盖任务级模式。与 XALG016 的动态包装问题分别回归。

**验收标准**：全局 off、任务 enforce 下，项目 deny 仍自动拒绝，已记住的 allow 正常生效，未匹配项进入人工审批；全局 enforce、任务 off 时按任务选择执行；两种入口和后续热切换保持一致。

## 7 任务创建后的资产围栏问题

项目中的资产记录、扫描入口 target 和实际执行授权不是同一个对象。新发现资产只应形成待分类信息；是否可执行仍应由有效项目 scope、排除项、端口和任务授权判断。创建任务成功不能替代运行中的目的地址校验。

### XALG018 项目围栏未覆盖各实际网络执行路径

**级别与性质**：P1，覆盖缺口，静态确认。操作者机器保护等既有检查仍存在，不能将缺少项目 scope 复查表述为“所有范围外地址都可访问”。

**当前实现**：初始 HTTP 有 engagement scope 检查；checkHTTPHop 检查本机、lane 和端口，但没有同等项目 scope 复查；browser 和 page agent 的项目范围覆盖不完整。terminal/Python 主要依赖字面目的地址提取。planner 的 scope kind 校验也未统一应用于普通 web/container kind。

**影响**：范围内初始请求跳转到范围外、浏览器后续导航、动态命令目的地址及派生 intent 的授权语义可能不一致。部分路径仍受其他 guard 限制，但不足以构成项目级完整围栏。

**证据**：[初始请求 scope](<internal/agent/agent_guard.go:445>)、[redirect hop](<internal/agent/agent_guard.go:593>)、[命令字面检查](<internal/agent/agent_guard.go:522>)、[planner kind 检查](<internal/orchestrator/planner.go:1534>)。

**修复建议**：统一 scope 判定器，并在意图写入、dispatch、每次实际目标请求和每一跳重定向使用；将实际目的地址与 payload 数据区分。需要强制子进程围栏的部署，应提供明确的实际出口控制，不能只靠字符串扫描作完整保证。

**验收标准**：范围内首次请求返回范围外 Location 时阻断；浏览器新导航、JS 触发请求、容器 HTTP 和子进程路径均有适用测试；deny target、端口与操作者机器规则不因代理或人工审批被扩大。

### XALG019 收紧项目范围未更新运行中的 scope 副本

**级别与性质**：P1，热更新缺陷，静态确认。

**当前实现**：Agent、planner 和 pool 在扫描启动时保存 scope 副本。项目 PUT scope 更新数据库并提示后续审批立即采用新 fence，但没有更新这些运行中副本。高危审批可读取新 fence，普通免审调用仍可能采用旧 scope。

**影响**：运行中排除某目标后，不同调用路径对其是否仍获授权存在分歧；原来未启用 fence 的 run 也不会自动得到新的 Agent scope 适配器。

**证据**：[启动 scope 副本](<internal/web/scan_session.go:537>)、[更新项目范围](<internal/web/project_scope.go:102>)、[审批时 fence 查询](<internal/web/scope_fence.go:321>)。

**修复建议**：使用统一、可原子更新的 scope revision 或共享策略 provider。收紧范围应立即撤销后续执行权；需要时停止越界在途工作。扩大范围必须有明确授权流程。

**验收标准**：任务已创建且 worker 正在运行时收紧范围，下一次普通 GET、terminal、browser 和新 intent 均使用新策略；扩大和缩小的审计可追踪；项目查询失败时不静默取消围栏。

### XALG020 CIDR 在分发和运行时判定不一致

**级别与性质**：P1，实现缺陷，已隔离复现。

**当前实现**：Web targetHostPort 将不带 scheme 的 slash 后内容截去，CIDR 范围被截成网络地址；hostInScope 仅做相等或域名后缀匹配。Agent scopeEntriesMatch 保留 CIDR 并做 net.ParseCIDR。

**复现**：范围为 10.20.30.0/24，目标为 10.20.30.4。Web 分发及复用该 matcher 的 fence 判 false，Agent matcher 判 true。

**影响**：合法内网目标可能不能分发或被审批 fence 拒绝，与“支持 CIDR”的能力声明不一致。

**证据**：[Web 解析和匹配](<internal/web/handlers_platform.go:1500>)、[Agent CIDR](<internal/agent/scope_guard.go:105>)。

**修复建议**：移除重复授权语法实现，集中支持域名、明确子域规则、IP、CIDR 和端口，保留原始范围类型，不先按 URL 截断 CIDR。

**验收标准**：创建、预览校验、运行分发、scheduler、HITL 和实际出口对同组 CIDR、wildcard、IP、域名及端口样本给出一致结论。

## 8 网络出口代理热切换问题

### XALG021 正常任务启动可能跳过代理 pin 安装

**级别与性质**：P1，实现缺陷，静态确认；独立复现目录尚未包含完整任务启动测试。

**当前实现**：初始 SetScanUpstream 位于 backfillSessionTaskCoordinates 内。该函数在 ProjectID 已存在时直接返回，且设置代理的代码嵌在原 auditMode 为空的分支。正常任务运行通常已经带有项目坐标与有效 audit mode，因而可跳过该初始化；没有另一个正常任务启动时的对应 pin 安装点。

**影响**：任务的代理选择虽已校验并保存，实际出口可能仍继承全局代理或直连状态。需要区分任务保存成功和出口安装成功。

**证据**：[backfill 入口](<internal/web/scan_session.go:253>)、[条件内的代理安装](<internal/web/scan_session.go:288>)、[正常上下文赋值](<internal/web/scan_session.go:331>)。

**修复建议**：将 run 代理安装移到统一启动流程，不依赖旧数据修复和 audit mode 是否为空。使用 sctx.ID 注册，在任何目标流量前完成有效代理选择，并检查 required 模式。

**验收标准**：全新正常任务分别选择代理 A、代理 B、inherit 和 direct；实际出口符合选择；带 ProjectID、TaskID、audit mode 的正常路径也生效；恢复路径与新任务一致。

### XALG022 代理热切换混用 instance ID 和 ScanContext ID

**级别与性质**：P1，实现缺陷，已隔离复现。

**当前实现**：在 instance ID 与 ScanContext ID 不同的正常身份模型下，scanUpstreams 和出口查询以 ScanContext ID 为键，热切换却传入 h.instanceID；关闭隧道也传 instance ID，而隧道记录的是 ScanContext ID。

**复现与影响**：已有 scan pin 为代理 A，任务改成 B 后，按实际 scan ID 查询仍返回 A；改为 inherit 也无法清除旧 pin。用 instance ID 关闭已有 scan 隧道返回 0。任务配置和真实网络出口分离。

**证据**：[热切换与隧道关闭](<internal/web/task_config_live.go:131>)、[pin 写入](<internal/web/task_config_live.go:208>)、[出口注册与读取](<internal/proxy/scanpolicy.go:38>)、[隧道键](<internal/trafficproxy/manager.go:434>)。

**修复建议**：使用 h.scanCtx.ID 更新出口和关闭该扫描隧道；若一个 run 同时有多个活跃 context，通过明确映射覆盖各 context。全局池替换、任务 pin、已建立浏览器和长连接分别定义生效语义。

**验收标准**：A 到 B、B 到 inherit、允许时到 direct 均在下一次连接使用新出口；旧隧道按正确 context 清理；其他 run 不受影响；required 模式不因热更新回落直连；实际观测到的 upstream 与任务配置一致。

全局代理池替换函数已存在，新解析调用可以取得新池；这不能代替任务 pin 安装与热切换的验收。既有浏览器、长连接、显式代理地址和已启动子进程也需单独验证，不能把“设置已保存”表述为“所有在途流量已经切换”。

### XALG026 Native browser 的上游选择可能绕过本地 MITM

**级别与性质**：P1，路由接线缺陷，静态确认；实际流量及 wire HITL 需浏览器集成验证。

**触发条件**：native 执行面、ProxyRequired 为 false、调用方没有显式指定 proxy、UpstreamFor 返回有效出口代理，且预期启用本地 MITM。

**当前实现**：ensureBrowser 先从 UpstreamFor 将 proxy 填成外部出口。之后进入本地 MITM 的分支要求 proxy 为空，因此被跳过；native launcher 把这个外部地址直接配置为 Chromium 的 proxy server。该控制流与附近注释所述“开启拦截时使用 run listener”不一致。

**影响**：该条件下浏览器目标流量可能不进入本地 MITM，wire 观测、归因及 wire 高危审批无法执行；工具层对 execute_js 等操作的审批仍然存在，不能表述为浏览器所有操作都免审批。这也影响既有浏览器的代理切换验收。

**证据**：[先填外部出口](<internal/tools/browser/browser.go:449>)、[本地入口空值条件](<internal/tools/browser/browser.go:490>)、[Chromium 代理参数](<internal/tools/browser/browser.go:550>)。

**修复建议**：将浏览器连接本地观测入口和本地入口选择最终出口分开。启用拦截时优先使用 run scoped listener，由代理内部选择出口；显式外部代理或关闭观测的模式必须与审计保证一致并可见。

**验收标准**：MITM 开启且配置上游时，native browser 的无害目标请求经过本地 listener，观测中有正确 run 和 upstream；由浏览器实际发出的适用高危请求在发送前等待审批；热切换后出口一致，其他 run 不受影响。

## 9 资源调度与配置问题

### XALG023 容器自动物化被历史地址复用记录压制

**级别与性质**：P1，实现缺陷，静态确认。

**当前实现**：materializeContainerIntents 将所有历史 container intent 的 target 放入 existingTargets，不限活跃状态；unit key 也以题号为主。新容器若复用了旧 IP:port，自动物化可能把它当作已有工作而跳过。

**影响**：引擎自动派生路径可能遗漏新容器；planner 仍可能通过其他新 intent 补位，因此不能将该问题扩大为所有新任务必然无法执行。该限制与第五轮记录的地址回收和反复归属复核问题相关，但未独立复演历史运行。

**证据**：[历史 target 收集和去重](<internal/orchestrator/pool.go:538>)、[复盘中的身份问题](<docs/BENCHMARK_ROUND5_ANALYSIS.md:129>)。

**修复建议**：以资源实例和 generation 为身份，地址只是带有效时间的属性。活跃去重、历史失败抑制和允许新证据重试分别处理；close 幂等键也应绑定正确资源实例。

**验收标准**：同 IP:port 先后分配给不同实例时可建立新工作；旧证据不自动迁移为新实例事实；同一活跃实例不会被重复派发；重建、关闭和重试有明确审计。

### XALG024 进展回收禁用值被默认值重新覆盖

**级别与性质**：P2，配置缺陷，静态确认。

**当前实现**：New 将负 WorkerProgressTimeout 改为 0，随后又将 0 改为默认值。注释定义的负数禁用语义无法保持到 watchProgress。

**影响**：操作者认为已关闭进展回收，但默认 20 分钟机制仍然有效。

**证据**：[配置规范化](<internal/orchestrator/pool.go:168>)。

**修复建议**：区分未指定、禁用和显式时长；使用互斥分支或显式 enabled 状态，环境变量解析与构造器保持同一契约。

**验收标准**：未指定使用默认值；off、0 或负值按正式环境契约分别验证；禁用后长时间无进展不触发该计时器，但总预算和人工停止仍有效。

## 10 渗透平台约束与能力裁决

以下项目需要明确产品与架构裁决，不能通过扩大工具权限或关闭所有安全机制一并处理。

| 约束或能力 | 当前判断 | 调整方向 |
|---|---|---|
| 操作者机器隔离与密钥剥离 | 保留 | 统一所有执行路径，区分实际目的地址和目标侧 payload |
| 项目 scope 与 deny | 保留并补齐 | 独立于 HITL，任何免审调用仍须遵守范围 |
| Finding 报告门及三标签 | 保留 | 证据引用和可疑候选完整保留，不由分数直接生成已验证漏洞 |
| 执行 lane 最低 30 分钟 | 固定值偏窄 | 合同区分短侦察、单次验证和长链工作，允许有理由续期 |
| 默认 20 分钟进展回收 | 观测不足时易误伤 | 用有效信息增量及真实长工具状态，保留有界预算 |
| Analysis lane 禁止全部本地执行 | 限制离线二进制及固件分析 | 定义无目标网络的本地分析工具面和产物权限 |
| 通用上传与对象存储写入 deny | 可能限制合法漏洞验证 | 基于任务授权、范围、可清理产物和受控副作用定义验证动作 |
| 显式端口自动成为唯一服务授权 | 适用于精确服务测试 | 已授权主机或网段任务使用明确 scope，入口地址不隐式缩窄全部授权 |
| 主 Agent 每个 intent 需再确认 | 属于明确设计 | 评估已授权任务中的重复交互成本，保持提案与执行授权可审计 |
| Worker 数量代替外部资源配额 | 资源模型不足 | 将扫描并发、容器名额、工具进程及审批等待分开计量 |
| 凭据 会话 访问路径交接 | 长链能力尚需证明 | 使用有来源、有效期和权限的对象引用，建立跨 intent 传递 |
| 逆向工具预装 | 能力准备不足 | 验证 pwntools、checksec、反编译与调试工具的可用性，不把未预装等同于绝对不可执行 |

每题至少启动一次、按分值排序和 flag 提交是 benchmark 适配层要求。普通渗透任务应以操作者目标、授权范围、风险、证据和覆盖义务驱动，不能直接套用固定题库或分数目标。

第五轮 32 道完成与 38 道启动的比例具有选择偏差。B、F2 等未获执行机会时，无法据此评判长链或逆向能力；也不能仅凭已启动题的转化率认定模型能力足够。应分别记录“未进入执行”“执行环境不可达”“证据不足”“执行失败”“发现已成立但目标未完成”等终态。

## 11 分批修复与关闭条件

### 11.1 第一批 恢复调度生命周期

范围：XALG001 至 XALG008，以及相关的 XALG023 和 XALG024。

交付要求：单 worker 完成协议、纠偏独立于新增准入、真实进展投影、在途工具状态、类型化取消及退出确认、租约丢失撤销执行权、资源实例身份和配置语义修复。所有取消记录真实原因，不能把自动回收伪装成人工停止。

关闭条件：满池可纠偏；无工作可正常返回；合法长工具不误杀；重复信息不延寿；kill 不绕过拒绝；图终态与实际执行一致；同一租约没有重复执行者。

### 11.2 第二批 统一授权与热更新

范围：XALG013 至 XALG022，以及 XALG025 和 XALG026。

交付要求：公共 scope 与风险判定入口、编码规范化、baseline 可到达、稳定的动态审批包装、故障模式裁决、scope revision、正确的出口 context 键和热切换观测。

关闭条件：所有适用执行通道对相同策略一致；范围收紧对下一次请求生效；audit 热切换工具和 MITM 同步；代理切换真实出口变化；required 不回落直连；基础设施故障不突破已选定的 enforce 保证。

### 11.3 第三批 完成信息决策闭环

范围：XALG009 至 XALG012。

交付要求：默认拓扑持续投影、统一引用解析、main 与 planner 的分页及详情读取、真实 worker 进展视图、required objective 主动收工判定。

关闭条件：图和内存进展在规定时限一致；索引引用均可解析；主 Agent 可以凭证据执行纠偏；队列耗尽与目标达成分别表示；未达目标的收场如实标记。

### 11.4 第四批 验证渗透能力

在前述关闭条件通过后，使用可控靶场分别验证单点 Web、跨端口服务、凭据交接、长链访问、离线二进制分析和受控文件上传。Benchmark 继续记录分数，同时记录有效工作时间、覆盖机会、取消原因、目标达成和证据完整率。

## 12 综合验收矩阵

| 场景 | 必须观察到的结果 |
|---|---|
| 一个 intent 完成且根任务未结束 | worker 正常完成并释放槽位，根任务继续 |
| 全部槽位占满并注入 stall | planner 能查询和纠偏，不等待自然空槽 |
| 首轮计划返回非法 JSON | 有界纠错与退避，随后可以正常产生工作 |
| 重复保存相同笔记 | 不刷新有效进展钟 |
| 新证据及新资产被接收 | 正确归属到 unit 并计入有效进展 |
| 合法长工具尚在 deadline 内 | 不因缺少工具结果被误当闲置 |
| Stagnant kill 遭近期进展拒绝 | 保持拒绝，不降级普通 kill |
| 取消 Docker 或 native 无害长命令 | 对应执行退出确认，无后续目标流量 |
| 租约被替换或失效 | 旧 worker 不再拥有执行权 |
| 主 Agent 无人工聊天输入 | 图仍持续反映 worker 的新状态 |
| 列表超过 50 条 | 可分页和按 ID 取详情，无永久不可见对象 |
| Frontier 为空但 required goal 未满足 | 不主动宣告目标完成 |
| 高危 GET 和编码后的写载荷 | 按裁决后的同一风险策略处置 |
| 上传及设置路由 | 策略实际执行，不被前层提前 Allow 绕过 |
| 已有任务 audit off 与 enforce 互切 | 下一次适用调用的工具和 MITM 结果一致 |
| 全局 off 且任务 enforce | 项目 observer 的 deny 和 remember 规则仍生效 |
| Native browser 配置上游且开启 MITM | 目标请求经过本地入口，观测及 wire HITL 生效 |
| 运行中收紧 scope | 下一次普通读 写 命令及 browser 行为使用新范围 |
| CIDR 内外地址 | 创建 预览 分发 审批 出口一致 |
| 正常新任务选择特定代理 | 首次目标请求即使用所选出口 |
| 运行中代理 A 切至 B 或 inherit | 实际出口正确，旧隧道按正确 context 清理 |
| 地址回收后新建容器实例 | 新工作可派发，旧事实不串用 |
| 审批或策略服务故障 | 按已裁决保证暂停或拒绝，审计明确且无重复执行 |

## 13 复现材料与复跑方式

### 13.1 已有材料

- 完整测试日志：`xalgorix-review-checks-20261124/results.txt`（审阅侧材料，未入仓）
- 审批策略复现：`xalgorix-review-checks-20261124/approval/review_policy_test.go`（审阅侧，未入仓）
- 调度与 worker 完成协议复现：`xalgorix-review-checks-20261124/orchestration/review_test.go`（审阅侧，未入仓）
- 代理身份与隧道清理复现：`xalgorix-review-checks-20261124/proxy/review_test.go`（审阅侧，未入仓）
- CIDR 判定差异复现：`xalgorix-review-checks-20261124/scope/review_test.go`（审阅侧，未入仓）

日志保留一次补充测试因测试桩遗漏字段导致的编译失败，补齐后 orchestration final 为 exit 0；不能把该中间编译失败归因于项目代码。

### 13.2 用例与问题对应

| 用例 | 对应问题 | 复现边界 |
|---|---|---|
| TestReviewWorkerHasNoCompletionTool | XALG001 | 真实工具面谓词 |
| TestReviewPlainCompletionIsRetried | XALG001 | 真实回复处理函数及必要依赖桩 |
| TestReviewFullPoolBlocksStallCorrection | XALG002 | 真实 round 和 WaitForSlot 及存储模型桩 |
| TestReviewInvalidFirstJSONBecomesDrained | XALG003 | 真实计数及门控函数 |
| TestReviewDuplicateNoteCountsAsProgress | XALG004 | 真实 hook 与进展 store，输入相同成功结果 |
| TestReviewRejectedStagnantKillFallsBackToPlain | XALG006 | 真实 fallback，kill 接口桩拒绝 |
| TestReviewPolicyCoverage | XALG013 XALG014 XALG015 | 真实审批分类和 baseline，未执行命令 |
| TestReviewHotPinUsesWrongIdentity | XALG022 | 真实注册与热切换函数，代理入口桩 |
| TestReviewTunnelCloseUsesWrongIdentity | XALG022 | 真实隧道筛选函数，连接关闭桩 |
| TestReviewCIDRScopeDisagreesAcrossLayers | XALG020 | Web 与 Agent 的真实解析和匹配函数 |

这些测试断言当前缺陷行为会出现，修复后应转换成针对期望行为的回归断言；保留缺陷断言通过不能作为修复完成依据。

### 13.3 独立用例复跑

以下 PowerShell 只运行独立文件集，不运行项目主程序。不要在模块根目录把它替换成全仓测试后声称完成集成验收。

```powershell
$reviewChecks = 'H:\pn ana\xalgorix-review-checks-20261124'
$env:GOTOOLCHAIN = 'local'
$env:GO111MODULE = 'off'
$env:GOVCS = '*:off'
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOFLAGS = '-buildvcs=false'
$env:GOCACHE = Join-Path $reviewChecks 'cache'
$env:GOTMPDIR = Join-Path $reviewChecks 'tmp'
foreach ($reviewSuite in @('approval', 'progress', 'orchestration', 'proxy', 'scope')) {
    Push-Location (Join-Path $reviewChecks $reviewSuite)
    try {
        $reviewGoFiles = @(Get-ChildItem -LiteralPath . -Filter '*.go' | Select-Object -ExpandProperty Name)
        & go test -v @reviewGoFiles
        if ($LASTEXITCODE -ne 0) { throw "Review suite failed: $reviewSuite" }
    } finally {
        Pop-Location
    }
}
```

修复验收还需在项目要求的工具链下运行相关原生单测，并补充默认 planner worker 拓扑、PG 失败、Docker 取消、实际代理出口与浏览器网络路径的集成测试。测试和构建继续禁用 Git 及 VCS 元数据探测。

## 14 整改状态（2026-11-24 第二批）

本节只记录代码现状，不替代真实 PG/Docker/网络集成验收。`go build ./...`、`go test ./...`
与变更包 `-race` 已通过；标注“部分”的项仍有明确缺口。

| 编号 | 状态 | 落地与缺口 |
|---|---|---|
| XALG001 P0 | 已修复 | 新增 `complete_work` 工具（worker/analysis/control-plane 可用，root 被拒）；`AfterTool` 结束本 lane 的模型循环并经 `ConcludeIntent` 结算；worker 提示词改为显式收尾。测试：`TestCompleteWorkIsReservedForDelegatedWorkers`、`TestCompleteWorkEndsDelegatedLane` |
| XALG002 P0 | 已修复 | 去掉 planner 前置 `WaitForCapacity`；`AvailableSlots` 在 `apply` 做准入裁剪；满池无触发时短路。测试见 `planner_test.go` |
| XALG003 | 已修复 | 新增 `planRounds` 只统计有效计划；非法 JSON / 全部被拒的计划不再标记 drained。测试：`TestPlannerRetriesAfterInvalidFirstPlan` |
| XALG004 | 已修复 | 重复 note 已去重；引擎侧路径也接入 worker 进展钟：确定性 verifier 实际执行、client-route 发现、recon 维度完成。原始工具输出本身仍不算进展（需先形成证据/覆盖）。测试：`TestHookProgressIgnoresUnchangedNoteWrite`、`TestVerifierBridgeMarksWorkerProgress`、`TestReconCompletionMarksWorkerProgress` |
| XALG005 | 已修复 | 每个工具记录 `ToolDeadline`（terminal 60m / python 30m / browser 10m / http 5m / 默认 15m）；deadline 内暂停停滞钟并拒绝 stagnant kill，超期后允许回收/kill。测试：`TestToolActiveBudget`、`TestProgressWatchReclaimsExpiredTool`、`TestKillWorkerAllowsExpiredActiveTool` |
| XALG006 | 已修复 | typed kill 被拒不再回落普通 kill；拒绝后继续观察。测试：`TestKillStagnantRefusalDoesNotFallBackToPlainKill` |
| XALG007 | 部分 | Agent 层 `WaitWorkerExit` 确认退出；queued kill 立即终结；`windDown` 等退出才结算。Docker 每次 exec 的进程/进程组终止仍未实现，需 runtime 集成验收 |
| XALG008 | 已修复 | 租约丢失/连续续约失败会 cancel 旧 worker（`lease_lost`）；owner 围栏结算防止改写新 owner。测试：`TestRenewLeaseLossStopsWorker`、`TestSettleIntentFencesOnOwner` |
| XALG009 | 已修复 | 主聊天新增周期性图投影（默认 30s），空闲无人工输入也刷新。测试：`TestStartGraphProjectionLoopProjectsWithoutInput` |
| XALG010 | 已修复 | `read_info_item` 可解析 ledger、aggregate node id、atom id 与 `to_<seq>` 归档引用；查询始终受 run scope 约束。测试：`TestReadInfoItemResolvesInformationSpaceID`、`TestReadInfoItemResolvesArchivedToolOutput`、`TestMainChatReadsToolArchiveAndCoverage` |
| XALG011 | 已修复 | 列表全部支持 `offset`/`next_offset`；`list_units` 显示真实 `progress=`/`tool=`；新增 `read_notes`、`read_info_item`、`read_tool_output`、`list_coverage`、`list_assets`、`list_deviations`。测试：`TestMainChatListIntentsPaginates`、`TestMainChatListUnitsShowsLiveWorkerProgress`、`TestMainChatReadNotesAndInfoItem`、`TestMainChatReadsToolArchiveAndCoverage`、`TestMainChatListsAssetsAndDeviations` |
| XALG012 | 已修复 | 主聊天 finish 重跑 `hookGoalFinishGate`；显式操作者停止仍立即生效。测试：`TestMainChatFinishRechecksRequiredObjective` |
| XALG013 | 部分 | GET/HEAD/OPTIONS 携带破坏性原语现在进入审批；普通 read 仍免审。更完整的“风险不由 method 决定”规则集仍需设计裁决 |
| XALG014 | 已修复 | query/body 做有界 percent/form 解码与 JSON 反转义后再匹配，保留原始字节供审计。测试见 `TestAssessHTTP` 编码用例 |
| XALG015 | 已修复 | eino broker 在 front Allow 前执行 fence + scope observer；proxy gate 同样先 fence/observer 再提交人工审批；legacy registry `SetApprovalGate` 无生产接线（仅测试使用），不构成生产路径 |
| XALG016 | 已修复 | 始终安装动态审批适配器；`RequestApproval` 每次读取 run 的 effective audit mode。测试：`TestTaskAuditEnforceCreatesCommandApprovalWithGlobalOff`、`TestApprovalBrokerInstalledForObserveRun` |
| XALG017 | 已修复（裁决：fail-closed） | enforce 下审批 store 不可用：broker 对 Require 调用返回错误；legacy `submit` 返回 `store_unavailable` 拒绝；代理 gate 在 scan context 消失时阻断。observe/off 保持原可用性策略。测试：`TestApprovalSubmitFailsClosedUnderEnforce`、`TestApprovalBrokerFailsClosedUnderEnforceWithoutStore` |
| XALG018 | 部分（边界已裁决） | 围栏只覆盖平台主动操作：HTTP/命令/端口扫描/显式浏览器导航/重定向每跳；页面自身加载的 CDN、统计、字体、XHR 不拦。`browser_action`/`page_agent` 已纳入 scope 检查；`execute_js` 静态 URL 会检查，运行时拼接 URL 仍是 best-effort。测试：`TestScopeFenceOnlyChecksPlatformInitiatedOperations`、`TestCheckHTTPHopEnforcesEngagementScope`；真实浏览器网络路径未集成验证 |
| XALG019 | 已修复 | Agent（含已注册子 worker）、Planner、Pool 增加 live `SetScope`；PUT 项目 scope 后遍历 project 的 live run 替换快照并通知主 Agent。测试：`TestSetEngagementScopePropagatesToChildren`、`TestPlannerSetScopeAppliesToNextPlan`、`TestPoolSetScopeAppliesToNextDispatch`、`TestApplyProjectScopeToLiveRuns` |
| XALG020 | 已修复 | Web `targetHostPort` 保留 CIDR，`hostInScope` 按网段包含匹配。测试：`TestCIDRScopeMatchesAgentSemantics`、`TestValidateTargetsInScopeAcceptsCIDR` |
| XALG021 | 已修复 | 代理 pin 安装移出 resume 修复分支，正常启动在目标流量前安装。测试：`TestInstallSessionEgressPinKeysByScanContext` |
| XALG022 | 已修复 | 热切换与隧道关闭改用 ScanContext ID。同上测试 |
| XALG023 | 已修复 | 历史 terminal 容器 lane 不再压制同地址新实例；新 lane 使用 `:g<N>` 身份与 `unique_code`。测试：`TestMaterializeContainerIntentsReusesTerminalAddress` |
| XALG024 | 已修复 | 显式 `WorkerProgressDisabled`；`off` 不再回落默认 20m。测试：`TestWorkerProgressDisabledIsNotDefault` |
| XALG025 | 已修复 | `observeScope` 不再用进程全局 mode 覆盖 run 的 enforce 选择 |
| XALG026 | 部分 | native browser 在自动解析 egress pin 时优先走本地 MITM listener，显式代理仍优先；浏览器网络路径尚未做集成测试 |

**仍属未关闭的主干缺口**：XALG018（浏览器运行时拼接 URL / 子进程实际出口的 best-effort 边界）、
XALG007（Docker 每 exec 进程终止），以及 XALG013/XALG026 的标注缺口。
当前计数：已修复 22、部分 4、未修复 0。这些不应被“单测通过”表述为已完成端到端验收。
