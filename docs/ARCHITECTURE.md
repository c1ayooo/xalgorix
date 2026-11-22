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

The local server exposes a REST API + an SSE event stream (`/api/stream`, the
former WebSocket hub) and serves the React
dashboard, which is compiled from `webui/` and embedded into the binary via
`//go:embed static/*`. It also owns scan orchestration and persistence.

After the v4.5.44 decomposition this package is split into cohesive files
(behavior-preserving; `server.go` is now a thin core + routing hub):

| File | Responsibility |
| --- | --- |
| `server.go` | `Server` struct, `NewServer`/`Start`, lifecycle, small handlers |
| `handlers_router.go` | route table / mux wiring |
| `auth_session.go` | dashboard auth: sessions, login backoff, CSRF, middleware |
| `sse_hub.go` | SSE event-stream hub: fan-out, ring buffer, Last-Event-ID replay |
| `orchestrator.go` | scan orchestration: single / DAST / wildcard / multi + subdomain collection |
| `scan_session.go` | run one scan session + event processing + phase inference |
| `scan_audit.go` | read-only domain audit projection and cold-path sync state APIs |
| `queue_state.go` | scan-queue persistence, resume, admission control |
| `scan_record.go` / `scan_query.go` / `scan_list.go` | scan-record assembly, lookup, listing/caching |
| `scan_state_backend.go` | R6.1 record state-authority seam (`web.SetScanStateBackend`): record safe point → PG `run_scan_records` + runs takeover columns, scan.json stopped; durable-row-first reads with the legacy file as pre-import fallback; run-state purge on deletion/retention |
| `scan_event_journal.go` | R6.1 event-journal seam (`web.SetEventStateBackend`): conversation/telemetry events → PG `run_events` ((scan_id, seq), instance-stamped); instance-union reads for run-form requests (D11), legacy `events.jsonl` only as import fallback |
| `scan_audit.go` | read-only domain audit projection + cold-path sync views (`/api/scans/{id}/audit`, `/api/scans/{id}/coldpath`) |
| `notify.go` | Discord + Telegram notifications (severity-gated) |
| `chat.go` | in-scan / post-scan chat with the agent |
| `schedules.go` / `scheduler.go` | scheduled scans; the tick snapshots due schedules + advances timing under `schedulesMu` and does task reverse-lookup / project-scope validation / run mounting lock-free (`dispatchScheduledScan`) |
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

The autonomous runtime binding. `Agent.Run` builds the system prompt encoding
the 22-phase methodology plus all scan state and budget/checkpoint preludes,
then hands the model/tool loop to the Eino ADK runtime until the objective is
met or a budget (iterations / duration / tokens) is hit.

| File | Responsibility |
| --- | --- |
| `agent.go` | scan prelude, lifecycle (`Run`/`Stop`), tool assembly, budget/watchdog |
| `eino_runtime.go` | starts the ADK runner, maps events/interrupts, terminal outcomes |
| `eino_observer.go` | obligations, model/provider recovery, tool gates/results, compaction |
| `agent_guard.go` | scope guard, phase-restriction guard, passive-recon guard, host/URL extraction |
| `agent_prompt.go` | system-prompt / closing-instruction / whitebox-guidance construction |
| `agent_messages.go` | tool-result formatting + conversation pruning/compaction helpers |
| `agent_ratepolicy.go` | request-rate policy parsing (from the instruction) |
| `hooks.go` | obligation hooks incl. stuck/loop detection (repeated identical calls) |
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
memory-bounded, and persists through `scanctx.LedgerBackend` so it survives
restart/resume. From R6.1 (PG primary storage, REDESIGN D12) production
persists whole snapshots into the `run_ledger_snapshots` row at every mutation
boundary (in-memory graph stays the run-time authority; `<scanDir>/ledger.json`
is only the nil-backend test path and the pre-import read fallback); snapshot
resolution is own row → newest same-`instance_id` sibling row (D11 continuity
for a re-dispatched session) → legacy file. A hypothesis carries identity, preconditions, a
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
- `portscan` — the `port_scan` tool: invokes the external `gogo` CLI
  (chainreactors/gogo, GPL-3.0 — see `NOTICE`) as a child process for
  port/service/banner/fingerprint discovery. Deliberately out-of-process: the
  gogo module root is `package main`, and importing its packages would make the
  whole binary a GPL derivative. The child rides the same safety envelope as
  `terminal`/`python` (process group + tree kill, `egress` proxy env and
  credential because a native scanner bypasses `DialGuard`, sanitised
  environment, bounded IO, rlimits); `target` is checked by the scope guard and
  the call is command-class for the approval gate. Results are parsed from the
  JSONL archive (`-o jl -f <file>`), deduplicated per host:port, and attributed
  to `service_discovery` / `tech_fingerprint` recon coverage. Missing binary =
  soft failure with an install hint, never a circuit-breaker failure.
- `browser` + `pageagent` — headless browser automation (JS-heavy apps, auth flows)
- `httpclient` — direct HTTP requests
- `websearch`, `codesearch`, `fileedit`, `notes`, `python` — research / analysis helpers
- `search_info` (registered by `internal/agent`) — read-only region/association/
  context activation over the optional InfoItem cold path; no verdict role
- `reporting` — the `report_vulnerability` tool + findings store (see below)
- `agentsgraph` — sub-agent graphs
- `proxy`, `oob`, `finish`, `iolimit`, `skills` — proxying, out-of-band callbacks, termination, IO limits, methodology corpus
- `mcp` — operator-registered MCP servers (stdio + streamable HTTP) bridged as
  ordinary registry tools named `mcp_<server>_<tool>`: they inherit the full
  execution chain (circuit breaker, approval gate, guards) and never bypass
  the scope dial/request checks; the verifier registry stays MCP-free. The
  per-scan `Manager` owns the sessions (no package globals); user skills ride
  the per-scan skill state (`skills.Register`), operator-authored delegation
  profiles merge behind the builtin specialists, and every invocation is
  audited to the domain journal (`skill_load` / `agent_delegate` / `mcp_call`)
  with a best-effort cross-scan JSONL projection under `<dataDir>/_invocations/`.

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
- `internal/domainjournal` — per-scan domain audit journal with monotonic
  `seq`, trailing-line repair, duplicate/gap/disorder projection, and
  non-blocking append observers used for cold-path wakeups. From R6.1 (PG
  primary storage, REDESIGN D12) appends persist through
  `domainjournal.Backend` into the `run_journal` table ((scan_id, seq)
  idempotent, rows stamped with the run's `instance_id`); the legacy
  `<scanDir>/domain_journal.jsonl` file remains only the nil-backend test
  path and the pre-import read fallback, and a backend read error surfaces
  instead of silently reading a stale file. `internal/storage.AppendSync`
  remains the append+fsync primitive of the legacy file path.
- `internal/infoitem/postgres` — optional PostgreSQL replica for InfoItems,
  relation edges, audit records and contiguous sync watermarks. It implements
  the three activation queries and never participates in verdicts.
- `internal/jobs` — pinned River v0.51.2 client/workers. Jobs only carry bounded
  cold-path journal ranges; they do not own scan lifecycle, HITL, agentsgraph,
  or event-stream (SSE) work.
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

Hot-path mutations remain in memory (the run-time authority); the durable
domain audit lives in PG `run_journal` (R6.1), with the per-scan file as the
legacy fallback. PostgreSQL is the primary store per REDESIGN D12 — a DSN is a
startup requirement, not an option: `XALGORIX_DATABASE_URL` enables startup
migrations, River, and reconciliation; PostgreSQL errors never gate a running
scan (failures record `failed` watermarks and the reconciler catches up,
charter §6.4).

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
- **Runs are mount records, not state — and since R6.1 the mount carries the
  takeover columns.** The run row keeps `task_id`, `instance_id` (unique),
  `legacy_scan_id`, `mode`, `trigger`, `created_at`; migration 0012 added the
  takeover columns (`scan_status` … `scan_detail`) that the newest TOP-LEVEL
  per-scan record stamps onto it at every record safe point. The per-record
  durable home is `run_scan_records` (migration 0016, one row per scan record
  keyed by the workspace directory basename, verbatim record as TEXT plus a
  dataDir-relative anchor): the web layer's record safe point writes it through
  `web.SetScanStateBackend` (`runstate.Store.PutScanRecord`, one transaction
  for the record row + the top-level runs-column update) and **never writes
  `scan.json` again** (charter v3.0 §5.1, REDESIGN D12 — the file is only the
  nil-backend test path and the pre-import fallback). The full scan set
  (`findAllScanSummaries`) sweeps `platform.Repository.ScanRunRecords` first
  and keeps the file walk only for un-imported directory residue; status/
  plan counters ride in the verbatim record instead of being re-derived from
  files.
- **Degradation** (charter v3.0 §6.6): PG is a required deployment dependency —
  no DSN refuses startup with guidance (`cmd/xalgorix/main.go` fail-fast);
  the platform `FileStore` (`_platform/platform.json`) remains only as the
  R6.0 importer's read-only migration source and the test harness. A store
  that cannot start (pool, ping, migrations) is a fatal configuration error,
  not a degraded mode.
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
- **Task findings** are a cold-path projection of already-accepted scan-record
  findings (the verbatim record row or, legacy, `scan.json`). With a DSN,
  `run_findings` stores one row per `(run_id, dedup_key)` (keyed by the shared
  `findingContentKey`) and task reads use `DISTINCT ON` (newest run wins);
  without one, the web layer pages through every run and applies the same
  deterministic dedup. Projection writes happen after scan finalization with
  startup backfill; they never participate in acceptance or verification.
- A mount whose record disappeared — directory removed or durable row deleted —
  without a durable pending-dispatch snapshot projects as `missing`, not
  `pending`, so it neither counts as an active run nor blocks project deletion.
  Retention and manual scan deletion remove the corresponding mount, the
  dispatch tombstone, and the run's durable state rows (`deleteScanRecordState`
  → record row + `run_events` + journal/ledger/state-files/watermark purge;
  findings cascade) — PG rows follow the run lifecycle (D12).
- Existing endpoints accept an optional `project_id` query parameter
  (`/api/instances`, `/api/findings`, `/api/findings/summary`, `/api/scans`);
  without the parameter behavior is byte-identical to before.
- **Scheduler→Run closure** (formerly the "transitional fact"): schedule
  dispatches — the 30s tick and the manual trigger endpoint alike — resolve
  the schedule task read-only (`TaskByLegacyScheduleID` on
  `context.legacy_schedule_id`, default project) and enforce the linked
  project's scope with the same `validateTargetsInScope` rule as task runs.
  The linkage is maintained at write time too: POST /api/schedules and every
  PUT re-link idempotently via `ensureScheduleTask`
  (`ImportScheduleTask` find-or-create), so runtime-created schedules never
  wait for the next importer pass. An empty project scope (the
  default-import state) keeps the schedule's own targets as the authorization
  source (operator's explicit pre-platform configuration); a nil store is
  the same posture plus legacy direct dispatch with no run mount (charter
  §6.6). With a store configured, every unexpected state is a **skip, not a
  bypass**: missing task, missing project or transient store error skips the
  cycle (tick) / answers 400 or 503 (trigger endpoint, `503` reserved for
  `errScheduleLinkUnavailable` transient failures) — a broken linkage must
  never widen target authorization to "whatever the schedule says". Skipped
  cycles still advance `LastRun`/`NextRun` (the 30s tick must not
  retry-storm). The run mounts with `trigger="schedule"` synchronously
  before the dispatch goroutine, so the mount is visible without racing the
  scan. `schedulesMu` covers only the due-snapshot + `LastRun`/`NextRun`
  advance; validation, mount and dispatch all run on the lock-free snapshot
  — a PG-backed mount can never stall `/api/schedules`. This authorization
  preflight never interrupts an in-flight Run: a store outage only defers new
  scheduled dispatches whose project scope cannot be verified (charter §6.1).

### Task objective — `internal/objective`, `internal/platform.Objective`

- `platform.Objective` (`{kind, statement, criteria[]}`) is the platform-level
  structured goal attached to a Task. In PostgreSQL it lives in the 1:1
  extension table `task_objectives` (migration `0007_task_objective.sql`,
  `LEFT JOIN`ed on every task read); the no-DSN file store persists the same
  struct inside the task snapshot. An absent objective means **"not
  structured"** — never inferred.
- `internal/objective` is a **pure evaluator**: it reads a signal snapshot
  (accepted findings + ledger hypotheses + plan counters) and returns per
  criterion `met / unmet / undetermined / manual_pending` plus a display-only
  attainment. It never writes back, never relabels a finding and never
  blocks a scan (charter §3.5 / §9 — the report pipeline stays the only exit
  for finding acceptance).
- Runtime wiring (`internal/web/objective_service.go`,
  `internal/web/scan_session.go`): the objective travels as its **own prompt
  section** (`## OBJECTIVE (operator-stated)`), as a **human-sourced observed
  InfoItem** in the scan's domain journal (`domainjournal.OpRecordObjective`),
  and as `agent.ObjectiveSpec` for the engine, where it only **marks** the
  plan tasks that serve a finding-class criterion (`Task.ObjectiveRef`).
  Marking never adds, removes or reorders a task and never changes the
  coverage denominator, so the golden scheduling parity is unaffected.
- `GET /api/tasks/{id}/objective` renders the objective state honestly:
  criterion states with their evidence reference, the loop position of the
  latest run **with its derivation basis**, the open gaps and the VOI-weighted
  schedulable pool. "Unknown" is returned when the persisted facts do not
  support a position.

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
- Production scan approvals use Eino's interrupt/resume wrapper
  (`engine.ApprovalTool`) before the tool endpoint. `tools.Registry` also
  exposes `SetApprovalGate` as a defense-in-depth seam for direct registry
  consumers: it is consulted after circuit-breaker/parameter checks and before
  `tool.Execute`. A denial returns `Result{Success:false}` and never touches
  `RecordFailure`. The **verifier registry is exempt** (documented): an
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
  `Proxy-Authorization` BEFORE the hop-by-hop strip on two channels: `Bearer`
  from Go tool transports, and `Basic` from curl/wget/python (proxy URL
  userinfo or workspace `.curlrc`/`.wgetrc`). Basic registry misses stay
  unattributed rather than impersonating a Run. Attributed traffic overrides
  the process dial-guard base with the scan's scopeguard config (per-scan
  provisioned loopback ports still work; Chromium bypasses the proxy for those
  ports). Browser traffic remains system-level because rod has no token channel.
- Subprocess approval: Basic-attributed requests never passed the tool-layer
  gate, so the proxy may adjudicate a high-risk HTTP request through
  `web.proxyHighRiskGate` / `trafficproxy.SetHighRiskGate`. It reuses the same
  `ApprovalService` slot queue, timeout=reject, WS events and redaction; Bearer
  traffic and unattributed traffic are never re-gated.
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

## Tool execution, safe files and egress

- `tools.Registry.ExecuteContext` is the single execution path. `Execute`
  remains a `context.Background()` wrapper. `Tool.ExecuteContext` is optional.
  Eino's `RegistryTool` runs the registry call in a bounded goroutine with the
  per-tool hard ceiling plus 30s grace and force-returns on expiry, so a
  non-cooperative tool cannot block the ADK tool node.
  Terminal pre-launch work (venv/install) inherits the call context, and
  terminal/Python cancellation or timeout returns a failed tool result rather
  than `Success=true`. `browser_action` binds `rod.Page.Context` for one action
  under `actionMu`; cancellation aborts that CDP operation without closing
  Chromium or losing page state.
- `internal/safefile` replaces "check path, then reopen path" in fileedit.
  On Linux each component is opened with `openat(O_NOFOLLOW)` from a directory
  fd, writes use a same-directory temp file plus `renameat`, and exclusive
  create uses `linkat`. The directory entry is fsynced through a separately
  opened read-only fd and fsync failure is returned. Non-Linux returns
  `ErrNotSupported` instead of silently falling back to an unsafe reopen.
- `internal/egress` records the per-scan boundary keyed by **`ScanContext.ID`**
  (the same key held by `Registry.GetScanContextID`). The default `proxy` mode
  is a routing posture, not a proven OS boundary: `Status.Enforced` stays false
  and children receive `XALGORIX_EGRESS_ENFORCED=0`. It configures browser and
  subprocess proxy variables only as far as those clients cooperate. With
  `XALGORIX_TRAFFICPROXY=off`, no egress markers are injected, preserving the
  legacy child environment.
- Linux `enforce` mode requires root + `nft`, derives the loopback proxy port
  from `trafficproxy.LocalPort`, and installs an isolated
  `inet xalgorix_egress_<pid>` table (allow only the dedicated UID to the proxy
  port over IPv4/IPv6, then DROP). Before launch, `PrepareChildAccess` transfers
  the scan workspace to the dedicated UID/GID and normalizes modes
  (dirs 0750, files 0640/0750); curl/wget and Python receive workspace-local
  copies of the public CA/bundle rather than the root-only CA directory.
  Terminal/Python then drop to that UID/GID via `SysProcAttr.Credential`.
  Browser cannot be launched under a different UID with rod's launcher, so it
  fails closed in enforce mode. Rules are refcounted per scan and removed when
  the last enforced scan ends; non-Linux reports unmanaged.
  `scan_session` prepares and forgets the status under `sctx.ID`.

## Platform capabilities — `internal/platformutil`

- `ConfigureProcessGroup` / `KillProcessTree`: Unix uses `Setpgid` and negative
  PID SIGKILL with the self-process-group guard; Windows is explicitly a
  single-process kill (no process-group claim).
- `DiskAvailable`: Unix `Statfs`; Windows `GetDiskFreeSpaceEx`.
- `TryLockFile` / `UnlockFile`: Unix non-blocking `flock`; Windows
  `LockFileEx`/`UnlockFileEx`. No in-process mutex fallback is used.
- `OpenReadNoFollow`: Unix `O_NOFOLLOW`; Windows opens read-only (callers that
  need reparse-point rejection must add an explicit check).
- Consumers: `scanctx/termstate`, `tools/terminal`, `tools/python`,
  `web/server.go`, `web/queue_state.go`, `auth/store.go`, and
  `resources.readDiskFree`. This is what makes the Windows build target work.

## Engine — `internal/engine` (Eino ADK)

`internal/engine` is the only scan runtime. `internal/agent.Agent.Run` builds
the prompt/state/checkpoint prelude and then hands the model/tool loop to an
Eino ADK `ChatModelAgent` + `Runner`; there is no second loop and no engine
selection variable.

- `runtime.go` builds `adk.ChatModelAgent` + `adk.Runner`, forces
  `ExecuteSequentially` (report/verifier calls cannot overlap), and installs a
  construction-time tool guard middleware for scope/safety decisions. Guard
  denials and expected tool failures become normal tool results, never
  graph-killing errors. The guard may return normalized arguments (used for
  terminal command normalization before execution).
- `chatmodel.go` implements Eino's `ToolCallingChatModel` over the existing
  `internal/llm.Client`. Native `tools`/`tool_calls` is the primary channel.
  For Anthropic, Gemini and Responses endpoints, which do not yet receive a
  native tools array, the same adapter renders tool calls/results into the XML
  dialect and parses the response back into `schema.ToolCalls`; the ADK still
  sees one native tool protocol. Streaming is explicitly disabled.
- `model_recovery.go` is the scan-owned provider policy seam. The adapter
  requests a retry after a failed provider call; the agent decides backoff,
  bounded provider-wait budget, context-overflow compaction, empty-response
  retry and no-tool recovery. This keeps transport retries inside one ADK turn
  instead of replaying tool side effects.
- `tools.go` wraps each registry tool as an Eino `InvokableTool`. Every call
  goes through `Registry.ExecuteContext`, preserving the security chain,
  circuit breaker and approval boundary. A per-invocation context value carries
  tool `Result.Metadata` back to the agent adapter so result hooks can observe
  fields such as skill-name/duplicate markers.
- `approval.go` wraps each tool with `tool.StatefulInterrupt`: the first call
  creates a pending approval through the broker, and
  `Runner.ResumeWithParams` delivers the operator decision. Denials return a
  normal tool result. The approval REST/WS contract is unchanged.
- `obligation.go` defines the per-turn/tool observer seam. ADK middleware
  applies nudges before each model call and lets the observer block
  `finish`/other tools. `internal/agent/eino_observer.go` fills it from the
  hook registry, branch budget, liveness, finish gate, bounded-context
  compactor, stuck-call policy, terminal normalization and tool-result
  archive.
- `internal/agent/eino_runtime.go` runs the ADK runner after the shared
  prelude. Interrupts are resolved by the web `einoApprovalBroker`, which
  creates the ordinary approval row and polls `WaitApproval` until a REST/WS
  decision, timeout, or cancellation; the same scan goroutine then resumes.
  Construction or execution failure is terminal and visible.
- `checkpoint.go`, `checkpoint_file.go`, `checkpoint_pg.go` implement Eino's
  `CheckPointStore` with PostgreSQL (`engine_checkpoints`, migration 0005) or
  atomic files under `<dataDir>/_engine/checkpoints` when no DSN exists.
  `scan_session` always opens the store; a caller that omits it gets a
  per-scan file/memory store at run time.
- Runtime gates: `TestGoldenSchedulingParity` drives a ledger snapshot through
  the single runtime and asserts the M5 claim order, tool sequence, finish
  semantics, turn count and on-wire tools array.
  `TestRuntimeToolsNodeConfigPinsVerifierSerialExecution` pins
  `ExecuteSequentially=true`; the verifier timing tests keep the entire
  report→verify chain serial with `maxInFlight==1`.

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
   → ledger mutation: domain journal append (PG run_journal via
     domainjournal.Backend; legacy fsync file only in nil-backend test mode),
     then the ledger snapshot (PG run_ledger_snapshots via scanctx.LedgerBackend)
   → reporting: gated + verified findings persisted to the scan record
   → storage: record safe point → PG run_scan_records + runs takeover columns
     (web.SetScanStateBackend; scan.json only in nil-backend test mode)
   → cold path: journal observer/reconciler → River → PostgreSQL
   → search_info: bounded region/association/context reads (no verdict role)
   → report.go / internal/reporting: branded PDF on demand
   → notify.go: Discord/Telegram lifecycle + finding notifications
                 (completion summaries are opt-in)
```

## Persistence & isolation

- `internal/storage` — atomic file writes for the byte-artifact layer (never a
  torn tool-output archive or traffic packet; the legacy state-file path uses
  the same primitive).
- `internal/scanctx` — per-scan context keyed by ID; findings stores and the
  delegation graph are scoped to it; each agent registry owns its verifier, so
  concurrent scans and parallel specialists are isolated.
- **PG primary storage (R6, REDESIGN D12, charter v3.0)** — each scan record
  (metadata, events, findings) persists as a `run_scan_records` row plus the
  runs mount row's takeover columns; events live in `run_events` (seq = the
  1-based journal line order), the domain audit in `run_journal`, and the
  ledger snapshot in `run_ledger_snapshots`. The in-memory `ScanContext` stays
  the run-time authority; the per-scan directory keeps only byte artifacts
  (tool-output archives, traffic packets `traffic/tr_*.json`, uploads, report
  deliverables, sandbox scratch). A missing DSN refuses startup (fail-fast).
- The domain journal is not the state authority and is not reconstructed from
  best-effort telemetry.
- PostgreSQL is the single durable state (not a replica): `(scan_id, seq)`
  makes writes idempotent, rows carry the run's `instance_id`, watermarks
  distinguish `pending/applied/gap/failed`, and a reconciler fills gaps after
  crashes. The R6.0 importer (`internal/runstate`) backfills pre-R6 scan
  directories once (watermark-guarded); the directories stay as byte-artifact
  homes and the legacy-file read fallback.

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

## Tool execution plane — `internal/runtime`

> The implementation plan (tool inventory, image contract, runnerd API,
> concurrency/isolation, resources/lifecycle, extension rules and open gaps) is
> in [`docs/TOOLBOX_CONTAINER_PLAN.md`](TOOLBOX_CONTAINER_PLAN.md).

Tools are described by `internal/tools`, but *where* they run is decided by
`internal/runtime`. Two backends exist behind one `Driver` interface:

| Backend | When | Behaviour |
|---|---|---|
| `native` (default) | `XALGORIX_RUNTIME_BACKEND` unset/`native` | Historical: the tool spawns its subprocess in the platform process. These code paths are preserved verbatim — they carry shell preludes, apt auto-install, credential files and RLIMIT guards whose byte-level parity is an acceptance criterion. |
| `docker` | `XALGORIX_RUNTIME_BACKEND=docker` | One container per scan ("project"). The container executes the command; the platform keeps policy, state and audit. |

Transports for the `docker` backend (`XALGORIX_RUNTIME_TRANSPORT`):

- `runnerd` (recommended for containerised platforms) — a host-side service
  (`cmd/runnerd`) owns `/var/run/docker.sock` and exposes only the eight
  operations the executor needs. The platform never sees the Docker socket.
- `socket` — the platform talks to the Docker socket directly (fastest to try,
  equivalent to host root; development only).

### Invariants

1. **The security chain runs before dispatch.** `scopeguard`, `sandbox.Policy`,
   `SanitEnv`, proxy injection and the approval gate all live in the platform.
   A driver is an executor, not a security boundary.
2. **Exec is never retried.** Docker's at-least-once semantics do not apply here:
   `Exec` is called once per tool invocation. Only `EnsureProject` / `StopProject`
   / `RemoveProject` are idempotent.
3. **Same-path mounts.** The scan directory is bind-mounted at the *same*
   absolute path inside the container, so `sandbox.Policy`, `tool-outputs/` and
   `traffic/` need no translation layer. Scan state itself has no file resident
   since R6.1 (PG primary storage) — the container only touches the byte
   artifacts in the workspace.
4. **River stays on the cold path.** The `toolbox` River queue owns janitorial
   work (orphan reclamation, adoption after restart, image prefetch, selftest,
   image GC) and nudges the desired/applied watermark. It never owns the scan
   loop, tool dispatch, approvals, or the SSE stream (charter §8).
5. **Desired vs actual state.** The container runtime owns actual state (read
   back through `xalgorix.*` labels). PostgreSQL (`toolbox_projects`) owns the
   desired state and the `desired_gen`/`applied_gen` watermark; the reconciler
   closes the gap after a crash.
6. **Capability minimisation.** Containers run with `--cap-drop=ALL` and add back
   only what the profile declares (`full` → `NET_RAW`,`NET_ADMIN`; `debug` →
   `SYS_PTRACE`). Read-only rootfs, `no-new-privileges`, never `--privileged`,
   never a Docker socket inside a tool container.
7. **Network posture.** `proxy-only` projects join an `--internal` network whose
   only reachable peer is the platform traffic proxy (fail-closed). `raw`
   projects get the capabilities they need plus the scope-guard's CIDR allow-list.
   Chromium cannot read `HTTP_PROXY`, so `XALGORIX_TOOLBOX_BROWSER_PROXY` must be
   set to a container-reachable proxy URL whenever browser traffic is proxied.

### Package layout

```
internal/runtime/contract.go     Driver, ExecSpec/ExecResult, error codes
internal/runtime/native.go       in-process driver (tests + reference)
internal/runtime/docker_client.go minimal Engine API over the UNIX socket
internal/runtime/docker_driver.go per-project containers, CDP endpoint, image GC
internal/runtime/runnerd_client.go client + server handler for the runnerd wire contract
internal/runtime/mounts.go       same-path mount plan and validation
internal/runtime/reconcile.go    desired→actual reconciler and orphan sweeper
internal/runtime/toolbox.go      handler set used by the River toolbox workers
internal/runtime/postgres/       DesiredStateStore backed by toolbox_projects
internal/jobs/toolbox.go         seven idempotent River jobs on the toolbox queue
cmd/runnerd/                     host-side service
cmd/toolshim/                    in-container filesystem / code-search executor
tools/toolbox/                   image definition, tool manifest, selftest
```
