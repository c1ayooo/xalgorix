#!/usr/bin/env python3
"""R6 metrics collector (read-only).

Reads one run/instance from PostgreSQL and emits the Information-to-Decision
metrics used by the R6 A/B comparison. It never writes to the database.

Usage:
  python3 scripts/r6/collect_metrics.py --instance <instance_id> \
      [--dsn "$XALGORIX_DATABASE_URL"] [--out metrics.json]
"""
import argparse
import json
import os
import re
import subprocess
import sys
from datetime import datetime, timezone

SEED_ORIGINS = {"auto", "auto-plan", "auto-plan-surface", "auto-surface-param", "engine", "recon"}
INSTANCE_RE = re.compile(r"^[A-Za-z0-9_.:-]+$")


def psql(dsn: str, sql: str) -> str:
    cmd = ["psql", dsn, "-At", "-v", "ON_ERROR_STOP=1", "-c", sql]
    out = subprocess.run(cmd, text=True, capture_output=True)
    if out.returncode != 0:
        sys.stderr.write(out.stderr)
        raise SystemExit(f"psql failed for: {sql[:80]}...")
    return out.stdout.strip()


def qjson(dsn: str, sql: str):
    raw = psql(dsn, sql)
    if not raw:
        return []
    return json.loads(raw)


def parse_time(s):
    if not s:
        return None
    try:
        return datetime.fromisoformat(s.replace("Z", "+00:00"))
    except ValueError:
        return None


def seconds_between(a, b):
    if not a or not b:
        return None
    return round((b - a).total_seconds(), 3)


def collect(dsn: str, instance: str) -> dict:
    if not INSTANCE_RE.match(instance):
        raise SystemExit("invalid instance id")
    iid = instance

    runs = qjson(dsn, f"""
        select coalesce(json_agg(row_to_json(t)), '[]') from (
          select id::text as run_id, legacy_scan_id, instance_id, scan_status,
                 scan_started_at, scan_finished_at, scan_current_phase,
                 scan_plan_total, scan_plan_completed, scan_detail::jsonb as scan_detail
          from runs
          where instance_id = '{iid}' or legacy_scan_id = '{iid}'
          order by scan_started_at desc limit 1
        ) t
    """)
    if not runs:
        raise SystemExit(f"no run for instance {instance}")
    run = runs[0]
    legacy = run.get("legacy_scan_id") or iid
    run_uuid = run.get("run_id")

    journal = qjson(dsn, f"""
        select coalesce(json_agg(record::jsonb order by seq), '[]')
        from run_journal
        where instance_id = '{iid}' or scan_id = '{legacy}'
    """)
    events = qjson(dsn, f"""
        select coalesce(json_agg(json_build_object(
                 'type', type, 'at', occurred_at, 'payload', payload::jsonb) order by seq), '[]')
        from run_events
        where instance_id = '{iid}' or scan_id = '{legacy}'
    """)
    nodes = qjson(dsn, f"""
        select coalesce(json_agg(json_build_object(
                 'kind', kind, 'state', state, 'payload', payload::jsonb,
                 'created_at', created_at) order by id), '[]')
        from exploration_nodes
        where scan_id = '{iid}' or scan_id = '{legacy}' or instance_id = '{iid}'
    """)
    snapshots = qjson(dsn, f"""
        select coalesce(json_agg(data), '[]')
        from run_ledger_snapshots
        where instance_id = '{iid}' or scan_id in ('{iid}', '{legacy}')
    """)
    findings = []
    if run_uuid:
        findings = qjson(dsn, f"""
            select coalesce(json_agg(finding), '[]')
            from run_findings where run_id = '{run_uuid}'
        """)

    op_counts = {}
    set_status = {}
    first_hyp = None
    reads = []
    attempts = []
    egress_pause = 0
    egress_stop = 0
    for rec in journal:
        op = rec.get("operation", "")
        op_counts[op] = op_counts.get(op, 0) + 1
        at = parse_time(rec.get("at"))
        if op == "record_hypothesis":
            if first_hyp is None or (at and at < first_hyp):
                first_hyp = at
        if op == "set_status":
            status = (rec.get("after") or {}).get("status") or ""
            if status:
                set_status[status] = set_status.get(status, 0) + 1
        if op in ("read_info_item", "search_info"):
            summary = rec.get("summary", "")
            reads.append({
                "op": op,
                "at": rec.get("at"),
                "has_refs": "refs=" in summary and not summary.rstrip().endswith("refs="),
                "truncated": "truncated=true" in summary,
                "incomplete": "incomplete=true" in summary,
                "found": "found=true" in summary,
            })
        if op == "action_attempt":
            summary = rec.get("summary", "")
            attempts.append({
                "at": rec.get("at"),
                "repeat_of": summary.split("prior=", 1)[1].split()[0] if "prior=" in summary else "",
            })
        if op == "egress_pause":
            egress_pause += 1
        if op == "egress_stop":
            egress_stop += 1

    run_start = parse_time(run.get("scan_started_at"))
    first_tool = None
    startup_tools = 0
    tool_calls = 0
    for ev in events:
        if ev.get("type") != "tool_call":
            continue
        tool_calls += 1
        at = parse_time(ev.get("at"))
        if first_tool is None or (at and at < first_tool):
            first_tool = at
        if first_hyp is None or (at and at < first_hyp):
            startup_tools += 1

    beliefs = [n for n in nodes if n.get("kind") == "belief"]
    intents = [n for n in nodes if n.get("kind") == "intent"]
    intent_basis = 0
    for n in intents:
        payload = n.get("payload") or {}
        refs = payload.get("decision_basis_refs") or []
        if refs:
            intent_basis += 1

    hyps = []
    for raw in snapshots:
        try:
            snap = json.loads(raw) if isinstance(raw, str) else raw
        except (TypeError, ValueError):
            continue
        hyps.extend(snap.get("hypotheses") or [])

    seeds = 0
    formal = 0
    quality = 0
    explicit_info_links = 0
    intent_links = 0
    for h in hyps:
        origin = (h.get("origin") or "").lower()
        if origin in SEED_ORIGINS:
            seeds += 1
        else:
            formal += 1
        if h.get("title") and h.get("preconditions") and h.get("falsification"):
            quality += 1
        for ev in h.get("evidence") or []:
            link = ev.get("info_item_id") or ""
            if link and not link.startswith("evidence:"):
                explicit_info_links += 1
            if ev.get("intent_id"):
                intent_links += 1

    detail = run.get("scan_detail") or {}
    metrics = {
        "instance_id": iid,
        "run_id": run_uuid,
        "status": run.get("scan_status"),
        "run_started_at": run.get("scan_started_at"),
        "run_finished_at": run.get("scan_finished_at"),
        "duration_s": seconds_between(run_start, parse_time(run.get("scan_finished_at"))),
        "time_to_first_tool_s": seconds_between(run_start, first_tool),
        "time_to_first_hypothesis_s": seconds_between(run_start, first_hyp),
        "startup_tool_calls_before_first_hypothesis": startup_tools,
        "tool_calls_total": tool_calls,
        "iterations": detail.get("iterations"),
        "tokens": detail.get("total_tokens"),
        "plan_total": run.get("scan_plan_total"),
        "plan_completed": run.get("scan_plan_completed"),
        "formal_hypotheses_journal": op_counts.get("record_hypothesis", 0),
        "claims_journal": op_counts.get("claim_hypothesis", 0),
        "status_changes": set_status,
        "merges": op_counts.get("merge_ledger", 0),
        "info_reads": len(reads),
        "info_reads_with_refs": sum(1 for r in reads if r["has_refs"]),
        "info_reads_truncated": sum(1 for r in reads if r["truncated"]),
        "info_reads_incomplete": sum(1 for r in reads if r["incomplete"]),
        "action_attempts": len(attempts),
        "repeat_attempts": sum(1 for a in attempts if a["repeat_of"]),
        "egress_pauses": egress_pause,
        "egress_stops": egress_stop,
        "belief_nodes": len(beliefs),
        "intent_nodes": len(intents),
        "intents_with_decision_basis": intent_basis,
        "formal_hypotheses_ledger": formal,
        "coverage_seed_hypotheses": seeds,
        "hypotheses_with_quality_fields": quality,
        "evidence_explicit_info_links": explicit_info_links,
        "evidence_intent_links": intent_links,
        "findings_total": len(findings),
        "findings_tags": {},
    }
    tags = {}
    for f in findings:
        for t in f.get("tags") or []:
            tags[t] = tags.get(t, 0) + 1
    metrics["findings_tags"] = tags
    return metrics


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--instance", required=True)
    ap.add_argument("--dsn", default=os.environ.get("XALGORIX_DATABASE_URL", ""))
    ap.add_argument("--out")
    args = ap.parse_args()
    if not args.dsn:
        raise SystemExit("missing --dsn or XALGORIX_DATABASE_URL")
    metrics = collect(args.dsn, args.instance)
    text = json.dumps(metrics, indent=2, ensure_ascii=False)
    if args.out:
        with open(args.out, "w") as fh:
            fh.write(text + "\n")
    print(text)


if __name__ == "__main__":
    main()
