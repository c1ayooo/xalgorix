# R6 — Controlled Experiment & Regression

R6 不是功能里程碑，而是用固定、可复现的受控靶场证明 R1–R5 恢复了“假设驱动探索”，而不是只减少请求。本文是实验协议；跑分在 R6 验收后单独执行。

## 1. 前置

- 新版本：`feat/hypothesis-driven-r1-r5`（R1–R5，提交 `1e5de1ad` 起）。
- 基线：R1 之前的 `79101f7f`（同一仓库，干净 worktree 构建）。
- 同一模型、同一温度、同一 instruction、同一靶场数据；每场景建议重复 3 次取中位数。

## 2. 固定靶场

`scripts/r6/lab/`：仅绑定 `127.0.0.1:18080` 的确定性靶场（不是 benchmark，不对外）。

| 端点 | 预期缺陷 |
|---|---|
| `GET /api/users?id=N` | IDOR：任意 id 返回用户与 token |
| `GET /search?q=` | SQLi：字符串拼接，单引号触发 DB 错误 |
| `GET /files?name=` | 路径穿越：可读 `../` 文件 |
| `GET /admin`（`X-Debug: 1`） | 访问控制缺陷，返回 `R6-ADMIN` |
| `POST /login`（admin/admin） | 默认弱口令 |
| `GET /redirect?next=` | 开放重定向 |

启动：`docker compose -f scripts/r6/lab/docker-compose.yml up -d --build`
（A/B 脚本会自动管理生命周期；只绑 loopback。）

## 3. 指标（`scripts/r6/collect_metrics.py`）

只读 PG，按 `instance_id` 汇总；不写库、不成为状态权威。

| 指标 | 含义 |
|---|---|
| `time_to_first_hypothesis_s` | run 开始 → 第一条模型 `record_hypothesis` |
| `startup_tool_calls_before_first_hypothesis` | 形成第一个假设之前的工具调用数（启动扫描强度） |
| `formal_hypotheses_journal` / `claims_journal` | 正式假设数 / 被 claim 数 |
| `intents_with_decision_basis` / `intent_nodes` | 带 `decision_basis_refs` 的 intent 比例 |
| `info_reads`（`_with_refs/_truncated/_incomplete`） | R3 读取审计 |
| `repeat_attempts` / `action_attempts` | R3 重复动作（`prior=A-x`） |
| `egress_pauses` / `egress_stops` | R4 目标限速与停止审计 |
| `formal_hypotheses_ledger` / `coverage_seed_hypotheses` | R2：正式信念 vs 覆盖种子 |
| `hypotheses_with_quality_fields` | claim + preconditions + falsification 齐全的假设 |
| `evidence_explicit_info_links` / `evidence_intent_links` | 观察是否挂回 InfoItem / Intent |
| `findings_total` / `findings_tags` | 发现数量与验证标签 |
| `tokens` / `tool_calls_total` / `duration_s` | 成本 |

注意：`coverage_seed_hypotheses` 在 R1–R5 后应为 0（新 run 不再写 seed）；历史 run 仍可能 >0。

## 4. 场景矩阵（`scripts/r6/scenarios.json`）

| id | 模式 | 说明 |
|---|---|---|
| `r7` | planner_worker | 默认拓扑 |
| `single` | legacy + 禁自动委派 | 单 Agent 消费路径 |
| `ctf` | legacy, quiet | CTF 单代理 + 低噪音 |

## 5. 运行

```bash
# 仅准备（靶场 + 基线/新版构建，不跑扫描）
python3 scripts/r6/run_ab.py --baseline-ref 79101f7f \
  --scenarios r7 --out /tmp/r6 --prepare-only

# 完整 A/B（需要 ~/.xalgorix.env 中可用的 LLM 与 DSN）
python3 scripts/r6/run_ab.py --baseline-ref 79101f7f \
  --scenarios r7,single,ctf --out /tmp/r6
```

产物：

- `/tmp/r6/<scenario>-<variant>.json`：单次指标；
- `/tmp/r6/comparison.json` / `comparison.md`：基线 vs 新版与 PASS/WARN 检查；
- `/tmp/r6/work/server-*.log`、`data-*`：隔离的 HOME/数据目录。

A/B 每次只启动一个服务（`127.0.0.1:19137`），共享 PG 但使用独立 `XALGORIX_DATA_DIR`；脚本退出时销毁靶场（`--keep-lab` 保留）。

## 6. 验收（R6 完成判据）

对每个场景：

1. 新版产生 ≥1 条 formal hypothesis，且 `time_to_first_hypothesis_s` 不显著晚于基线（脚本阈值：≤1.25×+30s，仅 WARN）。
2. `claims_journal > 0`（正式假设确实被消费）。
3. 有 intent 时，`intents_with_decision_basis > 0`。
4. `repeat_attempts` 有记录且可沿 `A-x` 回溯；`egress_pauses/stops` 出现时，后续请求由 R4 预算拦截（日志/`[EGRESS POLICY]`）。
5. `findings_total` 不低于基线；验证标签不得伪造。
6. R2–R5 的既有单测全绿（`go test ./internal/...` 相关包），Verifier/report/Finding/安全链未改。

**不得**用“请求更少/查询更多”单独判定成功；若指标冲突，以“信息是否改变假设/Intent、发现是否可复核”为准。

## 7. 解释与局限

- LLM 非确定性：单次结果不能定论，需重复并取中位数；固定模型/温度/instruction。
- 基线 `79101f7f` 没有 R2 的 quality 字段与 R3/R4 journal 操作，部分新指标在基线上为 0/缺失，属于版本差异，不是回归。
- 靶场是**确定性本地 fixture**，只用于行为对照，不代表真实目标难度。
- 跑分（benchmark）在 R6 验收后单独执行；R6 不修改认知原则或算法。
