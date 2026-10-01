"""Live UI polish regression on a throwaway board. Uses disposable API records."""

import json
import sys
import tempfile
import urllib.parse
import uuid
from pathlib import Path

from acceptance import URL, api
from playwright.sync_api import expect, sync_playwright

WIDTHS = [1440, 1280, 1100, 1041, 1040, 900, 800, 601, 600, 390]
SCHEMES = ["light", "dark"]

HEADER_FIT = """() => {
  const width = innerWidth, outside = [], tall = [];
  const name = (el) => el.id || el.textContent.trim();
  for (const el of document.querySelectorAll(".topbar button, .topbar input")) {
    if (!el.getClientRects().length) continue;
    const r = el.getBoundingClientRect();
    if (r.left < -0.5 || r.right > width + 0.5) {
      outside.push([name(el), Math.round(r.left), Math.round(r.right)]);
    }
  }
  for (const el of document.querySelectorAll(".topbar .btn")) {
    if (!el.getClientRects().length) continue;
    const height = el.getBoundingClientRect().height;
    if (height > 38) tall.push([name(el), Math.round(height)]);
  }
  return {
    width,
    scrollWidth: document.documentElement.scrollWidth,
    outside,
    tall,
  };
}"""


def refuse_real_boards():
    port = urllib.parse.urlsplit(URL).port or 7337
    if port in (7337, 7338):
        print(
            f"Refusing to run against {URL}. Set TODO_URL to a throwaway board"
            " such as http://127.0.0.1:7348.",
            file=sys.stderr,
        )
        sys.exit(2)


def open_page(browser, width, scheme, height=None):
    mobile = width <= 390
    context = browser.new_context(
        viewport={"width": width, "height": height or (844 if mobile else 800)},
        is_mobile=mobile,
        has_touch=mobile,
        color_scheme=scheme,
        reduced_motion="reduce",
    )
    page = context.new_page()
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))
    page.goto(URL)
    page.wait_for_load_state("networkidle")
    return context, page, errors


def finish(context, errors, label):
    assert not errors, f"{label}: page errors {errors}"
    context.close()


def check_header_fit(browser, evidence, ids):
    for width in WIDTHS:
        for scheme in SCHEMES:
            label = f"{width} {scheme}"
            context, page, errors = open_page(browser, width, scheme)
            fit = page.evaluate(HEADER_FIT)
            assert fit["scrollWidth"] <= fit["width"], (
                f"{label}: scrollWidth {fit['scrollWidth']} > {fit['width']}"
            )
            assert not fit["outside"], f"{label}: controls off screen {fit['outside']}"
            assert not fit["tall"], f"{label}: wrapped button labels {fit['tall']}"
            page.locator("#newTask").focus()
            scroll_x = page.evaluate("scrollX")
            assert scroll_x == 0, f"{label}: focusing New task scrolled to {scroll_x}"
            page.locator(".topbar").screenshot(
                path=str(evidence / f"header-{width}-{scheme}.png"),
                animations="disabled",
            )
            if width == 1280:
                lanes = page.locator(".board-scroll").evaluate(
                    "e => [e.scrollWidth, e.clientWidth]"
                )
                assert lanes[0] <= lanes[1] + 1, f"{label}: lanes scroll {lanes}"
            if width == 390:
                position = page.locator(".topbar").evaluate(
                    "e => getComputedStyle(e).position"
                )
                assert position == "static", f"{label}: header is {position}"
            if scheme == "dark":
                theme = page.locator("#themeBtn")
                expect(theme).to_have_attribute("aria-pressed", "true")
                before = page.evaluate("getComputedStyle(document.body).backgroundColor")
                theme.click()
                page.wait_for_function(
                    "before => getComputedStyle(document.body).backgroundColor !== before",
                    arg=before,
                    timeout=2000,
                )
                expect(theme).to_have_attribute("aria-pressed", "false")
            finish(context, errors, label)


CHECKS = {
    "header_fit": check_header_fit,
}


def selected_checks():
    args = sys.argv[1:]
    names = list(CHECKS)
    for index, arg in enumerate(args):
        if arg.startswith("--only="):
            names = arg.split("=", 1)[1].split(",")
        elif arg == "--only" and index + 1 < len(args):
            names = args[index + 1].split(",")
    unknown = [name for name in names if name not in CHECKS]
    if unknown:
        raise SystemExit(f"Unknown checks {unknown}; choose from {list(CHECKS)}")
    return names


def seed(prefix):
    billing = f"{prefix}-Billing"
    inputs = {
        "billing": {
            "title": f"{prefix} Billing retry hardening",
            "project": billing,
            "lane": "doing",
            "priority": 1,
            "notes": "Retry failed charges with backoff.\nKeep idempotency keys.\nShip behind a flag.",
            "subtasks": [
                {"title": "Write failing test", "lane": "done"},
                {"title": "Implement backoff", "lane": "doing"},
                {"title": "Update runbook", "lane": "today"},
            ],
            "links": [
                {
                    "ref": "https://gitlab.com/acme/billing/-/merge_requests/175",
                    "role": "required",
                },
                {
                    "ref": "https://gitlab.com/acme/web/-/merge_requests/88",
                    "role": "required",
                },
            ],
        },
        "review": {
            "title": f"{prefix} Review checkout copy",
            "project": f"{prefix}-Web",
            "lane": "today",
            "kind": "review",
        },
        "blocked": {
            "title": f"{prefix} Waiting on vendor credentials",
            "lane": "blocked",
        },
        "backlog_long": {
            "title": f"{prefix} Renewal reminders for long-tail accounts",
            "project": f"{prefix}-Customer Success Operations and Renewals",
            "lane": "backlog",
            "tag": "ExtremelyLongTagNameForTesting",
        },
        "done": {
            "title": f"{prefix} Rotate signing keys",
            "lane": "done",
            "subtasks": [
                {"title": "Generate new key", "lane": "done"},
                {"title": "Retire old key", "lane": "done"},
            ],
        },
    }
    created = api("POST", "/api/tasks", {"tasks": list(inputs.values())})["tasks"]
    ids = {role: task["id"] for role, task in zip(inputs, created)}
    for entry in [
        {"kind": "progress", "text": "Failing test committed"},
        {"kind": "decision", "text": "Generate key client-side"},
    ]:
        api("POST", f"/api/tasks/{ids['billing']}/work-log", entry)
    return ids


def main():
    refuse_real_boards()
    names = selected_checks()
    prefix = "QA-Polish-" + uuid.uuid4().hex[:10]
    evidence = Path(tempfile.mkdtemp(prefix="todo-ui-polish-"))
    baseline = {task["id"]: task for task in api("GET", "/api/tasks")["tasks"]}
    ids = {}
    try:
        ids = seed(prefix)
        ids["prefix"] = prefix
        (evidence / "ids.json").write_text(json.dumps(ids, indent=2))
        with sync_playwright() as p:
            browser = p.chromium.launch()
            for name in names:
                CHECKS[name](browser, evidence, ids)
            browser.close()
        print("PASS:", ", ".join(names))
        print("Evidence:", evidence)
    finally:
        created = [value for key, value in ids.items() if key != "prefix"]
        for task_id in created:
            api("DELETE", "/api/tasks/" + task_id)
        current = {task["id"]: task for task in api("GET", "/api/tasks")["tasks"]}
        assert not set(created) & set(current), "QA cards remain"
        for task_id, original in baseline.items():
            assert current[task_id] == original, "Real task changed: " + task_id
        print("Exact-ID QA cleanup complete; original tasks unchanged.")


if __name__ == "__main__":
    main()
