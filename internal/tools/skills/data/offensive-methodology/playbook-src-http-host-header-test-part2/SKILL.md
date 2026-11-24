---
name: playbook-src-http-host-header-test-part2
description: "短表「Host 毒重置信」在 §2 下，用标题搜即可。有重置/激活口就打 Host / XFH（dig-scope §4.2.2），不必先看见邮件长什么样。没这类口，不要拿随机页空改 Host 来勾表。（第 2/2 部分）"
tags: [playbook, src-hunting, http, host, header]
intent: offensive
---
## 10. TRICK NOTES — WHAT AI MODELS MISS

1. **Password reset poisoning doesn't require the victim to be logged in** — you request the reset, the victim just clicks the link. The token lands on your server.
2. **X-Forwarded-Host is the #1 missed bypass**: Most Host validation checks `Host` header but frameworks silently prefer `X-Forwarded-Host` when behind a proxy.
3. **Double Host header is protocol-valid but behavior-undefined**: RFC says reject with 400, but almost no server actually does this. The mismatch between proxy and app is the vulnerability.
4. **Absolute URI overrides Host per RFC**: `GET http://evil.com/path HTTP/1.1\nHost: target.com` — the spec says use the request-line URI. But not all implementations agree.
5. **Cache poisoning via Host requires the cache to exclude Host from the key**: Most CDNs include Host in the cache key. But custom Varnish/Nginx caches may not. Also test with `X-Forwarded-Host` as cache key differentiator.
6. **Connection-state attacks are rarely tested**: Automated scanners don't test keep-alive behavior. Manual testing via Burp Repeater's connection reuse is essential.
7. **DNS rebinding + Host attacks**: If you control DNS, point your domain to the target's IP → your domain resolves to their server → Host header says your domain, but request hits their server. Useful for bypassing IP-based access controls.


> 术语：`dig-scope` 指 `playbook-src-rule-dig-scope-workflow`。

> （续前：playbook-src-http-host-header-test 第 2/2 部分）
