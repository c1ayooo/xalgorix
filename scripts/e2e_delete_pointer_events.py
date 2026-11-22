#!/usr/bin/env python3
"""E2E regression: sequential run-row deletes must keep the page clickable.

Regression for the "删除一条运行历史后页面卡死" bug (2026-11-22):

    Root cause chain (two independent parts, both fixed):
    1. The bundle shipped TWO instances of @radix-ui/react-dismissable-layer
       (react-dialog@1.3.1 needs ^1.2.1 while the hoisted copy was 1.1.17),
       so dropdown menus and dialogs kept two independent
       layersWithOutsidePointerEventsDisabled sets. Stacked modal layers then
       captured/restored document.body.style.pointerEvents out of order and
       the last writer left `pointer-events: none` on <body> permanently —
       every click on the page dead until a forced reload.
       Fix: package.json "overrides" pins one dismissable-layer version.
    2. scans/findings/scan-detail row menus kept themselves open via
       event.preventDefault() in onSelect while the confirm AlertDialog
       mounted on top (overlapping modal layers even with one copy).
       Fix: the menu now unmounts before the dialog mounts.

    This script drives the real UI: it seeds throwaway scan dirs, logs in,
    deletes three rows back-to-back WITHOUT reloading, and asserts after
    every delete that body pointer-events is restored. Red = any delete
    leaves `pointer-events: none`.

Requirements: python3 + playwright (chromium or system chrome), a running
xalgorix instance, and write access to its data dir for seeding.

Usage:
    XALGORIX_E2E_URL=http://127.0.0.1:9237 \
    XALGORIX_E2E_USER=diag XALGORIX_E2E_PASS=... \
    XALGORIX_DATA_DIR=~/.xalgorix/data \
    python3 scripts/e2e_delete_pointer_events.py
"""

import datetime
import json
import os
import sys

from playwright.sync_api import sync_playwright

BASE = os.environ.get("XALGORIX_E2E_URL", "http://127.0.0.1:9237")
USER = os.environ.get("XALGORIX_E2E_USER", "diag")
PASS = os.environ.get("XALGORIX_E2E_PASS", "diag-pass-12345")
DATA_DIR = os.path.expanduser(os.environ.get("XALGORIX_DATA_DIR", "/tmp/xalg-diag/data"))
ROWS = int(os.environ.get("XALGORIX_E2E_ROWS", "3"))
CHROME = os.environ.get("XALGORIX_E2E_CHROME", "/usr/bin/google-chrome")


def seed_rows(ts: str) -> None:
    """Seed ROWS terminal scan records the server's walker will list.

    The scan dirs are inert (status=stopped, no instance is live) and the
    test deletes them at the end of its own run, leaving nothing behind.
    """
    for i in range(1, ROWS + 1):
        slug = f".e2e{ts}_{i * 7}aaaaaaaaaaaaaaaaaaaaaaaaaa"
        scan_dir = os.path.join(DATA_DIR, f"e2e{ts}-{i}", datetime.date.today().isoformat(), slug)
        os.makedirs(scan_dir, exist_ok=True)
        rec = {
            "id": slug,
            "target": f"e2e{ts}-{i}.invalid",
            "status": "stopped",
            "scan_mode": "single",
            "started_at": (datetime.datetime.now().astimezone() - datetime.timedelta(hours=1)).isoformat(),
            "total_tokens": 1,
            "instance_id": slug.replace("-", "").replace(".", "") + "000000000",
            "events_journal": True,
            "vulns": [],
        }
        with open(os.path.join(scan_dir, "scan.json"), "w", encoding="utf-8") as f:
            json.dump(rec, f)
        with open(os.path.join(scan_dir, "events.jsonl"), "w", encoding="utf-8") as f:
            f.write('{"seq":1,"type":"message","content":"x"}\n')


def body_state(page) -> dict:
    return page.evaluate(
        """(() => ({
            pe: getComputedStyle(document.body).pointerEvents,
            inline: document.body.getAttribute('style'),
        }))()"""
    )


def main() -> int:
    ts = datetime.datetime.now().strftime("%H%M%S")
    seed_rows(ts)
    failures = []

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True, executable_path=CHROME, args=["--no-sandbox"])
        page = browser.new_page()
        page.goto(BASE + "/login", timeout=15000)
        page.wait_for_load_state("domcontentloaded")
        page.locator("input").first.fill(USER)
        page.locator("input[type=password]").fill(PASS)
        page.get_by_role("button", name="登录").click()
        page.wait_for_timeout(1500)
        page.goto(BASE + "/scans", timeout=15000)
        page.wait_for_load_state("domcontentloaded")
        page.wait_for_selector("tbody tr", timeout=15000)

        for i in range(1, ROWS + 1):
            row = page.locator("tbody tr").filter(has_text=f"e2e{ts}-{i}").first
            row.get_by_role("button").last.click()
            page.wait_for_timeout(350)
            page.get_by_role("menuitem").filter(has_text="删除").last.click()
            page.wait_for_timeout(350)
            dlg = page.locator("[role=alertdialog]")
            if not dlg.count():
                failures.append(f"delete-{i}: confirm dialog did not open")
                break
            dlg.get_by_role("button").filter(has_text="删除").click()
            page.wait_for_timeout(900)
            st = body_state(page)
            print(f"delete-{i}: {json.dumps(st)}")
            if st["pe"] != "auto":
                failures.append(f"delete-{i}: body pointer-events stuck at {st['pe']!r}")
            # The page must still accept real clicks for the next row.
            try:
                page.locator("input").first.click(timeout=3000)
            except Exception as exc:  # noqa: BLE001 - report and fail below
                failures.append(f"delete-{i}: page not clickable after delete ({exc})")
                break

        browser.close()

    if failures:
        print("FAIL:")
        for f in failures:
            print("  -", f)
        return 1
    print(f"OK: {ROWS} sequential deletes, body pointer-events restored every time")
    return 0


if __name__ == "__main__":
    sys.exit(main())
