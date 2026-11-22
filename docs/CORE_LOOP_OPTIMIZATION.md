# 信念层运行兑现优化方案（M1/M4/M5 运行验收，2026-11-22）

> **地位**：`CORE_LOOP_AUDIT.md`（同日 v2）定位了「代码验收已过、运行未兑现」的缺口与机制；本文给出兑现路径。服从 `CORE_PHILOSOPHY.md` v2.1（落地顺序、§12 不做清单、§2 入门测试）；与 REDESIGN.md R 系列并轨，不重复设计信息层。
>
> **根因一句话**：M4 消费向量与 claim 纪律挂在「专业评估委派流」上；CTF/benchmark 任务形态结构性关闭该流（`isExplicitCTFMission` → 单代理 → spawn 硬拦截 → 委派 nudge 不触发 → 提示词无 ledger 纪律），且该形态自身没有等价的信念循环；M5 另有两个默认关闭的开关（w=0、模型不传 voi）。机制细节见审计 §3 缺口①③。

---

## O0 — 零代码兑现（下一轮跑分立即生效）

| 改动 | 载体 | 兑现 |
|---|---|---|
| `XALGORIX_VOI_WEIGHT=0.25` | 环境变量 | M5 排序激活（w=0 完全保持旧排序，宪章 §7.2；0.25 在 ≤0.30 上限内）|
| 信念纪律块进 instruction | 任务 instruction | M1（preconditions 显式）+ M4（claim/evidence/终态闭环）|
| flag 即 `report_vulnerability` | 任务 instruction | 证据管线闭环（审计 §5）|
| objective 结构化判据 | 任务 objective（REDESIGN D10）| goal gate / goal obligation / goal_analyzed 进审计投影（事件投影验收）|

ledger 五件套工具对所有 agent 无条件注册（agent.go:662），CTF 单代理下 record/claim/update 均可用（claim 的 recon-first 闸对 ctfMission 显式豁免，ledger_tools.go:421）——**零代码路径成立**。

### instruction 纪律块（草案，插入现有跑分指令）

```text
每题动手前：
1. record_hypothesis：vuln_class / endpoint / preconditions 显式给出（未知写 unknown），
   尽力给 voi（0-1 信息价值估计：预期得分增益 × 成功概率）。
2. claim_next_hypothesis 认领后再测；不重复 record 已有假设（ledger 按 class/endpoint/param/role 去重）。
3. 有结论立即 update_hypothesis：proven（先 report_vulnerability，再以 kind=finding_ref 关联）
   或 rejected（附 baseline）。
4. 卡 15 分钟：rejected 或放弃本题，claim 下一题——下一题由 claim 排序决定，不按 instruction 顺序。
```

**边界**（宪章 §4.4）：纪律块只约束 ledger 生命周期（宪章既有对象），不引入「每操作强制三类点亮查询」；record/claim 是工具调用，不改变任何权威归属。

**风险**：纯提示词纪律依赖模型服从度。若下轮 claim 仍零观测 → 进 O1。

---

## O1 — 引擎小改（可选；O0 验收不过、或需要并行清题时做）

> **落地状态（2026-11-22）**：O1.1 已落地（`7b9463c9`），O1.2 已落地（见 git log）；审计 §4.2 迭代上限硬地板同日补修（`5185d33b`，v1 审计误报已更正）。

### O1.1 hookBeliefPoolBrief：CTF/单代理形态的信念循环【已落地】

- **注册**：`OnIterationStart`，紧邻 `hookGoalObligation`（RegisterDefaultHooks 尾部）；specialist / DelegatedAgent 不触发——lane 纪律已由委派 nudge 承载，避免双重指令。
- **触发**（全部满足才投递，幂等去重）：`ctfMission || (!DelegationEnabled && plan 尾部空转)` ∧ `Schedulable(8) 非空` ∧ 池内容距上次投递有变化（`state.BeliefPoolDigest` 内容戳）。
- **内容**：top-8（confidence+VOI 加权，与 ClaimNextAs 同一 betterHypothesis 排序）可调度池 + 一句纪律（claim→close→report）+ 明确定位「这是**选题与止损顺序**，不是广度要求」——与 CTF depth-first 教义对齐而非冲突。
- **七环定位（入门测试）**：信念环投影为义务（引擎 Hint，对象映射不反向）。消费者点名：`claim_next_hypothesis` 可调度池 = 区域点亮既有消费者（宪章 §4.1）；本 hook 只是把同一确定性投影从「工具里查得到」提升为「每轮看得见」——不新增权威、不绕过任何闸门、不给 InfoItem 置信度任何判定角色。
- **确定性**：纯 ledger 快照排序，同一快照同一输出；VOI 只影响顺序（宪章 §7.2）。
- **验收钉（全绿）**：
  - 金样本 `TestGoldenSchedulingParity` 保持绿（金样本非 ctf、plan 未尾空转——不触发）；
  - `belief_pool_brief_test.go` 五个 hook 单测：投递、内容不变不重投、claim 后重投、specialist/委派 root/发现/纯侦察静默、无 ledger/空池/全终态静默、plan 空闲专业扫描投递；
  - 与 `hookGoalObligation` 的顺序：goal 义务最后说话的注册序不变。

### O1.2 CTF 委派开关（解锁并行清题；审计 v1 §6.4 只有在此之后才可执行）【已落地】

- **形态**：`XALGORIX_ENABLE_CTF_DELEGATE`（默认 **off**，现行为逐字节不变）——off 时 `hookSingleAgentSpawnGuard` / `hookDelegationCoordinator` / `delegationEnabled()` / `maybeAutoDelegate` 现状全保留；on 时 ctfMission 不再短路委派（gate 方法 `ctfDelegationBlocked`；其余 CTF 语义不动：`ProfessionalAssessment=false`、advisory commitment 关闭、单旗止损教义保留；`DisableAutoDelegate` 仍然压过本开关）。
- **前置设计题（已裁决）**：
  1. **child 的 report 语义**：复用 `report_vulnerability` 门管线，不改——宪章字段级权威与任务形态无关；零代码（管线本就 registry 级）。
  2. **child 契约**：`buildDelegatedTaskInstruction(ctfMission)` 从「裸任务」升级为「裸任务 + benchmark 版 lane 契约」（claim 纪律 + flag 即报 `report_vulnerability` 并 `kind=finding_ref` 关联——缺口②的教训）。该分支只在开关 on 时可达（off 时 spawn 被硬拦截），故 off 行为逐字节不变。
  3. **belief pool brief 与委派互斥**：开关 on 时 `DelegationEnabled=true` → brief 自动静默（既有条件），无双指令。
- **验收（全绿）**：off 全量回归不变；on 模式 `delegationEnabled()` 放行、`maybeAutoDelegate` 不再因 ctfMission 短路（wave 由既有里程碑逻辑裁决）、finish gate 按 owner 收敛（`hypothesisBelongsToOwner` 已按 AssignedTo/Origin 区分，无需改）；`ctf_delegate_test.go` 钉住以上全部。

---

## O2 — 度量与回归

- 审计投影操作类型已齐（OpClaimHypothesis / OpSetVOI / OpRecordObjective / OpRecordHypothesis…），**无需新表新迁移**；下轮跑分后按审计 §5 用 psql 按类型计数出对照表，回写审计文档 v3。
- M5 排序确定性验证走既有金样本重放；运行侧以审计投影中的 claim 顺序为准（不可变快照原则，宪章 §7.2）。

---

## 顺序与红线

**顺序**：O0（零风险，先跑）→ 审计 §5 对照 → 不过或需并行清题 → O1.1 →（可选）O1.2。每步独立可回滚（宪章 §10）；前一步验收不过不进下一步。

**红线核对**（宪章 §9/§12、REDESIGN §10）：

- 不动 Verifier、报告门与 13 道闸门顺序；O1.1/O1.2 都不触 `verifier.go` / `report_vulnerability` 管线；
- 不动热路径与 JSON 写入源；无 DSN 全功能（O1.1 只读内存 ledger，不触 PG）；
- Obligation 不被置信度/VOI 绕过（本方案只加投影与配置，不加权威）；
- 对象映射不反向（instruction 纪律块是给模型的 Hint；objective 已是 `source=human` 的 InfoItem）；
- River/审批/agentsgraph/SSE 范围不变（O1.2 只放开 spawn 的模式短路，不新增委派旁路）。
