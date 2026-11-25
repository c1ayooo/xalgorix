#!/usr/bin/env python3
"""R6 controlled A/B runner.

Builds a baseline binary from a git ref and the current working tree, starts
each against the fixed R6 lab, launches the same scenario through the dashboard
API, waits for a terminal run, and collects the R6 metrics for both variants.

This is experiment infrastructure: it never edits the repository and never
runs the benchmark/score loop. Usage:

  python3 scripts/r6/run_ab.py --baseline-ref <sha> [--env-file ~/.xalgorix.env] \
      [--scenarios r7,single] [--out /tmp/r6] [--prepare-only]

Requires: git, go, docker compose, psql, python3. The lab is bound to
127.0.0.1 only.
"""
import argparse
import json
import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "..", ".."))
LAB_COMPOSE = os.path.join(HERE, "lab", "docker-compose.yml")
LAB_URL = "http://127.0.0.1:18080"
BIND = "127.0.0.1:19137"
BASE_URL = "http://" + BIND
ACTIVE = {"pending", "running", "stopping", "paused", "queued", "admitting"}


def sh(cmd, cwd=None, env=None, check=True):
    proc = subprocess.run(cmd, cwd=cwd, env=env, text=True, capture_output=True)
    if check and proc.returncode != 0:
        sys.stderr.write(proc.stdout + proc.stderr)
        raise SystemExit(f"command failed: {' '.join(cmd)}")
    return proc


def load_env(path):
    env = {}
    if not path or not os.path.exists(path):
        return env
    for line in open(path):
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        env[k.strip()] = v.strip()
    return env


def write_home(home, env):
    os.makedirs(home, exist_ok=True)
    with open(os.path.join(home, ".xalgorix.env"), "w") as fh:
        for k, v in env.items():
            fh.write(f"{k}={v}\n")


def http_json(method, path, body=None, token=None, timeout=30):
    data = None
    headers = {}
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE_URL + path, data=data, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        raw = resp.read()
        return resp.status, json.loads(raw) if raw else {}


def wait_http(timeout=90):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            urllib.request.urlopen(BASE_URL + "/", timeout=2).read(1)
            return True
        except Exception:
            time.sleep(1)
    return False


def ensure_lab():
    sh(["docker", "compose", "-f", LAB_COMPOSE, "up", "-d", "--build"])
    deadline = time.time() + 120
    while time.time() < deadline:
        try:
            urllib.request.urlopen(LAB_URL + "/", timeout=2).read(1)
            return
        except Exception:
            time.sleep(2)
    raise SystemExit("R6 lab did not become healthy")


def stop_lab():
    sh(["docker", "compose", "-f", LAB_COMPOSE, "down", "-v"], check=False)


def build(ref, out_bin, workdir):
    if ref:
        wt = os.path.join(workdir, "baseline")
        sh(["git", "worktree", "add", "--detach", "--force", wt, ref], cwd=REPO)
        try:
            sh(["go", "build", "-o", out_bin, "./cmd/xalgorix"], cwd=wt)
        finally:
            sh(["git", "worktree", "remove", "--force", wt], cwd=REPO, check=False)
    else:
        sh(["go", "build", "-o", out_bin, "./cmd/xalgorix"], cwd=REPO)


def stop_variant(proc, log):
    try:
        proc.send_signal(signal.SIGTERM)
        proc.wait(timeout=20)
    except Exception:
        proc.kill()
    finally:
        log.close()


def launch_and_wait(scenario, token, max_wait):
    _, body = http_json("POST", "/api/scan", {
        "targets": [LAB_URL],
        "instruction": scenario["instruction"],
        "name": "r6-" + scenario["id"],
        "scan_mode": "single",
        "recon_mode": "active",
        "scan_intensity": "active",
        "phases": [],
    }, token=token)
    instance = body.get("instance_id")
    if not instance:
        raise SystemExit(f"scan launch returned no instance: {body}")
    deadline = time.time() + max_wait
    last = ""
    while time.time() < deadline:
        _, data = http_json("GET", "/api/instances", token=token)
        rows = data.get("instances") or []
        row = next((r for r in rows if r.get("ID") == instance or r.get("id") == instance), None)
        if row:
            status = row.get("Status") or row.get("status") or ""
            last = status
            if status and status.lower() not in ACTIVE:
                return instance, status
        time.sleep(5)
    return instance, "timeout:" + last


def collect(env, instance, out_path):
    dsn = env.get("XALGORIX_DATABASE_URL", "")
    if not dsn:
        raise SystemExit("missing XALGORIX_DATABASE_URL for metrics collection")
    sh([sys.executable, os.path.join(HERE, "collect_metrics.py"),
        "--instance", instance, "--dsn", dsn, "--out", out_path])
    return json.load(open(out_path))


def compare(baseline, new, scenario_id):
    checks = []
    def check(name, ok, detail=""):
        checks.append({"name": name, "ok": bool(ok), "detail": detail})
    if not new:
        return checks
    check("formal_hypotheses_created", new.get("formal_hypotheses_journal", 0) > 0,
          f"{new.get('formal_hypotheses_journal', 0)}")
    check("claims_consumed", new.get("claims_journal", 0) > 0, f"{new.get('claims_journal', 0)}")
    check("decision_basis_present", (new.get("intents_with_decision_basis", 0) > 0) or (new.get("intent_nodes", 0) == 0),
          f"{new.get('intents_with_decision_basis', 0)}/{new.get('intent_nodes', 0)}")
    if baseline:
        bt = baseline.get("time_to_first_hypothesis_s")
        nt = new.get("time_to_first_hypothesis_s")
        check("first_hypothesis_not_later", bt is None or nt is None or nt <= bt * 1.25 + 30,
              f"baseline={bt} new={nt}")
        check("findings_not_regressed", new.get("findings_total", 0) >= baseline.get("findings_total", 0),
              f"baseline={baseline.get('findings_total', 0)} new={new.get('findings_total', 0)}")
    return checks


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--baseline-ref", default="", help="git ref for the pre-R1 baseline")
    ap.add_argument("--env-file", default=os.path.expanduser("~/.xalgorix.env"))
    ap.add_argument("--scenarios", default="r7,single,ctf")
    ap.add_argument("--scenario-file", default=os.path.join(HERE, "scenarios.json"))
    ap.add_argument("--out", default="/tmp/r6")
    ap.add_argument("--prepare-only", action="store_true")
    ap.add_argument("--keep-lab", action="store_true")
    args = ap.parse_args()

    os.makedirs(args.out, exist_ok=True)
    workdir = os.path.join(args.out, "work")
    os.makedirs(workdir, exist_ok=True)
    token = "r6-" + uuid.uuid4().hex
    scenarios = {s["id"]: s for s in json.load(open(args.scenario_file))}
    selected = [s.strip() for s in args.scenarios.split(",") if s.strip()]
    for s in selected:
        if s not in scenarios:
            raise SystemExit(f"unknown scenario {s}")

    ensure_lab()
    try:
        bins = {}
        if args.baseline_ref:
            bins["baseline"] = os.path.join(workdir, "xalgorix-baseline")
            build(args.baseline_ref, bins["baseline"], workdir)
        bins["new"] = os.path.join(workdir, "xalgorix-new")
        build("", bins["new"], workdir)
        if args.prepare_only:
            print(json.dumps({"prepared": True, "bins": bins, "scenarios": selected}, indent=2))
            return

        results = {}
        for sid in selected:
            sc = scenarios[sid]
            results[sid] = {}
            for label, bin_path in bins.items():
                merged = load_env(args.env_file)
                merged.update({
                    "XALGORIX_BIND": BIND,
                    "XALGORIX_DATA_DIR": os.path.join(workdir, f"data-{label}-{sid}"),
                    "XALGORIX_API_TOKEN": token,
                    "XALGORIX_NO_AUTO_UPDATE": "1",
                    # The fixed lab is loopback-bound; the operator explicitly
                    # authorizes local targets for the experiment.
                    "XALGORIX_ALLOW_LOCAL_TARGETS": "true",
                })
                merged.update(sc.get("env") or {})
                home = os.path.join(workdir, f"home-{label}-{sid}")
                write_home(home, merged)
                proc_env = dict(os.environ)
                proc_env.update(merged)
                proc_env["HOME"] = home
                log = open(os.path.join(workdir, f"server-{label}-{sid}.log"), "w")
                proc = subprocess.Popen([bin_path, "--web"], env=proc_env, stdout=log, stderr=subprocess.STDOUT)
                if not wait_http():
                    stop_variant(proc, log)
                    raise SystemExit(f"{label}/{sid}: server did not start")
                try:
                    instance, status = launch_and_wait(sc, token, sc.get("max_wait_s", 1800))
                finally:
                    stop_variant(proc, log)
                metrics = collect(merged, instance, os.path.join(args.out, f"{sid}-{label}.json"))
                metrics["terminal_status"] = status
                results[sid][label] = metrics
                print(f"[{sid}/{label}] {instance} {status}")

        report = {"scenarios": results, "checks": {}}
        for sid in selected:
            base = results[sid].get("baseline")
            new = results[sid].get("new")
            report["checks"][sid] = compare(base, new, sid)
        with open(os.path.join(args.out, "comparison.json"), "w") as fh:
            json.dump(report, fh, indent=2, ensure_ascii=False)
        lines = ["# R6 A/B comparison", ""]
        for sid in selected:
            lines.append(f"## {sid}")
            lines.append("")
            lines.append("| metric | baseline | new |")
            lines.append("|---|---|---|")
            keys = ["terminal_status", "duration_s", "time_to_first_hypothesis_s", "startup_tool_calls_before_first_hypothesis",
                    "formal_hypotheses_journal", "claims_journal", "info_reads", "intents_with_decision_basis",
                    "repeat_attempts", "egress_pauses", "egress_stops", "findings_total", "tokens", "tool_calls_total"]
            for k in keys:
                b = (results[sid].get("baseline") or {}).get(k)
                n = (results[sid].get("new") or {}).get(k)
                lines.append(f"| {k} | {b} | {n} |")
            lines.append("")
            for c in report["checks"][sid]:
                lines.append(f"- {'PASS' if c['ok'] else 'WARN'} {c['name']} ({c['detail']})")
            lines.append("")
        with open(os.path.join(args.out, "comparison.md"), "w") as fh:
            fh.write("\n".join(lines))
        print("wrote", os.path.join(args.out, "comparison.md"))
    finally:
        if not args.keep_lab:
            stop_lab()


if __name__ == "__main__":
    main()
