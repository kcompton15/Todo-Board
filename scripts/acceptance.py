"""Live acceptance using only exact-ID disposable records.

uv run --with playwright python scripts/acceptance.py prepare /tmp/workboard-qa-UNIQUE
uv run --with playwright python scripts/acceptance.py run /tmp/workboard-qa-UNIQUE --restart
The optional prepare phase must run on the previous build to seed legacy state.
Run always cleans up known QA cards; evidence and original-task snapshots remain
in the supplied directory. It never writes the board's state file.
"""

import json
import os
import subprocess
import sys
import time
import urllib.request
import uuid
from pathlib import Path

from playwright.sync_api import expect, sync_playwright

URL = os.environ.get("TODO_URL", "http://127.0.0.1:7337")
ROOT = Path(__file__).resolve().parent.parent


def api(method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(
        URL + path,
        data=data,
        method=method,
        headers={"Content-Type": "application/json", "X-Actor": "acceptance-agent"},
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        payload = response.read()
        return json.loads(payload) if payload else None


def main():
    phase, directory = sys.argv[1:3]
    output = Path(directory)
    output.mkdir(parents=True, exist_ok=True)
    manifest = output / "manifest.json"
    if phase == "prepare":
        if manifest.exists():
            raise RuntimeError("Use a fresh acceptance directory")
        state = {
            "baseline": api("GET", "/api/tasks")["tasks"],
            "ids": [],
            "prefix": "QA-Trust-" + uuid.uuid4().hex[:10],
        }
        manifest.write_text(json.dumps(state, indent=2))
        legacy = api(
            "POST",
            "/api/tasks",
            {
                "title": state["prefix"] + " legacy",
                "lane": "done",
                "project": state["prefix"],
                "subtasks": [{"title": "Still open", "lane": "today"}],
            },
        )["tasks"][0]
        state["legacy"] = legacy["id"]
        state["ids"].append(legacy["id"])
        manifest.write_text(json.dumps(state, indent=2))
        print("Prepared legacy QA card", legacy["id"])
        return
    if phase != "run":
        raise ValueError("phase must be prepare or run")
    state = (
        json.loads(manifest.read_text())
        if manifest.exists()
        else {
            "baseline": api("GET", "/api/tasks")["tasks"],
            "ids": [],
            "prefix": "QA-Trust-" + uuid.uuid4().hex[:10],
        }
    )
    errors = []
    expected_conflicts = []
    expected_disconnects = []
    restarting = False
    try:
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            page = browser.new_page(
                viewport={"width": 1440, "height": 1050}, reduced_motion="reduce"
            )
            page.on("pageerror", lambda error: errors.append(str(error)))

            def console(message):
                if message.type != "error":
                    return
                if "409" in message.text:
                    expected_conflicts.append(message.text)
                elif (
                    restarting
                    and message.location.get("url", "").endswith("/api/events")
                    and (
                        "ERR_INCOMPLETE_CHUNKED_ENCODING" in message.text
                        or "ERR_CONNECTION" in message.text
                    )
                ):
                    expected_disconnects.append(message.text)
                else:
                    errors.append(message.text)

            page.on("console", console)
            page.goto(URL)
            page.wait_for_load_state("networkidle")
            expect(page.locator("#statusText")).to_have_text("live")
            page.locator("#search").fill(state["prefix"])
            if "legacy" in state:
                legacy_card = page.locator('[data-task-id="' + state["legacy"] + '"]')
                expect(legacy_card).to_be_visible()
                expect(legacy_card).to_contain_text("Open steps · reopen parent")
                context = subprocess.check_output(
                    [str(ROOT / "scripts/todo"), "context"], text=True
                )
                assert state["legacy"] in context, (
                    "legacy open work missing from agent context"
                )
                page.locator("#projectView").click()
                expect(legacy_card).to_be_visible()
                legacy_card.get_by_role("button", name="Edit", exact=True).click()
                page.locator("#fLane").select_option("today")
                page.locator("#saveTask").click()
                expect(page.locator("#drawer")).to_have_attribute("aria-hidden", "true")
                expect(
                    page.locator(
                        '[data-lane="today"] [data-task-id="' + state["legacy"] + '"]'
                    )
                ).to_be_visible()

            page.locator("#allView").click()
            page.locator("#newTask").click()
            title = state["prefix"] + " outcome"
            page.locator("#fTitle").fill(title)
            page.locator("#fProject").fill(state["prefix"])
            page.locator("#newSubtask").fill("Acceptance step")
            page.locator("#newSubtask").press("Enter")
            page.locator("#saveTask").click()
            expect(page.locator(".card-title", has_text=title)).to_be_visible()
            task = next(
                task
                for task in api("GET", "/api/tasks")["tasks"]
                if task["title"] == title
            )
            state["ids"].append(task["id"])
            manifest.write_text(json.dumps(state, indent=2))
            card = page.locator('[data-task-id="' + task["id"] + '"]')
            card.get_by_role("button", name="Edit", exact=True).click()
            expect(page.locator("#completionNotice")).to_contain_text(
                "1 unfinished step"
            )
            page.locator("#fLane").select_option("done")
            page.locator("#saveTask").click()
            expect(page.locator("#banner")).to_contain_text("unfinished subtasks")
            expect(page.locator("#drawerMessage")).to_contain_text(
                "unfinished subtasks"
            )
            expect(page.locator("#drawer")).to_have_attribute("aria-hidden", "false")
            assert api("GET", "/api/tasks/" + task["id"])["lane"] == "today"
            page.locator("#fLane").select_option("today")
            page.locator("#fNotes").fill("My unsaved browser draft")
            api("PATCH", "/api/tasks/" + task["id"], {"notes": "Agent update"})
            expect(page.locator("#editConflict")).to_be_visible()
            expect(page.locator("#saveTask")).to_be_disabled()
            expect(page.locator("#fNotes")).to_have_value("My unsaved browser draft")
            page.screenshot(path=str(output / "desktop-conflict.png"), full_page=True)
            page.once("dialog", lambda dialog: dialog.accept())
            page.locator("#reloadTask").click()
            expect(page.locator("#fNotes")).to_have_value("Agent update")
            expect(page.locator("#taskActivity")).to_contain_text("acceptance-agent")
            expect(page.locator("#taskActivity")).to_contain_text(
                "notes: ∅ → Agent update"
            )
            page.locator("#fNotes").fill("Reconciled browser note")
            page.locator("#saveTask").click()
            expect(page.locator("#drawer")).to_have_attribute("aria-hidden", "true")
            card.get_by_role("button", name="Edit", exact=True).click()
            expect(page.locator("#taskActivity")).to_contain_text(
                "Reconciled browser note"
            )
            page.locator("#closeDrawer").focus()
            page.keyboard.press("Shift+Tab")
            expect(page.locator("#deleteTask")).to_be_focused()
            page.keyboard.press("Tab")
            expect(page.locator("#closeDrawer")).to_be_focused()
            page.keyboard.press("Escape")
            expect(card.get_by_role("button", name="Edit", exact=True)).to_be_focused()
            page.locator("#activityButton").click()
            expect(page.locator("#boardActivity")).to_contain_text(title)
            page.keyboard.press("Escape")
            expect(page.locator("#activityButton")).to_be_focused()

            for width, theme in [
                (1440, "light"),
                (1440, "dark"),
                (390, "light"),
                (390, "dark"),
            ]:
                page.set_viewport_size(
                    {"width": width, "height": 1050 if width == 1440 else 844}
                )
                current = page.locator("html").get_attribute("data-theme") or "light"
                if current != theme:
                    page.locator("#themeBtn").click()
                for view in ["allView", "projectView"]:
                    page.locator("#" + view).click()
                    expect(card).to_be_visible()
                    page.screenshot(
                        path=str(output / f"{width}-{theme}-{view}.png"), full_page=True
                    )
                card.get_by_role("button", name="Edit", exact=True).click()
                expect(page.locator("#taskActivity")).to_contain_text(
                    "Reconciled browser note"
                )
                if width == 390 and theme == "light":
                    page.locator("#fLane").select_option("done")
                    page.locator("#saveTask").click()
                    expect(page.locator("#drawerMessage")).to_contain_text(
                        "unfinished subtasks"
                    )
                    assert api("GET", "/api/tasks/" + task["id"])["lane"] == "today"
                    page.locator("#fLane").select_option("today")
                if width == 390 and theme == "dark":
                    page.locator("#fNotes").fill("Preserved mobile draft")
                    api("PATCH", "/api/tasks/" + task["id"], {"tag": "MobileQA"})
                    expect(page.locator("#editConflict")).to_be_visible()
                    expect(page.locator("#fNotes")).to_have_value(
                        "Preserved mobile draft"
                    )
                    expect(page.locator("#saveTask")).to_be_disabled()
                    page.locator("#editConflict").scroll_into_view_if_needed()
                    page.screenshot(
                        path=str(output / "390-dark-conflict.png"), full_page=True
                    )
                    page.once("dialog", lambda dialog: dialog.accept())
                    page.locator("#reloadTask").click()
                    expect(page.locator("#fTag")).to_have_value("MobileQA")
                    expect(page.locator("#taskActivity")).to_contain_text("MobileQA")
                expect(page.locator("#drawer")).to_have_css("transform", "none")
                assert page.locator("#drawer").bounding_box()["width"] <= width + 0.5
                page.locator("#taskActivity").scroll_into_view_if_needed()
                page.screenshot(
                    path=str(output / f"{width}-{theme}-history.png"), full_page=True
                )
                page.keyboard.press("Escape")
                expect(page.locator("#drawer")).to_have_attribute("aria-hidden", "true")

            card.get_by_role("button", name="Edit", exact=True).click()
            page.locator("[data-drawer-check]").check()
            page.locator("#fLane").select_option("done")
            page.locator("#saveTask").click()
            expect(page.locator("#drawer")).to_have_attribute("aria-hidden", "true")
            expect(card).to_have_count(0)
            page.locator("#doneToggle").click()
            expect(card).to_be_visible()
            page.reload()
            page.wait_for_load_state("networkidle")
            page.locator("#search").fill(state["prefix"])
            expect(card).to_be_visible()
            before_restart = api("GET", "/api/tasks/" + task["id"])
            history_before = api("GET", "/api/activity?taskId=" + task["id"])
            if "--restart" in sys.argv:
                restarting = True
                subprocess.run(
                    ["docker", "compose", "restart", "board"], cwd=ROOT, check=True
                )
                for _ in range(30):
                    try:
                        api("GET", "/api/tasks")
                        break
                    except (OSError, ValueError):
                        time.sleep(0.5)
                assert api("GET", "/api/tasks/" + task["id"]) == before_restart
                assert (
                    api("GET", "/api/activity?taskId=" + task["id"]) == history_before
                )
                page.reload()
                page.wait_for_load_state("networkidle")
                page.locator("#search").fill(state["prefix"])
                expect(card).to_be_visible()
                expect(page.locator("#statusText")).to_have_text("live")
                restarting = False
            history = subprocess.check_output(
                [str(ROOT / "scripts/todo"), "history", task["id"], "--json"], text=True
            )
            assert (
                "Reconciled browser note" in history and "acceptance-agent" in history
            )
            assert not errors, errors
            browser.close()
            print(
                "PASS: completion, actor history, draft conflict/reload, keyboard, both views/themes, 390px, refresh, CLI history; no unexpected console/page errors"
            )
            print(
                "Legacy migration checked:",
                "legacy" in state,
                "Container restart checked:",
                "--restart" in sys.argv,
            )
            print("Expected rejected completion requests:", len(expected_conflicts))
            print(
                "Expected SSE interruptions during container restart:",
                len(expected_disconnects),
            )
    finally:
        exact_title = state["prefix"] + " outcome"
        for task in api("GET", "/api/tasks")["tasks"]:
            if task["title"] == exact_title and task["id"] not in state["ids"]:
                state["ids"].append(task["id"])
        manifest.write_text(json.dumps(state, indent=2))
        existing = {task["id"] for task in api("GET", "/api/tasks")["tasks"]}
        for task_id in state["ids"]:
            if task_id in existing:
                api("DELETE", "/api/tasks/" + task_id)
        after = {task["id"]: task for task in api("GET", "/api/tasks")["tasks"]}
        for original in state["baseline"]:
            current = after[original["id"]].copy()
            if "revision" not in original:
                current.pop("revision", None)
            assert current == original, "Original task changed: " + original["id"]
        assert not set(state["ids"]) & set(after), "QA records remain"
        print("Cleanup verified; original tasks unchanged.")


if __name__ == "__main__":
    main()
