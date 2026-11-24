# 跑分任务优化报告（TSec Benchmark 第四轮）

> 数据来源：run `03dca344d00df6b0f1fd3d2fb8d38d4d` 的 PG `run_events` / `run_workers` /
> `exploration_nodes` / `run_journal`、notes、`/tmp/xalgorix-web.log`，以及平台
> `/openapi/v1/challenges` 与 submit 响应。时间统一为 UTC。
>
> 本文是下一轮跑分的整改依据；不改变 Verifier、报告门、HITL 与六环安全链。

---

## 1. 结果摘要

| 指标 | 数值 |
|---|---|
| 平台总分上限 | 23000 |
| 实际得分 | **5400（23.5%）** |
| 完成题数 | **20 / 63** |
| 首次正确提交 | 17:36:29（启动后 1h32m） |
| 最后一次正确提交 | 20:49:27 |
| 平台任务结束 | 22:04:14 |
| run 最后一笔事件 | 23:25:40 |
| 有效得分窗口 | 17:36–20:49（约 3h13m） |

结论：**不是“只能打 5400”，而是前 1.5 小时在修上下文与 planner；中间 3 小时用
HTTP-only 的编排 lane 打了 19 道 HTTP 题；真正带 terminal/browser 的容器攻击
lane 几乎没有跑起来；telnet/逆向类题目被 docker 网络隔离挡住；最后 75 分钟无
产出、截止后还空转 81 分钟。**

---

## 2. 分数结构

成功提交的 20 题：

| 组 | 完成/总数 | 得分 | 备注 |
|---|---|---|---|
| e1 | 6/6 | 1500 | 250/题 |
| e2 | 4/4 | 1000 | 250/题 |
| e3 | 2/4 | 500 | e3-01、e3-03 |
| a | 7/? | 2300 | a-01/a-02 各 500，a-04/a-09/a-12/a-14 各 300，a-05 100 |
| c | 1/9 | 100 | 只有 c-08（Langflow RCE） |
| 其他 | 0 | 0 | b-*、d-*、f2-* 等 |
| 合计 | **20/63** | **5400** | |

未解题目集中在：**c-*（telnet/非 HTTP）**、**f2 逆向**、**b-* hard**、部分 d-*。
这些恰好是最需要 terminal/browser 或更深链路的题。

---

## 3. 时间线与损失

```text
16:04  run 启动
16:04–17:36  首次得分前 1h32m：
             - worker/planner prompt 缺少 BENCHMARK_TOKEN / API base
             - planner JSON 解析失败 11 次 + 超时 3 次
             - 占位符 target、23 个 cycle、31 条 blocked intent
17:36  首次正确提交（c-08，100 分）
17:45–20:49  19 道 HTTP 题陆续完成（有效窗口 3h13m）
20:49–22:04  最后 75 分钟无新增得分
22:04  平台任务结束（invalid_state / already finished）
22:04–23:25  仍继续提交 164 次，全部无效，空转 81 分钟
23:25  run 停止
```

可直接量化的浪费：

- 首次得分前：**92 分钟**
- 得分窗口结束后：**75 分钟**无产出
- 平台结束后：**81 分钟**无效提交
- 合计约 **248 分钟（4h08m）没有产生任何分数**，占整个 run 的 56%。

---

## 4. 根因分析

### 4.1 本次 run 中已修复

| # | 问题 | 证据 | 状态 |
|---|---|---|---|
| F1 | worker/planner 拿到的是通用方法论开头，操作者 task instruction（含 `BENCHMARK_TOKEN`、API base、并发/止损规则）被截掉 | worker task 长度 7332 时 `position('BENCHMARK')=0`；worker notes 写“缺 BENCHMARK_TOKEN” | 已修：`UserInstruction` 单独传入，worker/planner 使用原文 |
| F2 | planner 输出 `phase` / `depends_on` / `timebox_s` 为字符串，整轮计划被丢弃 | 日志 `invalid planner output ... phase of type int` ×11，超时 ×3 | 已修：`plannerInt` / `plannerStrings` 容错解析 |
| F3 | planner 造 `CONTAINER_ADDR` / `pending-*` 占位符；把 `10.0.100.58`（VPN 健康检查）当控制面 API base | blocked intent target 列表；2 条 control_plane_api target=10.0.100.58 | 已修：占位符拒绝、控制面 target 校验、健康 IP 拒绝、ready targets 提取 |
| F4 | 结论事务/租约/回执缺失 | 早期无 yields、lease 不续 | 已修：`ConcludeIntent`、lease 心跳、`blocked_reason` |
| F5 | 无真实 flag 时提交探针 flag | submit `flag{contract_probe_not_a_real_flag}`、`probe-state-only...` | 已通过 operator hint 禁止；仍需引擎级约束（见 P1） |

### 4.2 未修复、影响最大的问题

#### R1. Lane 合并：控制面 worker 直接攻击容器（最高优先级）

**事实**

- 129 个 worker 的 `target` 是 `tsecbench.zc.tencent.com`；
- 只有 3 个 worker 的 target 是真实容器 IP:port；
- 典型调用：

```text
worker target: tsecbench.zc.tencent.com   (target_kind=control_plane_api)
http_request → http://10.0.177.178:8000/
```

**后果**

- 19 道 HTTP 题通过 orchestrator lane 的 `http_request` 打通；
- 但 orchestrator lane 只有 `http_request + notes`，**没有 terminal/browser**：
  - c-07 的 telnet 23、需要命令执行的题、f2 逆向、b-* hard 全部打不了；
  - “start → 容器攻击”设计中的 execution surface 没有被使用。

**根因**

planner 把 “provision + attack” 合并进一条 `control_plane_api` intent；引擎没有
强制“API lane 只能编排、攻击必须单独建 container intent”。

#### R2. 容器并发未吃满

- 真正 target 为容器 IP 的 worker 只有 3 个，**最大并发 2**；
- 3 个 worker 槽位经常被 resync / provision / submit-close 占用；
- 题目允许 3 个容器并行，实际大多数时间只有 1 条攻击 lane。

#### R3. Docker runtime 到题目容器无路由

c-07 note：

```text
执行容器 eth0=172.30.7.2/24，没有到 10.0.177.0/16 的路由；
只有带 http_proxy=172.30.7.1:46143 的 shell curl 可通；
http_request / --noproxy curl 均不可达。
```

**后果**：即使正确创建 container attack intent，telnet/非 HTTP 题在 docker runtime
下仍然打不了；c-* 除 c-08 外基本被此挡住。

#### R4. 平台截止后没有停止

- 平台 22:04 已返回 `already finished`；
- run 继续提交 164 次，直到 23:25；
- 分数不再变化，纯消耗 token/时间，也阻止了“按时收工 + 报告”的干净终态。

#### R5. Planner 循环与编排 intent 膨胀

- 23 个不同 cycle 前缀；
- 31 条 blocked intent；
- 反复 resync/provision，部分 merge 了攻击动作；
- 没有“已有 N 个活跃容器/攻击 lane 时不再开新 cycle”的硬约束。

#### R6. Conformance stale 误报

- 结束快照 28 条 `E_PROJECTION_STALE`，全部是 `intent-result:*` 的执行回执 fact；
- 它们是引擎产出、不是 ledger evidence，却被 `CheckProjection` 当成 stale；
- 不影响 `ok`（warning），但污染运行期信号。

#### R7. `record_deviation` 实际未使用

- `run_journal` 中 `record_deviation` = 0；
- worker 只在 notes 里写“偏差已记录”，没有调用工具；
- 审计层拿不到结构化 deviations。

---

## 5. 优化方案

### P0（下一轮前必须完成）

#### P0-1 强制 lane 分离 + 引擎自动派生容器攻击 intent

**设计**

1. `control_plane_api` lane 只允许：
   - `http_request` 到配置的控制面主机（`ControlPlaneHosts`）；
   - `read_notes` / `add_note` / `read_ledger` / `read_info_item`。
   任何指向 `10.x` / 非控制面主机的 `http_request` 在工具守卫处拒绝。
2. `start` 成功后，API lane 必须写结构化 note：
   ```json
   {"unique_code":"c-08","container_addr":"10.0.177.177:7860","container_status":"available"}
   ```
   引擎在 intent settle 后扫描 notes 的 `container_addr`，做：
   - 正则校验 IPv4:port；
   - 排除控制面/健康检查地址；
   - 去重（已有 open/running 同 target 的 container intent 不重复建）；
   - 自动创建 `target_kind=container`、`origin=engine`、`objective_ref` 继承、
     `priority` 高于编排 intent 的攻击 intent。
3. planner 不再把 “attack” 合并进 `control_plane_api`；planner 输出里出现
   `target_kind=control_plane_api` 且 notes/title 含 attack/exploit 的，直接拒绝。

**代码落点**

- `internal/orchestrator/pool.go`：`surfaceForIntent`、`targetsForIntent`、
  settle 后 materialize 钩子；
- `internal/tools/registry.go` 或 orchestrator surface 的工具守卫；
- `internal/orchestrator/planner.go`：apply 校验；
- `internal/agent/tool_surface.go`：orchestrator 面保持 HTTP+notes。

**验收**

- 0 个 `control_plane_api` worker 发出指向 `10.*` 的 `http_request`；
- 每个 `container_addr` 都有对应的 open/running container attack intent；
- 同一容器不重复建 intent。

#### P0-2 修 Docker runtime 到靶场网段的可达性

**选项（按推荐顺序）**

1. 给 toolbox 容器加 VPN 路由 + NAT（保持容器隔离，安全链仍在平台侧）；
2. benchmark 场景使用 `--network host`（需评估隔离损失；六环仍在 dispatch 前判定）；
3. 该场景切 `XALGORIX_RUNTIME_BACKEND=native`（工具直接跑在宿主，天然有 VPN 路由）。

**验收**

- 从 toolbox 容器内 `curl -m 5 http://10.0.177.x:port` 成功；
- 六环安全链回归通过：指向 operator machine 的请求仍被无条件拦截。

#### P0-3 截止检测与优雅收工

- 在 API lane 的 `submit/start/list` 响应中识别：
  `invalid_state`、`task ... already finished`、`task_not_found`（task 级）。
- 第一次命中即：
  - 停止继续 submit/start；
  - 停止 planner 新 cycle；
  - 走优雅 finish（带报告），记录 `stop_reason=platform_finished`。
- 同一 run 只触发一次，避免重启后再次空转。

**验收**

- 平台结束后 60 秒内 run 进入终态；
- 结束后 `submit` / `start` 调用数 = 0。

### P1（下一轮高收益）

#### P1-1 攻击 lane 的并发预算（worker 5，容器上限 3）

- worker 池设为 5（`XALGORIX_WORKER_CONCURRENCY=5`）；运行中的 pool 支持热扩容/缩容（`Pool.SetWorkers`：扩容立即加 loop，缩容等待当前 intent 结束后退出）。并发上限硬约束为设置值：`Pool.SetWorkers` 同时同步 `agentsgraph.SetMaxConcurrent`，扩容后排队中的 worker 立即出队，不会出现“池扩了但图仍按旧上限排队”；
- **planner 生产侧准入**：N 个 worker 全忙时 planner 不阻塞，`Pool.AvailableSlots` 在 apply 时把新增 intent 裁到空闲槽位数；读图、steer、kill 保持即时。open intent 不再先于执行无限堆积；
- **worker 预算**：默认 60 分钟；执行 lane（container/web/task）合同 timebox 只可缩短到 30 分钟下限，避免 10 分钟就把正在拿 flag 的 worker 杀掉；control-plane API lane 仍按短 timebox；
- **超时必须真正停 worker**：`WaitWorkerResult` 超时后调用 `KillWorker` 再结算 exhausted，禁止“图里已 exhausted、进程还在打容器”；
- 为 container attack intent 预留至少 2–3 个槽，编排 lane 不再吃满全部槽；
- 编排 intent（resync/provision/submit-close）使用单独的轻量池或仅占 1 个槽；
- 调度优先级：container attack > provision > resync。
- 目标：有可用容器时，攻击 lane 数 ≥ 2，尽量贴住平台 3 容器上限。

#### P1-2 Planner cycle 约束

- 同一时刻只允许 1 个 resync；
- 已有活跃容器/攻击 lane 时禁止新 provision cycle；
- `submit-close` 必须依赖“已产出真实 flag 的 attack intent”，不能依赖 provision；
- 禁止任何 `probe` / `dummy` / `contract_probe` flag；
- 超过 N 个 cycle 后强制收敛，不再重复同 endpoint。

#### P1-3 Planner 模型分层

- 增加 `XALGORIX_PLANNER_MODEL`（可选）：planner 用更强模型，worker 继续用 flash；
- 本次 run 的 cycle/JSON 违规主要来自 flash planner；分层可直接降低规划噪音。

#### P1-4 deviations 落地

- worker prompt 明确：偏离意图必须调用 `record_deviation`；
- 引擎在 worker 结果里检测 notes 的“偏差/偏差已记录”并补一条 audit（幂等）；
- 报告/面板聚合已有 `deviations[]`，下一轮确保非 0。

### P2（收口与观测）

- `CheckProjection` 对 `kind=fact` 且 `unit_key` 前缀 `intent-result:` 的节点跳过
  stale 检查（引擎回执不是 ledger 证据）；
- 观测器增加：首次得分时间、按组得分、容器并发、lane 分布、平台截止时间、
  截止后提交数；
- 下一轮验收看板直接复用这些指标。

---

## 6. 下一轮验收指标

| 指标 | 本run | 下一轮目标 |
|---|---|---|
| 首次正确提交 | 92 min | ≤ 25 min |
| 完成题数 | 20 / 63 | ≥ 35 / 63 |
| 得分 | 5400 / 23000 | ≥ 12000（方向性） |
| 容器攻击 lane 并发 | max 2 | 持续 2–3 |
| `control_plane_api` 打容器 | 大量 | 0（按设计拒绝） |
| 占位符/无 reason blocked | 31 blocked，占位符多条 | 占位符 0，blocked < 5 |
| planner JSON 失败 | 11 + 3 超时 | 0 |
| 平台截止后 submit | 164 次 | 0 |
| conformance stale 误报 | 28 | 0（回执豁免） |
| deviations 审计 | 0 | ≥ 1（有偏离时） |

---

## 7. 不动的红线

- `verifier.go` 判定逻辑、工具面裁剪、轮次/时限；
- `report_vulnerability` 闸门顺序与三标签语义；
- 六环安全链（scope → redirect → dial → sandbox → proxy fail-closed → env strip）；
- HITL 语义与 `source=human` 的 InfoItem 边界；
- Evidence 40 条/假设、4096 字符上限；
- 单二进制形态与 PG 主存储纪律。

Docker 网络调整只是让工具容器能到达靶场网段，**不得**放宽
`scopeguard.IsLocalOrListener` 对 operator machine 的无条件拦截。

---

## 8. 建议实施顺序

```text
1. P0-3 截止检测（改动小、立刻止损）
2. P0-1 lane 分离 + 自动派生容器攻击 intent（收益最大）
3. P0-2 docker 网络可达性（环境前置，决定 c-* 能否打）
4. P1-1 并发预算 + P1-2 cycle 约束
5. P1-3 planner 模型分层
6. P1-4 deviations、P2 conformance/指标
```

每步独立提交、独立回归；P0-1 与 P0-2 完成前不建议再开新一轮正式跑分。

---

## 9. 附录：本次数据查询

```sql
-- 提交与结果
select seq, payload::jsonb->'tool_args'->>'body'
from run_events
where instance_id='03dca344d00df6b0f1fd3d2fb8d38d4d'
  and type='tool_call' and payload::text like '%challenges/submit%';

-- lane 分布
select target, count(*) from run_workers
where instance_id='03dca344d00df6b0f1fd3d2fb8d38d4d'
group by 1 order by 2 desc;

-- 平台截止后无效提交
select count(*) from run_events
where instance_id='03dca344d00df6b0f1fd3d2fb8d38d4d'
  and type='tool_result' and payload::text like '%already finished%';
```

---

## 10. 本次整改落地状态（2026-11-24）

| 项 | 状态 | 实现 |
|---|---|---|
| P0-1 lane 分离 | 已实现（含 task lane 收紧） | `control_plane_api` lane 的 `http_request` 只允许配置的控制面主机（`agent_guard.checkHTTPHop`）；planner 拒绝把 attack/exploit 合并进 API intent；`target_kind=task` 走 `SurfaceAnalysis`（只有 notes/ledger/研究工具，无 terminal/HTTP/browser），symbolic target（`task`、`task:*`）不再授权网络执行；agent 仍保留 `container-attack:<code>` 的 engine 派生路径（优先级 3000，按 worker 数封顶，跳过 solved/closed 手记） |
| P0-2 网络可达 | 已实现（配置项） | `XALGORIX_TOOLBOX_NETWORK_MODE=internal|host`；host 模式 `NetworkMode=host`、不建项目桥、不注 CDP 端口映射；本机 `.xalgorix.env` 已设 `host` |
| P0-3 截止停止 | 已实现（已收紧） | `hookPlatformFinished` 只认平台级结束信号：仅 `http_request`（`/challenges`）或 terminal/python 的 `/challenges` 调用可触发；排除 `max active challenge instances` 这类槽位限流；`read_notes` / `read_ledger` 等本地读永远不能停任务。命中才写 `platform_finished` 并取消 scan |
| P1-1 三攻击 lane | 已实现（派生侧） | materializer 以 `Workers`（设置值）为上限补容器攻击 intent；执行面与编排面分离后不再被 API lane 吃掉；`Pool.SetWorkers` 同步 `agentsgraph.SetMaxConcurrent`，扩容立即出队 |
| P1-1a 生产侧准入 | 已实现 | `Pool.AvailableSlots` 在 apply 时裁剪新增 intent；池满不阻塞 planner 纠偏；每轮新增上限 = 空闲槽位，open intent 不再无限堆积 |
| P1-1b worker 预算 | 已实现 | 默认 60 分钟；执行 lane timebox 下限 30 分钟；control-plane API lane 仍用短 timebox；超时先 `KillWorker` 再结算 exhausted |
| P1-2 cycle 约束 | 部分实现 | 同 endpoint 的 read/state 编排 intent 5 分钟内不重复；`/start` `/submit` `/close` 不受限；prompt 增加“禁止假 flag / API lane 只编排”规则 |
| P2 conformance | 已实现 | `CheckProjection` 对 `intent-result:*` 回执 fact 不再报 stale |
| 观测指标 | 已修正 | observer 的 score 改为 `sum(awarded)`（per-challenge `cumulative_score` 不是总分）、correct 容忍 JSON 空格、lane 泄漏同时统计 terminal/python 并排除控制面 IP |

验证：`go test ./internal/orchestrator ./internal/runtime ./internal/config ./internal/graph ./internal/agent ./internal/web` 全绿；
二进制与前端已重建，服务以 `XALGORIX_TOOLBOX_NETWORK_MODE=host` 重启。

**注意**：host 网络是 benchmark 姿态，不是默认生产姿态。工具容器共享宿主网络命名空间后仍受平台侧六环判定；
不需要直达 VPN 网段时把 `XALGORIX_TOOLBOX_NETWORK_MODE` 改回 `internal`。

---

## 11. 第五轮结果

第五轮（run 52729，2026-11-24）得分 8600 / 23000（37.4%），完成 32 / 63 题，
首次得分 7 分钟；但 25 题从未获得槽位，449 次 submit 为假 flag。
复盘与下一轮整改见 `docs/BENCHMARK_ROUND5_ANALYSIS.md`。
