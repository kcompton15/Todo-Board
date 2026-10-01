"""Live regression for crowded columns and notes-only expansion. Uses disposable API records."""

import json
import tempfile
import uuid
from pathlib import Path

from acceptance import URL, api
from playwright.sync_api import expect, sync_playwright


def check_subtask_editing(browser, task_id, title):
    page = browser.new_page(viewport={"width": 1440, "height": 800})
    errors, conflicts = [], []
    page.on("pageerror", lambda error: errors.append(str(error)))
    page.on(
        "response",
        lambda response: response.status == 409 and conflicts.append(response.url),
    )
    page.goto(URL)
    page.wait_for_load_state("networkidle")
    page.locator("#everythingView").click()
    page.locator("#search").fill(title)
    card = page.locator('[data-task-id="' + task_id + '"]')
    card.locator(".card-title").click()
    composer = card.locator("[data-quick-sub]")
    composer.click()
    for step in ["Typed step A", "Typed step B", "Typed step C"]:
        page.keyboard.type(step)
        page.keyboard.press("Enter")
    page.wait_for_function(
        "id => document.querySelectorAll(`[data-task-id='${id}'] .subtask-row`).length === 5",
        arg=task_id,
    )
    assert page.evaluate(
        "id => document.activeElement?.dataset.quickSub === id", task_id
    ), "composer lost focus after Enter"
    checks = card.locator(".subcheck")
    checks.nth(0).click()
    checks.nth(1).click()
    page.wait_for_function(
        "id => [...document.querySelectorAll(`[data-task-id='${id}'] .subcheck`)].slice(0, 2).every(c => c.checked)",
        arg=task_id,
    )
    page.wait_for_load_state("networkidle")
    saved = api("GET", "/api/tasks/" + task_id)
    assert [sub["done"] for sub in saved["subtasks"][:2]] == [True, True], saved
    assert [sub["title"] for sub in saved["subtasks"][2:]] == [
        "Typed step A",
        "Typed step B",
        "Typed step C",
    ], saved
    api("PATCH", "/api/tasks/" + task_id, {"tag": "sse-render"})
    expect(card).to_contain_text("sse-render")
    running = card.locator(".subtask-panel").evaluate("e => e.getAnimations().length")
    assert running == 0, f"subtask panel replayed its reveal animation ({running})"
    assert not page.locator("#banner.show").count(), page.locator("#banner").text_content()
    assert not conflicts, conflicts
    assert not errors, errors
    page.close()


def main():
    prefix = "QA-Scroll-" + uuid.uuid4().hex[:10]
    evidence = Path(tempfile.mkdtemp(prefix="workboard-scroll-"))
    baseline = {task["id"]: task for task in api("GET", "/api/tasks")["tasks"]}
    ids, editing_id = [], None
    try:
        inputs = [
            {
                "title": f"{prefix} {index:02d} completed outcome with a readable title",
                "project": prefix,
                "lane": "done",
                "notes": "Notes only, no subtasks.\n<img src=x onerror=alert(1)>\n"
                + "A long line that must wrap safely. " * 40,
            }
            for index in range(18)
        ]
        editing_title = f"{prefix} subtask editing"
        inputs.append(
            {
                "title": editing_title,
                "project": prefix,
                "lane": "today",
                "subtasks": ["Existing step one", "Existing step two"],
            }
        )
        created = api("POST", "/api/tasks", {"tasks": inputs})["tasks"]
        ids = [task["id"] for task in created]
        editing_id = ids.pop()
        (evidence / "ids.json").write_text(json.dumps(ids + [editing_id]))
        with sync_playwright() as p:
            browser = p.chromium.launch()
            check_subtask_editing(browser, editing_id, editing_title)
            page = browser.new_page(
                viewport={"width": 1440, "height": 800}, reduced_motion="reduce"
            )
            errors = []
            page.on("pageerror", lambda error: errors.append(str(error)))
            page.goto(URL)
            page.wait_for_load_state("networkidle")
            page.locator("#everythingView").click()
            page.locator("#doneToggle").click()
            shop = next(
                (
                    task
                    for task in baseline.values()
                    if task["title"] == "Email notification fix"
                ),
                None,
            )
            if shop:
                page.locator("#search").fill(shop["title"])
                card = page.locator('[data-task-id="' + shop["id"] + '"]')
                card.locator(".card-title").click()
                expect(card.locator(".card-notes p")).to_have_text(shop["notes"])
                expect(card.locator(".disclosure")).to_have_attribute(
                    "aria-expanded", "true"
                )
                page.screenshot(
                    path=str(evidence / "email-fix-expanded.png"),
                    full_page=True,
                    animations="disabled",
                )
                card.locator(".card-title").press("Enter")
                expect(card.locator(".subtask-panel")).to_be_hidden()
            page.locator("#search").fill(prefix)
            for width in [1440, 390]:
                page.set_viewport_size({"width": width, "height": 800})
                for theme in ["light", "dark"]:
                    if (
                        page.locator("html").get_attribute("data-theme") or "light"
                    ) != theme:
                        page.locator("#themeBtn").click()
                    for view in ["allView", "projectView"]:
                        page.locator("#" + view).click()
                        lane = page.locator('[data-lane="done"] .lane-body')
                        lane.scroll_into_view_if_needed()
                        sizes = lane.evaluate(
                            "e => ({height:e.clientHeight, total:e.scrollHeight, clipped:[...e.children].some(c => c.scrollHeight > c.clientHeight + 2)})"
                        )
                        assert sizes["total"] > sizes["height"] + 500, sizes
                        assert not sizes["clipped"], sizes
                        lane.hover()
                        page.mouse.wheel(0, 20000)
                        page.wait_for_function(
                            "() => {const e=document.querySelector('[data-lane=done] .lane-body');return e.scrollTop > 500}"
                        )
                        lane.focus()
                        lane.press("End")
                        page.wait_for_function(
                            "() => {const e=document.querySelector('[data-lane=done] .lane-body');return Math.abs(e.scrollHeight-e.clientHeight-e.scrollTop)<2}"
                        )
                        last = lane.locator('[data-task-id="' + ids[-1] + '"]')
                        expect(last.locator(".card-title")).to_be_in_viewport()
                        offset = lane.evaluate("e => e.scrollTop")
                        marker = f"{width}-{theme}-{view}"
                        api("PATCH", "/api/tasks/" + ids[0], {"tag": marker})
                        expect(
                            page.locator('[data-task-id="' + ids[0] + '"]')
                        ).to_contain_text(marker)
                        assert abs(lane.evaluate("e => e.scrollTop") - offset) < 2
                        last.locator(".card-title").focus()
                        last.locator(".card-title").press("Enter")
                        expect(last.locator(".card-notes")).to_be_visible()
                        assert last.locator(".card-notes img").count() == 0
                        assert last.locator(".card-notes").evaluate(
                            "e => e.scrollWidth <= e.clientWidth + 1"
                        )
                        page.screenshot(
                            path=str(evidence / f"{marker}.png"),
                            full_page=True,
                            animations="disabled",
                        )
                        last.locator(".card-title").press("Space")
                        expect(last.locator(".subtask-panel")).to_be_hidden()
                        lane.evaluate("e => e.scrollTop = 0")
            assert not errors, errors
            browser.close()
        print(
            "PASS: subtask composer keeps focus, rapid checks without 409, no reveal replay on SSE, uncompressed cards, wheel/keyboard Done scrolling, SSE scroll retention, notes-only and real card expansion, both views/themes, desktop/390px."
        )
        print("Evidence:", evidence)
    finally:
        for task_id in ids + ([editing_id] if editing_id else []):
            api("DELETE", "/api/tasks/" + task_id)
        current = {task["id"]: task for task in api("GET", "/api/tasks")["tasks"]}
        assert not set(ids) & set(current)
        for task_id, original in baseline.items():
            assert current[task_id] == original, "Real task changed: " + task_id
        print("Exact-ID QA cleanup complete; original tasks unchanged.")


if __name__ == "__main__":
    main()
