# Xalgorix Architecture

Xalgorix is a single Go binary that runs an autonomous, LLM-driven web/API
penetration test against an authorized target and produces an evidence-backed
report. This document describes the code as it is today; it is the source of
truth for how the packages fit together.

## High-level layers

```
Runtime            cmd/xalgorix        binary entrypoint + OS service wiring
   │  dispatches (web mode / scan mode)
   ▼
Web Platform       internal/web        local HTTP API + embedded React dashboard,
   │                                   scan orchestration, persistence, notifications
   ▼
Agent Core         internal/agent      the autonomous loop: reason → call tool →
   │                                   observe → repeat, under scope/phase guards
   ▼
Tooling            internal/tools      the tool registry + every capability the
   │                                   agent can invoke (shell, browser, http, …)
   ▼
Providers / LLM    internal/llm,       model routing, API-key store, provider
                   internal/providers  catalog, auth/credential drivers
```

Cross-cutting: `internal/scanctx` (per-scan isolation), `internal/reporting`
(findings store + PDF), `internal/storage` (atomic disk writes),
`internal/sandbox` + `internal/scopeguard` (safety), `internal/config`
(configuration).

## Runtime — `cmd/xalgorix`

- `main.go` — parses flags/config and dispatches: **web mode** starts the
  dashboard + API server (`internal/web`); **scan mode** (and CLI subcommands
  like `--start`, `--restart`) drive the engine directly. Holds the compiled-in
  `version` string. Default dashboard bind is `127.0.0.1:9137`.
- `exec_unix.go` / `exec_windows.go` — OS-specific process/service helpers
  (systemd integration on Linux with a background fallback, etc.).

## Web Platform — `internal/web`

The local server exposes a REST API + WebSocket telemetry and serves the React
dashboard, which is compiled from `webui/` and embedded into the binary via
`//go:embed static/*`. It also owns scan orchestration and persistence.

After the v4.5.44 decomposition this package is split into cohesive files
(behavior-preserving; `server.go` is now a thin core + routing hub):

| File | Responsibility |
| --- | --- |
| `server.go` | `Server` struct, `NewServer`/`Start`, lifecycle, small handlers |
| `handlers_router.go` | route table / mux wiring |
| `auth_session.go` | dashboard auth: sessions, login backoff, CSRF, middleware |
| `ws_hub.go` | WebSocket client pumps + event broadcast |
| `orchestrator.go` | scan orchestration: single / DAST / wildcard / multi + subdomain collection |
| `scan_session.go` | run one scan session + event processing + phase inference |
| `scan_audit.go` | read-only domain audit projection and cold-path sync state APIs |
| `queue_state.go` | scan-queue persistence, resume, admission control |
| `scan_record.go` / `scan_query.go` / `scan_list.go` | scan-record assembly, lookup, listing/caching |
| `scan_audit.go` | read-only domain audit projection + cold-path sync views (`/api/scans/{id}/audit`, `/api/scans/{id}/coldpath`) |
| `notify.go` | Discord + Telegram notifications (severity-gated) |
| `chat.go` | in-scan / post-scan chat with the agent |
| `schedules.go` / `scheduler.go` | scheduled scans |
| `handlers_platform.go` | projects/tasks/runs REST API (`/api/projects`, `/api/tasks`), run mounting, project-scope target validation, §3.2 `project_id` filters |
| `handlers_approvals.go` | approvals + guidance REST (`/api/approvals`, `/api/tasks/{id}/guidance`); decisions are session/CSRF-guarded state changes |
| `approvals_gate.go` | `ApprovalService`: per-run slot queue, timeout=reject timers, startup reconciliation, WS emission, payload redaction |
| `platform_legacy_import.go` | startup import of existing scans → Run mounts + schedules → tasks under the `default` project (idempotent, read-only) |
| `uploads.go` | target/instruction/logo/context uploads |
| `report.go` | report delivery API |
| `settings_env.go`, `handlers_profiles.go` | settings, provider keys, credential profiles, OAuth |
| `retention.go`, `data_dirs.go`, `legacy_import.go` | data-dir management + retention |

Concurrency: the `Server` owns the running scan instances and several
independently-locked caches. Each scan runs in its own goroutine and its own
`scanctx` context so concurrent scans never cross-wire.

## Agent Core — `internal/agent`

The autonomous loop. It builds a system prompt encoding the 22-phase
methodology, sends the conversation to the configured LLM, executes the tool
calls the model requests, feeds results back, and repeats until the objective
is met or a budget (iterations / duration / tokens) is hit.

| File | Responsibility |
| --- | --- |
| `agent.go` | the loop, tool dispatch, watchdog, lifecycle (`Run`/`Stop`) |
| `agent_guard.go` | scope guard, phase-restriction guard, passive-recon guard, host/URL extraction |
| `agent_prompt.go` | system-prompt / closing-instruction / whitebox-guidance construction |
| `agent_messages.go` | tool-result formatting + conversation pruning/compaction |
| `agent_ratepolicy.go` | request-rate policy parsing (from the instruction) |
| `hooks.go` | loop hooks incl. stuck/loop detection (repeated identical calls) |
| `verifier.go` | the independent Verifier agent that re-tests candidate findings |

Guards keep the agent inside the authorized scope and the selected phases, and
throttle to the requested request-rate policy. These are the safety boundaries
that make an autonomous offensive agent safe to point at a target.

## Multi-agent execution — `internal/tools/agentsgraph`

A full assessment uses a coordinator plus up to three hypothesis-driven
specialists. After reconnaissance, the coordinator divides the observed surface
into non-overlapping roles (typically authorization/business logic,
injection/server-side behavior, and source/data-flow or client/API analysis),
continues its own work, and collects every child result before finishing.

Every root scan owns one `agentsgraph.Graph`: its runner, worker semaphore,
cancellation context, status, partial evidence, and final results are all
scan-scoped. Descendants share that graph but cannot replace or stop it. This
means concurrent scans cannot cross-wire agents or clear one another during
cleanup. `spawn_agent`, `check_agent`, and `wait_agent` are always registered;
the finish gate blocks while children are running or their results remain
uncollected. Stopping a root cancels its descendants and cleanup waits briefly
for them to unwind before persisting findings and deleting tool stores. The
graph also owns one shared resource ledger: duration begins at the root, token
usage is aggregated across LLM clients, and agent iterations/tool calls are
atomically reserved against the scan-wide caps.

## Hypothesis/evidence ledger — `internal/scanctx` (`ledger.go`)

The ledger is the scan's durable "global exploitation context": a typed graph of
attack hypotheses and the evidence gathered for each. It lives on the shared
`ScanContext` (so the coordinator and every specialist read/write the same
graph), is deduplicated by normalized class/endpoint/parameter/role, is
memory-bounded, and persists atomically to `<scanDir>/ledger.json` so it
survives restart/resume. A hypothesis carries identity, preconditions, a
baseline/control, confidence, a lifecycle status (`queued`, `testing`, `proven`,
`rejected`, `blocked`, `exhausted`), the owning specialist, a next-best action,
and append-only evidence; confirmed findings are referenced by their reporting
ID rather than duplicated.

The ledger drives orchestration rather than sitting beside it:

- Once recon produces a plan, `hookLedgerSeed` seeds a hypothesis per candidate
  vulnerability class so the graph is populated deterministically.
- Delegation is ledger-driven: the coordinator assigns disjoint work to three
  deterministic specialist profiles (authorization/business-logic,
  injection/server-side, client/source), each with an explicit evidence
  contract (baseline + concrete proof; out-of-band callbacks for blind classes;
  browser-confirmed execution for XSS/DOM) and a stopping rule.
- Agents record and consult the graph with `record_hypothesis`,
  `add_hypothesis_evidence`, `update_hypothesis`, and `read_ledger`.
- A precision finish-gate refuses to complete while a hypothesis is marked
  proven but has no linked finding (bounded so it cannot deadlock), enforcing
  verify-by-execution and precision over volume.

## Deep testing — authorization matrix & XSS verification

Two capabilities give the specialists deterministic, evidence-producing tools
for the vulnerability classes that are hardest to get right:

- **`authz_matrix`** (`internal/agent/authz_matrix.go`) replays one request as
  each configured identity — primary session (role A), a second account
  (role B), and anonymous — using the exported `httpclient.SendRaw` primitive,
  and compares the outcomes. A lower-privileged identity receiving the same
  successful response as the authorized one is broken access control (IDOR/BOLA
  horizontally, auth bypass/BFLA vertically). It enforces scope
  (`scopeguard.IsLocalOrListener`) and the per-scan request-rate policy, and
  records role-scoped ledger hypotheses (keyed by the ledger's `Role` dimension)
  with the differential as evidence.
- **`browser_action command=verify_xss`** (`internal/tools/browser`) turns the
  headless browser's JavaScript dialogs into captured execution signals
  (`execsignals.go`). The action navigates a payload that raises a dialog
  carrying a unique nonce and confirms XSS only when that exact dialog fires —
  execution proof, not reflection — recording browser-confirmed XSS evidence in
  the ledger.

Both tools feed the shared ledger, so their results flow through the same
scheduling, verification, and precision finish-gate as everything else.

## Tooling — `internal/tools`

`registry.go` is the tool surface presented to the model. Each subpackage is one
capability:

- `terminal` — sandboxed shell execution (the workhorse for CLI security tools)
- `browser` + `pageagent` — headless browser automation (JS-heavy apps, auth flows)
- `httpclient` — direct HTTP requests
- `websearch`, `codesearch`, `fileedit`, `notes`, `python` — research / analysis helpers
- `search_info` (registered by `internal/agent`) — read-only region/association/
  context activation over the optional InfoItem cold path; no verdict role
- `reporting` — the `report_vulnerability` tool + findings store (see below)
- `agentmail`, `agentsgraph` — email/OOB interaction and sub-agent graphs
- `proxy`, `oob`, `finish`, `iolimit`, `skills` — proxying, out-of-band callbacks, termination, IO limits, methodology corpus

## Findings & verification — `internal/tools/reporting` + `internal/reporting`

Every reported vulnerability passes gates (valid method, mandatory
exploitation proof, false-positive and claim-consistency checks, dedup,
severity/CVSS normalization) and is handed to the reporting agent's independent
Verifier. The verifier callback is registry-local, so parallel hunters use
their own idle LLM client while blocked on their own report rather than
cross-wiring a sibling's client. Each
finding carries exactly one verification tag:

- `verified` — the Verifier independently reproduced it.
- `exploit-proven` — the Verifier was inconclusive/absent, but the finding's
  own proof shows a concrete exploitation outcome (command output, extracted
  data, OOB callback, …). Proven, not a guess.
- `needs-manual-verification` — preserved but not yet concretely proven.

`internal/reporting` renders the branded PDF from the persisted findings.

## Information model, domain audit & cold path

The CORE v2.1 information model is implemented as additive layers; it does not
replace scan state or the report/verifier authority chain.

- `internal/infoitem` — InfoItem model (seven coordinate dimensions, stable ID,
  observed/inferred nature, supports, relations) and the bounded
  region/association/context `Reader` contract.
- `internal/domainjournal` — per-scan `domain_journal.jsonl` with monotonic
  `seq`, fsync on append, trailing-line repair, duplicate/gap/disorder
  projection, and non-blocking append observers used for cold-path wakeups.
- `internal/storage.AppendSync` — the shared append+fsync primitive used by the
  domain journal.
- `internal/infoitem/postgres` — optional PostgreSQL replica for InfoItems,
  relation edges, audit records and contiguous sync watermarks. It implements
  the three activation queries and never participates in verdicts.
- `internal/jobs` — pinned River v0.51.2 client/workers. Jobs only carry bounded
  cold-path journal ranges; they do not own scan lifecycle, HITL, agentsgraph,
  or WebSocket work.
- `internal/coldpath` — composes PostgreSQL, River, the low-latency journal
  observer, and a periodic reconciler that compares local journals with the
  applied watermark and re-enqueues missing ranges.
- `scanctx.Ledger` projects ledger Evidence into the journal (and therefore the
  cold path) using `evidence:<scan_id>:<evidence_id>`. Evidence retains its
  existing `Kind` role classification and has a separate `Nature`
  (`observed`/`inferred`). Missing/legacy values normalize to `inferred`.
- `scanctx` also records hypothesis mutations (record/claim/assign/status/
  evidence/attempt/VOI/reset/merge) with an explicit actor before committing.
- `search_info` is the Agent-facing read-only activation consumer. It is
  registered in every agent, returns an explanatory result when no DSN is
  configured, and never feeds finding acceptance.
- Two engine-side M4 consumers annotate the ledger tools
  (`internal/agent/ledger_cross_scan.go`): region =
  `read_ledger(filter=schedulable)` per-entry same-endpoint cross-scan note,
  association = `claim_next_hypothesis` same-subject cross-scan section plus a
  current-scan edge traversal rooted at this scan's items (relation edges are
  per-scan; cross-scan edge graphs are charter §13 research). Both are
  output-only, capped by a per-call query budget, and degrade to unchanged
  output without a cold-path reader; reader errors and §6.4 sync gaps surface
  as visible notes (never silent). Contract tests pin the shapes, degradation,
  budget and gap passthrough in `internal/agent/ledger_cross_scan_test.go`,
  and real-store bridges in `ledger_cross_scan_pg_test.go`. The cross-scan
  region query uses `RegionQuery.AllScans` (mutually exclusive with ScanIDs).
- Two read-only HTTP views expose the audit surfaces (charter §5.2/§6.4 and
  the §4.3 human audit view): `GET /api/scans/{id}/audit` serves the domain
  audit projection (claim history, duplicate/gap/disorder detection, explicit
  `incomplete`), and `GET /api/scans/{id}/coldpath` reports the per-scan sync
  state (`pending`/`applied`/`gap`/`failed`, or `disabled` without a DSN —
  never `failed`). Both live in `web/scan_audit.go`; the projection never
  mutates ledger state and never participates in verdicts.

Hot-path mutations remain in memory + `ledger.json`; the journal is the durable
domain audit. PostgreSQL is a readable replica and can be absent. When present,
`XALGORIX_DATABASE_URL` enables startup migrations, River, and reconciliation;
PostgreSQL errors disable the cold path and never gate a scan.

The InfoItem `Reader` is process-global because it is infrastructure, not
per-scan state. Every query is still scoped by the caller's current `scan_id`.

## Platform layer — projects, tasks & runs (`internal/platform`)

The Project → Task → Run model (REDESIGN D6/D7) is a cold-path system-of-record
for *organization*, never a second scan-state authority.

- `internal/platform` — `Project`/`Task`/`Run` models, a `Repository` interface
  and two implementations: PostgreSQL (`pgxpool` opened from
  `XALGORIX_DATABASE_URL`) and a file fallback (`<dataDir>/_platform/platform.json`,
  full snapshot via `storage.WriteAtomic`, registered in
  `reservedTopLevelNames`). Every list/filter query takes an explicit
  `projectID` parameter (D7 row-level isolation; raw SQL bypasses do not exist
  — platform SQL lives only in `platform/postgres.go` and
  `platform/postgres_findings.go`). Migrations are shared with the InfoItem
  migrator (`infoitem/postgres/migrations/0002_...` and `0004_...`; the
  `migrate.go` engine is untouched). Explicit `project_id` with no wired store
  fails closed with 503 instead of returning an unfiltered cross-project view.
- **Runs are mount records, not state.** A run row stores only `task_id`,
  `instance_id` (unique), `legacy_scan_id`, `mode`, `trigger`, `created_at`.
  Status/started/finished/vuln/token numbers are projected at read time from
  `findAllScanSummaries` keyed by `instance_id` (fallback
  `legacy_scan_id == rec.ID`) — `scan.json` remains the single authority
  (charter §6). Nothing status-like is persisted in either store.
- **Degradation** (charter §6): no DSN → file store, platform API fully
  usable, implicit `default` project (fixed id, not deletable). DSN set but
  PostgreSQL unreachable → store stays nil, platform endpoints answer 503
  (`requirePlatformStore`), scanning is unaffected — assembly failure is
  log-only in `cmd/xalgorix/main.go`, mirroring the `coldpath` posture.
- `POST /api/tasks/{id}/runs` reuses the exact `/api/scan` dispatch sequence
  (reserve → queue ownership → pending-dispatch snapshot → durable queue
  state → `runMultiScan`) plus the same pre-dispatch guards
  (`checkScanRequestDestinations`, `resolveScanCredentials` fail-fast, blocked
  target rejection). Targets must fall inside the project scope (exact,
  domain-suffix or explicit `*.domain` match; `allow_ports` covers explicit and
  HTTP/HTTPS default ports; scope entries carrying an explicit `:port` are
  rejected at create/patch — port control lives in `allow_ports`).
- Task `context_paths` are persistent Context attachments: every path (plus an
  explicit request `scan_context`) is passed to the Run and merged by
  `attacksurface.LoadFromPaths`; the single-path Agent setter remains a
  compatibility wrapper.
- Legacy import (startup, idempotent by either `legacy_scan_id` or
  `instance_id` / `context.legacy_schedule_id`, partial unique expression
  indexes guard concurrent first starts): existing scans mount as runs under an
  umbrella "imported" task in the `default` project; existing schedules become
  tasks with a `schedule` property (legacy-only fields recorded in task
  `context`, which never reaches the wire). Original scan directories are never
  modified — pinned by a whole-tree content+mtime hash test.
- **Task findings** are a cold-path projection of already-accepted scan.json
  findings. With a DSN, `run_findings` stores one row per `(run_id, dedup_key)`
  and task reads use `DISTINCT ON` (newest run wins); without one, the web
  layer pages through every run and applies the same deterministic dedup.
  Projection writes happen after scan finalization with startup backfill; they
  never participate in acceptance or verification.
- A mount whose `scan.json` disappeared without a durable pending-dispatch
  snapshot projects as `missing`, not `pending`, so it neither counts as an
  active run nor blocks project deletion. Retention and manual scan deletion
  remove the corresponding mount (findings cascade).
- Existing endpoints accept an optional `project_id` query parameter
  (`/api/instances`, `/api/findings`, `/api/findings/summary`, `/api/scans`);
  without the parameter behavior is byte-identical to before.
- **Transitional fact**: scans triggered by the legacy scheduler (including
  imported schedule tasks) still dispatch through the original
  scheduler→`runMultiScan` path and do not pass project-scope validation;
  that wiring closes in R2.

## HITL approvals & guidance — `internal/approval`, `internal/web/approvals_gate.go`

- `internal/approval` — pure classifiers: tool category (`command` /
  `http` / none), the read-only recon allowlist ("best-effort classifier;
  when in doubt it pends" — segments with `$()`, unquoted redirects,
  `system(`/`eval(`, inline `PATH=`/`LD_PRELOAD=`-style assignment, or nmap
  output flags are never allowlisted; awk/sed/sort are not in the default
  list) and `AssessHTTP` (write methods with admin-path / destructive-payload
  / auth-mutation signatures pend; GET/HEAD/OPTIONS pass with an audit trail
  in the existing tool_call events — no approval row). R2b's trafficproxy
  reuses `AssessHTTP` as its second consumer.
- The gate lives in `tools.Registry` (`SetApprovalGate`, modeled after
  `RequestGuard`): consulted after the circuit-breaker/parameter checks and
  before `tool.Execute` — the last pre-execution ring. A denial returns
  `Result{Success:false}` and never touches `RecordFailure` (expected
  rejections are not tool failures); a nil gate keeps legacy behavior
  byte-identical. The **verifier registry is exempt** (documented): an
  approval stall inside the report gate would race the 15-minute watchdog,
  and the gate's synchronous blocking preserves the verifier timing
  invariant rather than breaking it.
- `web.ApprovalService` owns the runtime state: per-run pending slot queue
  (default 1; overflow waits in FIFO for a freed slot), timeout=reject
  timers (`XALGORIX_APPROVAL_TIMEOUT`, default 600s), scanctx-cancellation
  (stop/pause/delete flip pending rows to `cancelled`), startup
  reconciliation for orphaned pending rows, and store-call deadlines (3s →
  fail-open, per charter §6.1 PostgreSQL must never block tool execution).
  Approval payloads are value-redacted through the agent's secret registry
  plus header-name masking **before** persistence, WS emission and API
  response — keys never leave the process.
- Storage: `platform.Repository` approvals/guidance sections, dual
  implementation (`0003_approvals_guidance.sql` + `_platform/platform.json`);
  `pending_total` in list/summary responses is always cross-project (the
  one D7 exception, for OpsPulse). WS events (`approval_requested`,
  `approval_decided`, `guidance_*`) are emitted only from the web layer.
- Guidance: `POST /api/tasks/{id}/guidance` injects into a running agent
  via `Agent.SendMessage` (`status=injected`) or persists as `queued`;
  queued guidance is merged into the next session's instruction at
  `executeScanSession` startup (flipped to `injected` there). `consumed`
  has no automatic heuristic yet (`UpdateGuidanceStatus` is the seam).
- `XALGORIX_APPROVAL_MODE` defaults to **off** (headless scheduled scans
  must not stall on pendings); `enforce` enables the pipeline. Operator
  allowlist additions: `XALGORIX_APPROVAL_ALLOWLIST`.

## Traffic proxy — `internal/trafficproxy` (R2b)

The single-process shared loopback MITM proxy (REDESIGN §4/D4), default ON
(`XALGORIX_TRAFFICPROXY`, off restores the pre-R2b behavior byte-for-byte).

- `internal/proxy` stays the pure upstream pool (parse/rotation/shared
  clients); `internal/trafficproxy` owns the loopback listener, the CA
  (`ca.go`, stdlib ECDSA P-256, persisted under `XALGORIX_CA_DIR` default
  `~/.xalgorix/ca` via `storage.WriteAtomic`, 0600/0700, corrupt CA is
  fail-closed for interception — never re-signed), the MITM CONNECT path
  (`mitm.go`), and the observation sink (`observation.go`).
- Consumers: `httpclient.buildClient` + `send_request` route via
  `trafficproxy.LocalURL` and trust the CA when interception is active; the
  browser points at `LocalURL`; websearch providers use the same loopback
  route (system-level, no run token); terminal env + curl/wget wrappers
  (`--cacert`) and python `REQUESTS_CA_BUNDLE`/`SSL_CERT_FILE` carry the CA.
  Off mode keeps every legacy route (pool transport clients, blind tunnel).
  WebSocket/upgrade requests fall back to raw passthrough inside the tunnel
  (observed as `upgrade_passthrough` metadata only).
- Attribution: `web.executeScanSession` mints a per-session run token
  (`trafficproxy.RegisterRun`, unregistered in cleanup). The proxy resolves
  `Proxy-Authorization: Bearer` BEFORE the hop-by-hop strip; attributed
  traffic overrides the process dial-guard base with the scan's scopeguard
  config (per-scan provisioned loopback ports still work; Chromium bypasses
  the proxy for those ports).
- Observation: request/response summaries become observed InfoItems in the
  owning run's `domainjournal` via `OpObserveTraffic` (new operation),
  written by a bounded single-writer sink off the request path (overflow is
  dropped and counted). Header values never enter observations — only
  names; sensitive query values are masked. Without a DSN the journal is
  still written; the PG projection simply does not happen.
- Security chain: the proxy does not take over scope (ring 1 stays
  pre-proxy). The operator-machine dial guard (ring 3, `net.Dialer.Control`,
  GuardDials mechanism) moves into the proxy's direct upstream dials — pool
  addresses stay exempt like any operator proxy.

## Engine — `internal/engine` (R4 core)

`internal/engine` contains the Eino v0.9.23 ADK primitives. `XALGORIX_ENGINE`
is parsed in `internal/config`; the configured default is **eino**, while
`XALGORIX_ENGINE=legacy` is the immediate rollback. Unknown values fall back
loudly to legacy.

- `mode.go` defines `legacy` / `eino` parsing. `config.Engine` exposes
  `XALGORIX_ENGINE`; `scan_session` selects the runtime only for explicit eino
  (legacy stays byte-for-byte available).
- `chatmodel.go` implements Eino's `ToolCallingChatModel` over the existing
  `internal/llm.Client` (`ChatWithUsageToolCalls`), so retries, KeyRotator,
  provider protocols and token accounting stay centralized. `WithTools` returns
  a copy; streaming is explicitly disabled.
- `tools.go` wraps `tools.Registry` as Eino `InvokableTool`. Every invocation
  goes through `Registry.Execute`, preserving the six-ring security chain,
  circuit breaker and the R2 approval gate; the adapter never calls a tool
  closure directly.
- `runtime.go` builds `adk.ChatModelAgent` + `adk.Runner`, forces
  `ExecuteSequentially` (report/verifier calls cannot overlap), and installs a
  construction-time tool guard middleware for scope/safety decisions.
- `approval.go` wraps each tool with `tool.StatefulInterrupt`: the first call
  creates a pending approval through the broker, and
  `Runner.ResumeWithParams` delivers `ApprovalDecision`. Denials return a normal
  tool result; approval rows/REST/WS remain the R2 contract.
- `obligation.go` defines the per-turn/tool observer seam. ADK middleware applies
  nudges before each model call and lets the observer block `finish`/other tools.
  `internal/agent/eino_observer.go` fills it from the existing hook registry,
  budget, liveness and finish gate.
- `internal/agent/eino_runtime.go` runs the ADK runner from `Agent.Run` after
  the shared legacy prelude (prompt/state/checkpoint/budget). An interrupt is
  resolved by the web `einoApprovalBroker`, which creates the ordinary R2
  approval row and polls `WaitApproval` until REST/WS decision, timeout, or
  cancel; the same scan goroutine then calls `Runner.ResumeWithParams`.
- `scan_session` opens the checkpoint backend per run and installs the broker
  only in eino mode, so registry and callback approval never double-park a
  call. `XALGORIX_ENGINE=legacy` restores the old loop and registry gate.
- `checkpoint.go`, `checkpoint_file.go`, `checkpoint_pg.go` implement Eino's
  `CheckPointStore` with PostgreSQL (`engine_checkpoints`, migration 0005) or
  atomic files under `<dataDir>/_engine/checkpoints` when no DSN exists.
- Remaining R4 gap: full golden scheduling parity and broader Verifier timing
  evidence. The engine is live by default; `XALGORIX_ENGINE=legacy` is the
  rollback until those gates are extended.

## Providers, LLM & auth — `internal/llm`, `internal/providers`, `internal/auth`

- `internal/llm` — model **router** (resolves a model name to a provider
  endpoint), multi-provider **KeyStore**, and the chat client.
- `internal/providers` — the compiled-in provider catalog.
- `internal/auth` — credential profiles + OAuth drivers (PKCE, device-code,
  setup-token, CLI-reuse) for provider authentication.

The engine is model-agnostic: the LLM, API base, and key are configured at
runtime; there is no hard-coded provider.

## Scan modes

- **single** — one URL/host, full vulnerability testing.
- **dast** — deep crawl of a specific app: discovery → parameter mining → vuln testing.
- **wildcard** — subdomain enumeration (passive + active) → per-subdomain scan.

Multiple targets are processed through the scan queue with resume support.

## Data flow

```
target + instruction
   → web: create scan instance (own goroutine + scanctx)
   → coordinator: recon → decompose hypotheses → parallel specialist agents
   → each agent: reason → tool call → observe (events stream over WebSocket live)
   → ledger mutation: domain_journal.jsonl append + fsync, then ledger.json
   → reporting: gated + verified findings persisted to the scan record
   → storage: atomic scan.json per scan dir
   → optional cold path: journal observer/reconciler → River → PostgreSQL
   → search_info: bounded region/association/context reads (no verdict role)
   → report.go / internal/reporting: branded PDF on demand
   → notify.go: Discord/Telegram lifecycle + finding notifications
                 (completion summaries are opt-in)
```

## Persistence & isolation

- `internal/storage` — atomic file writes (never a torn `scan.json`).
- `internal/scanctx` — per-scan context keyed by ID; findings stores and the
  delegation graph are scoped to it; each agent registry owns its verifier, so
  concurrent scans and parallel specialists are isolated.
- Each scan record (metadata, events, findings) is a `scan.json` under its own
  scan directory in the data dir; finished records are immutable and cached.
- Each scan also writes `domain_journal.jsonl` (durable domain audit) and
  `ledger.json` (current hypothesis/evidence state). The journal is not the
  state authority and is not reconstructed from best-effort telemetry.
- PostgreSQL, when configured, stores InfoItem/edge/audit projections and sync
  watermarks. It is a replica: `(scan_id, seq)` makes replay idempotent and a
  reconciler fills gaps after crashes. No DSN means file-only mode.

## Configuration

Read from `~/.xalgorix.env` (and process env). Key variables:

| Variable | Purpose |
| --- | --- |
| `XALGORIX_LLM` | provider-native model ID |
| `XALGORIX_LLM_PROVIDER` | provider selected by the dashboard for catalog routing |
| `XALGORIX_API_KEY` / `XALGORIX_API_BASE` | LLM credentials / custom endpoint |
| `XALGORIX_BIND` | dashboard listen address, default `127.0.0.1` (set `0.0.0.0` to expose; port defaults to `9137`) |
| `XALGORIX_PASSWORD` / `XALGORIX_PASSWORD_HASH` | dashboard auth |
| `XALGORIX_DATA_DIR` | where scan records are stored |
| `XALGORIX_DATABASE_URL` | optional PostgreSQL InfoItem/audit cold path; empty = file-only |
| `XALGORIX_COLDPATH_RECONCILE_SECONDS` | journal-to-PostgreSQL gap reconcile interval, default 30 |
| `XALGORIX_VOI_WEIGHT` | deterministic VOI ranking weight, clamped to `[0,0.30]`; default 0 |
| `XALGORIX_MAX_ITERATIONS` / `_MAX_DURATION` / `_MAX_TOKENS` / `_MAX_TOOL_CALLS` | per-scan budgets |
| `XALGORIX_MAX_NON_PROGRESS` | active seconds without validated assessment evidence; default 3600, 0 disables the clock |
| `XALGORIX_MAX_INSTANCES` | authoritative concurrent scan capacity when set; otherwise capacity is derived from live RAM headroom |
| `XALGORIX_SCAN_MEMORY_BUDGET_MB` | estimated RAM required for each newly admitted scan (auto-scaled when unset) |
| `XALGORIX_RATE_LIMIT_REQUESTS` / `_RATE_LIMIT_WINDOW` / `_RATE_RPS` / `_RATE_BURST` | request throttling |
| `XALGORIX_PROXY_URL` / `_PROXY_FILE` / `_PROXY_ROTATION` | upstream proxying |
| `XALGORIX_OOB_*`, `XALGORIX_INTERACTSH_*` | out-of-band callback config |
| `XALGORIX_DISCORD_WEBHOOK` / `_DISCORD_MIN_SEVERITY` | Discord notifications |
| `XALGORIX_TELEGRAM_BOT_TOKEN` / `_CHAT_ID` / `_MIN_SEVERITY` | Telegram notifications |
| `XALGORIX_NOTIFY_SCAN_COMPLETE` | Opt in to end-of-scan Discord/Telegram summaries (default `false`; per-vulnerability alerts are unaffected) |

Secrets (password hash, bot token, API keys) are never returned by any API
response — only a `*_configured` boolean is surfaced.

When `XALGORIX_MAX_INSTANCES` is set, scan admission uses that exact total
capacity and does not reduce it using the adaptive RAM estimate. Heavy tool
execution remains separately throttled by live CPU and memory pressure, and a
critically full disk still blocks new scans. When the variable is unset,
admission treats the RAM-derived slot count as remaining headroom, then adds
the instances already running to produce the total live capacity. This prevents
current RAM use from being counted both in `MemAvailable` and again in the
running-instance comparison. A scan admitted during the last 30 seconds also
reserves one of those adaptive headroom slots while its agent and tool
allocations catch up with the operating system's `MemAvailable` reading; this
prevents concurrent pending waiters from reusing one snapshot.
