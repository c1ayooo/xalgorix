# 情报作为战果：跨 intent 交接与授权规格（Intel Handoff & Access Spec）

> 版本 v1.5（2026-11-24，收尾：前端操作面 + scope matcher 收敛；剩余仅报告附录与规则批）
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
- 不在本轮改 `intelligence` 的落库形态：保持 PG `run_state_files` 行（`kind=intelligence`）整包快照语意（R6.1 已落地）。把它拆成逐条表属于 R6 后续批次；本文只新增派生表，**不迁移** `intelligence.Item` 本体。

---

## 2 现状（代码实测）

> 本表是 v1.1 修订时的实测基线。M1a/M1b 落地后的差异见 §8「落地状态」（读取契约与种子段两行已按 M1a/M1b 更新）。

| 能力 | 现状 | 位置 |
|---|---|---|
| 情报条目 | `intelligence.Item{ID,Fingerprint,Kind,Title,Content,Endpoint,Confidence,CreatedAt,UpdatedAt}`，`Kind ∈ {credential, lead, sensitive_data}`；标题 512 / 正文 4096 / 字段 512 截断 | `internal/tools/intelligence/intelligence.go:41,55,71` |
| 指纹 | `sha256(kind + NUL + 归一化 content + NUL + endpoint)` 前 16 位，任务级身份，同一观察跨 run 聚合 | 同上 `:73` |
| 写入 | 工具 `record_intelligence`，参数 kind/title/content/endpoint/confidence | `internal/tools/intelligence/intelligence.go:507` |
| 读取 | 工具 `read_intelligence` **零参数**，`ReadForContext` 把**全部**条目的正文截 300 字整体输出；工具面：`main`/`planner` 因 `read_` 前缀自动可用，`orchestrator`/`analysis` 两条显式 allowlist **未含**，worker 因 denylist 可用 | 同上 `:475-505,522-527`；`internal/agent/tool_surface.go:131-145` |
| 持久化 | `intelligence.Backend`：`SaveIntelSnapshot(scanID, instanceID, seq, data)` 整包重写、`LoadIntelSnapshot`、`LoadLatestIntelSnapshotForInstance`（D11 同胞会话继承）。已落地为 PG `run_state_files` 行（`kind=intelligence`，R6.1）；legacy `intelligence.json` 仅测试模式与 import 兜底 | `internal/tools/intelligence/backend.go:27`、`:1-17` |
| 人工分诊 | `platform.IntelTriage{Status ∈ open/confirmed/dismissed}`，键 `(task_id, fingerprint)`，引擎记录与人工标注冲突保留可见 | `internal/platform/intel_triage.go` |
| 任务级视图 | `GET /api/tasks/{id}/intelligence` 跨 run 聚合 + 指纹去重 + 分诊叠加；`PATCH /api/tasks/{id}/intelligence/{fingerprint}` 写分诊 | `internal/web/handlers_task_intelligence.go:48` |
| worker 种子 | 段落为 hints / intents / beliefs / findings / observations / coverage / notes / actions，**无 intelligence 段** | `internal/agent/context_snapshot.go:279-501` |
| 种子预算 | `snapIntentsMax=12`、`snapBeliefsMax=15`、`snapFindingsMax=12`、`snapObsMax=10`，总预算为模型窗口 25% | 同上 `:47-93` |
| planner | 全局 grep 无 intelligence 读取 | `internal/orchestrator/planner.go` |
| 可达性判定 | `targetsForIntent` 只读 intent 自身 `target_kind/target/endpoint`；`scope` 类走 `scopeTargetAuthorized` | `internal/orchestrator/pool.go:1053` |
| 结案闸门 | `done` 只能经 `ConcludeIntent`；前置条件为 `payload->'contract'->'preconditions'` 的 `(kind, unit_key, state)` 三元组 | `internal/graph/pg.go:261,416` |

**结论**：观察层、去重、分诊、任务级视图都已经存在；缺的是四样——**读取契约**（M1a：无参只给身份、`fingerprint`/`I-N` 取详情）、**种子可见性**（M1b）、**可达性语义**（M2/M3）、**使用回执**（M4）。对应审计：读取契约与种子引用 → XALG010（索引可解析）、可达性 → XALG023（身份而非地址）/XALG018-020（判定收敛）、回执 → XALG001（有交棒动作才谈得上交接）。读取契约排最前是硬依赖：工具继续全量倾倒正文时，任何「种子只给身份」的设计都是空的。

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

- **选择**：`context_snapshot` 新增 `Available intel` 段，每行 `fingerprint | kind | endpoint | conf`（M1b 版；`grants/state/expires_in` 随 M2 的 `intel_access` 增量补上），正文一律不进种子，用 `read_intelligence(fingerprint=…)` 取。行尾固定挂 `(content: read_intelligence(fingerprint=<fp>))`，模型不需要猜调用方式。
- **理由**：种子预算只有模型窗口 25%，且明文进上下文等于永久落在 transcript 与日志里。`read_intelligence` 在 M1a 之前是零参数全量倾倒，种子给身份而工具照样全量吐正文——两件事必须一起改，否则种子这一侧的设计是空的。
- **代价**：模型多一步读取（一次工具调用）。
- **补偿**：每行自带指纹与取数提示；无参 `read_intelligence` 现在返回身份列表而不是正文，模型即使误调也不会泄密。
- **过滤时机（v1.1 修订）**：**M1b 不做 target 过滤**，按 `UpdatedAt` 降序截断到 `snapIntelMax=6`。理由：可达性（哪条情报能开哪扇门）是 `intel_access` 的语义，M2 才存在；在那之前按 intent target 过滤会正好滤掉接力场景——A 主机上发现、B 主机上使用的凭据。M2 起种子改由 `ListUsable(scanID, target)` 供数，才真正按目标与 grants 过滤。

### D6 交棒必须有回执，否则不算结束

- **选择**：每个执行 lane 的 intent 结束必须声明 `exit ∈ {none, access:<fp>, finding:<id>}`；`none` 必须带 `blocked_reason`。这条挂在 XALG001 的 `complete_work` 协议上。
- **理由**：没有回执，链断在哪一环永远查不出来（第五轮 79 条无原因取消就是这种）。
- **代价**：依赖 XALG001 先落地。
- **补偿**：XALG001 未落地前，M1a/M1b/M2 仍可先做「只读可见」（读取契约 + 种子 + 派生），回执留到 M4。

### D7 围栏三档决定 capability 的发放策略

| 场景 | 策略 | 自动化程度 |
|---|---|---|
| `fence.enforce=false`（靶场/自建演练） | grants 自动并入许可，不发申请 | 全自动 |
| `fence.enforce=true`（真实项目，默认） | 越界必须 capability，走确认门 | 人批或规则批 |
| 命中 `Fence.DenyTargets` | 永不发放，连申请都不生成 | 硬拒绝 |

- **理由**：同一引擎两种姿态，跑分不被 HITL 拖速，客户项目不越界。
- **代价**：两套语义要各自验收。
- **补偿**：策略在 dispatch 前一处判定，验收用同一组样本跑两档。

**M3 实现注记（v1.3）**：判定器 `intelaccess.Judge` 已落地，三档行为如下——
`enforce=false` → 一律放行（`fence_off`；没有边界就没有越界，且 `kind=scope` 无 scope 列表时仍拒绝，`no_scope`）；
`enforce=true` → scope 内 `in_scope`，scope 外有可用 grant 但无 capability 时 `needs_capability`（intent 转 `paused` 停车），有 capability 则 `capability` 并消费一次；命中 `DenyTargets` 一律 `deny_target`（永不生成申请，即便有 grant）。
**规则层自动签发（同段/白名单 RuleIssuer）延后**：M3 的签发入口只有决策收件箱的人工 `issue_capability`；`enforce=false` 的全自动来自「不判定」，不需要签发。

### D8 身份以 scan 为准，triage 保持 task 级

- **选择**：`intel_access` 唯一键 `(scan_id, fingerprint)`。`project_id` 只作为信息列（便于跨项目审计导出），不参与身份与查询。人工分诊仍按 `(task_id, fingerprint)` 存（`platform.IntelTriageStore`），两套键的关系在读取时由 task→scan 集合展开。
- **理由（实测矛盾）**：`fingerprint` 是任务级身份（同一观察跨 run 聚合），而 `intel_access` 只参与**本扫描**的 dispatch 判定。若唯一键取 `(project_id, fingerprint)`，同一项目两次扫描各发现同一凭据会命中同一行，只有先到者的 `scan_id`，后一个扫描的 Judge 按 scan 查不到——正是本文 §1.2「不做跨任务复用」要防的事。triage 是人的判断，跨 run 复用合理；可达性与能力是授权，跨 run 复用即永久扩权。
- **代价**：同一战果在新扫描里重新派生一行（幂等键变了），`use_count` 不跨扫描累计。
- **补偿**：派生是确定性的（同一 fingerprint 同一结果），重派生不丢信息；跨扫描的复现次数由任务级视图与 `UpdatedAt` 表达，不需要 `intel_access` 承担。

### D9 判定只在 dispatch 前一次，之后各环不消费 capability

- **选择**：`Judge.Allowed` 只在 **dispatch 前**调用一次（含审批恢复后的重新判定）。此后六环按既有语义执行：`RequestGuard` 每跳复查（`tools.GuardRedirects`）、`DialGuard` 复查解析后 IP（`tools.GuardDials`）、`agent_guard` 拦截 operator-machine。重定向/浏览器导航**不**再查 capability，也不再递减 `uses_left`。
- **理由**：若每跳都消费 capability，一次 3 跳重定向会吃掉 3 次额度并在中途掐断已建立的连接（半放行/半阻断都不可审计）；若只在 dispatch 判定而每跳也判定，则两处语义会漂移。授权是「这条 intent 能不能打这个目标」的意图级问题，不是 HTTP 跳数问题。
- **代价**：capability 授权的是 intent 的目标集合，不是每一次字节出口；一次授权可覆盖该 intent 对该目标的全部请求。
- **补偿**：`uses_left` 按 **intent 目标次数**计数（每命中一个 target 减一），不是按请求计数；真正按请求的额度属于代理限速，不在本文范围。审计事件记录判定与消费两个时刻，见 §4 审计形状。

---

## 4 数据模型

```sql
-- 情报的可达性投影：一条 intelligence.Item 对应 0..1 行（同一 scan 内）。
-- 只记录「这条战果能开哪扇门」，不复制正文、不存明文。
CREATE TABLE IF NOT EXISTS intel_access (
    id              BIGSERIAL   PRIMARY KEY,
    -- 信息列，不参与身份：身份是 (scan_id, fingerprint)，见 D8。
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
    revoke_reason   TEXT        NOT NULL DEFAULT '', -- 自动/人工撤销的原因（失败策略、到期清理）
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_intel_access_kind  CHECK (kind IN ('credential','session','path')),
    CONSTRAINT ck_intel_access_state CHECK (state IN ('proposed','confirmed','active','expired','revoked')),
    -- 已确认必须有确认人和时间，禁止「默认已确认」；proposed 与 expired 不要求
    -- （expired 可能是从未确认就到期；revoked 由 revoke_reason 解释）。
    CONSTRAINT ck_intel_access_confirmed CHECK (
        state IN ('proposed','expired')
        OR (confirmed_by <> '' AND confirmed_at IS NOT NULL)
        OR (state = 'revoked' AND revoke_reason <> '')
    ),
    CONSTRAINT ck_intel_access_revoke CHECK (
        state <> 'revoked' OR revoke_reason <> ''
    ),
    -- 身份键：同一扫描内一个 fingerprint 只有一行（D8）。跨扫描重新派生。
    CONSTRAINT intel_access_identity UNIQUE (scan_id, fingerprint)
);

CREATE TABLE IF NOT EXISTS intel_access_targets (
    access_id  BIGINT NOT NULL REFERENCES intel_access(id) ON DELETE CASCADE,
    target     TEXT   NOT NULL,   -- 规范化 host:port 或 CIDR（唯一规范化器见 D2）
    via        TEXT   NOT NULL DEFAULT '',  -- path 类型：经哪一跳
    PRIMARY KEY (access_id, target),
    CONSTRAINT ck_intel_access_target_shape CHECK (
        target LIKE '%:%' OR target LIKE '%/%'
    )
);

-- 一次性通行证：越界使用战果时签发，用完/过期即失效。
-- 一个 intent 命中 N 个 target = N 条 capability，共享 batch_id（一次批准）。
CREATE TABLE IF NOT EXISTS access_capabilities (
    id         BIGSERIAL   PRIMARY KEY,
    scan_id    TEXT        NOT NULL,
    intent_id  BIGINT      NOT NULL,
    access_id  BIGINT      NOT NULL REFERENCES intel_access(id),
    target     TEXT        NOT NULL,
    batch_id   TEXT        NOT NULL DEFAULT '', -- 同一次批准的多目标共享
    uses_left  INT         NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    issued_by  TEXT        NOT NULL,   -- operator:<user> / rule:<name> / fence:off
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ck_cap_uses     CHECK (uses_left > 0),
    CONSTRAINT ck_cap_lifetime CHECK (expires_at > created_at),
    CONSTRAINT ck_cap_issued   CHECK (issued_by <> ''),
    -- 同一 intent 对同一 target 同时只有一条有效通行证（重签先作废旧行）。
    CONSTRAINT cap_identity UNIQUE (scan_id, intent_id, access_id, target)
);

-- 使用与判定回执：审计投影（不是状态权威；状态在 intel_access/access_capabilities）。
-- 每次 dispatch 前判定写一行；allow/deny 都写，reason 即判定结论。
CREATE TABLE IF NOT EXISTS intel_access_use (
    id         BIGSERIAL   PRIMARY KEY,
    scan_id    TEXT        NOT NULL,
    intent_id  BIGINT      NOT NULL,
    fingerprint TEXT       NOT NULL DEFAULT '',
    access_id  BIGINT,
    target     TEXT        NOT NULL,
    decision   TEXT        NOT NULL,   -- allow | deny
    reason     TEXT        NOT NULL,   -- in_scope | capability | expired_capability | denied | deny_target | not_confirmed
    capability_id BIGINT,
    uses_left_after INT,               -- allow 且消费时写；in_scope 时 NULL
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS intel_access_scan_idx ON intel_access (scan_id, state);
CREATE INDEX IF NOT EXISTS cap_lookup_idx ON access_capabilities (scan_id, intent_id, target);
CREATE INDEX IF NOT EXISTS intel_access_use_scan_idx ON intel_access_use (scan_id, created_at DESC);
```

**不变量清单**（DB 约束是兜底，领域层先校验）

1. `intel_access` 必须能追到**同一 scan 内**一条 `intelligence.Item` 的 fingerprint；派生失败则整条不写。
2. `proposed` 不出现在任何判定里；只有 `confirmed/active` 参与可达性计算。
3. `expires_at` 过期的 access 与 capability 一律按不存在处理（判定层先看时间，不依赖后台清理）。
4. capability 只对 `(scan_id, intent_id, target)` 精确匹配生效，不支持前缀、通配、CIDR 展开到超集；一个 intent 命中 N 个 target 就是 N 条 capability，共享 `batch_id`。
5. `DenyTargets` 命中时，`intel_access_targets` 允许记录（事实），但永不签发 capability（授权）。
6. 明文密码/密钥永远不进 `intel_access`、不进种子、不进图节点 payload、不进**自动拼装**的 LLM 请求体。唯一的例外是模型**显式**调用 `read_intelligence(fingerprint=…)` 的详情拉取：那是模型的主动动作，**返回原文、不做内容脱敏**（target 侧凭据没有形状掩码，这是设计而非遗漏），按 §4 审计表记一行 `decision=allow reason=detail_fetch`，不因此放宽种子与自动注入的约束。
7. 同一 `(scan_id, fingerprint)` 的重放只更新 `updated_at`，不新增行（幂等）；跨扫描是不同行（D8）。
8. 判定只在 dispatch 前发生（D9）；重定向/导航各环不消费 capability，`uses_left` 按 intent 命中目标计次。
9. `intel_access_use` 是审计投影：写失败不得阻断 dispatch 判定（记日志 + reconciler 追赶），但 `use_count`/`last_used_at` 的状态更新必须与消费同一事务，否则回执与状态会分叉。
10. 派生经结案钩子跑在结案事务的 **savepoint** 内：钩子失败只回滚该 savepoint、记日志并由下一次结案或离线 `rebuild` 追赶，**结案照常提交**。投影是可重放的纯函数，不是结案的前置条件。

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

**intent 生命周期新增约束**：`running → done` 必须带 `exit` 声明（D6）；`exit=access:<fp>` 时校验该 access 的 `source_intent_id` 等于本 intent，防止冒领。**派生与 exit 校验必须在 `ConcludeIntent` 的同一个事务内**：先派生（幂等 upsert）、再校验 `source_intent_id`、再迁移状态；分成两个事务会留下「A 声明 access:fp1 而 fp1 由 B 派生」的冒领窗口。

`revoked` 必须携带 `revoke_reason`（失败策略 / 人工撤销 / 到期清理），所以失败自动失效不是「假装被确认过」，而是显式原因行。

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
   → 分诊键 `(task_id, fp1)`；写入 intel_access 时按 task→scan 集合展开，逐 scan 行写 confirmed_by/at（D8）

4. planner 物化下一条 intent（目标 10.0.177.9）
   → targetsForIntent：自身 target 在 scope 内则直接可派
   → 若越界：检查 capability（无）→ 生成确认请求（带 fp1 的 triage 结论）→ intent=paused

5. 人批准 / 规则放行
   → access_capabilities(intent=B, access_id=fp1, target=10.0.177.9:22, uses_left=1, TTL=10m, batch_id=…)
   → intent=B 恢复 running（dispatch 前重新判定一次）

6. worker-B 启动，种子含 [intel] fp1 | credential | endpoint=10.0.177.5 | conf=0.9（行尾提示 read_intelligence(fingerprint=fp1)）
   → 需要口令原文时才 read_intelligence(fingerprint=fp1)：这是全链路唯一的明文时刻，回执写 intel_access_use(decision=allow, reason=detail_fetch)
   → dispatch 前 capability 消费一次（uses_left-1），access.use_count+1，审计写 intel_access_use(decision=allow, reason=capability)
   → 之后的重定向/导航各环只做 IP 与 operator-machine 复查，不再消费 capability（D9）

7. worker-B 拿到第二段战果
   → 继续 record_intelligence / exit=access:fp2 或 finding
```

**唯一写者**：`intel_access` 与 `intel_access_targets` 只由 `ConcludeIntent` 事务与人工分诊端点写；`access_capabilities` 只由审批端点与规则引擎写；工具与 worker 一律没有直写权限。

---

## 7 组件边界

```go
// internal/intelaccess（新增包，PG 实现 + 内存桩）
type Store interface {
    // Derive 在结案事务里由 fingerprint 派生可达性投影（幂等，键 (scan_id, fingerprint)）。
    Derive(ctx context.Context, from ConcludeInput) (Access, error)
    // Get 供种子与判定读取；过期按不存在返回。
    Get(ctx context.Context, scanID, fingerprint string) (Access, bool, error)
    // ListUsable 按目标过滤，供种子使用（只返回 confirmed/active 且未过期）。
    ListUsable(ctx context.Context, scanID, target string, limit int) ([]Access, error)
    // Confirm 由人工分诊触发（按 scan 行写）；Revoke 由失败策略或人工触发，必须带原因。
    Revoke(ctx context.Context, scanID, fingerprint, reason string) error
}

// 判定器：全仓唯一实现。调用点只有 dispatch 前一处（D9）——重定向/导航各环
// 复用六环既有实现，不再查 capability，也不递减 uses_left。
type Judge interface {
    Allowed(ctx context.Context, intentID int64, dst string) (Decision, error)
}
type Decision struct {
    Allow     bool
    Reason    string // in_scope | capability | denied | expired_capability | not_confirmed | deny_target
    ViaAccess string // 命中的 fingerprint，用于审计
}

// capability 签发（仅审批端点与规则引擎可达）
type CapabilityIssuer interface {
    Issue(ctx context.Context, in IssueInput) (Capability, error)
    // Consume 只在 dispatch 前判定为 allow 时调用；与 access.use_count 同事务。
    Consume(ctx context.Context, id int64, dst string) (bool, error)
}

// 审计投影（写失败不阻断 dispatch；状态更新与消费同事务，见 §4 不变量 9）
type AuditStore interface {
    RecordUse(ctx context.Context, row UseRow) error
}
```

`intelaccess.PGStore` 的 `Derive` 由 `graph.Repository.ConcludeIntent` 在同一事务内调用（D1 + §5），不暴露给工具与 worker。

**内置实现清单**：`intelaccess.PGStore`（权威）、`intelaccess.MemoryStore`（单测与无 DSN 的本地运行）、`intelaccess.RuleIssuer`（同段/白名单自动签发）、`intelaccess.OperatorIssuer`（面板批准）。

---

## 8 里程碑（生成 / 验证分列）

| 阶段 | 内容 | 生成 | 验证 |
|---|---|---|---|
| M1a 读取契约 | `read_intelligence` 补 `fingerprint` 参数（同时接受 `I-N` 实例 ID，覆盖 XALG010 的同一形状）；无参列表改为**只给身份**（fp / kind / endpoint / conf / title + `has_detail` 提示），不再倾倒正文；not found 走普通 Output 文本（照 `read_notes` 先例，不置 `Result.Error`，避免模型猜错 fingerprint 触发工具熔断）；`read_intelligence` 加入 `orchestrator`/`analysis` 两条 allowlist（`main`/`planner` 靠 `read_` 前缀已可用） | 1 天 | 0.5 天（3 条情报：无参只出身份、`fingerprint=` 取到正文、`I-2` 可取、错 fp 返回 not found 且不熔断；两条 allowlist 断言 `Allows("read_intelligence")`） |
| M1b 种子可见 | 种子加 `Available intel` 段：身份行（`fp | kind | endpoint | conf` + 取数提示），正文不进种子；`snapIntelMax=6`，按 `UpdatedAt` 降序、指纹升序做确定性截断；放在 confirmed findings 之后。**不做 target 过滤**——可达性投影 M2 才有，先过滤会滤掉 A→B 的接力凭据（见 D5） | 1 天 | 0.5 天（3 条情报：断言种子出现身份行、正文不在种子内、超过 6 条只渲染 6 行；与正文一起 grep 0 命中） |
| M2 可达性投影 | `intel_access` / `intel_access_targets` 迁移（键 `(scan_id, fingerprint)`）；`ConcludeIntent` 单事务派生；分诊 PATCH 按 task→scan 展开联动状态；情报 tab 补 grants 编辑 | 3 天 | 2 天（派生幂等、重启重放、dismissed 不产生行、跨扫描各自一行） |
| M3 授权与通行证 | `Judge` 收敛为唯一实现（替换 `pool.go:1175/1224`、`planner.go:1623` 两处副本）；`access_capabilities` 签发/消费（N target = N 行 + `batch_id`）；围栏三档；判定只在 dispatch 前（D9）；暂停-恢复复用 `requires_confirmation`+`paused` | 3.5 天 | 3 天（两跳靶：自动/人批/超时拒绝/第二次再问；重定向三跳只消费一次） |
| M4 回执与失效 | `exit` 声明闸门（依赖 XALG001 的 `complete_work`）；`intel_access_use` 审计写入；失败 N 次自动 `revoked`（带 `revoke_reason`）；报告附录 | 2 天 | 1.5 天（无 exit 的 done 被拒、失效后不再签发、审计行与状态同事务） |

总量：生成 10.5 天 / 验证 7.5 天。依赖顺序：M1a → M1b → M2 → M3 → M4。**M1a 可独立合入先行**（不依赖任何表、不依赖 XALG001），它的验收里「无参列表不泄正文」是后续所有「种子不泄密」断言的前提；M4 必须在 XALG001 之后。

**落地状态（2026-11-24）**：

- **M1a/M1b 已实现**：`read_intelligence` 支持 `fingerprint`（含 `I-N` id，大小写不敏感，不做前缀匹配），无参只返回身份列表；未命中走普通 Output（不置 `Result.Error`，不触熔断）；`orchestrator`/`analysis` 两条 allowlist 已补。
- **M2 已实现**：迁移 `0034_intel_access.sql`；新包 `internal/intelaccess`（`PGStore` 权威 + `MemoryStore` 单测、`NormalizeTarget` 唯一规范化器、`DeriveItem` 纯函数派生）；结案钩子 `graph.SetConcludeHook` 在 savepoint 内派生（`graph/hook.go`，失败不阻断结案）；分诊 PATCH 同一次动作完成 Confirm/Revoke + `grants` 编辑，GET 叠加 `access`；worker 种子改由 `ListUsable` 供数（confirmed/active + grants，`snapIntelMax=6`）。
- **实现差异（与本节表格的偏差，均为提前或延后，不是漏项）**：
  1. `source_intent_id` 在 M2 一律为 NULL——归属要等 M4 的 `exit` 声明；因此 M2 的派生对「谁产出了这条战果」不作断言，也不做冒领校验。
  2. 种子只列 confirmed/active：`proposed` 是观察不是门。这让 M1b 的「全部身份」行为被 M2 收紧，未确认的情报仍需人工 triage 才进下一条 intent 的种子；`read_intelligence` 的无参列表不受影响。
  3. 围栏三档（`fence.enforce=false` 自动确认/放行）属 M3：M2 里确认一律来自人工分诊（或直接调用 API）。
  4. 「种子按 target 过滤」随 M2 变成「按 usable 状态过滤」，按 target 的收窄由 `ListUsable(scanID, target, …)` 提供，M3 的判定器复用同一查询（`FindGrant`）。
- **M3 已实现（2026-11-24）**：
  - 迁移 `0035_access_capabilities.sql`；`intelaccess` 新增 `Capability`、`IssueCapabilities`（N target = N 行 + `batch_id`，同键重签是 upsert 不叠加授权）、`ConsumeCapability`（`uses_left-1` + `use_count/last_used_at` 同事务，confirmed→active）、`ListCapabilities`、`FindGrant`（精确 + CIDR 包含，唯一匹配实现）。
  - `agent.EngagementScope` 增补 `DestinationDenied`/`Configured`，与 `DestinationAllowed` 共用同一 matcher；`intelaccess.Judge` 用它们做唯一判定。
  - dispatch 判定接入 `Pool.authorizeDispatch`：`needs_capability` → `SettleIntent(running→paused)`（owner-fenced，原因写 `blocked_reason`，槽位释放），其余 deny → blocked；`kind=scope` 只有在**未配置**判定器时才保留 M3 前的预过滤。
  - 操作面：决策收件箱 `GET /api/scans/{id}/decisions` 新增 `paused` 列表；`POST` 新增 `issue_capability`（targets 缺省取 intent 目标，uses 1–10，TTL 1m–2h，逐 target 找 grant 后签发，再 `paused→open` 恢复；无 grant → 409，非法 target → 400）。
  - ~~**仍未收口**：审批/命令字面量路径与 dispatch/agent 是两套 host 匹配实现~~ → **已收敛（2026-11-24 收尾）**：见下「收尾」块。
- **M4 已实现（2026-11-24）**：
  - `complete_work` 新增 `exit`（`none|access:<fp>|finding:<id>`）与 `blocked_reason`。非法声明或 `exit=none` 缺原因**不结束车道**（返回普通 Output，不置 `Result.Error`，不触熔断），模型可改对再调。
  - 声明经 `scanctx.ProgressStore.RecordExit`（按 delegated agent id）交给结算权威；`Pool.conclude` 的 exit 闸门：**无声明 → blocked**（`worker completed without an exit declaration`）、`exit=none` 缺原因 → blocked、`access:<fp>` 不存在或归属别的 intent → blocked；通过时把 `exit`/`blocked_reason` 写进 receipt fact。没有 ScanCtx 的测试/CLI 环境保留 M4 前路径。
  - 归属（`source_intent_id`）改为**派生时写入**：新派生行记录当前结案 intent；重派生不改写（first-wins）。因此 `exit=access` 的冒领校验可执行；`source_intent_id=0` 的历史行按「未归属」放行。
  - 迁移 `0036_intel_access_use.sql`：判定器每次 `Decide` 写一行 `allow|deny|needs_capability`（best-effort，写失败不阻断 dispatch）；模型显式 `read_intelligence(fingerprint=…)` 经 `intelligence.SetDetailObserver` 写 `detail_fetch` 行（main 接线，context id → run id 映射）。
  - 失败自动撤销：`Store.RecordFailure` 在 `fail_count` 达 3 时把 access 置 `revoked`（`repeated dispatch failure`）；pool 在消费了 capability 的 lane 失败结算时记账。
  - 只读投影 `GET /api/scans/{id}/intel-access`：access 行（状态+grants）+ capabilities + 审计尾部，供面板与报告使用。
  - **身份键修正**：access/capability/audit 统一按**图 run id** 键；intel 内存 store 按会话 context id 键——`ConcludeIntent` 的 context 现在携带 `intelaccess.WithIntelContextID`，worker 种子改用 `planGraphScanID()`，任务级读写按 `run.InstanceID` 展开。
  - **仍未做**：报告附录（等报告格式那一轮）；`finding:<id>` 只校验非空（finding 真伪由报告门负责）。
- **收尾（2026-11-24）**：
  - **前端操作面**（`webui/`）：
    - 决策收件箱（探索链路 tab）新增 `paused` 分组：显示挂起原因/目标，`批准越界` 打开目标/次数/有效期表单走 `issue_capability`，成功自动恢复 intent；另可 `撤销` 该意图。
    - 任务情报 tab：每行新增引擎侧 access 状态章与「门槛」编辑弹窗（host:port / CIDR，与分诊同一次 PATCH 保存）；展开行显示当前 grants。
    - 探索链路 tab 新增折叠的「情报门槛」只读面板（`GET /api/scans/{id}/intel-access`）：access 行、一次性通行证（剩余次数/到期/签发人）、最近判定与详情拉取审计。
    - 全部 chrome 文案进 en/zh 词典；`npm run typecheck` 通过，`make webui` 已重建嵌入产物。
  - **scope matcher 收敛（XALG018/019/020 的第四处）**：创建任务校验与审批/命令字面量 fence 不再自带 `hostInScope`/`fenceDenyMatch`，统一走 `agent.EngagementScope`（dispatch 判定与 agent 出口本来就用它）。唯一 matcher 补了两条之前只在 web 存在的语义：**CIDR 目的地按整段判定**（allow 条目必须包含整段；deny 与目标段重叠即拒；仅 base 地址在范围内不算授权）与**裸 IPv6 字面量**（不经 URL 解析）。web 侧只保留命令/HTTP 目的地提取与面向操作员的措辞映射；创建路径保留「无端口 target 必须两个默认端口都在 allow_ports」的更严格端口策略（不随运行时放宽）。
  - **验收**：`internal/web/scope_match_test.go` 的 `TestScopeSamplesAgreeAcrossPaths` 用同一组样本（含通配符、CIDR、显式/默认端口、越界端口）断言**创建 / dispatch 判定 / 审批 fence / 共享 matcher** 四处结论一致；deny 目标在运行时三处一致拒绝（创建无 deny 概念）；`TestCIDRScopeMatchesAgentSemantics` 覆盖整段授权与 IPv6。

---

## 9 验收标准（可执行断言）

1. **两跳接力**：主机 A 产出凭据后，未做任何人工提示，下一 intent 的种子出现该 fingerprint 与 endpoint，且 `read_intelligence(fingerprint=…)` 能取到正文。
2. **种子不泄密（范围精确到自动注入）**：抓 worker 的种子与**未经模型显式取详情**的全部 LLM 请求体，grep 口令原文 0 命中；只有 fingerprint、kind、endpoint、title 与 `has_detail` 类提示。模型显式调用 `read_intelligence(fingerprint=…)` 之后的请求体**允许出现正文原文（不脱敏）**，但必须有一条 `intel_access_use(reason=detail_fetch)` 回执。
3. **未确认不可用**：`state=proposed` 的 access 既不出现在种子，也不参与判定；`dismissed` 的不签发 capability。
4. **越界必须批**：`fence.enforce=true` 时越界动作让 intent 进入 `paused`，worker 不空转等待（槽位释放），批准后恢复。
5. **一次一用按目标计**：批准的 capability `uses_left=1` 时，第二次同目标 intent 重新申请；一次 3 跳重定向只消费 1 次；把 `expires_at` 拨到过去后，判定返回 `expired_capability`。
6. **拒绝不降级**：capability 被业务拒绝（不是基础设施错误）后，不得 fallback 为放行。
7. **身份而非地址**：同一 `IP:port` 换给新容器实例（generation 变化）时，旧 access 的 grants 不自动沿用，需重新派生（对齐 XALG023）；同一 fingerprint 在新扫描里派生新行，旧扫描的行不参与新扫描判定（D8）。
8. **范围一致**：同一组 `CIDR / wildcard / host:port` 样本在「创建任务、dispatch、审批、实际出口」四处结论一致（对齐 XALG020）；host 提取只有 `scopeguard.ReadingHosts` 一个实现。
9. **回执闭合**：`exit=none` 且无 `blocked_reason` 的 intent 无法进入 `done`；`exit=access:fp` 时校验 `source_intent_id`，且校验与派生在 `ConcludeIntent` 同一事务内。
10. **失效不再签发**：`fail_count` 达阈值后 state=`revoked` 且 `revoke_reason` 非空，后续申请直接被拒并给出理由。
11. **读取契约**：`read_intelligence` 无参时只返回身份行（不含 `content`）；`fingerprint=` 与 `I-N` 实例 ID 都能解析；不存在的键返回明确的 not found 文本且**不产生** `Result.Error`（不触熔断）。
12. **工具面覆盖**：`ToolSurface.Allows("read_intelligence")` 在 `main`/`planner`/`worker`/`orchestrator`/`analysis` 五个面上都为 true（前两个靠前缀，后三个靠显式表）。

---

## 10 风险表

| 风险 | 影响 | 对策 |
|---|---|---|
| 存储分裂：情报在 PG `run_state_files` 整包快照，可达性在派生表 | 两处真相不一致 | 派生单点 + 幂等 + `rebuild` 重放；判定层只信派生表 |
| 分诊键（task 级）与可达性键（scan 级）错位 | 人批了但扫描行没更新，或更新错扫描 | D8 的 task→scan 集合展开必须单元测试覆盖（一个 task 两个 run → 两行都写） |
| 种子膨胀 | 上下文被占满、模型忽略关键项 | 按 target 过滤 + 行级截断 + 只列 confirmed/active + `snapIntelMax` 上限 |
| 审批疲劳 | 操作者无脑点批 | 规则层自动归类（同段/白名单）、`batch_id` 批量批准、申请里带 triage 结论 |
| 与并行整改冲突（XALG001/018/019/020 正由 ZCode 改） | 合并冲突、语义重复 | 本文只加新表与新段，不改既有判定函数签名；`Judge` 落地时替换 `pool.go:1175/1224` 与 `planner.go:1623` 两份副本 |
| 明文泄漏 | 凭据进 transcript/日志 | 种子与自动注入只给引用；详情拉取**返回原文、不脱敏**（模型主动取，留回执），因此不把它当泄密事件而按使用审计；现有 `agent.redactSecrets` 只按值替换 operator 认证值，**不覆盖** target 侧凭据形状，不要误以为它兜底；派生表无正文字段 |
| 查不到的情报被当成不存在 | 模型反复猜 fingerprint、触发工具熔断 | M1a 明确 not found 为普通 Output（不置 `Result.Error`），并在响应里给出已有条目的身份列表 |
| 凭据失效未标记 | 后续 intent 反复试死凭据 | `fail_count` + 连接失败分类 + 自动 `revoked`（带原因）+ 人工 revoke |
| 许可证边界 | 4zreco 为 AGPL-3.0 | 只借鉴「事实→资产→唤醒」的机制形状，不复制代码、prompt 与正则 |

---

## 11 与基线差异

| 维度 | 改前 | 改后 |
|---|---|---|
| 情报用途 | 只供人看（面板 + 分诊） | 人看 + 引擎可读 + 授权可挂 |
| 战果身份 | 文本 + fingerprint | fingerprint（身份）+ `intel_access`（可达性）+ capability（授权） |
| worker 视野 | 无 intelligence 段 | 有，按 target 过滤，只给身份 |
| 跨 intent 传递 | 依赖模型自发 `search_info` | 确定性：结案派生 + 种子可见 |
| 情报读取 | `read_intelligence` 零参数，全量倾倒 300 字正文 | 无参只给身份；`fingerprint=`/`I-N` 按需取详情，not found 明确 |
| 越界处理 | scope guard 一律拒绝 | scope 内直接可用；越界走 capability（可批、可过期） |
| 结束语义 | 干到预算耗尽或被 kill | 必须声明 exit（none/access/finding） |
| 审计 | 无使用回执 | use_count / last_used_at / capability 发放记录 |

---

## 12 与审计清单的挂接

| 审计项 | 本文关系 |
|---|---|
| XALG001（worker 无完成协议） | D6 的前置；M4 必须在它之后。`complete_work` 现有 4 个参数、无 `exit`（`internal/tools/finish/finish.go:30-35`），M4 要扩参数并加闸门 |
| XALG010（索引 ID 解析不了） | M1a 先修：种子里的引用必须都能被 `read_intelligence(fingerprint=…)` 解析（同时接受 `I-N`），且 not found 与解析失败分开 |
| XALG011（主 Agent 查询能力不足） | 种子与只读视图共用同一 `ListUsable`，主 Agent 复用；主 Agent 已可调 `read_intelligence`（`read_` 前缀），缺口在 `orchestrator`/`analysis` 两条 allowlist |
| XALG018/019（围栏未覆盖各通道、收紧不更新） | M3 的 `Judge` 收敛就是这两条的单点实现；当前两份副本在 `internal/orchestrator/pool.go:1175/1224` 与 `:1623` |
| XALG020（CIDR 两套判定） | D2 的规范化器与 M3 验收第 8 条；host 提取复用 `scopeguard.ReadingHosts`，不再新写解析器 |
| XALG023（地址当身份） | 验收第 7 条；`intel_access_targets` 不假设地址稳定，且身份键带 `scan_id`（D8） |
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
- 不用等 M4 才验证：M1a 单独就能验「无参读取不再泄正文」，M1b 单独就能观察「AI 知不知道自己有钥匙」。
- 不用纠结掩码策略：target 侧凭据没有形状掩码，详情拉取**返回明文原文、不做脱敏**，安全边界是「谁主动取、什么时候取」靠回执审计；operator 认证值仍由 `agent.redactSecrets` 按值替换。
- 不用猜 host 解析该怎么写：`scopeguard.ReadingHosts` 就是唯一规范化器（IPv4 变体、IDNA、URL/裸 host:port 都覆盖）。
