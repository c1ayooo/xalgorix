# 报告交付规格（Report Delivery Spec）

> 状态：已实现（2026-11-23）；实现说明见 §15
> 日期：2026-11-23
> 关联：`docs/CORE_PHILOSOPHY.md`、`docs/REDESIGN.md`（D12）、`docs/PG_PRIMARY_STORAGE_SPEC.md`、`docs/INFO_GRAPH_CONTRACT.md`

## 0. 已裁决事项

1. **报告只手动生成**：彻底取消扫描结束自动出 PDF；手动点击、选择格式、选择模式。
2. **总报告 5 章**：执行摘要、风险总览、漏洞详情、攻击链、修复建议。
3. **子报告**：每个 target / wildcard 子域都生成一份，无论是否有漏洞。
4. **中文优先**：默认 `zh-CN`，按国内 SRC / 等保渗透报告习惯组织内容。
5. **报告撰写 Agent 可选**：模板模式 0 token；Agent 模式手动触发，可做攻击链与风险排序，但不得改 finding 事实。
6. **发现页漏洞详情**：发现页每条漏洞必须能展开完整说明（描述、技术分析、复现、证据、修复），并支持单漏洞导出。
7. **单漏洞导出**：`md` / `html` / `json`，包含证明漏洞存在的 HTTP raw 请求/响应；未捕获时明确标注，不伪造。

## 1. 边界与不变量

- **只读投影**：报告与导出只读取已持久化事实（scan record findings、ledger evidence、traffic archive、recon summary）。不产生、不修改 finding。
- **字段级权威不变**：finding 接受、`verified / exploit-proven / needs-manual-verification` 标签、severity / CVSS 仍由 `report_vulnerability` 报告门管线决定（`internal/tools/reporting`）。报告可以建议优先级，但不得改写原字段。
- **Verifier / 闸门 / 工具面不动**（宪章 §11）。
- **存储分层（D12）**：报告元数据落 PG；PDF/MD/HTML 是字节产物，落文件系统，经 `storage.WriteAtomic`。
- **扫描隔离**：报告任务不进入扫描主循环，不挂 `scanctx` 可变状态；读取仅按 task / run / scan 目录进行。
- **不自动生成**：扫描生命周期（finish、phase 22、通知）不再触发报告产物。
- **密钥与敏感数据**：原始流量归档是未脱敏的（`internal/scanctx/traffic.go` 顶部说明），但**导出与报告必须脱敏**；API 永不回传密钥。

## 2. 现行实现的改造点

| 现状 | 位置 | 处置 |
|---|---|---|
| 会话收工后自动生成 PDF | `internal/web/scan_session.go:939-996` | 删除报告生成、`report_ready` 广播、`markPhaseWorked(22)`、扫描完成通知附件 |
| wildcard 子域自动生成 PDF | `internal/web/orchestrator.go:1695-1739` | 删除；由手动生成统一覆盖 |
| 手动下载 `/api/report/{id}` 每次重渲染 | `internal/web/server.go:4013-4070` | 改为读已生成报告版本；不再按需重渲染 |
| 报告页按 scan record 列行 | `webui/src/pages/reports.tsx` | 改为按 task 聚合的报告版本列表 |
| 任务报告 tab 按 run 链 `/api/report/{instance_id}` | `webui/src/pages/task-detail.tsx:843-894` | 改为报告版本列表 + 手动生成入口 |
| Phase 22 派生文案 “report generated” | `internal/agent/phase_disposition.go:233-249` | 改为“finish accepted; assessment settled; report delivered manually” |
| findings 页面无详情/导出 | `webui/src/pages/findings.tsx` | 新增详情抽屉 + 导出 |

`scanSession.genReport` 字段与 `orchestrator` 的赋值一并移除；`report_ready` 事件仅由手动生成成功后发出（可选，见 §9.3）。

## 3. 数据模型与存储

### 3.1 PG 表（迁移 `0028_reports.sql`）

```sql
CREATE TABLE reports (
    id             UUID PRIMARY KEY,
    project_id     UUID NULL,
    task_id        UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    revision       INT  NOT NULL,
    run_ids        JSONB NOT NULL DEFAULT '[]',
    scan_ids       JSONB NOT NULL DEFAULT '[]',
    status         TEXT  NOT NULL, -- queued | generating | ready | failed | cancelled
    template       TEXT  NOT NULL, -- standard
    language       TEXT  NOT NULL, -- zh-CN
    mode           TEXT  NOT NULL, -- template | agent
    model          TEXT  NOT NULL DEFAULT '',
    prompt_version TEXT  NOT NULL DEFAULT '',
    formats        JSONB NOT NULL DEFAULT '[]',
    error          TEXT  NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at     TIMESTAMPTZ NULL,
    finished_at    TIMESTAMPTZ NULL,
    UNIQUE (task_id, revision)
);

CREATE TABLE report_artifacts (
    report_id  UUID NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL, -- total | sub
    scan_id    TEXT NOT NULL DEFAULT '',
    target     TEXT NOT NULL DEFAULT '',
    format     TEXT NOT NULL, -- pdf | md | html
    path       TEXT NOT NULL,
    bytes      BIGINT NOT NULL DEFAULT 0,
    sha256     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (report_id, kind, scan_id, format)
);
```

- `revision` 按 task 单调递增；同一 task 同时只允许一个 `queued|generating`。
- `scan_ids` 是生成时的快照；后续扫描变化不影响已生成版本。
- 报告任务进程重启：启动时把 `queued|generating` 标记为 `failed`（`interrupted`），可手动重试。

### 3.2 文件布局

```
<dataDir>/reports/<task_id>/<report_id>/
    report.json                 # ReportSpec（事实包 + 叙述包，审计/重渲染源）
    report.pdf
    report.md
    report.html
    sub/<sanitized_target>-<scan_id>.<format>
```

- 仅写请求的格式；`report.json` 必写。
- 用 `storage.WriteAtomic`；渲染前的临时文件放同目录，避免跨文件系统 rename。
- 路径必须经目录边界校验，禁止 `..`、绝对路径和符号链接逃逸。
- 删除 task 时级联删除报告目录；删除单个报告版本时删 PG 行与目录。

### 3.3 Repository 接口

`platform.Repository` 增加：

```go
CreateReport(ctx, platform.ReportMount) (*platform.Report, error)
UpdateReportStatus(ctx, reportID string, status string, errMsg string) error
GetReport(ctx, reportID string) (*platform.Report, error)
ListReportsByTask(ctx, taskID string) ([]platform.Report, error)
PutReportArtifacts(ctx, reportID string, arts []platform.ReportArtifact) error
ListReportArtifacts(ctx, reportID string) ([]platform.ReportArtifact, error)
DeleteReport(ctx, reportID string) error
```

PG 实现为主；文件存储实现只用于测试/兼容，行为与 PG 对齐。

## 4. ReportSpec（中间模型）

同一份 `report.json` 驱动 PDF / MD / HTML 三个渲染器；单漏洞导出复用 `Findings[]`。

```json
{
  "spec_version": 1,
  "meta": {
    "report_id": "...",
    "task_id": "...",
    "task_name": "...",
    "project_id": "...",
    "revision": 3,
    "run_ids": ["..."],
    "scan_ids": ["..."],
    "targets": ["example.com"],
    "title": "XX 系统渗透测试报告",
    "classification": "内部",
    "language": "zh-CN",
    "template": "standard",
    "generator": "agent",
    "model": "...",
    "prompt_version": "report-v1",
    "generated_at": "2026-11-23T21:30:00+08:00",
    "testing_window": {"start": "...", "end": "..."},
    "authorization_note": "..."
  },
  "facts": {
    "severity_counts": {"critical": 1, "high": 2, "medium": 3, "low": 0, "info": 0},
    "risk_score": 78,
    "assets": [{"target": "...", "ip": "...", "ports": ["443/tcp"], "tech": ["nginx"], "urls": ["..."]}],
    "findings": [
      {
        "id": "XALG-3",
        "scan_id": "...",
        "title": "...",
        "severity": "high",
        "original_severity": "high",
        "cvss": 8.1,
        "cvss_vector": "CVSS:3.1/...",
        "cwe": "CWE-89",
        "owasp": "A03:2021",
        "cve": "",
        "target": "...",
        "endpoint": "...",
        "method": "POST",
        "description": "...",
        "impact": "...",
        "technical_analysis": "...",
        "poc_description": "...",
        "poc_script": "...",
        "exploitation_proof": "...",
        "remediation": "...",
        "fix": "...",
        "verification_method": "time_based",
        "verified": true,
        "tags": ["verified"],
        "discovered_at": "2026-11-23T20:11:00+08:00",
        "traffic": [
          {
            "session": "...", "seq": 12, "captured_at": "...", "channel": "proxy",
            "request_raw": "POST /api/user HTTP/1.1\r\nHost: ...\r\n...\r\n\r\n{\"id\":\"1\"}",
            "response_raw": "HTTP/1.1 200 OK\r\n...",
            "request_truncated": false, "response_truncated": false,
            "available": true
          }
        ],
        "evidence_refs": ["finding_ref:XALG-3"],
        "info_item_ids": ["..."]
      }
    ],
    "coverage": {
      "phases": [{"id": 1, "status": "completed", "reason": "..."}],
      "tested_endpoints": 42,
      "limitations": ["仅测试授权域名", "未进行拒绝服务测试"]
    },
    "risk_scores": [
      {"finding_id": "XALG-3", "base": 0.87, "band": "immediate", "rationale": "公网未授权 SQL 注入"}
    ]
  },
  "narrative": {
    "executive_summary": {
      "background": "...",
      "conclusion": "...",
      "key_findings": [{"finding_id": "XALG-3", "text": "..."}]
    },
    "risk_overview": {"narrative": "..."},
    "finding_narratives": [
      {"finding_id": "XALG-3", "description": "...", "impact": "...", "reproduction": "...", "remediation": "..."}
    ],
    "attack_chains": [
      {
        "id": "chain-1",
        "title": "...",
        "confidence": "confirmed",
        "steps": [{"finding_id": "XALG-3", "action": "..."}],
        "impact": "..."
      }
    ],
    "remediation_plan": [
      {"priority": "immediate", "finding_ids": ["XALG-3"], "immediate": "...", "root_fix": "...", "verification": "..."}
    ]
  }
}
```

**事实 / 叙述分离**：

- `facts` 只能由确定性装配产生；叙述字段只能由模板或 Agent 产生。
- 渲染器对 `facts` 字段做原样输出；叙述字段做转义/换行处理。
- `narrative` 中任何事实性断言（等级、CVSS、CWE、验证标签、请求包）必须来自 `facts`，不得被叙述覆盖。

## 5. 总报告（5 章）

封面（非章节）：报告标题、报告编号、密级、版本、任务/run 标识、目标范围、生成时间、生成方式（模板 / Agent+模型）、授权与免责声明摘要。

### 第 1 章 执行摘要

- 测试背景与目标（`task.goal` / `objective`）
- 总体结论与整体风险评级（由 `facts.risk_score` 与 severity 统计驱动）
- 关键漏洞 Top 3–5（按风险分排序，带 finding 引用）
- 测试完成度、覆盖限制与未测试项摘要

### 第 2 章 风险总览

- 严重 / 高危 / 中危 / 低危数量与 CVSS 分布
- 受影响资产 / 系统列表（含子报告索引）
- 整体风险分、排序说明、确定性 tie-break（finding ID）
- 风险矩阵（等级 × 资产暴露面）

### 第 3 章 漏洞详情

按风险优先级排序，每条固定结构：

1. 标题、ID、等级、CVSS / 向量、CWE / OWASP / CVE
2. 资产：target、URL、method、参数位置、权限要求
3. 描述与技术分析
4. 前置条件与复现步骤
5. PoC 描述与脚本
6. **证明数据：HTTP raw request / response**（见 §7）
7. 影响（已证实 / 可能）
8. 修复建议（临时缓解、根治、验证方法）
9. 验证方式、标签、发现时间、证据引用

无漏洞时明确写“本次测试未发现已确认漏洞”，并列出未覆盖项。

### 第 4 章 攻击链

- 每条链：目标、步骤（阶段 → 动作 → finding ID → 证据引用）、置信度、最终影响
- 置信度只允许：`已证实` / `高置信` / `推测`
- 只允许串联 `verified` / `exploit-proven` finding 作为主链
- 推测链单列“待验证风险”，不进入漏洞详情
- 无可串联漏洞时明确写“未形成完整攻击链”

### 第 5 章 修复建议

- 按优先级分组：立即处置 / 高 / 中 / 低
- 每条：关联漏洞 ID、临时缓解、根治方案、验证方法、建议 SLA
- 复测建议与回归范围
- 优先级可由 Agent 调整，但必须引用 finding 并给出理由；同时保留原始 severity

## 6. 子报告

- 每个 target / wildcard 子域一份，**无论是否有漏洞**。
- 与总报告同模板、同 5 章，范围收窄到该 target 的 scan records。
- 总报告第 2 章资产列表索引子报告；第 3 章漏洞详情可链接子报告。
- 文件命名：`sub/<sanitized_target>-<scan_id>.<format>`；同名冲突追加序号。
- 子报告不单独出现在报告页；随总报告版本一起下载。

## 7. 单漏洞导出与发现页详情

### 7.1 发现页详情（必做）

发现页每行可展开抽屉（复用/抽取 `scan-detail.tsx` 的 `FindingDetailsDialog`），展示：

- **概览**：严重等级、标题、CVSS、验证标签、CWE / OWASP / CVE
- **资产**：target、endpoint、method、所属扫描、发现时间
- **说明**：description、impact、technical_analysis
- **复现**：poc_description、poc_script
- **证据**：exploitation_proof + HTTP raw request/response（按 `traffic_refs` 懒加载；缺失/截断显式提示）
- **修复**：remediation、fix
- **元数据**：finding ID、source_scan_id、tags、traffic_refs、replaces
- **操作**：导出 MD / HTML / JSON

数据来源：`GET /api/findings` 已返回完整 `VulnSummary`（`FlatFinding`），无需新详情接口；原始包按 `scan_id` + `traffic_refs` 取。任务详情“发现”tab 复用同一抽屉。

### 7.2 单漏洞导出接口

```
GET /api/scans/{scanID}/findings/{vulnID}/export?format=md|html|json&download=1
```

- `scanID`：finding 的 `source_scan_id` / `FlatFinding.scan_id`
- 解析 scan 目录后，按 `TrafficRef.Session` 找到对应 session 目录（D11 sibling sessions），读 `traffic/tr_%06d.json`
- 内容结构见 §7.3；`download=1` 时返回 `Content-Disposition: attachment`，文件名 `<sanitized-title>-<vulnID>.<ext>`
- 未找到 finding / scan → 404；格式不支持 → 400
- 只读；不写任何状态

### 7.3 导出内容

1. 标题（单位/系统 + 漏洞类型 + 位置）
2. 基本信息：类型、危害等级、CVSS、CWE / OWASP / CVE、资产、权限要求
3. 漏洞描述（原理 + 触发条件）
4. 复现步骤（编号；前置条件、请求、预期/实际、关键回显）
5. **证明数据（HTTP Raw）**
6. 影响（已证实 / 可能）
7. 修复建议（临时 + 根治 + 验证）
8. 测试声明（授权范围、未破坏数据、测试数据已删除、联系方式、测试时间）
9. 附件索引（PoC、原始包、截图）

**HTTP raw 格式**：

```http
POST /api/user/get HTTP/1.1
Host: target.example.com
Content-Type: application/json
Cookie: <redacted:cookie>

{"id":"1 AND SLEEP(5)"}
```

```http
HTTP/1.1 200 OK
Content-Type: application/json
...

{"data":...}
```

- 直接使用归档中的 `request_raw` / `response_raw`，保持请求行、头、空行、body 的原始形态。
- 附采集元数据：`session:seq`、采集时间、通道、是否截断。
- 响应体过大时按 64 KiB 截断并标注 `[truncated]`；请求侧沿用归档的 128 KiB 截断标记。
- **缺包**（无 `traffic_refs`、归档缺失、`request_raw` 为空）：

  ```markdown
  > 未捕获原始 HTTP 请求（原因：代理未开启 / 流量未归因 / 非 HTTP）。
  > 以下为依据 finding 元数据重建的 PoC，非抓包原文：
  ```bash
  curl -sk -X POST 'https://...' --data '...'
  ```
  ```

- **截断**：保留原文并在末尾标注 `[truncated at N bytes；原始包已截断]`，不得补写内容。
- **绝不**把重建 curl 标成 raw 抓包。

### 7.4 脱敏（强制）

traffic 归档未脱敏，导出前必须：

- 头：`Cookie`、`Set-Cookie`、`Authorization`、`Proxy-Authorization`、`X-Api-Key`、`X-Auth-Token`、`X-CSRF-Token` 等按名单替换为 `<redacted:kind>`，保留头名与结构。
- body / query：`password`、`passwd`、`pwd`、`token`、`secret`、`api_key`、`access_token`、`refresh_token`、`session`、`otp` 等键的值替换。
- PII：邮箱、手机号、身份证号按正则打码。
- 输出固定附“已脱敏”说明。
- 单测必须覆盖：原始包中有 Cookie/token/密码时，导出结果不含原值。

## 8. 报告撰写 Agent

### 8.1 定位

收工后的可插拔叙述层，不在扫描循环内：

```
持久化事实 → 确定性事实包（facts） → [可选] Agent 叙述包（narrative）
            → ReportSpec → PDF / MD / HTML
```

- 模板模式：`narrative` 由确定性模板生成，0 token。
- Agent 模式：只填 `narrative`，`facts` 由代码装好。
- 失败 / 超时 / 校验失败：回退模板模式，报告仍可交付，并标注回退原因。

### 8.2 输入与输出

- 输入：`ReportFacts`（finding 全字段、脱敏证据摘要、recon、coverage、风险分底座）。**不把完整原始流量给 Agent**；只给证据文本与关键回显摘要。
- 输出：严格符合 `NarrativeSpec` 的 JSON（见 §4 `narrative`），不得输出 `facts` 字段。
- 模型与预算：复用已配置 LLM；生成对话框中可选模型；按 finding 数 × 章节估算 token 并在生成前展示。
- prompt 版本写入 `reports.prompt_version`。

### 8.3 校验（任一失败 → 一次修复重试 → 回退模板）

1. 所有 `finding_id` 必须存在于事实包。
2. 叙述中不得出现新的 severity / CVSS / CWE / OWASP / 验证标签；出现即拒绝。
3. 攻击链主链步骤只能引用 `verified` / `exploit-proven`；`推测` 链必须单列。
4. 风险优先级与确定性底座的等级差 ≤ 1，且必须有 `rationale`。
5. 长度上限、中文语言检查、敏感信息正则扫描。
6. HTML 渲染前转义；MD 中禁止原始 HTML 注入。

### 8.4 风险排序底座（确定性）

```
base = 0.40 * norm(CVSS)
     + 0.30 * verification_strength
     + 0.20 * exposure
     + 0.10 * asset_weight      // 默认 0.5，可手工填 0..1

verification_strength: exploit-proven 1.0 / verified 0.8 / needs-manual 0.4
exposure: 公网未授权 1.0 / 公网需认证 0.7 / 内网 0.4
band: >=0.85 立即处置 / >=0.70 高 / >=0.50 中 / 其余 低
tie-break: base desc, CVSS desc, finding_id asc
```

Agent 可上下调一个 band，必须写理由；报告中同时展示“原始等级”和“报告优先级”。

## 9. 生成流程与 API

### 9.1 接口

```
POST /api/tasks/{taskID}/reports
{
  "run_ids": ["..."],              // 可选；缺省为该 task 的最新 run
  "formats": ["pdf","md","html"],  // 至少一个
  "template": "standard",
  "language": "zh-CN",
  "mode": "template" | "agent",
  "model": "",                     // agent 模式可选
  "include_sub_reports": true      // 固定 true；服务端忽略 false
}
→ 202 {"report_id":"...","revision":3,"status":"queued"}

GET    /api/tasks/{taskID}/reports             → 报告版本列表
GET    /api/reports/{reportID}                 → 元数据 + artifact 列表
GET    /api/reports/{reportID}/artifacts/{format}?kind=total|sub&scan_id=...
DELETE /api/reports/{reportID}
GET    /api/reports/jobs/{jobID}               → 进度（可选，SSE 为主）
```

- 同一 task 并发保护：已有 `queued|generating` 时返回 409 + 现有 report_id。
- 生成 job 在 web 进程内执行（不进 River：报告是用户触发的同步交付，不属于冷路径追赶，宪章 §8）。
- 进度事件：`report.progress` / `report.ready` / `report.failed`，按 task 的 run 所属 instance 广播；失败保留错误信息。

### 9.2 生成步骤

1. 解析 task、run(s)、scan records（含 sibling sessions 与 wildcard 子域）。
2. 装配 `facts`：findings（按 findingContentKey 去重）、traffic（按 refs 解析，脱敏）、recon、coverage、风险分。
3. 逐 target/子域装配子报告 facts。
4. 模板模式直接生成 `narrative`；Agent 模式调用模型 + 校验。
5. 渲染请求的格式；写 `report.json` 与各格式文件；PG 写 artifacts。
6. 状态置 `ready`；发事件；失败置 `failed`（保留部分产物供排查，但不对外提供下载）。

### 9.3 与扫描事件的关系

- 删除扫描结束的 `report_ready` 广播。
- 手动生成成功后，可向包含的 instance 广播一次 `report_ready`，`Content` 为 `/api/reports/{reportID}`；前端 live feed / execution trace 继续按现有类型展示。若实现成本高，可仅依赖生成对话框进度，不广播。

## 10. 前端改动

### 10.1 报告页

- 数据源改为 `GET /api/tasks/{id}/reports` 的聚合列表（或新增 `GET /api/reports?project_id=`）；每行一个 task 的最新版本，展开看历史。
- 显示：任务名、目标范围、最新版本、生成时间、模式（模板/Agent）、格式徽标、漏洞数、状态。
- 行操作：下载（按格式）、查看历史、重新生成、删除。
- 「生成报告」对话框：任务选择、run 选择（默认最新）、格式多选、模式（模板/Agent）、Agent 模型、预估 token、开始。
- 进度：对话框内进度条 + 阶段文案（装配数据/撰写/渲染/完成），可取消。
- 未挂载任务的遗留扫描（无 task_id）：单独分组“未关联任务”，仅支持模板模式单扫描报告，不进入任务报告版本。

### 10.2 任务详情报告 tab

- 复用报告页的版本列表（按 task 过滤）。
- 提供「生成报告」按钮。
- 移除按 run 的 `/api/report/{instance_id}` 链接。

### 10.3 发现页

- 行点击/“详情”打开详情抽屉（§7.1）。
- 行操作菜单新增：导出 MD / 导出 HTML / 导出 JSON。
- 批量导出不在本期范围。
- 任务详情“发现”tab 复用详情抽屉。

### 10.4 扫描详情

- 头部 PDF 按钮改为「查看报告」：若 scan 挂载了 task，跳到任务报告 tab；未挂载则提示“该扫描未关联任务，前往报告页生成”或保留模板单扫描报告入口。
- 移除“报告已生成”的自动假设。

### 10.5 i18n

新增 `reports.generate*`、`reports.mode.*`、`reports.format.*`、`findings.detail.*`、`findings.export.*`、`report.rawMissing`、`report.rawTruncated`、`report.redacted` 等键；中文为默认语言。

## 11. 中文与 SRC 写作规范

- 等级统一：严重 / 高危 / 中危 / 低危；映射固定（CVSS 9.0–10 严重，7.0–8.9 高危，4.0–6.9 中危，0.1–3.9 低危）。
- `已验证存在` 仅用于 `verified` / `exploit-proven`；`needs-manual-verification` 写“疑似/待人工确认”。
- 不夸大：无实测数据不写“影响百万用户”；未验证不写“可导致”。
- 强制脱敏；附脱敏说明。
- 时间统一 `Asia/Shanghai`，格式 `YYYY-MM-DD HH:mm:ss`。
- 固定合规声明：授权范围、测试窗口、未破坏数据、未留存敏感数据、测试账号已停用。
- 术语中英对照表放封面或附录（本期可省略附录，保留封面声明）。

## 12. 验收

1. **手动-only**：扫描结束、wildcard 子域结束均不产生 PDF；无 `report_ready` 自动事件。
2. **总报告 + 子报告**：一个 task 含 3 个 target / 2 个子域时，生成 1 份总报告 + 5 份子报告；子报告即使 0 漏洞也存在。
3. **格式**：PDF / MD / HTML 内容同源，中文 PDF 无缺字（内嵌 CJK 字体）。
4. **发现页详情**：每条漏洞可展开完整说明；raw HTTP 从 `traffic_refs` 加载；缺失/截断有提示。
5. **单漏洞导出**：MD/HTML/JSON 均含 HTTP raw 请求；原始包含 Cookie/token 时导出已脱敏；无包时标注缺失并给重建 curl。
6. **Agent 校验**：编造 finding_id、改写 severity、引用未验证 finding 作为主链、无理由调级 → 校验失败并回退模板。
7. **版本与并发**：重复点击返回 409；revision 递增；历史版本可下载。
8. **存储**：报告元数据在 PG，文件在报告目录；删除 task 级联删除；路径逃逸测试通过。
9. **回归**：findings 接受、验证标签、Verifier、闸门、扫描生命周期测试不变；`go test ./... -race` 与 `npm run typecheck` 通过。

## 13. 不在本期范围

- DOCX 输出、报告在线编辑、截图自动采集与打码、SRC 平台自动提交、邮件/IM 附件通知（可保留配置位）、批量漏洞导出、报告模板市场。

## 14. 待实现顺序（建议）

1. `ReportSpec` 类型 + 事实装配（只读，含脱敏器、traffic refs 解析）。
2. PG 迁移 + repository + 文件布局 + 手动生成 job（模板模式，先 PDF/MD/HTML）。
3. 移除自动生成（scan_session / orchestrator / phase 文案），更新测试。
4. 报告页 / 任务 tab 改造。
5. 发现页详情抽屉 + 单漏洞导出（MD/HTML/JSON）。
6. 报告撰写 Agent + 校验 + 回退；风险排序与攻击链。
7. 子报告渲染与索引；并发/重启/取消收口。

## 15. 实现说明（2026-11-23）

- 报告实体：PG `reports` / `report_artifacts`（迁移 `0028_reports.sql`）+ `<dataDir>/reports/<taskID>/<reportID>/`；同一任务同时只允许一个 queued/generating（PG 部分唯一索引 + 文件存储同语义）。
- 生成入口：`POST /api/tasks/{id}/reports`，进程内 job（不进 River），失败/取消/重启 reconciliation 均落 PG 状态。
- 总报告 5 章；每个 target/subdomain 一份子报告（含 0 漏洞目标）；`report.json` 为可重渲染的 ReportSpec。
- 发现页详情复用扫描详情抽屉；单漏洞导出 `GET /api/scans/{scanID}/findings/{vulnID}/export`，输出 md/html/json，HTTP raw 强制脱敏，缺包标注。
- 报告撰写 Agent：仅写总报告叙述（模板仍生成子报告），输出经 finding/证据引用、攻击链验证级别、±1 band 调整与脱敏校验；任何失败回退模板。
- 兼容：未挂载任务的遗留扫描仍可用 `/api/report/{id}` 生成单扫描 PDF；任务报告页只列任务级报告。
