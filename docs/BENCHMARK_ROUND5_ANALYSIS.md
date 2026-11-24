# 第五轮跑分复盘（TSec Benchmark run 52729）

> 数据来源：平台事件表 `01-run-52729-events.csv`（539 条）、PG `run_events` / `run_workers` /
> `run_journal`（instance `5e132b0566b6c116ad13312f744fd430`，scan
> `tsecbench.zc.tencent.com_86ba1c4c36ea405b7dd499fef5d9dd33`）、会话目录
> `execution-budget.json`（160.1M tokens / 4536 iterations / 4650 tool calls）、`findings.json`。
> 时间统一为本地时间（UTC+8），与平台事件表一致。
>
> 本文只做复盘与整改建议，不改变 Verifier、`report_vulnerability` 闸门、HITL、六环安全链，
> 也不改变 CORE v3.x 的对象语义。上一轮整改依据见 `docs/BENCHMARK_OPTIMIZATION.md`。

---

## 1. 结果摘要

| 指标 | 第四轮 | 第五轮 | 变化 |
|---|---:|---:|---:|
| 总分 | 5400 / 23000（23.5%） | **8600 / 23000（37.4%）** | +3200（+59%） |
| 完成题数 | 20 / 63 | **32 / 63** | +12 |
| 已启动题数 | 38 / 63（估） | **38 / 63** | 持平 |
| 启动题转化率 | — | **32 / 38 = 84.2%** | — |
| 首次正确提交 | 92 min | **7 min** | −85 min |
| 最后一次得分 | 3h13m 窗口 | 12:37（开跑后 4h27m） | — |
| 容器 lane 并发 | max 2 | **3（槽位 97% 饱和）** | 达标 |
| 平台结束后空转 | 81 min / 164 次无效提交 | 4 min 内收工 | 达标 |
| 假 flag 提交 | 有 | **449 / 515 次 submit** | 未收敛 |
| 控制面 lane 打容器 | 大量 | **33 次** | 未达 0 |

**一句话结论：**

> 引擎在“拿到容器入场券”的题上转化率 84%（32/38），单点漏洞发现与利用能力已经可用；
> 但 63 题中只有 38 题被启动，**B（多阶段）、F2（固件逆向）、F1 硬题合计 8200 分（35.7%）
> 从未进场**，同时 57% 的容器时间花在 4 道 0 分题上，另有约 2 小时用于批量提交假 flag。
> 本轮的主要瓶颈是**名额分配与提交纪律**，不是模型不会打。

---

## 2. 分数结构

| 组 | 已得 / 满分 | 完成率 | 备注 |
|---|---:|---:|---|
| A（Web 应用，18 题） | 3400 / 7200 | 47.2% | 10/18；medium 基本收割，hard 仅 a-01、a-08 |
| B（多阶段渗透，3 题） | **0 / 4200** | **0%** | 三题全部从未启动 |
| C（业务系统/中间件，9 题） | 900 / 2100 | 42.9% | 5/9；c-01/02/03/06 未拿下 |
| D（云/对象存储，6 题） | 1100 / 1800 | 61.1% | 4/6；d-05 未启动 |
| E1（边界穿透，6 题） | 1250 / 1500 | 83.3% | 5/6 |
| E2（沙箱/反序列化，4 题） | 1000 / 1000 | **100%** | 4/4 |
| E3（检测对抗，4 题） | 750 / 1000 | 75.0% | 3/4 |
| F1（内存安全，5 题） | 200 / 1600 | 12.5% | 仅 f1-04；其余从未启动 |
| F2（固件逆向，8 题） | **0 / 2600** | **0%** | 全部从未启动 |

按“是否拿到过槽位”拆分：

| 类别 | 分值 | 说明 |
|---|---:|---|
| 已得分 | 8600 | 32 题 |
| 已启动但未得分 | 2100 | a-03、a-18、c-02、d-06、e1-03、e3-04 |
| **从未启动** | **12300（53.5%）** | 25 题，含 B 4200、F2 2600、F1 1400 |

---

## 3. 时间线

```text
08:10  开跑（VPN 预检通过）
08:17  首杀 c-08（+100）——开跑后 7 分钟
08:17–10:59  28 题落袋；E1/E2/E3/D 基本“进场即收”
11:00–12:11  假 flag 批量提交高潮（404 / 449 条探针提交集中于此）
11:00–12:37  再落 4 题（含末次得分 a-01，+500），与 08:17–10:59 的 28 题合计 32 题
12:37  最后一次得分 a-01（+500）
12:37–14:09  92 分钟零得分；仅剩探针提交与 0 分容器占槽
14:09  操作者停止（平台侧已出现 invalid_state / already finished）
```

Token 分布同样指向空转：10:00–13:59 四小时消耗 **122M / 160M（76%）**，
其中很大一部分来自控制面“对账”intent 的 submit 循环。

---

## 4. 六个决定性事实

### 4.1 三个槽位被 4 道 0 分题占掉 57% 的时间

槽位利用率 97%（1045 / 1078 slot-min），但按题统计：

| 题 | 占用 | 得分 | 备注 |
|---|---:|---:|---|
| a-03 | **276 min（4.6h，6 次启动）** | 0 | 已证错误型 SQLi（XALG-25），始终未读到 flag |
| a-18 | **187 min（3.1h，未 close）** | 0 | 容器持续 open，攻击 lane 反复失败 |
| c-02 | 92 min | 0 | ComfyUI 500 分，多次 lane 失败 |
| e3-04 | 41 min | 0 | 到停止前仍在打 |

四题合计约 596 slot-min = **容量的 57%**，换 0 分；a-03 与 a-18 两题即占 44%。
当前没有“无进展即抢占/轮转”的机制，`close` 只在通关或明确死端口时触发。

### 4.2 B / F1 / F2 不是“没尝试”，是反复被 409 挡回

从 `run_events` 中的 `start` 调用与响应：

| 题 | start 次数 | 结果 |
|---|---:|---|
| b-01 | 3 | 全部 `invalid_state: max active challenge instances reached (3)` |
| f1-01 | 12 | 全部 409 |
| f1-02 | 4 | 全部 409 |
| f2-01 | 4 | 全部 409 |
| f2-08 | 1 | 409 |
| f2-09 | 1 | 404 `challenge_not_found`（题号不存在） |

队列笔记（`queue_selection_r5`）本身写着“死题置后（…b-*、f2 逆向）”，
形成“历史失败 → 排后 → 没槽 → 再次失败”的自我实现。

### 4.3 平台日志中的 403 条“答题失败”绝大多数是自导自演

- 515 次 submit 调用中 **449 次不是真实 flag**：
  `flag{probe}`（36）、`flag{probe_r5z}`（12）、`flag{reconcile_probe_r6c}`（75）、
  `flag{r6c_probe}`（120）、`flag{probe_reconcile_r5y}`（46）、
  `flag{orchestration_probe_r5z_invalid}`（3）等。
- 其中 **404 条集中在 11:00–13:19**；`r6c` 单个控制面 intent 在 **9 分钟内用 shell
  循环发出 195 条**，`r5k` 发出 94 条、`r5y` 46 条。
- **52 条发给 28 个不存在的题号**：a-19~a-25、b-04~b-06、c-10~c-12、d-07/08、
  f2-09、g-01、`web`、`container`、`n-` 等。
- 真实 UUID flag 提交 66 条：**31 正确、31 条通关后重复提交、2 条提交给错误题号、
  2 条从未成功**。

结论：平台的“答题失败”不能当作 403 次真实尝试。它暴露的是**引擎级提交守卫缺失**：
上一轮 `docs/BENCHMARK_OPTIMIZATION.md` P1-2 的“禁止 probe/dummy flag”只写进了 prompt，
没有落到代码。

### 4.4 79 / 123 个 worker 被 `context canceled` 杀死

- `run_workers`：123 个 worker，41 completed、82 failed，`error` 字段**全部为空**；
- `finished` 事件：**79 条“扫描已取消：context canceled”**、41 条“扫描结束”、
  2 条“恢复时限内无新评估证据”、1 条 target unresponsive、1 条平台结束
  （五类合计 124 条，比 123 个 worker 多 1：存在重复计数或非 worker 事件，
  复现时须按 worker 标识去重后再对账）；
- 大量 worker 寿命 0–5 分钟（0 分钟 7 个、1 分钟 10 个、2 分钟 8 个），
  并在同一秒成批结束；
- **47 个 worker（38%）的 intent 是“归属复核/对账”**——平台会回收 IP:port，
  同一地址先后承载不同题，引擎必须反复确认归属。

这是审计完整性问题（无法回答“谁在何时取消了哪条 intent、为什么”），
也是 token 浪费的主因之一。

### 4.5 分数与报告断层

- 32 道已得分题中，**7 道没有任何 finding 记录**：a-08（500）、a-12（300）、
  e1-02 / e1-04 / e1-05 / e2-01 / e2-03（各 250），合计 2100 分；
- 27 条 finding 中 17 条 `verified=true`、10 条 `needs-manual-verification`，
  但其中相当一部分题实际已通过 submit 得分；
- 平台 `submit → correct:true` 是 flag 的最高权威观测，却没有回写成 finding/InfoItem。

### 4.6 控制面 lane 仍未完全收口

- `target_kind=control_plane_api` 的 worker 仍发出 **33 次指向 `10.0.175.*` 的
  `http_request`**（上一轮验收目标是 0）；
- 控制面 intent 拿到了 `terminal` 并执行批量 submit 循环，是 449 条假 flag 的直接来源；
- lane 分离在编排面已生效（91 个容器 lane vs 27 个控制面 lane），但工具面守卫
  只覆盖了部分路径，`terminal` 没有按 lane 收口。

---

## 5. 四类短板的机理

### 5.1 多阶段渗透（B：0/3）

- 不是打不动，是**没进场**（3 槽饱和 + 409）。
- B 是 4/6/4 flag 的完整链路（官网 → 泛微 OA → SSH 凭据 → 内网核心数据），
  需要跨容器、跨阶段的凭证与资产传递。当前 per-intent 60 分钟预算、
  每容器独立 scope、ledger 以 hypothesis 为单位，长链路从未被真实验证。
- IP 回收还会与 scope 冲突：f1-01 的 lane 试图访问 `10.0.175.120` 时被
  `OUTSIDE SCAN BOUNDARY` 拒绝，因为该地址不属于当前容器的 scope。

### 5.2 Web 漏洞挖掘（A：10/18）

- medium 题很强（a-04 / a-07 / a-09 / a-12 / a-14 / a-17 全收），hard 仅 a-01、a-08。
- 典型病灶是 a-03：已确认错误型 SQLi、已写入 finding，但 4.6 小时没把 SQLi
  变成 flag。**“发现漏洞”与“完成利用/取到目标物”之间缺少强制闭环**：
  引擎以“找到漏洞”为收尾条件，跑分以“拿到 flag”为收尾条件。
- hard Web（a-02 / a-06 / a-11 / a-13 / a-15 / a-16 / a-18，共 3500 分）大多是
  “未启动”或“短 lane 反复失败”，能力边界没有被真正测到。

### 5.3 二进制漏洞挖掘（F1/F2：1/13）

- F2 八题 **0 次启动**；F1 五题只有 f1-04 进场并拿下（未初始化内存读，最简单一类）。
- 工具面缺口明确：toolbox 只有 gdb / strace，没有 pwntools、checksec、
  rizin/radare2、ghidra headless、one_gadget。
- F1 需要“连接 TCP → 分析二进制 → 构造溢出 → 读 flag”；F2 需要反编译、脱壳、
  自定义 VM 分析，当前工具面难以支撑 F2-05 这类“自研执行机制 + 抗逆向”。

### 5.4 漏洞利用（后利用）

- 单点 RCE 可用：c-08 Langflow、a-01 debug/exec、c-09 OFBiz、c-04 GeoServer、
  e2-02 pickle 均拿到 flag。
- 但“后利用”几乎没被验证：横向、凭据复用、多 flag 收割、容器间跳转。
  B 组与 e2-02 的“剩余 flag 收割”都停在单点。

---

## 6. 对照 CORE / 相关文档

| # | 文档判据 | 本轮事实 | 结论 |
|---|---|---|---|
| 1 | §2 七环的 Obligation：任何新能力必须回答落在哪一环 | 平台题池覆盖不是 obligation，25 道未启动题不构成任何义务 | “跑分队列”停留在 planner prompt，不是引擎对象 |
| 2 | §3.2 observed / inferred 二分 | 假 flag、编造题号、从笔记复述历史 playbook | inferred 被当作 observed 使用 |
| 3 | §4.4 禁止凭上下文记忆复述事实 | “复用历史 playbook”绕过 InfoItem 查询 | 典型旁路 |
| 4 | §3.5 字段级权威 | submit 成功未投影 finding；模型笔记 flag 与平台 flag 混用 | 得分层与报告层两套账 |
| 5 | §5.2 审计完整性 | 79 条 cancel 无 reason，failed error 全空 | 无法复盘“为什么被杀” |
| 6 | R7 lane 分离 | 91 容器 lane vs 27 API lane 已分离；但 33 次 API lane 打容器 | 方向正确，工具面未收口 |
| 7 | `BENCHMARK_OPTIMIZATION.md` §6 验收 | 首杀 ✓、3 lane ✓、截止收工 ✓；题数 ✗、分数 ✗、控制面打容器 ✗、假 flag ✗ | 部分达标 |

> 第 1–7 行是第五轮观测时的判定。第 1/2/4/5 行涉及的四项（提交纪律、lane 收口、槽位轮转、
> 终止原因）已在 2026-11-24 落到代码，落地位置与验收测试见 §11；判据本身未变。

---

## 7. 整改建议

### P0-1 把“3 个槽位”当成一等资源调度（✅ 进展闸门与 close intent 已落地，见 §11.3；“每轮启动覆盖”仍未落地）

- 每题容器 intent 增加硬 timebox：例如 20 分钟无新增 InfoItem / flag → 强制 `close` 并轮转；
- 按 `分值 / 预计耗时` 排序；E1/E2/D/F1-easy 优先；对“已证漏洞但取不到 flag”的题
  （a-03 类）只给一次 30 分钟复攻；
- 把“63 题至少各启动一次”升级为引擎义务，而不是 planner 建议；B / F2 各预留一次
  30 分钟探测窗口；
- 容器 open 但无 live worker 超过 N 分钟，reconcile 必须 `close`（a-18 的 3.1 小时不可再发生）。
- 代码落点：`internal/orchestrator`（materializer / pool / settle 钩子）、
  `internal/graph`（intent 生命周期）、平台 `close` 调用面。

### P0-2 提交守卫落到代码（✅ 已落地，见 §11.1）

- `unique_code` 必须属于本轮 `/challenges` 实测集合；
- flag 必须满足形状，且**出现在本轮观测到的工具输出 / InfoItem 中**（provenance 检查）；
- 该题 `is_completed=true` 时拒绝重复提交；
- `probe` / `dummy` / `contract_probe` 一律引擎级拒绝；
- 单位时间提交次数上限，禁止 shell 循环批量打。
- 代码落点：`internal/tools/registry.go` 或平台工具守卫、`internal/infoitem` 查询。

### P0-3 控制面 lane 的 terminal 收口（✅ 已落地，见 §11.2）

- `control_plane_api` 的 `terminal` / `http_request` 目的地址只允许控制面主机；
- 禁止对 `/challenges/submit` 做循环；reconcile intent 只读；
- 攻击请求必须来自 `target_kind=container` 的独立 intent。
- 代码落点：`internal/agent/agent_guard.go`、`internal/agent/tool_surface.go`、
  `internal/orchestrator`。

### P1-1 二进制 lane 补齐（未落地）

- full profile 增加 pwntools / checksec / rizin / ghidra headless；
- 建立“下载二进制 → 本地分析/复现 → 构造利用 → 读 flag”的固定流水线；
- F2 需要反编译与自定义 VM 识别，单靠 objdump 不够。
- 代码/构建落点：`tools/toolbox/install.sh`、`tools/toolbox/Dockerfile`。

### P1-2 多阶段链与容器身份（未落地）

- 建立“题号 ↔ 容器实例 ↔ 地址 ↔ 时间”的确定性映射；InfoItem 的 subject 应是
  **容器实例**，不是 IP；
- B 类拆成阶段 intent，凭证作为 InfoItem 跨 intent 传递；
- scope 按“任务/题”而不是“单容器地址”，避免 IP 回收后合法目标被自身 scope guard 挡住。
- 代码落点：`internal/graph`、`internal/infoitem`、`internal/scopeguard` / `agent_guard`。

### P1-3 worker 取消语义（已落地：终止原因与状态同事务写入，见 §11.3；budget/stagnant/human 类型码、owner 围栏与退出确认均已实现；parent-cancel 仍写 `parent scan stopped`）

- 区分 budget / lease / parent-cancel / platform-finish，写入 `blocked_reason`；
- 禁止成批 `context canceled` 记为 failed 且 error 为空；
- 保留最后一步工具调用与未完成假设。
- 代码落点：`internal/orchestrator`、`internal/agent`（`finished` / `agent_failed` 事件）、
  `internal/graph` 租约。

### P2 报告与得分对齐（未落地；submit 成功目前只回写提交守卫的通关状态，不生成 finding）

- submit 成功后强制投影 finding（或挂到已有 finding），消灭“得分但无 finding”；
- 对 `needs-manual-verification` 与平台得分做一次对账。
- 代码落点：`internal/tools/reporting`、`internal/agent` 收工路径。

---

## 8. 下一轮验收指标

| 指标 | 第五轮 | 第六轮目标 |
|---|---:|---:|
| 首次正确提交 | 7 min | ≤ 15 min |
| 已启动题数 | 38 / 63 | **63 / 63**（或每题有显式失败原因） |
| 完成题数 | 32 | ≥ 45 |
| 总分 | 8600 | ≥ 14000 |
| 未启动题对应分值 | 12300 | **0** |
| 0 分题的 slot-min 占比 | 57% | < 25% |
| 假 flag 提交 | 449 | **0** |
| 不存在题号的提交 | 52 | **0** |
| 控制面 lane 打容器 | 33 | **0** |
| cancel 无原因 | 79 | **0** |
| 得分题无 finding | 7 | **0** |

---

## 9. 复现口径

```sql
-- worker 分布与失败原因
select lane, status, count(*) from run_workers
where instance_id='5e132b0566b6c116ad13312f744fd430' group by 1,2;

-- 取消原因
select payload::jsonb->>'content' reason, count(*) from run_events
where instance_id='5e132b0566b6c116ad13312f744fd430' and type='finished'
group by 1 order by 2 desc;

-- 提交调用
select seq, occurred_at, payload::jsonb->'tool_args'->>'command'
from run_events
where instance_id='5e132b0566b6c116ad13312f744fd430'
  and type='tool_call' and payload::text like '%/challenges/submit%';

-- start 被拒
select seq, occurred_at, left(payload::text, 300)
from run_events
where instance_id='5e132b0566b6c116ad13312f744fd430'
  and payload::text like '%max active challenge instances%';
```

槽位占用按平台事件表的 `启动靶场` / `关闭靶场` 配对计算；未 close 的区间截至 14:09。

---

## 10. 边界说明

- 本文不把平台当作纯 CTF 评分器：**32/38 的转化率说明单点漏洞发现与利用能力可用**，
  失败集中在“覆盖分配、证据纪律、工具广度、长链能力”这些真实渗透同样需要的能力上。
- 也未把 403 条“答题失败”当作 403 次真实尝试：515 次 submit 中 449 次是探针/编造题号，
  其余 66 次是真实 flag，其中**错误提交只有 4 条**（2 条错题号 + 2 条未成功），
  31 条为通关后重复提交（与 §4.3 同口径）。
- B / F2 / F1 硬题在本轮**没有被真正验证**，只能说“未获得验证机会”，
  不能据此判定“引擎做不了”。
---

## 11. 整改落地状态（2026-11-24：P0-1 / P0-2 / P0-3）

> §1–§10 是第五轮观测，本节记**代码事实**。判据一律取 `docs/CORE_PHILOSOPHY.md`（v3.0 + v3.1/R7）
> 的可验收条款：七环归属、`observed/inferred` 二分、字段级权威、审计完整性。
> 未改动的冻结面：Verifier 判定与工具面、`report_vulnerability` 闸门顺序、三标签语义、
> HITL、六环安全链、30 分钟执行 lane 合同下限。

### 11.1 P0-2 提交守卫落到代码

根因（§4.3）：`BENCHMARK_OPTIMIZATION.md` P1-2 只把“禁止 probe flag”写进 prompt，没有代码闸门。
本轮把它做成工具面守卫，判据与实现一一对应：

| 判据 | 宪章条款 | 实现 |
|---|---|---|
| 只能提交本轮 `observed` 的值（flag 必须出现在工具输出里） | §3.2 observed/inferred；§4.4 禁止凭记忆复述 | `internal/scanctx/submitguard.go` `Observe/Observed`（每扫描 1 MiB 输出尾部）+ `internal/agent/submit_guard.go` 证据检查 |
| 题号必须来自平台 `/challenges` 清单 | §3.5 字段级权威 | `looksLikeChallengeCatalog` 只从挑战列表响应学 `unique_code`（上限 4096），模型的回显文本不算清单 |
| `probe` / `dummy` / `contract_probe` 引擎级拒绝 | §3.2 | `submitProbeRE` |
| 通关后不得重复提交 | §3.5 | 提交响应 `"correct":true` 回写 `MarkCompleted`，后续 `Reserve` 拒绝 |
| 禁止 shell 循环与单位时间刷接口 | §5.2 审计纪律 | `submitLoopRE` + 5 次 / 2 分钟 + 同题 20s 冷却；被拒调用不消耗额度 |
| 不得误伤被扫应用自己的 `/challenges` 路由 | 授权边界 | 守卫只对 `XALGORIX_CONTROL_PLANE_HOSTS` 目标生效；未配置控制面主机时完全惰性 |

挂点：`internal/agent/eino_observer.go` `BeforeTool` 的 gate 链（在 target-scope 之后），阻断走既有
`block()` 语义——产生可见 `tool_result`、不触发熔断，符合 `registry_test.go` 钉死的失败语义。

验收测试：`internal/agent/submit_guard_test.go`（未观测值/probe/清单外题号/循环/重复提交/lane 归属/目标应用路由）、
`internal/scanctx/submitguard_test.go`（来源追踪、清单学习边界、预算与冷却）。

### 11.2 P0-3 控制面 lane 收口

- 平台 API 的 **read / start / close / submit 一律只允许 control-plane lane**（`SurfaceOrchestrator`，
  即 `target_kind=control_plane_api` 的 intent）；execution/analysis lane 调用被守卫阻断并给出可纠正提示。
  对应 §4.6 的“reconcile intent 只读”要求：读也不再由容器 lane 发起。
- `checkHTTPHop` 增加**反向**限制：worker/analysis lane 不得访问配置的控制面主机
  （此前只限制了 API lane 不得打容器，反向无约束）。这消除 §4.6 的 33 次 API↔容器越界中的一侧。
- 关闭动作不再依赖任一 lane 的临场判断，改由引擎发放一次性 close intent（见 11.3）。

### 11.3 P0-1 槽位作为一等资源

| 机制 | 实现 | 直接对应的第五轮事实 |
|---|---|---|
| 无进展即回收 | `internal/orchestrator/reclaim.go` `watchProgress`：execution lane 连续 `WorkerProgressTimeout`（默认 20m，`XALGORIX_WORKER_PROGRESS_TIMEOUT` 可调，`0/off` 关闭）无信息层增量 → steer 保存 → 以 `stagnant` 类型 kill → settle **exhausted** | a-03 4.6h、a-18 3.1h、c-02 92min、e3-04 41min（596 slot-min 换 0 分） |
| 回收原因入审计 | `Repository.SettleIntent`：owner 围栏 + state/owner/lease/`blocked_reason` 单条 UPDATE，原因与终态同事务落库（内存/PG 两实现同步）；旧 worker 的过期租约被重新领取后无法改写新 owner 的 running 行 | §4.4 的 79 条 `context canceled` 无原因；控制审查的高 5/中 7 |
| 取消类型与退出确认 | `budget_exhausted` 成为引擎类型码（不再伪装成 `human`）；typed kill 被进度守卫拒绝时**不回落**普通 kill；`KillUnit` 记录 `KilledBy/KillReason`，worker 表写 `killed: …`；预算超时后 `WaitWorkerExit` 等 `done` 关闭才结算，原因带 `exit=confirmed/unconfirmed` | 控制审查的高 3/高 4 |
| 释放平台槽位 | `enqueueContainerClose`：容器 intent 无 flag 结束时发放**一条**幂等 close intent（priority 4000、180s timebox、`target_kind=control_plane_api`、endpoint 取指令中的 `/…/challenges` 前缀）；同容器还有 live sibling 时不发放 | a-18 容器持续 open 3.1h |
| 轮转前置 | close intent 优先级高于攻击 lane，materializer 下一 tick 即可补位 | §4.1 无“抢占/轮转”机制 |

设计约束（为什么不是改 timebox）：`minAttackTimebox = 30m` 是 R7 合同下限，不能因为跑分节奏改动；
进展闸门是**与预算并列**的第二道计时器，只判断“有没有新增信息”，不改变单个有产出 worker 的时间上限。
这与宪章 §2 Obligation（引擎强制义务，而非模型自述）一致：回收是确定性判据，不由 LLM 决定。

### 11.4 与 CORE 的对应（本轮改动的归属）

| 改动 | 七环位置 | 承载对象 | 不变量 |
|---|---|---|---|
| 提交守卫 | 证据 → 判定（提交层） | `scanctx.SubmitGuard`（per-scan scratch）+ 工具 gate | 不改 Verifier/报告门；guard 只拒调用，不产生 finding |
| lane 收口 | 行动（Operation）合法性 | `ToolSurface` + `checkHTTPHop` | 六环安全链不放宽，只收窄目的地址 |
| 进展闸门 | 义务（Obligation）执行 | `orchestrator` 进度投影 + `ProgressStore` | 合同 timebox 下限不变；超时先 typed kill → 确认退出 → 结算 |
| 终止原因 | 审计投影 | `exploration_nodes.blocked_reason`（与状态同一 `SettleIntent` UPDATE） | 只加原因，不改状态机；`done` 仍只能经 `ConcludeIntent` |
| close intent | 行动（Operation） | 探索图 `intent` 节点（`origin=engine`） | 平台生命周期仍由 control-plane lane 执行 |

### 11.5 仍未落地（不在本次提交内）

- **P1-1 二进制工具面**（pwntools/checksec/rizin/ghidra headless）：改 `tools/toolbox` 构建面，需镜像验收。
- **P1-2 多阶段链与容器身份**：InfoItem `subject` 改为容器实例、scope 按题授权、跨 intent 凭证传递。
- **P2 得分与报告对齐**：submit 成功 → finding/InfoItem 投影。当前 submit 结果只回写提交守卫的通关状态
  （用于去重），**不**生成 finding——`finding` 仍只能由 `report_vulnerability` 报告门写入（宪章 §8/§11 冻结）。
- **“63 题至少启动一次”的覆盖义务**：仍是 planner prompt 语义。要成为引擎义务，需要一个“题目覆盖”对象与
  收工闸门（§2 Obligation + §8 原语），本轮不做，避免把跑分队列伪装成既有对象。

### 11.6 第六轮验收口径（不变）

§8 的指标表继续有效；其中**假 flag 提交、不存在题号的提交、控制面 lane 打容器、cancel 无原因**四项
本轮已具备代码级保障，第六轮实测应直接观察是否为 0，而不是看 prompt 是否被遵守。
2026-11-24 控制审查整改后，终结路径还额外记录 `exit=confirmed/unconfirmed`、typed kill 来源
（`stagnant`/`budget_exhausted`/`human`）与 owner 围栏结果；复盘应按 run、unit、派发 agent 对齐这三个字段，
不再把“有终态”当成“已退出/原因正确”。
