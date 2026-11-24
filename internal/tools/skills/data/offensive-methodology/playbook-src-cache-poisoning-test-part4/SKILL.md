---
name: playbook-src-cache-poisoning-test-part4
description: "结构：上半原有是主线（unkeyed / 欺骗）；下半补充 + 文末附件加深。先确认缓存键再打投毒。短表「缓存欺骗偷会话页」在原有「四、Web 缓存欺骗」下，用标题搜即可。（第 4/4 部分）"
tags: [playbook, src-hunting, cache, poisoning, test]
intent: offensive
---
## 9. TESTING CHECKLIST

```
□ Identify cache layer and CDN product
  - Check: X-Cache, cf-cache-status, Age, Via, X-Varnish, X-Served-By
□ Determine cache key composition
  - Test: adding query params, headers, cookies — does cache key change?
□ Discover unkeyed inputs
  - Headers: X-Forwarded-Host, X-Forwarded-Scheme, X-Original-URL, True-Client-IP
  - Parameters: utm_*, fbclid, gclid, callback, jsonp
  - Body: GET request with body parameters
□ Check reflection of unkeyed inputs
  - In HTML body, JavaScript, response headers, redirect Location
□ Verify caching of poisoned response
  - X-Cache: HIT on follow-up clean request
  - Response still contains poison → confirmed
□ Test parameter cloaking
  - Semicolon separator differences
  - Duplicate parameter handling
□ Check Vary header
  - Missing Vary: Cookie on personalized content?
  - Can influence Vary header value?
□ CDN-specific tests
  - ESI support?
  - Debug headers enabled?
  - Cache key reveal features?
□ Impact assessment
  - Stored XSS via cache poisoning?
  - Account takeover via session fixation?
  - DoS via broken resources?
```

> （续前：playbook-src-cache-poisoning-test 第 4/4 部分）
