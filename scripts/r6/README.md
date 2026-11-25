# R6 experiment kit

- `lab/` — fixed loopback-only vulnerable app (IDOR, SQLi, traversal, default creds, debug admin, open redirect).
- `scenarios.json` — fixed instruction/env per topology (r7/single/ctf).
- `collect_metrics.py` — read-only PostgreSQL metrics for one instance.
- `run_ab.py` — builds baseline (git ref) + current tree, runs both against the lab, collects and compares metrics.

Protocol, metric definitions and acceptance live in `docs/R6_EXPERIMENT.md`. The kit never edits the repository and never runs the external benchmark/score loop.
