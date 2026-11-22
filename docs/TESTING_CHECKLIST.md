# Xalgorix Testing Checklist

This checklist tracks the automated and manual gates for the CLI, web UI, LLM providers, scan orchestration, and agent tools.

## Automated baseline

- [ ] `go test ./...`
- [ ] `go test ./... -cover`
- [ ] `go test ./... -race`
- [ ] `go vet ./...`
- [ ] `node --check internal/web/static/assets/app-*.js`
- [ ] `go build ./cmd/xalgorix`
- [ ] `make test-ci`

## Config and LLM providers

- [ ] Env file loading covers LLM, API base, API key, retry, dashboard auth, rate limit, proxy, telemetry, browser, and web search settings.
- [ ] Secret values are masked in logs and settings responses.
- [ ] Gemini API bases use `generateContent` with `x-goog-api-key`, including unprefixed custom model names.
- [ ] Anthropic API bases use `/v1/messages`, `x-api-key`, and `anthropic-version`, including unprefixed custom model names.
- [ ] OpenAI-compatible providers use `/v1/chat/completions` and bearer auth for OpenAI, DeepSeek, Groq, MiniMax, Ollama-compatible, and custom providers.
- [ ] Non-retryable errors return immediately: `400`, `401`, `403`, `404`, invalid auth, permission denied, and missing model.
- [ ] True rate limits are classified only from `429`, explicit rate-limit text, or `RESOURCE_EXHAUSTED`.
- [ ] Live provider smoke tests are opt-in and skipped unless provider keys are present.

## Web API, security, and persistence

- [ ] Auth status, login, session cookie flags, logout, and expired sessions.
- [ ] JWT session flow (API_CONTRACT §3.9): login mints a three-segment HS256 cookie; the random secret persists at `<dataDir>/auth/jwt.key` (0600) so sessions survive restarts; logout revokes the jti immediately; expired/tampered/wrong-secret tokens 401; `POST /api/auth/setup` works only while `needs_setup` (empty store + no env seed); env credentials seed an admin on first start.
- [ ] RBAC matrix: viewer GET 200 / any write 403; operator blocked (403) from writing settings/auth-profiles/mcp-servers/skills/agent-profiles and from `/api/users` entirely; admin unrestricted; machine Bearer tokens bypass RBAC and keep their route denylist; last enabled admin cannot be disabled/demoted/deleted (409); user list never returns password hashes.
- [ ] CSRF rejection for cross-site unsafe API requests; same-origin and non-browser API clients still work.
- [ ] Rate limiting enforces API limits and bypasses WebSocket/static routes.
- [ ] `/api/scan` rejects invalid method, invalid JSON, and empty target lists without starting a scan.
- [ ] Upload endpoints parse target lists and instruction files.
- [ ] Queue state saves, loads, reports status, clears, and handles corrupt/missing state.
- [ ] Scan listing, latest scan lookup, scan deletion, and instance rebuild from persisted `scan.json`.
- [ ] Instance detail, stop, restart, and event replay APIs.
- [ ] WebSocket origin checks, subscribe/unsubscribe, replay, and unauthorized access.
- [ ] Goal analysis (R-goal): `POST /api/projects/{id}/analyze-goal` returns 200 proposal (defaults applied: verify_by=verifier, required=true), 400 on empty goal, 404 unknown project, 405 on GET, 502 on LLM/parse failure, 503 without wiring; the analyzer prompt carries goal, instruction and project scope; round-0 at task run start persists the normalized objective on the task, adopts an existing structured objective without an LLM call, falls back to the raw goal on failure, no-ops under `XALGORIX_GOAL_ANALYSIS=off` and for legacy `/api/scan` requests; `goal_analyzed` event carries task/project/status in ResultMeta.

## Agent and tools

- [ ] Agent parser handles valid XML, malformed XML, multiple calls, hidden thought stripping, stuck detection, and prune alignment.
- [ ] Terminal blocks destructive commands, encoded destructive commands, known noisy scanners, and common obfuscation.
- [ ] Terminal classifies heavy tools, applies timeout tiers, parses missing commands, and maps known install packages.
- [ ] File editor creates, views, replaces, inserts, lists, and returns precise validation errors.
- [ ] Browser actions cover launch, goto, snapshot, input, tabs, iframes, cookies, screenshots, sessions, and cleanup.
- [ ] Proxy tool covers request methods, headers, bodies, timeout, Caido unavailable, and request listing.
- [ ] Reporting rejects weak evidence, deduplicates findings, preserves strong evidence, and generates reports.
- [ ] Python tool covers validation, stdout, stderr, timeout, exit code, and missing interpreter.
- [ ] Web search, CVE/exploit search, sub-agents, and skills cover configured, unconfigured, timeout, and failure paths.
- [ ] Goal finish gate (R-goal): finish refused with per-criterion reasons while a required engine-decidable criterion is unmet (unverified candidate counts as unmet); passes once a finding carries the verified tag; `verify_by=manual` and non-required criteria never block; capability/deterministic demands concrete exploitation proof; coverage uses the plan counters (completed >= planned); valve stands down after `MaxFinishRejections`; delegated specialists unaffected; no-objective scans byte-identical (`TestGoldenSchedulingParity` stays green). Plan-complete auto-finalize defers into bounded goal obligations (`goalObligationLimit=3`, idle tail resets per deferral) then finalizes honestly; a met goal finalizes exactly as before; obligation directive delivered once per distinct content via the default registry.

## Frontend and end-to-end

- [ ] Dashboard loads in authenticated and unauthenticated modes.
- [ ] Model fields allow custom IDs while showing current suggestions.
- [ ] Gemini suggestions include current text models such as `gemini-3.5-flash` and do not force deprecated models.
- [ ] DeepSeek suggestions include `deepseek-v4-pro` and `deepseek-v4-flash`.
- [ ] Start, stop, queue, resume, chat, report download, and instance detail flows work from the UI.
- [ ] Empty states, long target names, long logs, upload errors, WebSocket reconnect, and mobile layout render cleanly.
- [ ] Sequential run-row deletes keep the page clickable: run `scripts/e2e_delete_pointer_events.py` against a live instance and require green (guards the Radix dismissable-layer single-instance override in `webui/package.json` plus the menu-closes-before-dialog pattern in scans/findings/scan-detail; regression for the 2026-11-22 "删除运行历史后页面卡死" wedge where two dismissable-layer copies left `body { pointer-events: none }` after the first delete).
- [ ] Full e2e scans run only against local or explicitly authorized targets.
