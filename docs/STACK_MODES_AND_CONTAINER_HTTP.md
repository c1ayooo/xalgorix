# 三形态构建/部署与容器内 HTTP 执行面

> 状态：设计稿（2026-11-23）
> 关联：`docs/TOOLBOX_CONTAINER_PLAN.md`、`docs/ARCHITECTURE.md`、`AGENTS.md` 第 7.5 条
> 目标：把「平台/工具是否容器化」做成构建时可选的三形态，并把目标 HTTP I/O 从平台进程搬进项目容器；安全链、Verifier 判定和状态权威仍留在平台。

---

## 1. 三形态定义

`STACK_MODE` 是唯一选择器，取值：

| `STACK_MODE` | 平台 | 工具 | 是否需要 toolbox 镜像 | 是否需要 Docker |
|---|---|---|---|---|
| `native` | 宿主二进制 | 平台进程内执行 | **否** | 否（PG 可用宿主/外部） |
| `hybrid` | 宿主二进制 | 每扫描一个 toolbox 容器 | 是 | 是（仅工具容器） |
| `split` | slim 平台容器 | 每扫描一个 toolbox 容器 | 是 | 是（平台 + runnerd + 工具） |

**必须明确的一点**：`native` 是唯一“连工具也不构建镜像”的形态。`hybrid` / `split` 的工具在容器里，必须有 toolbox 镜像；镜像构建本身是 Docker 构建，这不是运行方式的选择。

三形态共享：

- 同一套 scope、sandbox、凭证剥离、代理、Verifier、报告闸门；
- 同一份 `scanctx` 状态权威（进程内 + PG）；
- 同一套 `Driver` 契约，只是 `native` 用 `NativeDriver`，`hybrid`/`split` 用容器 driver。

---

## 2. 构建/运行选择器

### 2.1 `STACK_MODE`

| `STACK_MODE` | 构建产物 | 运行命令 |
|---|---|---|
| `native` | `build/xalgorix` | `XALGORIX_RUNTIME_BACKEND=native ./build/xalgorix --web` |
| `hybrid` | `build/xalgorix` + `xalgorix-toolbox:<profile>` | `XALGORIX_RUNTIME_BACKEND=docker XALGORIX_TOOLBOX_IMAGE=... ./build/xalgorix --web` |
| `split` | `build/xalgorix` + `xalgorix:<version>-slim` + `xalgorix-toolbox:<profile>` + `xalgorix-runnerd:<version>` | `docker compose --profile split up -d` |

Makefile 目标：

```make
STACK_MODE ?= native
stack-plan:            # 打印该形态需要的产物、env、Docker 依赖
stack-build:           # 只构建该形态需要的产物
stack-run:             # native/hybrid 直接起宿主进程；split 打印 compose 命令
```

依赖关系：

- `native`：`stack-build: build`
- `hybrid`：`stack-build: build image-toolbox`
- `split`：`stack-build: build image-platform(prebuilt) image-toolbox image-runnerd`

### 2.2 平台构建：宿主编译，Docker 只打包

目标：平台二进制与前端由宿主 toolchain 产出，Docker 镜像只 `COPY` 产物；这样 `hybrid` / `split` 都不会在 Docker 内重复跑 npm/go。

- `make build`：`npm ci && npm run build` → `internal/web/static`；`go build` → `build/xalgorix`。
- 新增 Dockerfile stage `platform-slim-prebuilt`：
  ```dockerfile
  FROM debian:bookworm-slim AS platform-slim-prebuilt
  # 与 platform-slim 相同的 CA/时区/entrypoint，只把 COPY 来源换成宿主产物
  COPY build/xalgorix /usr/local/bin/xalgorix
  ```
- `PLATFORM_PACKAGING`：
  - `native`（默认本地）：`image-platform` 先 `build`，再用 `platform-slim-prebuilt`。
  - `docker`（CI/可复现）：保留现有 `platform-slim`，在 Docker 内编译 webui+Go。
- `allinone` 仍保留为兼容目标，但三形态默认不使用它。
- `.dockerignore` 必须允许 `build/xalgorix` 进入上下文。

### 2.3 工具构建

- `make image-toolbox TOOLBOX_PROFILE=core|full|web`：在 Docker 内安装工具。
- `native` 形态不调用该目标，也不要求任何 toolbox 镜像。
- `hybrid` / `split` 必须使用 digest 固定的 toolbox 镜像；开发可用 `XALGORIX_ALLOW_UNPINNED_TOOLBOX=1`。

两条不变量保证开发回环不会反复重建 14GB 镜像：

- **输入哈希门控**：`tools/toolbox/build-inputs.sh` 对 recipe（Dockerfile/.dockerignore/install.sh/versions.lock/toolpack.yaml）、helper 源码（`cmd/toolshim`、`cmd/httpworker`、`tools/toolbox/selftest`、go.mod/go.sum）、profile、apt 镜像和基础镜像 digest 做 sha256；`make image-toolbox` 把它写进镜像 label `xalgorix.build_inputs`，下一次读到同值就跳过 `docker build`。改 `internal/` 等平台代码不会触发重建（它们不进镜像）；`GOPROXY` 不算输入（版本由 `versions.lock` 钉死）。需要无条件重建时用 `TOOLBOX_FORCE=1`。
- **层序**：仓库内编译的 helper（gobuild 产物）只落在 profile 安装层**之上**（`core`/`full`/`web` 是 `*-tools` 之上的 final stage）。把 `COPY --from=gobuild` 放到安装层下面会让"改一行 helper"重跑 6GB 的 `install.sh core`。

---

## 3. 容器内 HTTP 执行面

### 3.1 为什么

当前 `http_request` / `send_request` 在平台进程内用 Go `net/http` 发请求。目标返回的恶意响应、TLS/解压/重定向处理都发生在平台进程。把 **HTTP I/O** 搬进项目容器可以：

- 让不可信目标响应先落在容器进程；
- 让所有目标 HTTP 统一经过项目网络 → 网关代理 → `trafficproxy`，保留归属、观测和 dial guard；
- 与 terminal/browser/portscan 一样受容器隔离。

### 3.2 范围

第一阶段只覆盖：

- `http_request`
- `send_request`

暂不覆盖（第二批）：

- `verify_*`、`probe_hypothesis`、`authz_matrix`、`ProbeTarget`
- `web_search` / `cve_search` / `exploit_search` / `oob_callback`
- `list_requests`（平台状态读取，不能搬）

Verifier 判定、闸门顺序、LLM 时序在第二批也不得改变；只把“发请求”这一层委托给容器。

### 3.3 架构：容器内 worker + 平台侧 RoundTripper

```
platform process
  httpclient / proxy tool
    └─ http.Client (redirects, CheckRedirect guard, timeout, cookies)
         └─ containerRoundTripper
              └─ unix socket  <scanDir>/.xalgorix/http-worker.sock
                   └─ httpworker (in project container)
                        └─ net/http single request
                             └─ HTTP_PROXY=http://<gateway>:8081
                                  └─ platform trafficproxy
```

关键决策：**重定向、Cookie、超时、`CheckRedirect` 仍由平台 `http.Client` 处理**。`containerRoundTripper` 只实现一次单跳请求：

- 平台 `http.Client` 继续调用 `CheckRedirect` + `GuardRedirects`，逐跳检查 scope；
- 301/302/303/307/308 的 method/body 重写、header 复制由标准库完成；
- worker 端 `CheckRedirect` 固定返回 `http.ErrUseLastResponse`，绝不自动跟随；
- 代理/CA 由容器环境提供，worker 不直连目标。

这样既把 I/O 搬进容器，又不复制一套重定向语义。

### 3.4 worker 协议

传输：unix socket 上的 HTTP/1.1，`POST /v1/request`，JSON。

请求：

```json
{
  "method": "GET",
  "url": "https://target/api",
  "headers": { "Authorization": ["Bearer ..."], "Accept": ["*/*"] },
  "body_base64": "",
  "timeout_ms": 30000,
  "max_body": 524288,
  "tls_skip_verify": false,
  "proxy_authorization": "Basic <run-token>:"
}
```

响应：

```json
{
  "status": 200,
  "headers": { "Content-Type": ["application/json"] },
  "body_base64": "....",
  "truncated": false,
  "duration_ms": 42,
  "error": ""
}
```

规则：

- `status` 为 0 且 `error` 非空 → 传输层失败（dial/TLS/timeout/cancel）；
- 任何 HTTP 响应（含 4xx/5xx/3xx）都是 `status` 非 0、`error` 为空；
- `max_body` 由平台按 `max_bytes`（默认 50KB，硬上限 512KB）传入；worker 读满即截断并置 `truncated`；
- `timeout_ms` 由平台按工具 timeout 传入；worker 同时受平台 `context` 取消；
- `body_base64` 避免二进制/非 UTF-8 损坏；
- headers 保留多值；
- `proxy_authorization` 由平台注入，worker 只在该字段非空时设置 `Proxy-Authorization`；token 不写日志。

### 3.5 生命周期

- 项目容器创建时，`DockerDriver.EnsureProject` 在容器启动后启动 worker：
  ```text
  /opt/xalgorix/bin/httpworker --socket <scanDir>/.xalgorix/http-worker.sock
  ```
  使用已有的 `createExec` + `startExecDetached`，与 Chromium 启动方式一致。
- 复用已存在容器时，`EnsureProject` 先探测 socket（`GET /v1/health`）；不通则重启 worker。
- worker 随容器删除而消失；socket 文件由容器写入 scan 目录，平台在校验失败后清理。
- worker 是内部 helper，不进入 `toolpack.yaml` 的 50 项安全工具清单；但必须打进 toolbox 镜像（`/opt/xalgorix/bin/httpworker`）。
- `native` 形态不启动 worker，`http_request` 走现有 native 路径。

### 3.6 平台侧路由

`httpclient.buildClient` 与 `proxy.httpClient`：

- 若 `tools.ContainerActive()`：
  - 构造 `containerRoundTripper{scanCtx}`；
  - 不再设置 `tr.Proxy` / `GuardDials`（容器侧走网关代理，代理侧做 dial guard）；
  - 继续使用 `http.Client` 的 `Timeout`、`CheckRedirect`、`GuardRedirects`；
  - `TrafficProxy` 的 attribution 由代理 URL userinfo / `Proxy-Authorization` 承担。
- 否则：现有 native 分支逐字节保留。

`containerRoundTripper.RoundTrip`：

1. 读取 `req.Body`（有上限），编码 base64；
2. 复制 headers（多值）；
3. POST 到 worker unix socket；
4. 解码响应，构造 `http.Response`（StatusCode、Header、Body、ContentLength=-1）；
5. worker 返回 `error` → `RoundTrip` 返回 error；`http.Client` 按现有语义包装。

代理归属：

- 每个 run 在平台的流量代理里有一个**专用监听端口**（`trafficproxy.ListenForScan`）；容器 spec 只携带端口（`ProxyGatewayPort`），不携带 token。driver 把 `HTTP_PROXY` / `HTTPS_PROXY` 设为 `http://<项目网络网关>:<该 run 端口>`，代理按「端口 → run」归属，因此 terminal / Python / 容器内 Chromium 都无需 token 即可被观测与归档。
- `httpworker` 的单跳请求额外带 `proxy_authorization`（Bearer），与端口归属一致；token 不落日志、不进 `ProjectSpec`。
- 平台重启/resume 后端口会重新分配：driver 在复用旧容器时比较其 `HTTP_PROXY` 端口，不一致则重建容器，避免指向已关闭的端口。

### 3.7 安全链

| 环节 | 位置 | 变化 |
|---|---|---|
| scope / local-host 拦截 | 平台 | 不变；每个 redirect hop 继续 `CheckRedirect` |
| sandbox / 路径 | 平台 | 不变（HTTP 不涉及文件） |
| 凭证剥离 | 平台 | 不变 |
| 代理 fail-closed | 平台 + 容器 | 容器 `proxy-only` 网络，只有网关代理可达 |
| dial guard / DNS rebinding | 平台 `trafficproxy` | 容器所有 HTTP 经代理；代理侧 dial guard 生效 |
| 归属 token | 平台 → 容器代理 URL | 保留 per-scan 归属 |
| Verifier 判定 | 平台 | 第一阶段不动 |
| 报告闸门 | 平台 | 不动 |

### 3.8 失败语义

| 场景 | 行为 |
|---|---|
| worker socket 不存在 | 平台尝试启动一次；仍失败 → `E_RUNTIME_UNAVAILABLE`，不回落 native |
| worker 返回传输错误 | `RoundTrip` error；工具层返回 `Result{Error}`，不伪造 HTTP 响应 |
| 目标返回 4xx/5xx | 正常 HTTP 响应，不报错 |
| 3xx | 平台 `http.Client` 按 `follow_redirects` 处理；guard 拒绝时返回 3xx 响应并附提示 |
| body 超限 | worker 截断 + `truncated`；平台按 `max_bytes` 再截断 |
| 容器重建 | worker 随容器重启；平台 `EnsureHTTPWorker` 探测/重启 |
| `native` 形态 | 不进入 worker 分支 |

### 3.9 性能

- 常驻 worker，unix socket；每个 HTTP 请求一次本地 socket round-trip，无 Docker exec 开销。
- worker 并发处理连接；平台侧每 scan 一个 client，连接可复用（HTTP keep-alive over unix socket）。
- 大响应仍然只回传上限字节；完整响应不落盘（与 native 行为一致）。

### 3.10 测试

单元：

- `httpworker`：单跳、3xx 不跟随、header 多值、body base64、超时、body 截断、proxy auth、TLS skip verify。
- `containerRoundTripper`：用假 worker（unix socket + httptest handler）验证请求/响应映射、错误映射、body 上限。
- `httpclient`：容器分支与 native 分支的 redirect/guard 行为一致性；`follow_redirects=false`；max_bytes。
- `proxy`（send_request）：同上。

集成：

- 项目容器 + 真实 `httpworker` + 内部网络 + 平台代理：GET 公网/内网目标 200；3xx 不自动跟随；scope 拒绝的 redirect 不跟随。
- `native` 形态全回归，确认没有走 worker。

---

## 4. 配置矩阵

| 变量 | native | hybrid | split |
|---|---|---|---|
| `STACK_MODE` | `native` | `hybrid` | `split` |
| `XALGORIX_RUNTIME_BACKEND` | `native` | `docker` | `docker` |
| `XALGORIX_RUNTIME_TRANSPORT` | — | `socket`/`runnerd` | `runnerd` |
| `XALGORIX_TOOLBOX_IMAGE` | — | 必填 digest | 必填 digest |
| `XALGORIX_TOOLBOX_PROFILE` | — | `core`/`full`/`web` | `core`/`full`/`web` |
| `XALGORIX_HOST_DATA_DIR` | — | 宿主二进制不需要 | 必填，且等于 `XALGORIX_DATA_DIR` |
| `XALGORIX_TRAFFICPROXY_BIND` | 可选 | 建议 `0.0.0.0:<port>` | 建议 `0.0.0.0:<port>` |
| `XALGORIX_TOOLBOX_PROXY_URL` | — | 可选；为空时走网关代理 | 可选；为空时走网关代理 |
| `XALGORIX_DATABASE_URL` | 必填（宿主/外部） | 必填 | 必填 |

---

## 5. 实施顺序

1. **P0**：`STACK_MODE` + `stack-plan` / `stack-build` / `stack-run`；`PLATFORM_PACKAGING=native` 的 `platform-slim-prebuilt`。
2. **P1**：`cmd/httpworker` + Dockerfile 集成 + worker 协议/health。
3. **P1**：`containerRoundTripper` + `httpclient`/`proxy` 容器分支；`EnsureProject` 启动/探测 worker。
4. **P1**：单元测试 + 容器集成测试；`native` 回归。
5. **P2**：`verify_*` / `probe_hypothesis` / `authz_matrix` 的 I/O 复用 worker，判定不动。
6. **P2**：外部查询工具（web_search/CVE/OOB）按需搬容器。

## 6. 验收清单

- [ ] `STACK_MODE=native` 只产出宿主二进制，不构建 toolbox 镜像，运行 `RUNTIME_BACKEND=native`。
- [ ] `STACK_MODE=hybrid` 产出宿主二进制 + toolbox 镜像，运行 docker backend。
- [ ] `STACK_MODE=split` 产出 slim 平台 + toolbox + runnerd，compose split 可起。
- [ ] `PLATFORM_PACKAGING=native` 的 slim 镜像不在 Docker 内跑 npm/go。
- [ ] `hybrid`/`split` 下 `http_request`/`send_request` 的 I/O 经项目容器 worker 和网关代理。
- [ ] 3xx 不自动跟随；每个 redirect hop 仍过平台 guard。
- [ ] worker 不可用时 fail-closed，不回落 native。
- [ ] `native` 形态现有行为逐字节不变。
- [ ] Verifier 判定、报告闸门、ledger/plan/notes 仍在平台。
- [ ] 单测、race、compose config、三形态冒烟通过。

---

## 7. 实施状态（2026-11-23）

### 已完成

- `STACK_MODE=native|hybrid|split`：`make stack-plan` / `stack-build` / `stack-run` 已可用。
- `PLATFORM_PACKAGING=native|docker`：`platform-slim-prebuilt` stage 已加入 Dockerfile；`make image-platform PLATFORM_VARIANT=slim` 默认宿主编译 + Docker 只 COPY，已验证镜像 `xalgorix v4.6.133`。
- `allinone` 仍自动使用 Docker 内编译（没有预构建变体）。
- `cmd/httpworker`：单跳 HTTP worker，unix socket，health/request 协议，3xx 不跟随，body 上限，proxy/CA/TLS 支持。
- 平台侧 `containerRoundTripper`：`http_request` / `send_request` 在 `ContainerActive()` 时把 I/O 交给 worker；重定向、guard、超时、响应格式化仍在平台。
- `DockerDriver.EnsureProject` 自动启动/探测 worker；`HTTPWorkerSocket` 进入 `ProjectSpec`，runnerd 路径同样生效。
- worker socket 权限修正：目录 0755、socket 0666，宿主平台进程可连接。
- 已通过：
  - `cmd/httpworker` 单测（单跳、3xx、截断、传输错误）；
  - `internal/tools` 单测（worker 响应映射、错误映射）；
  - `internal/runtime` Docker 集成测试（driver 创建容器并自动启动 worker，`XALGORIX_HTTP_WORKER_INTEGRATION=1`）；
  - 容器内 worker 对 `10.0.100.58` 真实请求返回 200；
  - 本功能涉及的包 `go test` 全绿。

### 尚未完成

- 平台进程当前被外部重启为 **native**，所以新的容器 HTTP 路由尚未在运行中的平台生效；需要以 `STACK_MODE=hybrid`（或 `split`）重启。
- `toolbox` 的 core 镜像已包含 `httpworker`；full/web 镜像尚未重建，暂不能使用容器 HTTP worker。
- `verify_*`、`probe_hypothesis`、`authz_matrix`、`ProbeTarget` 仍走 native；第二批再搬。
- 全仓 `go build ./...` / `go test ./...` 当前被另一会话正在修改的 `internal/web`、`internal/agent`、`internal/infoitem/postgres/migrations` 阻断，与本功能无关。
- 本次改动尚未提交 git。

### 激活混合形态

```bash
make build                    # 宿主编译平台二进制
make image-toolbox TOOLBOX_PROFILE=core GOPROXY=https://goproxy.cn,direct
XALGORIX_RUNTIME_BACKEND=docker \
XALGORIX_RUNTIME_TRANSPORT=socket \
XALGORIX_TOOLBOX_IMAGE=xalgorix-toolbox:dev-core \
XALGORIX_TOOLBOX_PROFILE=core \
XALGORIX_ALLOW_UNPINNED_TOOLBOX=1 \
XALGORIX_TRAFFICPROXY=1 \
XALGORIX_TRAFFICPROXY_BIND=0.0.0.0:8081 \
XALGORIX_TRAFFICPROXY_ADVERTISE=127.0.0.1:8081 \
./build/xalgorix --web
```
