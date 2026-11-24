---
name: playbook-src-open-redirect-test-part2
description: "open-redirect（第 2/2 部分）"
tags: [playbook, src-hunting, open, redirect, test]
intent: offensive
---
### Combined URL parser differential table

| Payload | Validator Sees | Browser Navigates To |
|---------|---------------|---------------------|
| `//evil.com` | Relative path | `https://evil.com` |
| `\/\/evil.com` | Path `\/\/evil.com` | `https://evil.com` |
| `//evil.com\@target.com` | Contains `target.com` | `https://evil.com` |
| `//target.com@evil.com` | Starts with `target.com` | `https://evil.com` |
| `/%0d%0aLocation: https://evil.com` | Path string | Header injection → redirect |
| `//evil%252ecom` | `evil%2ecom` (not a domain) | `evil.com` (after double decode) |

> （续前：playbook-src-open-redirect-test 第 2/2 部分）
