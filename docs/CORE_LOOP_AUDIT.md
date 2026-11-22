# 核心循环审计 — 宪章 vs 实测（TSec Benchmark 实战基线，2026-11-22）

> **用途**：以一次真实 benchmark 会话（TSecBench，token task_202611vcabzij3o1）的运行数据为证据，逐条对照 `docs/CORE_PHILOSOPHY.md` v2.1 的信息驱动循环，标记**已兑现 / 部分兑现 / 未兑现**，并给出 M1/M4/M5 二开的可度量验收标准。
>
> **审计方法**：不查代码意图，只查运行痕迹——ledger.json（×2 轮会话）、events.jsonl、引擎日志、PG psql 计数（info_items / info_audit_records / runs / approvals / river_job）、findings 管线日志、平台 API 对账。
>
> **修正声明**：初版审计曾误判「PG 信息层为空」——实为查询展示截断。实测 `info_items 6702 行 / info_audit_records 21493 行`，M3 冷路径为本次审计中**流转量最大的环节**。

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
| 1 | **七环**：观察→信念→义务→行动→证据→判定→收工 | 观察/行动/收工三环有完整痕迹（95+ tool calls、finish 管线、journal 全落盘）；信念/证据两环只有积累没有驱动（见 §3）| ⚠️ 三环实转、两环空转 |
| 2 | 对象映射不得反向（义务=Hint 非人工输入） | obligationMiddleware 在 eino 主循环注册（runtime.go:124-128）；BenchmarkIsolationGuard 存在但本次未启用 | ✅ 骨架在（基准场景近似旁路）|
| 3 | **InfoItem 信息为中心建模** | PG `info_items 6702 行`——InfoItem 冷路径真实高写入 | ✅ **M3 实证**（本次审计流转量最大环节）|
| 4 | 三类点亮必须点名消费者 | region/context/association 表存在；flag 类情报走了 tool_observation 捕获；但**无 claim 消费证据**（见 §3 缺口②）| ⚠️ 入口在、消费未验证 |
| 5 | **字段级权威** | findings 标签权威只在 `report_vulnerability`（XALG-1..8 exploit-proven/verified 全部经门管线：dedup→fabrication→method→proof→CVSS→verifier→severity cap 顺序实测执行）；「置信度高所以判真」未出现 | ✅ 实测合规 |
| 6 | **事件是审计投影，不是状态权威** | journal 只含 WS 事件（message/tool_call/tool_result——**无信息层事件**）；信息层事件全部走 PG 审计投影（21493 行）；chat_transcript 为只读投影 | ✅ 按设计（跨进程重放从未成为权威）|
| 7 | **存储分层**（JSON 热路径 + PG 冷路径） | scan.json/notes/ledger/intelligence JSON 原子写 ✓；PG 冷路径接通后 `[coldpath] enqueued seq N..N` 实时入队 ✓；scan_id+seq 幂等 ✓ | ✅ **M3 实证**（当天接通当天高流转）|
| 8 | River 限定范围 | river_job/river_leader/river_migration 随 DSN 激活；异步补全未接管主队列/审批/SSE | ✅ 按限定范围 |
| 9 | 单二进制降级 | 会话前半段（无 DSN）全程纯文件运行，核心能力不减 | ✅ 实证 |
| 10 | VOI 只作排序加权 | 无运转证据（claim/VOI 排序零观测）| ❌ **M5 未兑现** |
| 附 | 收工闸门 | gatekeeper→ledger→goal gate 真实拦截过模型 finish；无 objective 时按设计放行 | ✅ 按设计 |

---

## 3. 核心缺口（「形」到了，「魂」没成为主引擎）

### 缺口① 信念层只积累、不调度（M1/M4 主战场）
两轮累计 **459 条 hypotheses，proven 仅 8 条（<2%）**，其余全部 queued 无人认领。agent 的行动由 **instruction 清单驱动**，不是由 hypothesis 池驱动——`claim_next_hypothesis` 的可调度消费在 benchmark 任务形态下没有接管主循环。宪章的「信念→义务→行动」退化为「指令→行动」。

### 缺口② 证据层被任务形态绕过
benchmark 指令要求「拿到 flag 立即提交平台」，**未要求同步 `report_vulnerability`** → 10 个 flag 只有 1 个（c-07）形成 finding + finding_ref evidence。结果：ledger evidence 近零、info_items 里 purpose=finding 的条目缺失、判定层只能走 exploit-proven 直判。**这是指令取舍造成的绕过，不是引擎缺陷——改一条指令即可让证据层闭环**（下轮验收标准见 §5）。

### 缺口③ VOI 未激活（M5）
零观测。排队假设无 claim、无加权排序、无确定性 tie-break 的运行痕迹。

### 缺口④ 硬件级损耗（工程，非宪章）
- 模型能力边界：hard 档（500 分×9、逆向类 f2、b-* 多 flag）零突破；250 分 hard 仅破 4 题
- 止损纪律双刃剑：easy/medium 全清（✓），但 hard 需要的整块时间投入被结构性排除
- 工程切换 3 次 + 首任务 token 超时：损耗约 40-60 分钟有效攻击时间（热续跑已把单次切换成本压到 ≈1 分钟）

---

## 4. 本次实战顺带发现并修复的引擎缺陷（跨宪章价值）

均为真实运行暴露，已随 `6f41eb22` / `d171041c` / `0ad3825f` 修复并推送：

1. **孤儿事实（违反不变量 §8）**：eino AfterTool 丢弃 tool-result metadata → `report_vulnerability` 的 vuln_id 到不了 web 层 → scan.json vulns 运行中恒空，发现页整个会话不可见，直至收尾才合并。
2. **迭代上限 20 的硬地板**：`engine/runtime.go` 把 `<=0` 强制为 20，与 config 注释「0=unlimited」矛盾——两轮会话在恰好第 20 次调用被 `exceeds max iterations` 掐死。
3. **陈旧 cancel checkpoint 秒终**：上会话 cancel 存的 checkpoint 在同 ID 重派时被 `rt.Run` 恢复 → 恢复已终结的图，零 LLM 调用直接 finish。重开路径现丢弃陈旧 checkpoint（L1 跨进程 Resume 在当前 eino 版本不可行，已记录）。
4. **1:1 重派四件套**：queueOwnership 未移交、孤儿 pending 快照拒绝、重建幽灵实例碰撞、SIGKILL 半途快照卡死 running——全部修复（见 `0ad3825f`）。

---

## 5. 下轮跑分的验收标准（M1/M4/M5 基线，全部可用 SQL/日志度量）

| 项 | 验收标准 | 度量方式 |
|---|---|---|
| M1 调度接管 | hypothesis 存在 queued→testing→proven/refuted **状态迁移记录**；queued 积压占比 < 70% | ledger.json 状态分布 + PG 审计投影中的 claim 事件 |
| M4 消费闭环 | **evidence 条数 ≥ 解出 flag 数**；info_items 中 purpose=finding 条目 ≥ flag 数 | psql 计数 + ledger evidence |
| M5 VOI 激活 | claim 排序携带非零 voi 字段；同一 ledger 快照两次排序结果确定一致（确定性 tie-break） | 审计投影中的 claim 顺序对比 |
| 证据管线 | 每个 flag 同步 `report_vulnerability`（instruction 强制）| findings 条数 ≥ flag 数 |
| 事件投影 | 信息层事件（claim/obligation/goal_analyzed）出现在 PG 审计投影而非仅 journal | psql 按类型计数 |

---

## 6. 下轮 instruction 改进清单（跑分策略层）

1. **flag 同步走 `report_vulnerability`**（finding_ref evidence）——不再绕过证据管线
2. **hard 档预算重分配**：e1-e3 系列的 250 分 hard 已证明可解（4 题）——与 medium 并行穿插，不再整体后置；最后 1/3 时间专攻 500 分档
3. **hint 策略放宽**：hard/medium 卡 15 分钟即用 hint——扣 20% 分远好于 0 分（本轮 0 hint = 严格守纪但过度保守）
4. **spawn_agent 委派**：easy/medium 档开子 agent 并行清（突破串行上限；本轮未启用）
5. **禁宿主机 find /** 固定字典路径（已验证有效的条目保留：并发 3、flag 即交、平台域名只走 API、10 分钟止损）

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
