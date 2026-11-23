# 统一工具容器实施方案

> 预构建统一工具镜像 + 平台按项目调用 + 多项目并发编排
>
> 文档状态：实施方案 v1（对齐仓库当前实现，2026-11-23）
> 权威实现：`internal/runtime/`、`tools/toolbox/`、`cmd/runnerd/`、`cmd/toolshim/`、`internal/jobs/toolbox.go`
> 架构背景：`docs/ARCHITECTURE.md`「Tool execution plane」、`AGENTS.md` 第 7.5 条

---

## 0. 结论与部署形态判定

### 0.1 一句话结论

平台保留**控制面**（agent 主循环、策略、安全链、状态权威、审计），把**执行面**下沉为「每个扫描/项目一个工具容器」。工具来自一个预构建、按 profile 分层、按 digest 锁定的统一镜像；平台通过统一的 `Driver` 接口调用容器，通过 PostgreSQL 的 `desired_gen/applied_gen` 水位 + River 保洁队列完成崩溃追赶和多项目并发编排。

### 0.2 平台是否运行在容器内：**是**

官方部署路径（根 `docker-compose.yml`、`deploy/docker-compose.yml`、`deploy/chart/`）都把 Xalgorix 平台本身作为容器运行（`privileged: true`，仅暴露 `127.0.0.1:9137`）。因此本方案以「**平台在容器内、工具容器也在宿主 Docker 上**」为基准形态，并明确：

- 平台**不把 Docker 嵌进自己**，也不在工具容器里放 Docker socket。
- 工具容器由**宿主 Docker daemon** 统一管理；平台与 daemon 之间的唯一通道是：
  - **runnerd（推荐）**：宿主侧常驻服务持有 `/var/run/docker.sock`，只暴露 ensure/exec/stop/remove/inspect/list/capacity 七个操作；平台容器通过 bind mount `/run/xalgorix` 访问其 unix socket。
  - **socket（仅开发）**：平台容器直接挂载 `/var/run/docker.sock`，等价于把宿主 root 交给平台。生产禁止。
- 平台以宿主机二进制运行（无容器）时，同一套接口走 `socket` transport 即可，代码路径不变。

### 0.3 平台在容器内时必须先解决的三个「同宿主」接缝

这三项不是可选优化，任何一项不成立都会让容器执行面在生产形态下静默失效：

| 接缝 | 约束 | 现状 | 实施要求 |
|---|---|---|---|
| **扫描目录同路径挂载** | 平台把 `scanDir` 原样交给 daemon 做 bind mount，宿主必须存在**完全相同绝对路径** | 已加启动预检：容器内 `XALGORIX_HOST_DATA_DIR` 必须存在且等于 `XALGORIX_DATA_DIR`；split compose 用 `${XALGORIX_DATA_DIR}:${XALGORIX_DATA_DIR}` | 部署时把宿主机目录挂到**完全相同的绝对路径**，例如 `/srv/xalgorix/data:/srv/xalgorix/data` 并设 `XALGORIX_DATA_DIR=/srv/xalgorix/data`。`/srv/...:/data` 不满足同路径契约 |
| **流量代理可达性** | `proxy-only` 网络是 `--internal`，唯一出口是平台流量代理 | 代理支持 `XALGORIX_TRAFFICPROXY_BIND` / `XALGORIX_TRAFFICPROXY_ADVERTISE`；工具侧用 `XALGORIX_TOOLBOX_PROXY_URL` | 绑定项目网络可达地址（或 sidecar），并让 `TOOLBOX_PROXY_URL` 指向同一地址；未配置且要求代理时 fail-closed。绑 `0.0.0.0` 会暴露拦截代理，必须防火墙限制 |
| **Chromium CDP 可达性** | `web` profile 把 DevTools 发布到宿主 `127.0.0.1:<ephemeral>` | 已实现 runnerd TCP relay：runnerd 用 host network 探测宿主端口，平台经 `host.docker.internal` 访问 relay | compose split 已加 `extra_hosts: host.docker.internal:host-gateway` 与 runnerd `network_mode: host`（Linux）。宿主二进制平台仍可直接用 `127.0.0.1` |

### 0.4 标记约定与本文结构

本文按六个部分组织（工具清单 → 镜像与集成 → 调用接口 → 并发与隔离 → 资源与生命周期 → 扩展机制），并在第 7 节给出「现状差距 → 实施顺序 → 验收」。需要标注现状的表格使用：

- ✅ 已实现且可用
- ⚠️ 已实现但有明确缺口
- ❌ 尚未实现

---

## 1. 当前可纳入容器体系的工具清单

### 1.1 三个层次，不要混为一谈

| 层 | 是什么 | 运行位置 | 清单来源 |
|---|---|---|---|
| **L1 预装 CLI 工具** | 镜像里预构建的外部安全工具（nmap、nuclei、sqlmap……） | 工具容器内，由 `bash -lc` / 专用工具直接调用 | `tools/toolbox/toolpack.yaml`（CI 健康门） |
| **L2 平台执行型工具** | agent 可调用的工具面中，需要落到执行面的部分（terminal/python/file/browser/portscan/codesearch） | 路由到工具容器 | `internal/tools/*` + `internal/agent/agent.go` 注册 |
| **L3 平台控制型工具** | ledger、plan、report、verify、http_request、notes、skills 等 | 平台进程内（安全链、状态权威所在） | `internal/agent/*` |

**原则**：L3 不搬进容器。安全链（scope guard、sandbox policy、凭证剥离、代理注入、审批）在 dispatch 前完成；容器是执行器，不是安全边界。L1 是「能力供给」，L2 是「调用入口」，L1 的可用性由 L2 暴露给模型。

### 1.2 L1：镜像预装工具（50 项，`toolpack.yaml` schema 1）

统计：`core` 27 项、`full` 22 项、`web` 1 项（`full` 以 `core` 为基础，`web` 以 `core` 为基础）。

#### Recon / 资产发现（8，core）

| 工具 | 用途 | 能力 |
|---|---|---|
| subfinder | 被动子域枚举（多数据源聚合） | — |
| dnsx | DNS 解析、爆破与记录枚举 | — |
| assetfinder | 从公开来源枚举关联网域 | — |
| gau | 从归档源收集历史 URL | — |
| waybackurls | 从 Wayback Machine 拉取历史 URL | — |
| katana | 现代化 Web 爬虫（支持 JS 渲染） | — |
| gospider | 快速 Web 蜘蛛 | — |
| hakrawler | 轻量爬虫，提取链接与表单 | — |

#### HTTP 探测（4，core）

| 工具 | 用途 | 能力 |
|---|---|---|
| httpx | HTTP 存活、标题、技术栈与状态码探测 | — |
| nuclei | 模板化漏洞扫描（含官方模板库） | — |
| nikto | Web 服务器配置与已知漏洞扫描 | — |
| whatweb | Web 技术指纹识别 | — |

#### 内容发现 / 模糊测试（4，core）

| 工具 | 用途 | 能力 |
|---|---|---|
| ffuf | 目录、参数与虚拟主机模糊测试 | — |
| gobuster | 目录/DNS/vhost 爆破 | — |
| dirsearch | Web 路径扫描 | — |
| arjun | HTTP 参数发现 | — |

#### WAF（1，core）

| 工具 | 用途 | 能力 |
|---|---|---|
| wafw00f | WAF 存在性探测 | — |

#### TLS（1，core）

| 工具 | 用途 | 能力 |
|---|---|---|
| sslscan | TLS 配置与协议/套件审计 | — |

#### 运行时（5，core）

| 工具 | 用途 | 能力 |
|---|---|---|
| python3 | 执行模型生成的 Python 脚本 | — |
| pipx | 隔离安装 Python 工具 | — |
| go | 构建期/长尾工具安装工具链 | — |
| rg | 白盒源码检索（`code_search` 后端） | — |
| toolshim | 容器内文件读写与代码检索执行器 | — |

#### 通用工具（4，core）

| 工具 | 用途 | 能力 |
|---|---|---|
| uro | URL 列表去重与降噪 | — |
| jq | JSON 管道处理 | — |
| curl | HTTP 客户端（手工验证与 PoC） | — |
| git | 源码克隆与 Git 泄露利用 | — |

#### 网络 / 原始套接字（6，full）

| 工具 | 用途 | 能力 |
|---|---|---|
| nmap | 端口与服务扫描（SYN/版本/脚本） | NET_RAW, NET_ADMIN |
| masscan | 大规模快速端口扫描 | NET_RAW, NET_ADMIN |
| naabu | ProjectDiscovery 端口扫描器 | NET_RAW |
| gogo | `port_scan` 工具的后端扫描引擎 | NET_RAW |
| arp-scan | 二层主机发现 | NET_RAW, NET_ADMIN |
| netdiscover | 被动/主动 ARP 网段发现 | NET_RAW, NET_ADMIN |

#### MITM（2，full）

| 工具 | 用途 | 能力 |
|---|---|---|
| responder | LLMNR/NBT-NS/mDNS 投毒与凭据捕获 | NET_RAW, NET_ADMIN |
| ettercap | 中间人嗅探与注入 | NET_RAW, NET_ADMIN |

#### 利用（2，full）

| 工具 | 用途 | 能力 |
|---|---|---|
| sqlmap | SQL 注入检测与数据提取 | — |
| searchsploit | ExploitDB 本地检索 | — |

#### 凭据（5，full）

| 工具 | 用途 | 能力 |
|---|---|---|
| hydra | 在线口令爆破（多协议） | — |
| medusa | 并行在线口令爆破 | — |
| john | 离线口令破解 | — |
| hashcat | GPU/CPU 哈希破解 | — |
| impacket-secretsdump | Windows SAM/NTDS 凭据导出 | — |

#### SAST（3，full）

| 工具 | 用途 | 能力 |
|---|---|---|
| semgrep | 多语言静态分析 | — |
| bandit | Python 安全静态检查 | — |
| brakeman | Ruby on Rails 安全审计 | — |

#### 密钥扫描（2，full）

| 工具 | 用途 | 能力 |
|---|---|---|
| gitleaks | Git 历史中的密钥泄露检测 | — |
| trufflehog | 高熵密钥与凭据扫描 | — |

#### 调试（2，full）

| 工具 | 用途 | 能力 |
|---|---|---|
| gdb | 二进制调试（白盒利用） | SYS_PTRACE |
| strace | 系统调用跟踪（进程行为分析） | SYS_PTRACE |

#### 浏览器（1，web）

| 工具 | 用途 | 能力 |
|---|---|---|
| chromium | 无头浏览器，供 browser_action/page_agent 通过 CDP 驱动 | — |

> **清单口径**：`toolpack.yaml` 是「CI 必须验证存在且可执行」的契约，不是镜像文件清单。`install.sh core` 实际还会装入 `notify/qsreplace/unfurl/anew/httprobe/gron/nmap/naabu/gogo` 等长尾二进制；它们不进入 selftest 契约，因此不得被平台当作稳定依赖。要成为稳定依赖，必须先在 `toolpack.yaml` 登记并补 `smoke`。

### 1.3 L2：平台工具 → 容器路由

| 平台工具 | 容器内执行方式 | 代码入口 | 备注 |
|---|---|---|---|
| `terminal_execute` | `bash -lc <command>`，同工作目录、同提示文案 | `internal/tools/terminal/terminal.go` → `tools.RunInProject` | 原生路径逐字节保留；容器路径只换进程命名空间 |
| `python_action` | `python3 -c <code>` | `internal/tools/python/python.go` | cwd 取 scanDir / 终端 cwd |
| `str_replace_editor` | `toolshim --request <json>`：view/create/str_replace/insert | `internal/tools/fileedit/fileedit.go` → `tools.RunToolshim` | 平台先过 `sandbox.Policy`，shim 再做容器内 containment 复核 |
| `list_files` / `search_files` | toolshim：list / search | 同上 | `search` 用 rg |
| `code_search` | toolshim：code_search | `internal/tools/codesearch/codesearch.go` | 白盒源码挂载在同路径 |
| `port_scan` | `gogo <args>`，结果文件写回 scan 目录 | `internal/tools/portscan/exec.go` | 需要 `net_profile=raw` + NET_RAW；proxy-only 直接拒绝 |
| `browser_action` | 连接容器内 Chromium 的 CDP endpoint | `internal/tools/browser/container.go` → `tools.ProjectCDPURL` | 仅 `web` profile；Chromium 常驻，跨调用复用 |
| `page_agent` / `discover_client_routes` | 复用同一 CDP 连接 | `internal/tools/pageagent/` | 同上 |
| Verifier 的 terminal/browser 调用 | 与主循环同一路由 | `internal/agent/verifier.go` | 验证命令也落在项目容器，保持隔离 |
| `http_request` / `send_request` / verify_* | **不路由**，平台内执行 | `internal/tools/httpclient/`、`internal/agent/verify_*.go` | 安全链与证据记录必须在平台侧 |
| ledger / plan / report / notes / info / skills | **不路由** | `internal/agent/*.go` | 状态权威，不进入执行面 |

### 1.4 为什么有些工具不进容器

- **安全链不可下沉**：scope、redirect/dial 复查、DNS rebinding、路径策略、密钥剥离、代理 fail-closed 都在平台。容器只接收「已裁决的 argv」。
- **状态权威不可下沉**：ledger、plan、finding 报告门、InfoItem 投影在平台/PG。容器是可丢弃的，扫描状态不是。
- **凭证面最小化**：容器无 LLM key、无 dashboard token、无数据库 DSN；`SanitEnv` 在平台侧先剥离。

---

## 2. 容器镜像的预定义方式与工具集成方法

### 2.1 单一事实源：`tools/toolbox/toolpack.yaml`

每项工具声明：

```yaml
- name: nuclei
  bin: /opt/xalgorix/bin/nuclei   # 容器内绝对路径
  category: http                  # 分组（面板/文档）
  purpose: 模板化漏洞扫描（含官方模板库）
  capabilities: []                # NET_RAW | NET_ADMIN | SYS_PTRACE
  profiles: [core]                # core | full | web
  smoke: ["nuclei", "-version"]   # 必须 exit 0 的冒烟命令
```

规则：

1. **未知 capability 必须拒绝**（设计规则）：防止某个工具静默获得 NET_ADMIN。当前 selftest 只解析 `capabilities`、未做取值校验，需按 7.1 P2 补齐。
2. `bin` 必须绝对路径；`smoke` 必须能在镜像内以非交互方式退出 0。
3. `profiles` 决定该工具属于哪个镜像目标；`full`/`web` 都从 `core` 派生。
4. 新增工具必须同时更新 `toolpack.yaml`、`versions.lock`（如可固定）、`install.sh` 对应分段、`selftest` 预期。

### 2.2 三镜像 profile 与构建图

```
base (kali-rolling + base 依赖 + toolshim/selftest/toolpack)
  ├── core   →  recon / http / fuzz / tls / waf / runtime
  ├── full   →  core + net + exploit + sast(credentials/debug/secrets)
  └── web    →  core + chromium
verify       →  core + 运行 toolbox-selftest（CI 门）
```

构建命令（仓库根目录）：

```bash
docker build -f tools/toolbox/Dockerfile --target core -t xalgorix-toolbox:dev .
docker build -f tools/toolbox/Dockerfile --target full -t xalgorix-toolbox:dev-full .
docker build -f tools/toolbox/Dockerfile --target web  -t xalgorix-toolbox:dev-web .
```

### 2.3 可复现性

- `versions.lock` 固定 Go/Python/Ruby 工具版本；apt 包由镜像快照固定。
- 禁止 `@latest`；空值只用于上游无稳定 tag 的例外，且生产镜像必须以 digest 引用，把解析结果冻结进镜像。
- `TOOLS_REFRESH` build-arg 用于显式打破安装层缓存（与主镜像的 nuclei 刷新契约一致），不是隐式追新。
- 发布必须输出 digest；平台只接受 `repo:tag@sha256:...`。**当前代码只在注释里要求 digest，未做启动校验（见 7.1 P0）。**

### 2.4 工具集成方法（四条路径）

| 路径 | 适用 | 做法 | 例子 |
|---|---|---|---|
| **PATH wrapper** | 与发行版同名但语义不同的工具 | `install.sh` 在 `/opt/xalgorix/bin` 建软链，PATH 首位优先 | `httpx` 必须指向 ProjectDiscovery，而非 Kali 的 Python httpx |
| **预装 CLI + 平台参数构造** | 参数稳定、输出可解析 | 平台只拼 argv，容器只执行 | `port_scan` → `gogo`；`terminal_execute` → `bash -lc` |
| **toolshim JSON 协议** | 需要结构化文件操作/检索 | 请求写 scan 目录下 `.toolshim/req-*.json`，容器内 shim 读文件返回 JSON | view/create/str_replace/insert/list/search/code_search |
| **常驻服务 + CDP** | 浏览器等长生命周期进程 | 容器内 detached 启动，平台按 endpoint 连接 | Chromium `--remote-debugging-port=9222` |

### 2.5 安全与洁净约束

- 镜像**无密钥、无扫描状态、无数据库 DSN、无 dashboard token**。
- 构建期剥离文件 capabilities：容器以「丢弃全部 capability」运行，二进制自带 setcap 会 `execve EPERM`（nmap/masscan/arping 的经典问题），因此必须 `setcap -r`。
- rootfs 只读；仅 `/tmp` tmpfs 与 scan 目录可写。
- `cmd/toolshim` 与 `tools/toolbox/selftest` 是纯 Go、CGO 关闭、静态构建，复用仓库 `go.mod`。

---

## 3. 平台调用容器的接口设计

接口分三层：**Go Driver（进程内契约）→ runnerd HTTP（跨容器/跨进程）→ toolshim/CDP（容器内执行协议）**。三层语义一致，错误码同一套。

### 3.1 Go Driver：平台内部唯一入口

```go
type Driver interface {
    Backend() Backend
    EnsureProject(ctx, ProjectSpec) (ProjectHandle, error)  // 幂等
    Exec(ctx, ExecSpec) (*ExecResult, error)                // 绝不重试
    ExecStream(ctx, ExecSpec, onFrame func(StreamFrame)) (*ExecResult, error)
    StopProject(ctx, projectID string, grace time.Duration) error
    RemoveProject(ctx, projectID string) error
    Inspect(ctx, projectID string) (ProjectState, error)
    Capacity(ctx) (CapacityReport, error)
    Close() error
}
```

实现：`NativeDriver`（历史行为）、`DockerDriver`（socket/宿主内）、`RunnerdDriver`（unix socket 转发）。`runtime.ActiveContainer()` 是工具层「本地还是容器」的**唯一开关**。

### 3.2 请求契约：`ExecSpec`

```json
{
  "project_id": "scan-7f3a",
  "scan_id": "scan-7f3a",
  "call_seq": 42,
  "tool": "terminal_execute",
  "argv": ["bash", "-lc", "nuclei -u https://target -severity high"],
  "cwd": "/data/scans/scan-7f3a",
  "env": ["XALGORIX_RATE_DELAY_MS=0"],
  "stdin": null,
  "timeout": 600000000000,
  "stdout_limit": 1048576,
  "stderr_limit": 524288,
  "artifact_dir": "/data/scans/scan-7f3a/tool-outputs",
  "heavy": true,
  "net_profile": "proxy-only"
}
```

调用方必须已经完成：

1. scope guard（含 loopback/本机网卡/仪表盘监听地址无条件拦截）；
2. `sandbox.Policy` 路径检查；
3. `SanitEnv` 凭证剥离；
4. 代理注入（`HTTP_PROXY/HTTPS_PROXY/ALL_PROXY/NO_PROXY` + CA bundle）；
5. 审批门。

`argv` 是最终值，执行器不得改写、不得再解释、不得重试。

### 3.3 响应契约：`ExecResult`

```json
{
  "call_seq": 42,
  "exit_code": 0,
  "stdout": "...",
  "stderr": "...",
  "stdout_truncated": false,
  "stderr_truncated": false,
  "archive_path": "/data/scans/scan-7f3a/tool-outputs/42.out",
  "timed_out": false,
  "canceled": false,
  "duration": 1234567890,
  "container_id": "b1c2..."
}
```

关键语义：

- **命令退出非 0、超时、缺二进制 → `ExecResult`，不是 Go error**。避免 registry 熔断器把「agent 可纠正的软失败」记成工具缺陷。
- **基础设施失败 → `*ExecError`**（见 3.4）。
- 输出超限不报错：截断并写 `archive_path`（完整产物落盘，模型只看到引用/摘要）。
- `Success()` = `ExitCode==0 && !TimedOut && !Canceled`。

### 3.4 错误分类（稳定机器码）

| Code | 含义 | 可重试 | 熔断相关 |
|---|---|---|---|
| `E_RUNTIME_UNAVAILABLE` | runtime/transport 不可达 | 是 | 是 |
| `E_IMAGE_MISSING` | 镜像 digest 缺失或不可信 | 否 | 是 |
| `E_PROJECT_LIMIT` | 项目容器上限 | 是 | 是 |
| `E_LEASE_TIMEOUT` | 等待启动租约超时 | 是 | 是 |
| `E_CONTAINER_START` | 创建/启动容器失败 | 是 | 是 |
| `E_EXEC_SPAWN` | 无法 fork/attach 进程 | 否 | 是 |
| `E_NOT_FOUND` | 容器内二进制不存在 | 否 | 是 |
| `E_TIMEOUT` | 超时 | 否 | 是 |
| `E_CANCELED` | 扫描/项目取消 | 否 | **否** |
| `E_OUTPUT_LIMIT` | 输出超上限 | 否 | 是 |
| `E_SANDBOX_DENIED` | 容器内路径策略拒绝 | 否 | **否** |
| `E_SCOPE_DENIED` | 目标解析为操作者机器 | 否 | **否** |
| `E_UNSUPPORTED` | 该 backend 不支持 | 否 | 是 |

`CircuitBreakerRelevant` 明确排除取消、scope 拒绝、sandbox 拒绝——这些是操作者/引擎的主动结果，不是工具缺陷。

### 3.5 runnerd HTTP API（平台 ↔ 宿主）

传输：unix socket，JSON；exec 支持 NDJSON 流。

| 方法 | 路径 | 请求 | 响应 |
|---|---|---|---|
| POST | `/v1/projects` | `ProjectSpec` | `{ "project": ProjectHandle }` |
| POST | `/v1/projects/{id}/exec` | `ExecSpec` | `{ "result": ExecResult }` |
| POST | `/v1/projects/{id}/exec?stream=1` | `ExecSpec` | `application/x-ndjson` 帧流 |
| POST | `/v1/projects/{id}/stop` | `{ "grace": "10s" }` | `{ "ok": true }` |
| DELETE | `/v1/projects/{id}` | — | `{ "ok": true }` |
| GET | `/v1/projects/{id}` | — | `{ "project": ProjectState }` |
| GET | `/v1/projects` | — | `{ "projects": [ProjectState] }` |
| GET | `/v1/capacity` | — | `{ "capacity": CapacityReport }` |

错误包络（HTTP 状态同时映射）：

```json
{ "error": { "code": "E_PROJECT_LIMIT", "message": "...", "retryable": true, "phase": "ensure" } }
```

映射：404 `E_NOT_FOUND`；429 `E_PROJECT_LIMIT`/`E_LEASE_TIMEOUT`；408 `E_CANCELED`；501 `E_UNSUPPORTED`；502 `E_CONTAINER_START`/`E_IMAGE_MISSING`；其余 500。

NDJSON 帧：

```json
{"type":"stdout","seq":1,"data":"..."}
{"type":"stderr","seq":1,"data":"..."}
{"type":"exit","result":{...}}            // 或 {"type":"exit","error":{...}}
```

`seq` 单调递增，传输中断后调用方可重同步；`exit` 帧必须存在，缺失按 `E_RUNTIME_UNAVAILABLE` 处理。

**为什么 runnerd 是独立进程**：平台容器不需要 `/var/run/docker.sock`，因此不会获得宿主 root 等价能力；runnerd 不接数据库、不认识扫描，只是无状态执行端点。

### 3.6 容器内协议：toolshim

平台把请求写到 scan 目录（同路径挂载两侧可见），不在 stdin 上传（Docker exec 无法在不 hijack 的情况下附 stdin，且 create 请求可能很大）。

请求：

```json
{ "op": "str_replace", "root": "/data/scans/scan-7f3a", "path": "/data/scans/scan-7f3a/app.py",
  "old_str": "debug=True", "new_str": "debug=False" }
```

响应：

```json
{ "output": "Replaced 1 occurrence in /data/...", "error": "" }
```

支持 `op`：`view | create | str_replace | insert | list | search | code_search`。shim 用 `contain(root, path)` 做容器内二次 containment 检查（纵深防御，不替代平台 `sandbox.Policy`）。

### 3.7 容器内协议：CDP

- `ProjectSpec.PublishCDP=true`（仅 `web` profile）→ 容器 9222 发布到宿主 `127.0.0.1:0`（临时端口）。
- `CDPEndpoint` 幂等启动 `chromium --headless=new --remote-debugging-address=0.0.0.0 --remote-debugging-port=9222`，轮询 `/json/version`，返回改写为宿主端口的 `webSocketDebuggerUrl`。
- Chromium 不读 `HTTP_PROXY`：代理必须作为 `--proxy-server=<url>` 传入，且 `<url>` 必须能被项目网络解析（不能是 loopback）。
- 平台容器形态下 CDP 的 loopback 不可达，见 0.3。

### 3.8 超时、取消、输出与重试

| 维度 | 规则 |
|---|---|
| 超时 | 平台侧 `context.WithTimeout` + 容器内 `timeout -k 5 <secs> --` 双保险；Docker exec 本身无法被 API kill |
| 取消 | 平台 cancel 使 exec 流返回，`ExecResult.Canceled=true`；不要等待 inspect |
| 输出 | `iolimit` 限制 + 完整输出 spool 到 `artifact_dir` |
| 重试 | **Exec 永不重试**。只有「容器已消失（`E_NOT_FOUND`）」允许 `EnsureProject` 重建后重放一次，因为 Ensure 幂等而 Exec 不幂等 |
| 熔断 | 仅基础设施错误计数；`E_CANCELED`/`E_SCOPE_DENIED`/`E_SANDBOX_DENIED` 不计 |

---

## 4. 多项目并发下的容器编排与隔离

### 4.1 编排模型：一个扫描 = 一个项目 = 一个容器

- `ProjectID = ScanID`，容器名 `xalg-toolbox-<sanitized-project>`，网络名 `xalg-proj-<sanitized-project>`。
- 容器标签是重启后唯一可识别的身份：`xalgorix.role=toolbox`、`xalgorix.project`、`xalgorix.scan`、`xalgorix.instance`、`xalgorix.profile`、`xalgorix.created_at`。
- **多目标并发 ≠ 多项目**：一个扫描实例可以并发跑多个 target（`XALGORIX_TARGET_CONCURRENCY`，上限 4），但共享同一项目容器；容器级隔离单位仍是扫描。
- 不同平台实例（不同 `instance_id`）绝不 adopt/清理彼此的容器。

### 4.2 两层背压（现状）+ 两层资源租约

| 层 | 作用域 | 机制 | 现状 |
|---|---|---|---|
| L1 项目容器数 | 全平台 | `DockerDriver.slots`（`XALGORIX_MAX_PROJECTS`），或 `resources.AcquireProjectSlot` | ⚠️ `MaxProjects=0` 时 driver 无上限，且热路径未调用 `AcquireProjectSlot` |
| L2 容器内并发 exec | 每个 driver | `DockerDriver.execSlots`（`XALGORIX_TOOLBOX_CONCURRENCY`，默认 4） | ✅ |
| L3 工具启动租约 | 平台 | `resources.AcquireToolLeaseContext`（heavy/light） | ✅ |
| L4 扫描准入 | 平台 | `XALGORIX_MAX_INSTANCES` / RAM 推导 | ✅ |

L1 缺口是 7.1 的 P0：并发多项目时如果没有统一项目配额，容器数会跟扫描数线性增长，直到宿主 OOM。

### 4.3 隔离矩阵

| 维度 | 策略 | 实现 |
|---|---|---|
| 进程/文件系统 | 每项目独立容器；rootfs 只读；仅 `/tmp` tmpfs + scan 目录 rw | `ReadonlyRootfs:true`、`Tmpfs`、`Binds` |
| 网络 | 每项目独立 bridge 网络；`proxy-only` 为 `--internal`；`raw` 才授予 NET_RAW/NET_ADMIN | `NetworkSpec`、`networkName()` |
| 能力 | `CapDrop:["ALL"]`，按 profile/net_profile 加回；`no-new-privileges` | `capabilitiesFor()` |
| 特权 | **永不 `Privileged`**；工具容器内**永不放 docker.sock** | `hostCfg.Privileged=false` |
| 挂载 | 业务路径同绝对路径；共享资产用固定容器路径 | `mounts.go` |
| 凭证 | 平台先 `SanitEnv` 剥离；容器 env 只含扫描作用域变量 | `ContainerEnvFor` |
| 状态 | 容器内无持久状态；scan 产物落同路径挂载 | `BuildMounts` |
| 生命周期 | 容器可随时删除；重跑由 PG 期望态 + reconciler 重建 | `tb_projects` |

网络 profile：

| `net_profile` | 网络 | 典型用途 | 约束 |
|---|---|---|---|
| `none` | `none` | 纯分析、selftest | 无出口 |
| `proxy-only`（默认） | `--internal` + 代理 | 所有 HTTP/CLI 工具 | 必须有项目网络可达的代理；否则 fail-closed |
| `raw` | bridge + NET_RAW/NET_ADMIN | SYN 扫描、ARP、MITM | 只能由 scope guard 的允许清单驱动 |
| `direct` | 默认 bridge | 操作者明确未启用代理 | **不得**在要求代理时选择，否则绕过观测/归因 |

### 4.4 崩溃追赶协议

- **期望态在 PG**（`toolbox_projects.desired_state`），**实际态在运行时**（标签 + inspect）。
- 写路径先 `RecordProject`（bump `desired_gen`），再 `EnsureProject`，再 `MarkApplied`（推进 `applied_gen`）。
- 崩溃在「容器已建、PG 未写」之间：孤儿清理删除；崩溃在「PG 已写、容器未建」之间：reconciler 重建。
- `status ∈ pending|applied|gap|failed`；瞬时压力（`E_PROJECT_LIMIT`/`E_RUNTIME_UNAVAILABLE`）保持 pending，不升级为 gap。
- River `toolbox` 队列只做保洁与追赶，**不接管**扫描主循环、调度、审批、SSE。

---

## 5. 资源分配与生命周期管理

### 5.1 每项目预算

`resources.CurrentProjectBudget()` 解析：

| 资源 | 默认 | 覆盖变量 | 说明 |
|---|---|---|---|
| 内存 | 可用 RAM / 项目槽位，夹在 768–4096 MB | `XALGORIX_PROJECT_MEM_MB` | 槽位 = CPU 核数（上限 6） |
| CPU | 1 核（quota=period） | 预算内固定 | 硬 quota 会让扫描时长不可预测，默认交给内核时间片 |
| PIDs | 512 | `XALGORIX_PROJECT_PIDS` | 防 fork bomb |
| `/tmp` tmpfs | 512 MB | `XALGORIX_PROJECT_TMPFS_MB` | `rw,size=...,exec` |
| 磁盘 | 4096 MB | `XALGORIX_PROJECT_DISK_MB` + `XALGORIX_PROJECT_DISK_ENFORCE` | opt-in `StorageOpt size`；仅覆盖容器可写层，scan 目录需外部 quota |
| 项目数 | RAM 推导 | `XALGORIX_MAX_PROJECTS` | ⚠️ 未接入 driver/热路径 |

cgroup 落地：

```go
Memory:     spec.Limits.MemoryMB * 1024 * 1024
MemorySwap: same              // 禁用 swap，超限在容器内 OOM
NanoCPUs:   CPUQuota/CPUPeriod * 1e9
PidsLimit:  spec.Limits.Pids
Tmpfs:      "/tmp": "rw,size=<n>m,exec"
```

### 5.2 生命周期状态机

```
（无） → pending → ready → busy ⇄ idle → stopping → stopped → removed
                     ↘ failed
容器存在但无 PG 行 → orphaned
```

- `EnsureProject`：幂等。同名容器存在且镜像 digest 匹配 → 复用（未运行则 start）；digest 变了 → 删除重建，绝不原地改配置。
- `Exec`：不改变项目期望态；只 `Touch` 活动时间。
- `StopProject`：宽限期后 kill；容器保留，可再次 ensure 启动。
- `RemoveProject`：停 + 删容器 + 删网络 + 释放项目槽。
- `Inspect/ListProjects`：读实际态，用于面板和 GC。

### 5.3 GC 与回收（River `toolbox` 队列，7 类作业）

| 作业 | 频率 | 作用 |
|---|---|---|
| `xalgorix_toolbox_gc_sweep` | 1 min | 删「有本实例标签、无期望态行」的孤儿容器，再 reconcile |
| `xalgorix_toolbox_reap_idle` | 2 min | 空闲超过 `XALGORIX_PROJECT_IDLE_TTL_SECONDS`（默认 600s）→ 置 `stopping` |
| `xalgorix_toolbox_adopt` | 启动/GC | 重启后把存活容器重新挂回 PG 行；不 adopt 无行容器 |
| `xalgorix_toolbox_prefetch_image` | 启动/1 h | 预拉镜像 digest |
| `xalgorix_toolbox_selftest` | 按需 | 在新 digest 上跑 `toolbox-selftest`，结果写 `toolbox_image_health` |
| `xalgorix_toolbox_reconcile_project` | 按需 | 单项目追赶 |
| `xalgorix_toolbox_image_gc` | 1 h | 保留最近 N 个 digest（默认 3），不删在用镜像 |

### 5.4 平台关闭

- 停止接受新工作 → 列出本实例项目容器 → `StopProject(5s)` → 停 River → 清全局 driver/store/toolbox → 关闭 transport/pool。
- 容器被显式停止，避免平台退出后留下带特权网络状态的容器。
- 平台崩溃不保证停容器：由 adopt/GC 在下次启动收口。

### 5.5 失败姿态

| 场景 | 行为 |
|---|---|
| docker backend 启动时 daemon 不可达 | 启动失败（fail-fast），不回退 native |
| `XALGORIX_TOOLBOX_IMAGE` 为空 | 启动失败，给出配置指引 |
| PG 写失败 | 不阻断已启动 Run；记 failed，reconciler 追赶 |
| 容器创建失败 | `E_CONTAINER_START`；热路径不重试，交给下一次工具调用/reconciler |
| 容器消失 | `E_NOT_FOUND` → 重建一次 → 重放该次 Exec |
| 代理要求但不可达 | 直接报错，绝不回落直连 |

---

## 6. 后续新增工具时的扩展机制

### 6.1 新增一个「预装 CLI」

1. **登记**：在 `tools/toolbox/toolpack.yaml` 增加条目（name/bin/category/purpose/capabilities/profiles/smoke）。
2. **固定版本**：可固定的写入 `versions.lock`；无可固定 tag 的留空并注明，由镜像 digest 冻结。
3. **安装**：在 `install.sh` 对应分段安装（`base|core|net|exploit|sast|browser`）；`core` 必须成功，可选工具失败只 WARN。
4. **PATH**：若与发行版同名冲突，在 `/opt/xalgorix/bin` 建 wrapper/软链，保证 PATH 首位命中。
5. **健康门**：`toolbox-selftest --profile <p> --json` 必须包含它并通过；CI `toolbox.yml` 对 core/full/web 三 profile 跑 selftest，失败阻断发布。
6. **发布**：推送 `ghcr.io/<owner>/xalgorix-toolbox:<sha>-<profile>`，记录 digest。
7. **上线**：更新 `XALGORIX_TOOLBOX_IMAGE` 为 `...@sha256:...`；平台 `PrefetchImage` + `Selftest` 作业验证后生效。

**能力声明纪律**：新增 capability 必须同时改 selftest 校验和 `capabilitiesForProfile`；任何工具不得因为「需要跑起来」而静默获得 NET_ADMIN/SYS_PTRACE。

### 6.2 新增一个「平台执行型工具」

1. 在 `internal/tools/<pkg>` 实现工具，原生路径保持可运行。
2. 用 `tools.ContainerActive()` 分支：
   - 命令行工具 → `tools.RunInProject(ctx, sc, ExecSpec{...})`；
   - 文件/检索 → `tools.RunToolshim`；
   - 浏览器 → `tools.ProjectCDPURL`。
3. 遵守约束：
   - 不自行重试 Exec；
   - 预期内失败返回 `Result`，基础设施失败返回 `error`；
   - 不直接 broadcast/写 HTTP；
   - 输出走 `iolimit` + `ArtifactDir`；
   - 路径先过 `sandbox.Policy`。
4. 更新 `internal/web/tools_catalog.go`（目录漂移是文档 bug，不是执行 bug）。
5. 测试：native 与 container 两条路径都要有；容器路径至少覆盖「无 driver → 原生回退」和「driver 存在 → 路由」。

### 6.3 新增一个 profile

1. Dockerfile 增加 `FROM core AS <profile>` 或从 `full` 派生。
2. `toolpack.yaml` 的 `profiles` 增加该值；`selftest` 的 `profileMatches` 自动生效。
3. 更新 `runtime.ProfileCore/Full/Web` 常量、`capabilitiesForProfile`、`BuildProject` 的 `PublishCDP` 判断。
4. CI matrix、发布 tag、文档四处同步。
5. 若需要新 capability，先评审网络/调试面，再改 `capabilitiesFor`。

### 6.4 schema 演进

- `toolpack.yaml` 带 `schema: 1`；读取方忽略未知字段、拒绝未知 capability。
- 加字段保持向后兼容；删字段/改语义必须升 schema 并同时改 selftest 与 CI。
- `ProjectSpec`/`ExecSpec` 是跨进程契约：只加可选字段；`json` tag 不复用、不改义。runnerd 与平台必须同版本发布。

---

## 7. 现状差距与实施顺序

### 7.0 实施状态（2026-11-23）

已落地并有测试/配置门禁：

- digest 强制（`NewDockerDriver`、`BuildProject`、runnerd 下发路径）+ `XALGORIX_ALLOW_UNPINNED_TOOLBOX` 仅开发逃生阀；
- 项目配额兜底（`resources.CurrentProjectBudget().MaxProjects`）+ 重启后槽位对齐 + `Capacity` 真实上限；
- 代理 fail-closed（`XALGORIX_TOOLBOX_PROXY_URL`，不再回落 loopback）；
- 最大寿命（`XALGORIX_PROJECT_MAX_LIFE_SECONDS`，默认 21600s）+ `ReapIdle` 硬上限优先于 idle；
- 镜像 GC 按 `xalgorix-toolbox` 仓库名过滤，保护在用镜像；
- selftest capability/profile 白名单与 `--profile` 拼错 fail-closed；
- 构建/部署三轴：`PLATFORM_VARIANT=slim|allinone`、`TOOLBOX_PROFILE=core|full|web`、`RUNTIME_MODE=native|docker`，compose `allinone|split`；
- split 数据目录同路径 bind mount、entrypoint 跟随 `XALGORIX_DATA_DIR`、共享 CA/cache 目录自动准备；
- 根 compose 默认包含 PostgreSQL 与 DSN，`docker compose up -d` 不再因空 DSN 退出；
- `main.go` 接线：max-life 作业入参、`XALGORIX_HOST_DATA_DIR` 容器内同路径预检；
- `web` profile 容器化 CDP relay：runnerd 侧 TCP 转发 + 平台侧 `CDPEndpoint`，compose 用 host network + `host.docker.internal`；
- 磁盘配额 opt-in：`XALGORIX_PROJECT_DISK_ENFORCE=1` 把 `DiskMB` 写成 Docker `StorageOpt size`（仅覆盖可写层，scan 目录需外部 quota）；
- 流量代理可配置监听：`XALGORIX_TRAFFICPROXY_BIND` / `XALGORIX_TRAFFICPROXY_ADVERTISE`，可绑定项目网络可达地址。

构建与运行验证（2026-11-23）：

- `xalgorix:4.6.133-slim` 225MB，`xalgorix --version` → `v4.6.133`（用最新代码重建）；
- `xalgorix-runnerd:dev` 136MB，`runnerd --version` → `4.6.133`；
- `xalgorix-toolbox:dev-core` selftest 27/27 通过；
- `xalgorix-toolbox:dev-full` 带 `NET_RAW/NET_ADMIN/SYS_PTRACE` 的继承 profile selftest 49/49 通过；web 28/28（core + chromium）通过；
- runnerd 真实链路冒烟通过：`ensure project → exec（echo/toolshim/subfinder）→ remove`，并修复了容器内 `timeout --` 导致 Exec 返回 127 的缺陷；
- CDP relay 端到端通过：web 项目启动 Chromium 后，经 runnerd relay 的 `/json/version` 返回 Chrome 156 与可用 `webSocketDebuggerUrl`；Chromium 只监听容器回环，容器内 Python 转发器把 `0.0.0.0:9222` 桥到 `127.0.0.1:9223`；
- 构建默认使用 `GOPROXY=https://goproxy.cn,direct`、`KALI_MIRROR=http://mirrors.ustc.edu.cn/kali`，可在 `make` 命令行覆盖；
- 最终门禁：`go build ./...`、`go test ./...`、`go test -race ./internal/runtime ./internal/trafficproxy ./cmd/xalgorix`、`go vet`、`gofmt -l`、`make deploy-check`、四组 `docker compose config`、两个 workflow YAML 解析、`bash -n install.sh/docker-entrypoint.sh` 全部通过。

仍待下一轮：

- 一次完整容器化扫描（需要 LLM key 与授权目标；目前验证到项目容器 exec 层）；
- 镜像体积优化：Go module/build cache 残留在镜像层内，core 14GB、full 18GB；后续把 `go clean -modcache -cache` 并入安装层可显著缩小；
- scan 目录级磁盘 quota（`StorageOpt` 只覆盖容器可写层）；
- `debug` profile 文档收口（目前统一到 `full` + SYS_PTRACE 工具）。

### 7.1 必须补齐的缺口

| 优先级 | 缺口 | 影响 | 建议实现 |
|---|---|---|---|
| **P0** | 数据目录不是宿主同路径 bind mount（compose 用 named volume） | 容器挂到空目录，所有扫描产物不可见 | docker backend 必须把宿主机目录挂到**完全相同的绝对路径**，例如 `/srv/xalgorix/data:/srv/xalgorix/data` 并设 `XALGORIX_DATA_DIR=/srv/xalgorix/data`。`/srv/...:/data` 不满足同路径契约（除非实现宿主路径翻译），平台交给 daemon 的 `/data/...` 在宿主上不存在 |
| **P0** | 项目容器配额未接入：`MaxProjects=0` 时 driver 无 cap，`AcquireProjectSlot` 未被热路径调用 | 多项目并发线性吃满宿主内存 | `setupToolRuntime` 用 `resources.CurrentProjectBudget().MaxProjects` 兜底；`EnsureProject` 前统一 `AcquireProjectSlot`，`RemoveProject` 释放 |
| **P0** | 代理 `127.0.0.1:0` 不可被工具容器访问 | `proxy-only` 默认网络下所有工具无出口 | 增加 `XALGORIX_TOOLBOX_PROXY_URL`；代理监听可配置地址或 sidecar；要求代理时未配置即 fail-closed |
| **P0** | 镜像 digest 未强制校验 | 可被 tag 漂移/供应链替换 | 启动时解析 `@sha256:`；缺失则拒绝启动（或显式 `XALGORIX_ALLOW_UNPINNED_TOOLBOX=1` 仅开发） |
| **P1** | `web` profile CDP 在容器化平台不可达 | 浏览器工具在生产形态不可用 | runnerd 增加 CDP relay；或平台加入项目网络按容器名寻址；短期文档限定宿主平台 |
| **P1** | `XALGORIX_PROJECT_MAX_LIFE_SECONDS` 未实现 | 长扫描容器可能无限存活 | 在 reconcile/reap 中对比 `created_at`，超时置 `stopping` |
| **P1** | `DiskMB` 未落配额 | 单项目可写满宿主磁盘 | `StorageOpt size`（需存储驱动支持）或 scan 目录 quota；不支持时明确报「未强制」 |
| **P1** | 镜像 GC 过滤 `xalgorix/*` 与实际 `ghcr.io/.../xalgorix-toolbox` 不匹配 | GC 空转，旧镜像堆积 | 改为按 `LabelRole=toolbox` 或可配置 repo 前缀过滤 |
| **P2** | 共享 cache 挂载与只读 rootfs 下运行时安装 | 长尾工具无法在容器内 `go install/apt` | 明确「镜像外不安装」；如确需，cache 目录必须 rw 且 PATH 指向 cache bin |
| **P2** | `debug` profile 在文档出现但未实现 | 文档与实现漂移 | 要么实现 `debug`（SYS_PTRACE），要么从文档移除，统一到 `full` |
| **P2** | `capabilities` 只解析、不校验；平台也未按「工具→capability」做最小授权 | 清单里的能力声明是文档而非强制契约 | selftest 拒绝未知 capability；`capabilitiesForProfile` 只按净 profile 授予，不按工具声明扩散 |

### 7.2 实施顺序（每步独立可验收）

1. **P0 部署接缝**：同路径 bind mount + 代理可达 + digest 校验 + 项目配额。验收：compose 形态下起一个扫描，容器内能读到 scan 目录、能经代理出网、并发 N 个项目被 cap 住。
2. **P1 生命周期收口**：max life + disk 配额 + 镜像 GC 过滤。验收：超过 TTL 的项目被回收；镜像只保留 N 个且在用的不被删。
3. **P1 web profile**：CDP relay 或平台入网。验收：容器化平台下 `browser_action` 可导航并返回 snapshot。
4. **P2 洁净度**：cache/auto-install 策略、debug profile 文档收口。验收：镜像内无运行时安装依赖，文档与 `toolpack.yaml`/常量一致。

### 7.3 验收清单

**镜像**
- [ ] `core/full/web` 三 profile 均构建成功，CI selftest 全绿。
- [ ] 每个 manifest 工具 `bin` 存在、`smoke` 退出 0。
- [ ] 镜像无密钥、无 scan 状态、无 docker.sock、无 setcap。
- [ ] 发布为 digest，平台以 digest 启动。

**接口**
- [ ] `EnsureProject` 重复调用不重建；digest 变化才替换。
- [ ] `Exec` 非 0 退出返回 `ExecResult`，不触发熔断。
- [ ] 取消/scope 拒绝/sandbox 拒绝不计入熔断。
- [ ] 输出超限截断且 `archive_path` 可读。
- [ ] runnerd NDJSON 帧 `seq` 单调，`exit` 帧必达。

**并发与隔离**
- [ ] N 个并发扫描各自一个容器、各自网络，互不可见。
- [ ] 项目数达到上限时新项目得到 `E_PROJECT_LIMIT` 而非无限创建。
- [ ] 工具容器内看不到 docker.sock、看不到其他项目目录。
- [ ] 平台重启后 adopt 成功，孤儿被清，期望态收敛。

**资源与生命周期**
- [ ] 内存/CPU/PIDs/tmpfs 限额生效（容器内压测触发 OOM/限流）。
- [ ] 空闲 TTL 回收；max life 回收。
- [ ] 平台关闭时项目容器被停止。
- [ ] 代理要求但不可达时工具报错，不回落直连。

### 7.4 构建与部署变体（已落地）

部署拓扑是**构建/部署时选择**，不是 Go build tag：`platform-slim`、`platform-allinone`、`runnerd` 三个 Dockerfile target 共用同一个 `xalgorix` 二进制。

**选择器（Makefile）**

| 变量 | 取值 | 默认 | 作用 |
|---|---|---|---|
| `PLATFORM_VARIANT` | `slim` / `allinone` | `allinone` | `image-platform` 选择 Dockerfile target |
| `TOOLBOX_PROFILE` | `core` / `full` / `web` | `core` | `image-toolbox` / `selftest` / `toolbox-push` |
| `RUNTIME_MODE` | `native` / `docker` | `native` | `make run` 注入 `XALGORIX_RUNTIME_BACKEND` |
| `GOPROXY` | 任意 Go module proxy | `https://proxy.golang.org,direct` | 镜像内 `go mod download`；网络不可达官方源时改用可达镜像 |
| `KALI_MIRROR` | 任意 Kali apt 镜像 | `http://mirrors.ustc.edu.cn/kali` | 工具镜像的 apt 源；默认中科大，可用 `KALI_MIRROR=` 覆盖 |

| 目标 | 产物 |
|---|---|
| `make image-platform PLATFORM_VARIANT=slim\|allinone` | `$(PLATFORM_IMAGE)-<variant>`（默认 `xalgorix:<version>-<variant>`） |
| `make image-toolbox TOOLBOX_PROFILE=core\|full\|web` | `$(TOOLBOX_IMAGE)-<profile>`（默认 `xalgorix-toolbox:dev-<profile>`） |
| `make image-runnerd` | `$(RUNNERD_IMAGE)`（默认 `xalgorix-runnerd:dev`） |
| `make deploy-check` | 两个 compose 文件 × `allinone`/`split` 的 `config` 校验 + 默认形态选中服务的断言 |

非法取值在 make 解析期报错，`make -n` 同样拦截（不会先打印再执行 Docker）。

镜像构建默认走官方 Go module proxy；在无法访问 `proxy.golang.org` 的网络里追加
`GOPROXY=https://goproxy.cn,direct`（或企业内网代理），例如：

```bash
make image-platform PLATFORM_VARIANT=slim GOPROXY=https://goproxy.cn,direct
make image-toolbox TOOLBOX_PROFILE=core GOPROXY=https://goproxy.cn,direct
```

**镜像 tag 约定**

| 产物 | tag |
|---|---|
| 平台 all-in-one（`docker build .` 的默认目标） | `<repo>:<version>-allinone`，并继续占用 `latest` / `X.Y.Z` / `X.Y` / `edge` |
| 平台 slim | `<repo>:<version>-slim`、`<major>.<minor>-slim`、`slim`（发布）/ `edge-slim`（分支） |
| 工具 | `<toolbox-repo>:<version>-core\|full\|web`（发布）/ `<sha>-<profile>`（每次 main 构建） |
| runnerd | `<repo>:<tag>`（`make image-runnerd`，默认 `xalgorix-runnerd:dev`；本地构建，无 CI 发布） |

`latest` 与 semver 继续只指向 all-in-one，slim 不占用 `latest`；默认 `docker build` 仍是 all-in-one，老用户不受影响。

**Compose 形态**

- 根 `docker-compose.yml` 与 `deploy/docker-compose.yml`：all-in-one 服务**不挂 profile**，裸 `docker compose up -d` 行为不变；split 服务挂在 `profiles: ["split"]` 下。`allinone` 是隐式默认形态，`--profile allinone` 等价于默认。
- Compose 总会启用无 profile 服务，因此 split 必须**点名服务**启动，避免同时拉起 all-in-one：
  ```
  docker compose -f deploy/docker-compose.yml --profile split up -d app-split
  ```
- split 组成：slim 平台（`RUNTIME_BACKEND=docker`、`TRANSPORT=runnerd`、digest 固定的 `TOOLBOX_IMAGE`、`TOOLBOX_PROXY_URL`）+ runnerd（挂 `/var/run/docker.sock` 与 `/run/xalgorix`）+ 宿主同路径数据 bind mount（`XALGORIX_DATA_DIR`，默认 `/srv/xalgorix/data`）+ PG。
- 默认端口：all-in-one `9137`，split `9138`（`XALGORIX_SPLIT_PORT` 可改），避免误用 `--profile split up` 时端口冲突。

**CI**

- `.github/workflows/docker.yml`：矩阵 `variant: [allinone, slim] × arch`，构建时 `--target platform-<variant>`；digest 产物按 `variant + arch` 分离；merge 按 variant 出 manifest，tag 规则见上。
- `.github/workflows/toolbox.yml`：`core`/`full`/`web` 三 profile 构建 + selftest + SBOM；发布 tag 增加 `<version>-<profile>`。

**宿主机（无容器）平台**：`make build` 产出二进制，运行时设 `XALGORIX_RUNTIME_BACKEND=docker` 并接 runnerd/socket 即可，不需要新增镜像。

**运行态接线（已落地）**
- `main.go` 已接入 max-life 作业入参和 `XALGORIX_HOST_DATA_DIR` 同路径预检；配额在 driver 层按资源预算兜底。
- 代理可达性：`trafficproxy` 支持 `XALGORIX_TRAFFICPROXY_BIND` / `XALGORIX_TRAFFICPROXY_ADVERTISE`；split 形态仍需操作者选择项目网络可达地址并配合 `XALGORIX_TOOLBOX_PROXY_URL`，否则 docker backend fail-closed。
- `web` profile 的 CDP relay 已实现：runnerd 提供 TCP relay，平台通过 `host.docker.internal`（compose 已加 host-gateway 与 host network）访问。
- 仍未落地：scan 目录级磁盘 quota（`StorageOpt` 只覆盖容器可写层）、真实端到端扫描（需要 LLM 与目标）。

---

## 8. 配置速查

| 变量 | 默认 | 作用 |
|---|---|---|
| `XALGORIX_RUNTIME_BACKEND` | `native` | `native` / `docker` |
| `XALGORIX_RUNTIME_TRANSPORT` | `socket` | `runnerd`（推荐）/ `socket`（开发） |
| `XALGORIX_DOCKER_SOCKET` | `/var/run/docker.sock` | socket transport |
| `XALGORIX_RUNNERD_SOCKET` | `/run/xalgorix/runner.sock` | runnerd transport |
| `XALGORIX_HOST_DATA_DIR` | 空 | 平台在容器内时必填，且必须等于 `XALGORIX_DATA_DIR` |
| `XALGORIX_TOOLBOX_IMAGE` | 空 | 必须 digest 固定（强制，tag 被拒绝） |
| `XALGORIX_ALLOW_UNPINNED_TOOLBOX` | `0` | 仅开发：允许 tag 引用 |
| `XALGORIX_TOOLBOX_PROFILE` | `core` | `core` / `full` / `web` |
| `XALGORIX_TOOLBOX_BROWSER_PROXY` | 空 | Chromium 代理（项目网络可达） |
| `XALGORIX_TOOLBOX_PROXY_URL` | 空 | 通用工具代理（项目网络可达；要求代理时必填） |
| `XALGORIX_MAX_PROJECTS` | 0（RAM 推导） | 项目容器上限 |
| `XALGORIX_TOOLBOX_CONCURRENCY` | 4 | 单容器并发 exec |
| `XALGORIX_PROJECT_MEM_MB` | RAM 推导 | 每项目内存 |
| `XALGORIX_PROJECT_PIDS` | 512 | 每项目 PID 上限 |
| `XALGORIX_PROJECT_TMPFS_MB` | 512 | `/tmp` tmpfs |
| `XALGORIX_PROJECT_DISK_MB` | 4096 | 磁盘预算 |
| `XALGORIX_PROJECT_DISK_ENFORCE` | `0` | opt-in：写成 Docker `StorageOpt size`（仅可写层） |
| `XALGORIX_PROJECT_IDLE_TTL_SECONDS` | 600 | 空闲回收 |
| `XALGORIX_PROJECT_MAX_LIFE_SECONDS` | 21600 | 最长寿命 |
| `XALGORIX_RUNNERD_CDP_BIND` | `127.0.0.1:0` | runnerd CDP relay 监听地址 |
| `XALGORIX_RUNNERD_CDP_ADVERTISE` | 空 | relay 返回给平台的 host（容器化平台用 `host.docker.internal`） |
| `XALGORIX_TRAFFICPROXY_BIND` | `127.0.0.1:0` | 流量代理监听地址 |
| `XALGORIX_TRAFFICPROXY_ADVERTISE` | 空 | 代理 URL 的 host:port（bind 为 0.0.0.0 时必填） |
| `XALGORIX_TOOLBOX_KEEP_DIGESTS` | 3 | 镜像 GC 保留数 |
| `XALGORIX_TARGET_CONCURRENCY` | 1（上限 4） | 单扫描多目标并发；共享同一项目容器 |

---

## 9. 关键代码索引

| 主题 | 文件 |
|---|---|
| Driver / ExecSpec / 错误码 | `internal/runtime/contract.go` |
| 配置与工厂 | `internal/runtime/service.go` |
| Docker 实现（ensure/exec/资源/CDP/GC） | `internal/runtime/docker_driver.go` |
| Docker Engine API 最小客户端 | `internal/runtime/docker_client.go` |
| runnerd 客户端 + 服务端 handler | `internal/runtime/runnerd_client.go` |
| 期望态存储 | `internal/runtime/desiredstate.go`、`internal/runtime/postgres/desiredstate.go` |
| 同路径挂载 | `internal/runtime/mounts.go` |
| reconciler / 孤儿清理 | `internal/runtime/reconcile.go` |
| River 作业封装 | `internal/runtime/toolbox.go`、`internal/jobs/toolbox.go` |
| 工具路由 | `internal/tools/runtime_exec.go` |
| 容器内 shim | `cmd/toolshim/main.go` |
| 宿主侧 runtime 服务 | `cmd/runnerd/main.go` |
| 镜像与清单 | `tools/toolbox/Dockerfile`、`toolpack.yaml`、`versions.lock`、`install.sh` |
| 镜像健康门 | `tools/toolbox/selftest/main.go`、`.github/workflows/toolbox.yml` |
| 表结构 | `internal/infoitem/postgres/migrations/0015_toolbox_projects.sql` |
| 平台启动/关闭 | `cmd/xalgorix/main.go`（`setupToolRuntime`） |
| 面板投影 | `internal/web/handlers_runtime.go`、`internal/web/tools_catalog.go` |
