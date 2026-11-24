# 核心循环审计 — 宪章 vs 实测（TSec Benchmark 实战基线，2026-11-22）

> **用途**：以一次真实 benchmark 会话（TSecBench，token task_202611vcabzij3o1）的运行数据为证据，对照 `docs/CORE_PHILOSOPHY.md` v2.1，标记运行兑现情况，并给出下一轮的可度量验收标准。优化方案见 `docs/CORE_LOOP_OPTIMIZATION.md`。
>
> **两套验收的关系**：宪章 §10 是**代码验收**（测试钉住，控制里程碑合并顺序；REDESIGN §9 记录 2026-11-21 宪章 M0–M5 已全部落地）。本文是**运行验收**——证明能力在真实任务形态下「实转」。运行验收不过 ≠ 回滚代码；它说明该形态下消费路径未闭合，按优化方案补配置或形态。
>
> **审计方法**：不查代码意图，只查运行痕迹——ledger.json（×2 轮会话）、events.jsonl、引擎日志、PG psql 计数（info_items / info_audit_records / runs / approvals / river_job）、findings 管线日志、平台 API 对账。
>
> **修正声明**：初版审计曾误判「PG 信息层为空」——实为查询展示截断。实测 `info_items 6702 行 / info_audit_records 21493 行`，M3 冷路径为本次审计中**流转量最大的环节**。v2 修订：补里程碑状态总表（§2.1）、缺口①③的机制级归因（§3）、验收标准对齐宪章 §10 术语（§5）、纠正不可执行的指令项（§6.7）。

---

## 1. 会话与数据速览

| 项 | 值 |
|---|---|
| 时间窗 | 2026-11-22 15:44（首个有效派发）→ ~20:30（任务时限耗尽），约 5 小时 |
| 会话 | 3 轮文件态（其中 2 轮被工程问题浪费）+ 1 轮 PG 冷路径态 |
| 战果 | **30/63 题通关，≈7150-7350 / 23000 分（~31%）**：easy 8/8、medium 18、hard 4（均为 e1-e3 系列 250 分档）|
| 信念层 | ledger 累计 **152 + 307 条 hypotheses**（150/301 queued，2/6 proven）|
| 冷路径 | **info_items 6702 行，info_audit_records 21493 行**，river_job 激活 |
| findings | XALG-1..8（critical×4 / high×2 / medium×1 等），**仅 1 个 flag 走了完整证据管线** |
| hint | 0 次调用（纪律零容忍，代价是 hard 档放弃率 100%）|

---

## 2. 逐条对照：宪章条目 → 实测证据 → 判定

| # | 宪章条目 | 实测证据 | 判定 |
|---|---|---|---|
| 1 | **七环**：观察→信念→义务→行动→证据→判定→收工 | 观察/行动/收工三环有完整痕迹（95+ tool calls、finish 管线、journal 全落盘）；信念/证据两环只有积累没有驱动（见 §3 缺口①②）| ⚠️ 三环实转、两环空转 |
| 2 | 对象映射不得反向（义务=Hint 非人工输入） | obligationMiddleware 注册于 eino 主循环（runtime.go:124-128）；义务链真实拦截过模型 finish（见附行）。`hookBenchmarkIsolationGuard` 存在但本次未启用（基准隔离可选项） | ✅ 实测合规 |
| 3 | **InfoItem 信息为中心建模** | PG `info_items 6702 行`——InfoItem 冷路径真实高写入 | ✅ **M3 实证**（本次审计流转量最大环节）|
| 4 | 三类点亮必须点名消费者 | region/context/association 表存在；flag 类情报走了 tool_observation 捕获；但**无 claim 消费证据**（见 §3 缺口①）| ⚠️ 入口在、消费未验证 |
| 5 | **字段级权威** | findings 标签权威只在 `report_vulnerability`（XALG-1..8 exploit-proven/verified 全部经门管线：dedup→fabrication→method→proof→CVSS→verifier→severity cap 顺序实测执行）；「置信度高所以判真」未出现 | ✅ 实测合规 |
| 6 | **事件是审计投影，不是状态权威** | journal 只含 WS 事件（message/tool_call/tool_result——**无信息层事件**）；信息层事件全部走 PG 审计投影（21493 行）；chat_transcript 为只读投影 | ✅ 按设计（跨进程重放从未成为权威）|
| 7 | **存储分层**（JSON 热路径 + PG 冷路径） | scan.json/notes/ledger/intelligence JSON 原子写 ✓；PG 冷路径接通后 `[coldpath] enqueued seq N..N` 实时入队 ✓；scan_id+seq 幂等 ✓ | ✅ **M3 实证**（当天接通当天高流转）|
| 8 | River 限定范围 | river_job/river_leader/river_migration 随 DSN 激活；异步补全未接管主队列/审批/SSE | ✅ 按限定范围 |
| 9 | 单二进制降级 | 会话前半段（无 DSN）全程纯文件运行，核心能力不减 | ✅ 实证 |
| 10 | VOI 只作排序加权 | 无运转证据。归因见 §3 缺口③：**三个开关全关**（w 默认 0 + 模型未传 voi + 零 claim），非引擎缺失 | ❌ 运行未激活（代码验收已过，见 §2.1）|
| 附 | 收工闸门 | gatekeeper→ledger→goal gate 真实拦截过模型 finish；无 objective 时按设计放行 | ✅ 按设计 |

### 2.1 里程碑状态总表（代码验收 × 运行验收）

| 里程碑 | 代码验收（宪章 §10，2026-11-21 全过） | 运行验收（本次 TSec） |
|---|---|---|
| P0 稳定基线 | 修复项回归全过 | 本次实战再暴露 4 处缺陷并已修（§4）|
| M0 Evidence nature | `Nature` 独立字段 + 缺省按 inferred + 金样本含 observed/inferred 证据 | ⚠️ 仅 1 条 finding_ref 走通（缺口②，指令取舍非引擎）|
| M1 Hypothesis 前置条件 | `record_hypothesis` preconditions REQUIRED（未知须显式写 `unknown`，ledger_tools.go:75）| ❌ 运行零 record_hypothesis 观测——CTF 形态无此纪律（缺口①）|
| M2 审计记录与只读投影 | journal seq/缺口/乱序投影 + `/api/scans/{id}/audit` | ✅ 21493 行审计投影，缺口/乱序可识别 |
| M3 InfoItem 冷路径落 PG | 幂等重投 + reconciler 追平 + 四态水位 | ✅ 6702 行高流转，实时入队 |
| M4 激活入口接真实消费者 | ledger 五件套对所有 agent 注册（agent.go:662）+ `read_ledger(filter=schedulable)` + claim 跨扫描注记（ledger_cross_scan.go，限本项目任务之间，契约测试钉住）+ objective 面板可调度池 | ❌ 零消费：claim 0 次——消费向量被 CTF 单代理模式结构性关闭（缺口①）|
| M5 确定性 VOI 加权 | `TestGoldenSchedulingParity`：加权排序 + tie-break + 不可变快照（w∈[0,0.30]）| ❌ 未激活：w 默认 0、模型未传 voi、零 claim（缺口③）|

---

## 3. 核心缺口（「形」到了，「魂」没成为主引擎）

### 缺口① 信念层只积累、不调度——归因：CTF 任务形态关闭了消费路径（非引擎能力缺失）

**表象**：两轮累计 459 条 hypotheses（152+307），proven 仅 8 条（<2%），其余全部 queued 无人认领。agent 的行动由 instruction 清单驱动，宪章的「信念→义务→行动」退化为「指令→行动」。

**机制**（代码链，全部锚定到实测行为）：

1. instruction 含 CTF/flag 关键词 → `isExplicitCTFMission` 判真（agent_prompt.go:589）→ `ctfMission=true`；
2. `delegationEnabled()` 被 ctfMission 短路（agent.go:1352）→ `DelegationEnabled=false` → 单代理模式；
3. 单代理模式下：`hookSingleAgentSpawnGuard` 对 spawn_agent **硬拦截**（ForceSkip，hooks.go:856）；`hookDelegationCoordinator` 永不触发（hooks.go:3646）；系统提示词的 Coordinator 段被替换为 Single-Agent Mode 段、专业评估广度教义不注入（agent_prompt.go:474/506）；
4. **claim 纪律的唯一注入向量 = 委派 nudge**（ledger_hooks.go:184-190「反复 claim_next_hypothesis 直到 lane 清空」）——该向量在 CTF 模式结构性缺席；CTF 提示词从头到尾未提及 ledger 纪律；
5. `hookLedgerSeed` 无 CTF 闸 → 459 条假设照常 seed；`hookLedgerFinishGate` 只拦 proven-unreported / testing / lane-assigned-unclaimed——**全 queued 时空过**，收工闸门对积压无感。

**判定**：宪章 M4 代码验收已过；运行缺口是**任务形态层面的结构性关闭（设计如此）**。真正的引擎缺口是：**CTF/benchmark 形态自身没有信念循环**——而 63 题的选题、止损、预算分配恰恰是 VOI 排序可调度池的用武之地（优化方案 O1）。

**连带纠正**：v1 审计 §6.4「spawn_agent 并行清」在该模式下**不可执行**（硬拦截），已从指令清单移入优化方案 O1.2（需引擎开关）。

### 缺口② 证据层被任务形态绕过

benchmark 指令要求「拿到 flag 立即提交平台」，**未要求同步 `report_vulnerability`** → 10 个 flag 只有 1 个（c-07）形成 finding + finding_ref evidence。结果：ledger evidence 近零、info_items 里 purpose=finding 的条目缺失、判定层只能走 exploit-proven 直判。**这是指令取舍造成的绕过，不是引擎缺陷——改一条指令即可让证据层闭环**（下轮验收标准见 §5）。

### 缺口③ VOI 未激活——三个开关全关，非引擎缺失

- `XALGORIX_VOI_WEIGHT` 默认 0（config.go:587；宪章 §7.2：w=0 完全保持旧排序）——本次未设；
- VOI 估计由模型在 `record_hypothesis`/`update_hypothesis` 传可选 `voi` 参数——instruction 无此要求，零观测；
- claim 零次（缺口①）→ 排序根本没有运行。

判定：代码验收已过（金样本钉住加权+tie-break）。**任一开关打开都会产生观测，三个全关 = 完全不可见**。M5 运行兑现是配置+指令问题，不是引擎问题。

### 缺口④ hard 档与工程损耗（非宪章）

- 模型能力边界：hard 档（500 分×9、逆向类 f2、b-* 多 flag）零突破；250 分 hard 仅破 4 题
- 止损纪律双刃剑：easy/medium 全清（✓），但 hard 需要的整块时间投入被结构性排除
- 工程切换 3 次 + 首任务 token 超时：损耗约 40-60 分钟有效攻击时间（热续跑已把单次切换成本压到 ≈1 分钟）

---

## 4. 本次实战顺带发现并修复的引擎缺陷（跨宪章价值）

均为真实运行暴露。1/3/4 已随 `6f41eb22` / `d171041c` / `0ad3825f` 修复并推送；**2 的「已修复」是 v1 审计误报**——地板其实烙在 eino ADK 内部（`adk/react.go` genReactState 对 0 默认 20），声称的修复提交从未触及 runtime.go，本版复核时发现仍在并已补修（0 → 不可达哨兵，`TestMaxIterationsZeroMeansUnlimited` 钉住）：

1. **孤儿事实（违反不变量 §8）**：eino AfterTool 丢弃 tool-result metadata → `report_vulnerability` 的 vuln_id 到不了 web 层 → scan.json vulns 运行中恒空，发现页整个会话不可见，直至收尾才合并。
2. **迭代上限 20 的硬地板（v1 误报已修，本版补修）**：`engine/runtime.go` 的 `<=0` 钳制与 config 注释「0=unlimited」矛盾——两轮会话在恰好第 20 次调用被 `exceeds max iterations` 掐死。深层根因：eino ADK 对 0 自带 20 上限，透传无效；修复 = 0 → MaxInt32 哨兵（时长/token/工具调用预算仍兜底）。
3. **陈旧 cancel checkpoint 秒终**：上会话 cancel 存的 checkpoint 在同 ID 重派时被 `rt.Run` 恢复 → 恢复已终结的图，零 LLM 调用直接 finish。重开路径现丢弃陈旧 checkpoint（L1 跨进程 Resume 在当前 eino 版本不可行，已记录）。
4. **1:1 重派四件套**：queueOwnership 未移交、孤儿 pending 快照拒绝、重建幽灵实例碰撞、SIGKILL 半途快照卡死 running——全部修复（见 `0ad3825f`）。

---

## 5. 下轮跑分的运行验收标准（对齐宪章 §10 术语；全部可用 SQL/日志度量）

| 里程碑 | 运行验收标准 | 度量方式 |
|---|---|---|
| M1 前置条件 | `record_hypothesis` 调用时 preconditions 全部显式给出（`unknown` 单独计数） | ledger.json 假设字段 + PG 审计投影 record 事件 |
| M4 消费闭环 | claim 事件（OpClaimHypothesis）≥ 解出 flag 数；ledger evidence 条数 ≥ flag 数；purpose=finding InfoItem ≥ flag 数 | psql 按操作类型计数 + ledger evidence |
| M5 VOI 激活 | w>0 下 claim 顺序携带 VOI 加权；**不可变快照**两次排序一致——连续两次 ClaimNext 不是有效验收（第一次调用会改变快照，宪章 §7.2） | 审计投影 claim 顺序对比 + 金样本重放 |
| 证据管线 | 每个 flag 同步 `report_vulnerability`（instruction 强制）| findings 条数 ≥ flag 数 |
| 事件投影 | goal_analyzed / claim / set_voi 出现在 PG 审计投影而非仅 journal | psql 按类型计数 |

---

## 6. 下轮 instruction 与配置改进清单（跑分策略层）

1. **flag 同步走 `report_vulnerability`**（finding_ref evidence）——不再绕过证据管线
2. **信念纪律写进 instruction**（O0，零代码）：每题 `record_hypothesis`（preconditions 显式、尽力给 voi）→ `claim_next_hypothesis` 认领后测试 → 终态 `update_hypothesis`（proven 链 finding / rejected 带 baseline）——M1/M4 运行兑现的最短路径
3. **env：`XALGORIX_VOI_WEIGHT=0.25`**（≤0.30 宪章上限）——M5 激活
4. **objective 结构化判据**（D10）：任务配 required 判据（flag 数/类覆盖）→ goal gate / goal obligation / goal_analyzed 事件全部激活
5. **hard 档预算重分配**：e1-e3 系列的 250 分 hard 已证明可解（4 题）——与 medium 并行穿插，不再整体后置；最后 1/3 时间专攻 500 分档
6. **hint 策略放宽**：hard/medium 卡 15 分钟即用 hint——扣 20% 分远好于 0 分（本轮 0 hint = 严格守纪但过度保守）
7. ~~spawn_agent 委派~~ → **O1.2 开关已落地**（`XALGORIX_ENABLE_CTF_DELEGATE=true`；CTF 模式默认硬拦截 spawn_agent，见缺口①）；跑分时置 true 即可并行清题
8. 保留已验证有效项：并发 3、flag 即交、平台域名只走 API、10 分钟止损、禁宿主机 find /

---

## 7. 时间线附录

```
15:44 首个有效派发（第二任务 token）
15:52 首旗 a-07(100)          ——easy/medium 清盘期
17:38 10 题/1800              ——迭代上限修复后重派
18:35 17 题/3800              ——收紧纪律重派（并行 3 + 搁置名单生效，5 分钟 +7 题）
19:29 18 题/4100              ——前后端更新热切换
20:00 a-14(300) 命令注入→root RCE
20:24 a-10(500) 商城 price-manipulation
20:30+ 30 题/≈7150-7350       ——时限耗尽，hard 长尾未破
```
