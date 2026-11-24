# 情报作为战果：跨 intent 交接与授权规格（Intel Handoff & Access Spec）

> 版本 v1.0（2026-11-24）
> 适用对象：orchestrator / agent / graph / web 后端，情报 tab 维护者，HITL 与围栏负责人
> 上位文档：`docs/CORE_PHILOSOPHY.md`（v3.0 + v3.1/R7）、`docs/INFO_GRAPH_CONTRACT.md`、`docs/DUAL_GRAPH_ORCHESTRATION.md`
> 外部输入：`docs/XALGORIX_CONTROL_REVIEW_20261124.md`、`docs/PROJECT_IMPLEMENTATION_ISSUES_20261124.md`
> 本文只做一件事：把「情报 tab 已经记下来的东西」接成「下一条 intent 能接手战果、授权跟着战果走」。不改 Verifier 判定、`report_vulnerability` 闸门顺序、三标签语义与六环安全链其余部分。

---

## 1 目标与非目标

### 1.1 目标

1. 一条 intent 产出的战果（凭据、会话、打通的路径）能确定性地下传给同一扫描内的后续 intent，不依赖模型自觉。
2. 战果带来的是**可达性事实**，不是**授权**；授权由围栏与人工确认决定，可审计、可失效。
3. 每次使用战果都有回执（谁、何时、对哪个目标、结果如何），失败可标记失效，不再反复试死凭据。
4. 情报条目从不进模型上下文的明文里；种子只给身份与引用，正文按需拉取。

### 1.2 明确不做

- 不做永久扩权：批准一次只对一次目标生效，不写回任务 scope。
- 不做跨项目、跨任务的情报复用（`project_id` 之外一律不可见）。
- 不做战果自动生成 finding：`finding` 仍只能由 `report_vulnerability` 报告门产生。
- 不把 `intelligence.Item` 改成可变对象：观察不可改，派生可重放。
- 不引入新的编排引擎或新状态机框架；复用现有 `paused` / `requires_confirmation` / 图节点。
- 不在本轮改 `intelligence` 的落库形态（`run_state_files` 整包快照保持）。

---

## 2 现状（代码实测）

| 能力 | 现状 | 位置 |
|---|---|---|
| 情报条目 | `intelligence.Item{ID,Fingerprint,Kind,Title,Content,Endpoint,Confidence,CreatedAt,UpdatedAt}`，`Kind ∈ {credential, lead, sensitive_data}`；标题 512 / 正文 4096 / 字段 512 截断 | `internal/tools/intelligence/intelligence.go:41,55,71` |
| 指纹 | `sha256(kind + NUL + 归一化 content + NUL + endpoint)` 前 16 位，任务级身份，同一观察跨 run 聚合 | 同上 `:73` |
| 写入 | 工具 `record_intelligence`，参数 kind/title/content/endpoint/confidence | `internal/tools/intelligence/intelligence.go:507` |
| 读取 | 工具 `read_intelligence` 存在，worker 工具面未被禁 | 同上 `:522`；`internal/agent/tool_surface.go` |
| 持久化 | `intelligence.Backend`：`SaveIntelSnapshot(scanID, instanceID, seq, data)` 整包重写、`LoadIntelSnapshot`、`LoadLatestIntelSnapshotForInstance`（D11 同胞会话继承） | `internal/tools/intelligence/backend.go:27` |
| 人工分诊 | `platform.IntelTriage{Status ∈ open/confirmed/dismissed}`，键 `(task_id, fingerprint)`，引擎记录与人工标注冲突保留可见 | `internal/platform/intel_triage.go` |
| 任务级视图 | `GET /api/tasks/{id}/intelligence` 跨 run 聚合 + 指纹去重 + 分诊叠加；`PATCH /api/tasks/{id}/intelligence/{fingerprint}` 写分诊 | `internal/web/handlers_task_intelligence.go:48` |
| worker 种子 | 段落为 hints / intents / beliefs / findings / observations / coverage / notes / actions，**无 intelligence 段** | `internal/agent/context_snapshot.go:279-501` |
| 种子预算 | `snapIntentsMax=12`、`snapBeliefsMax=15`、`snapFindingsMax=12`、`snapObsMax=10`，总预算为模型窗口 25% | 同上 `:47-93` |
| planner | 全局 grep 无 intelligence 读取 | `internal/orchestrator/planner.go` |
| 可达性判定 | `targetsForIntent` 只读 intent 自身 `target_kind/target/endpoint`；`scope` 类走 `scopeTargetAuthorized` | `internal/orchestrator/pool.go:1053` |
| 结案闸门 | `done` 只能经 `ConcludeIntent`；前置条件为 `payload->'contract'->'preconditions'` 的 `(kind, unit_key, state)` 三元组 | `internal/graph/pg.go:261,416` |

**结论**：观察层、去重、分诊、任务级视图都已经存在；缺的是三样——种子可见性、可达性语义、使用回执。这三样正好对应审计里 XALG010（索引可解析）、XALG023（身份而非地址）、XALG001（有交棒动作才谈得上交接）。

---

## 3 架构决策

### D1 情报保持不可变，另建「可达性投影」

- **选择**：`intelligence.Item` 一字不改；新增 `intel_access`（派生）与 `intel_access_targets`（可达目标）两张 PG 表，由结案事务派生。
- **理由**：`SaveIntelSnapshot` 是整包重写语意，并发写会互相覆盖；且 dispatch 前的判定跑在热路径，必须能同步读一张表，而不是解一个大 JSON 快照。
- **代价**：一条情报变成两处状态，存在短暂不一致窗口。
- **补偿**：派生只由 `ConcludeIntent` 单点触发、以 `fingerprint` 幂等；另提供 `rebuild intel_access from snapshot` 的离线重放，随时可对齐。

### D2 可达性用结构化目标，不用文本解析

- **选择**：`intel_access_targets.target` 只接受规范化后的 `host:port` 或 `CIDR`，写入前过统一规范化器；解析不出结构化目标的情报只保留 `lead`，**不产生 grants**。
- **理由**：XALG020 的教训就是同一套范围语法存在两实现（web 侧 `targetHostPort` 截断 CIDR、agent 侧 `ParseCIDR` 正常），文本解析在授权路径上是事故源。
- **代价**：拿不到结构化目标的凭据不能自动接力（需要人补 `grants`）。
- **补偿**：情报 tab 的 PATCH 允许人工补 `grants`，与分诊同一次动作完成。

### D3 授权扩张只能通过一次性 capability

- **选择**：新增 `access_capabilities`（intent_id、access_id、target、uses_left、expires_at、issued_by）。判定式唯一：`allowed = inScope(dst) || capabilityValid(intent, dst, now)`。
- **理由**：写回 scope 等于永久扩权，审计上无法回答「谁批准了哪一次」。
- **代价**：越界使用多一次审批往返。
- **补偿**：批量批准（同 /24 或同 access 的多目标一条批）+ 规则层自动归类，把八成申请挡在人之前。

### D4 人的点头复用 triage，不新建审批状态机

- **选择**：`triage=confirmed` 表示「这条战果真实且可用」；它允许**在 scope 内**使用。越出 scope 才需要 capability。`dismissed` 直接不让用。
- **理由**：情报 tab 的人工确认本来就是「人点头」，再造一套审批对象会让同一件事有两个状态机。
- **代价**：两个概念（可用 / 可越界）要在 UI 与文档里写清楚，不能混。
- **补偿**：capability 申请里回显来源 fingerprint 的 triage 结论，审批人一眼看到「战果已被确认，这次只是问越界」。

### D5 种子只给身份，不给正文

- **选择**：`context_snapshot` 新增 `Available intel (confirmed access)` 段，每行 `fingerprint | kind | subject | grants | state | expires_in`，正文与明文一律不进种子，用 `read_intelligence` 按 fingerprint 取。
- **理由**：种子预算只有模型窗口 25%，且明文进上下文等于永久落在 transcript 与日志里。
- **代价**：模型多一步读取。
- **补偿**：种子按本 intent 的 target 过滤，一行即可判断「有没有我需要的」；缺失时给明确提示「read_intelligence(fingerprint=…)」。

### D6 交棒必须有回执，否则不算结束

- **选择**：每个执行 lane 的 intent 结束必须声明 `exit ∈ {none, access:<fp>, finding:<id>}`；`none` 必须带 `blocked_reason`。这条挂在 XALG001 的 `complete_work` 协议上。
- **理由**：没有回执，链断在哪一环永远查不出来（第五轮 79 条无原因取消就是这种）。
- **代价**：依赖 XALG001 先落地。
- **补偿**：XALG001 未落地前，本规格的 M1/M2 可先做「只读可见」（种子 + 解析），回执留到 M3。

### D7 围栏三档决定 capability 的发放策略

| 场景 | 策略 | 自动化程度 |
|---|---|---|
| `fence.enforce=false`（靶场/自建演练） | grants 自动并入许可，不发申请 | 全自动 |
| `fence.enforce=true`（真实项目，默认） | 越界必须 capability，走确认门 | 人批或规则批 |
| 命中 `Fence.DenyTargets` | 永不发放，连申请都不生成 | 硬拒绝 |

- **理由**：同一引擎两种姿态，跑分不被 HITL 拖速，客户项目不越界。
- **代价**：两套语义要各自验收。
- **补偿**：策略在 dispatch 前一处判定，验收用同一组样本跑两档。

---

## 4 数据模型

```sql
-- 情报的可达性投影：一条 intelligence.Item 对应 0..1 行。
-- 只记录「这条战果能开哪扇门」，不复制正文、不存明文。
CREATE TABLE IF NOT EXISTS intel_access (
    id              BIGSERIAL   PRIMARY KEY,
    project_id      TEXT        NOT NULL DEFAULT '',
    scan_id         TEXT        NOT NULL,
    fingerprint     TEXT        NOT NULL,           -- intelligence.Item.Fingerprint
    kind            TEXT        NOT NULL,
    subject         TEXT        NOT NULL DEFAULT '', -- 账号/主体，如 user@host 或 会话标识
    state           TEXT        NOT NULL DEFAULT 'proposed',
    source_intent_id BIGINT,                         -- 产出它的 intent 节点
    confirmed_by    TEXT        NOT NULL DEFAULT '',
    confirmed_at    TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,
    last_used_at    TIMESTAMPTZ,
    use_count       INT         NOT NULL DEFAULT 0,
    fail_count      INT         NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_intel_access_kind  CHECK (kind IN ('credential','session','path')),
    CONSTRAINT ck_intel_access_state CHECK (state IN ('proposed','confirmed','active','expired','revoked')),
    -- 已确认必须有确认人和时间，禁止「默认已确认」
    CONSTRAINT ck_intel_access_confirmed CHECK (
        state IN ('proposed') OR (confirmed_by <> '' AND confirmed_at IS NOT NULL)
    ),
    CONSTRAINT intel_access_identity UNIQUE (project_id, fingerprint)
);

CREATE TABLE IF NOT EXISTS intel_access_targets (
    access_id  BIGINT NOT NULL REFERENCES intel_access(id) ON DELETE CASCADE,
    target     TEXT   NOT NULL,   -- 规范化 host:port 或 CIDR
    via        TEXT   NOT NULL DEFAULT '',  -- path 类型：经哪一跳
    PRIMARY KEY (access_id, target),
    CONSTRAINT ck_intel_access_target_shape CHECK (
        target LIKE '%:%' OR target LIKE '%/%'
    )
);

-- 一次性通行证：越界使用战果时签发，用完/过期即失效。
CREATE TABLE IF NOT EXISTS access_capabilities (
    id         BIGSERIAL   PRIMARY KEY,
    scan_id    TEXT        NOT NULL,
    intent_id  BIGINT      NOT NULL,
    access_id  BIGINT      NOT NULL REFERENCES intel_access(id),
    target     TEXT        NOT NULL,
    uses_left  INT         NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    issued_by  TEXT        NOT NULL,   -- operator:<user> / rule:<name> / fence:off
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_cap_uses     CHECK (uses_left > 0),
    CONSTRAINT ck_cap_lifetime CHECK (expires_at > created_at),
    CONSTRAINT ck_cap_issued   CHECK (issued_by <> '')
);

CREATE INDEX IF NOT EXISTS intel_access_scan_idx ON intel_access (scan_id, state);
CREATE INDEX IF NOT EXISTS cap_lookup_idx ON access_capabilities (scan_id, intent_id, target);
```

**不变量清单**（DB 约束是兜底，领域层先校验）

1. `intel_access` 必须能追到一条 `intelligence.Item` 的 fingerprint；派生失败则整条不写。
2. `proposed` 不出现在任何判定里；只有 `confirmed/active` 参与可达性计算。
3. `expires_at` 过期的 access 与 capability 一律按不存在处理（判定层先看时间，不依赖后台清理）。
4. capability 只对 `(intent_id, target)` 精确匹配生效，不支持前缀、通配、CIDR 展开到超集。
5. `DenyTargets` 命中时，`intel_access_targets` 允许记录（事实），但永不签发 capability（授权）。
6. 明文密码/密钥永远不进 `intel_access`、不进种子、不进图节点 payload。
7. 同一 fingerprint 的重放只更新 `updated_at`，不新增行（幂等）。

---

## 5 状态机

**战果（intel_access）**

```text
  record_intelligence 落 Item
        │
        ▼
   proposed ──(人 triage confirmed)──▶ confirmed ──(首次成功使用)──▶ active
        │                                   │                        │
        │(人 dismissed)                     │(人撤销 / 到期)          │(fail_count≥N 或 到期)
        ▼                                   ▼                        ▼
     (不建行)                            revoked                  expired / revoked
```

**通行证（access_capabilities）**

```text
  越界申请 ──(规则放行 / 人批准)──▶ issued(uses_left=n, expires_at=T)
                                      │
              ┌───────────────────────┼───────────────────────┐
              ▼                       ▼                       ▼
        每次使用 -1                uses_left=0              now > expires_at
              │                       │                       │
              ▼                       ▼                       ▼
          继续可用                  失效                      失效
```

**intent 生命周期新增约束**：`running → done` 必须带 `exit` 声明（D6）；`exit=access:<fp>` 时校验该 access 的 `source_intent_id` 等于本 intent，防止冒领。

---

## 6 执行链路（一次两跳交接的时序）

```text
1. worker-A 在主机 A 上发现 SSH 口令
   → record_intelligence(kind=credential, title=..., content=..., endpoint=10.0.177.5:22)
   → intelligence.Item 落快照，fingerprint=fp1

2. worker-A 结案
   → ConcludeIntent 事务内派生 intel_access(kind=credential, fingerprint=fp1, state=proposed,
     source_intent_id=A)
   → 规范化 host:port 写 intel_access_targets（10.0.177.9:22 等 grants）
   → exit 声明 access:fp1

3. 人分诊（情报 tab）
   → PATCH /api/tasks/{id}/intelligence/fp1 {status: confirmed}
   → intel_access.confirmed_by/at 写入，state=confirmed

4. planner 物化下一条 intent（目标 10.0.177.9）
   → targetsForIntent：自身 target 在 scope 内则直接可派
   → 若越界：检查 capability（无）→ 生成确认请求（带 fp1 的 triage 结论）→ intent=paused

5. 人批准 / 规则放行
   → access_capabilities(intent=B, access_id=fp1, target=10.0.177.9:22, uses_left=3, TTL=10m)
   → intent=B 恢复 running

6. worker-B 启动，种子含 [intel] fp1 | credential | user@10.0.177.5 | grants=10.0.177.9:22 | confirmed
   → 需要口令原文时 read_intelligence(fp1)
   → 每次使用 → capability uses_left-1，access.use_count+1，写审计

7. worker-B 拿到第二段战果
   → 继续 record_intelligence / exit=access:fp2 或 finding
```

**唯一写者**：`intel_access` 与 `intel_access_targets` 只由 `ConcludeIntent` 事务与人工分诊端点写；`access_capabilities` 只由审批端点与规则引擎写；工具与 worker 一律没有直写权限。

---

## 7 组件边界

```go
// internal/intelaccess（新增包，PG 实现 + 内存桩）
type Store interface {
    // Derive 在结案事务里由 fingerprint 派生可达性投影（幂等）。
    Derive(ctx context.Context, from ConcludeInput) (Access, error)
    // Get 供种子与判定读取；过期按不存在返回。
    Get(ctx context.Context, scanID, fingerprint string) (Access, bool, error)
    // ListUsable 按目标过滤，供种子使用（只返回 confirmed/active 且未过期）。
    ListUsable(ctx context.Context, scanID, target string, limit int) ([]Access, error)
    // Confirm 由人工分诊触发；Revoke 由失败策略或人工触发。
    Confirm(ctx context.Context, projectID, fingerprint, by string, expiresAt time.Time) error
    Revoke(ctx context.Context, fingerprint, reason string) error
}

// 判定器：全仓唯一实现，dispatch / 重定向每跳 / 浏览器导航 / 审批共用
type Judge interface {
    Allowed(ctx context.Context, intentID int64, dst string) (Decision, error)
}
type Decision struct {
    Allow     bool
    Reason    string // in_scope | capability | denied | expired_capability
    ViaAccess string // 命中的 fingerprint，用于审计
}

// capability 签发（仅审批端点与规则引擎可达）
type CapabilityIssuer interface {
    Issue(ctx context.Context, in IssueInput) (Capability, error)
    Consume(ctx context.Context, id int64, dst string) (bool, error)
}
```

**内置实现清单**：`intelaccess.PGStore`（权威）、`intelaccess.MemoryStore`（单测与无 DSN 的本地运行）、`intelaccess.RuleIssuer`（同段/白名单自动签发）、`intelaccess.OperatorIssuer`（面板批准）。

---

## 8 里程碑（生成 / 验证分列）

| 阶段 | 内容 | 生成 | 验证 |
|---|---|---|---|
| M1 可见性 | 种子加 `Available intel` 段（只读、按 target 过滤）；`read_intelligence` 的 fingerprint 解析补齐（覆盖 XALG010 的同一形状：图节点 ID 与 fingerprint 都能解析） | 1.5 天 | 0.5 天（构造 3 条情报，断言种子只出现相关 2 条、正文不在种子内） |
| M2 可达性投影 | `intel_access` / `intel_access_targets` 迁移；`ConcludeIntent` 派生；分诊 PATCH 联动状态；情报 tab 补 grants 编辑 | 2.5 天 | 1.5 天（派生幂等、重启重放、dismissed 不产生行） |
| M3 授权与通行证 | `Judge` 收敛为唯一实现；`access_capabilities` 签发/消费；围栏三档；暂停-恢复链路复用 `requires_confirmation`+`paused` | 3 天 | 2.5 天（两跳靶：自动/人批/超时拒绝/第二次再问） |
| M4 回执与失效 | `exit` 声明闸门（依赖 XALG001 的 `complete_work`）；失败 N 次自动 `revoked`；审计事件与报告附录 | 1.5 天 | 1.5 天（无 exit 的 done 被拒、失效后不再签发） |

总量：生成 8.5 天 / 验证 6 天。M1 独立可交付，不依赖 XALG001；M4 必须在 XALG001 之后。

---

## 9 验收标准（可执行断言）

1. **两跳接力**：主机 A 产出凭据后，未做任何人工提示，下一 intent 的种子出现该 fingerprint 与 grants，且 `read_intelligence` 能取到正文。
2. **种子不泄密**：抓 worker 的种子与全部 LLM 请求体，grep 口令原文 0 命中；只有 fingerprint 与 `has_detail` 类提示。
3. **未确认不可用**：`state=proposed` 的 access 既不出现在种子，也不参与判定；`dismissed` 的不签发 capability。
4. **越界必须批**：`fence.enforce=true` 时越界动作让 intent 进入 `paused`，worker 不空转等待（槽位释放），批准后恢复。
5. **一次一用**：批准的 capability `uses_left=1` 时，第二次同目标动作重新申请；把 `expires_at` 拨到过去后，判定返回 `expired_capability`。
6. **拒绝不降级**：capability 被业务拒绝（不是基础设施错误）后，不得 fallback 为放行。
7. **身份而非地址**：同一 `IP:port` 换给新容器实例（generation 变化）时，旧 access 的 grants 不自动沿用，需重新派生（对齐 XALG023）。
8. **范围一致**：同一组 `CIDR / wildcard / host:port` 样本在「创建任务、dispatch、审批、实际出口」四处结论一致（对齐 XALG020）。
9. **回执闭合**：`exit=none` 且无 `blocked_reason` 的 intent 无法进入 `done`；`exit=access:fp` 时校验 `source_intent_id`。
10. **失效不再签发**：`fail_count` 达阈值后 state=`revoked`，后续申请直接被拒并给出理由。

---

## 10 风险表

| 风险 | 影响 | 对策 |
|---|---|---|
| 存储分裂：情报在 `run_state_files` 整包快照，可达性在 PG | 两处真相不一致 | 派生单点 + 幂等 + `rebuild` 重放；判定层只信 PG |
| 种子膨胀 | 上下文被占满、模型忽略关键项 | 按 target 过滤 + 行级截断 + 只列 confirmed/active |
| 审批疲劳 | 操作者无脑点批 | 规则层自动归类（同段/白名单）、批量批准、申请里带 triage 结论 |
| 与并行整改冲突（XALG001/018/019/020 正由 ZCode 改） | 合并冲突、语义重复 | 本文只加新表与新段，不改既有判定函数签名；`Judge` 落地时替换旧实现的两个副本 |
| 明文泄漏 | 凭据进 transcript/日志 | 种子只给引用；`read_intelligence` 的结果按现有密钥掩码策略处理；access 表无正文字段 |
| 凭据失效未标记 | 后续 intent 反复试死凭据 | `fail_count` + 连接失败分类 + 人工 revoke |
| 许可证边界 | 4zreco 为 AGPL-3.0 | 只借鉴「事实→资产→唤醒」的机制形状，不复制代码、prompt 与正则 |

---

## 11 与基线差异

| 维度 | 改前 | 改后 |
|---|---|---|
| 情报用途 | 只供人看（面板 + 分诊） | 人看 + 引擎可读 + 授权可挂 |
| 战果身份 | 文本 + fingerprint | fingerprint（身份）+ `intel_access`（可达性）+ capability（授权） |
| worker 视野 | 无 intelligence 段 | 有，按 target 过滤，只给身份 |
| 跨 intent 传递 | 依赖模型自发 `search_info` | 确定性：结案派生 + 种子可见 |
| 越界处理 | scope guard 一律拒绝 | scope 内直接可用；越界走 capability（可批、可过期） |
| 结束语义 | 干到预算耗尽或被 kill | 必须声明 exit（none/access/finding） |
| 审计 | 无使用回执 | use_count / last_used_at / capability 发放记录 |

---

## 12 与审计清单的挂接

| 审计项 | 本文关系 |
|---|---|
| XALG001（worker 无完成协议） | D6 的前置；M4 必须在它之后 |
| XALG010（索引 ID 解析不了） | M1 一并修：种子里的引用必须都能被 `read_intelligence` 解析，且错误与不存在分开 |
| XALG011（主 Agent 查询能力不足） | 种子与只读视图共用同一 `ListUsable`，主 Agent 复用 |
| XALG018/019（围栏未覆盖各通道、收紧不更新） | M3 的 `Judge` 收敛就是这两条的单点实现 |
| XALG020（CIDR 两套判定） | D2 的规范化器与 M3 验收第 8 条 |
| XALG023（地址当身份） | 验收第 7 条；`intel_access_targets` 不假设地址稳定 |
| XALG012（收工不算 objective） | 不在本文范围，但 `exit` 声明与它的收工闸门共用同一结案入口 |

---

## 13 参考实现对照（4zreco，AGPL-3.0：只借机制）

**它做对的**：事实写入（`record_fact`）→ 资产图（`insert_assets`）→ 图变更唤醒 planner，资产真的进了调度回路；凭据与资产是同一张图里的对象，交接靠图而不是靠 prompt。

**它的不足（= 本文增量）**：资产只是身份，没有「可达性」边，也没有一次性通行证；审批与越界没有和资产挂钩，跨 intent 只能靠人工提示；资产命中率没有回执。

**印证**：它证明了「战果必须是调度对象的一部分，否则永远只是展示」——本文在 xalgorix 里的对应做法是 `intel_access` 派生 + `Judge` 唯一判定 + capability 一次性授权，而不是把 4zreco 的状态机搬过来。

---

## 14 开工前不用再想的事

- 不用新建「战果」概念容器：情报 tab 的 `intelligence.Item` 就是它，扩字段即可。
- 不用再设计一套 HITL 状态机：`paused` + `requires_confirmation` + `triage` 三件套够用。
- 不用改 Verifier、报告门、六环安全链其余五环。
- 不用给前端新造页面：情报 tab 加 grants 编辑与状态列即可。
- 不用等 M4 才验证：M1（种子可见）单独就能观察「AI 知不知道自己有钥匙」。
