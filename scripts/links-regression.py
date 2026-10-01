"""Links browser regression. Mutates only disposable cards via API/UI. Run against a board started with TODO_JIRA_SITE=example.atlassian.net."""

import json
import subprocess
import tempfile
import time
import uuid
from pathlib import Path

from acceptance import ROOT, URL, api
from playwright.sync_api import expect, sync_playwright


def main():
    prefix = "QA-LINKS-" + uuid.uuid4().hex[:10]
    evidence = Path(tempfile.mkdtemp(prefix="workboard-links-"))
    baseline = {t["id"]: t for t in api("GET", "/api/tasks")["tasks"]}
    ids = []
    try:
        task = api("POST", "/api/tasks", {
            "title": prefix, "project": prefix, "lane": "today", "kind": "review",
            "agentContext": "Not rendered in ordinary UI", "subtasks": ["API review"],
            "links": [{"ref": "PROJ-931"}, {"ref": "team/api!175", "state": "opened"}],
        })["tasks"][0]
        ids.append(task["id"])
        path = "/api/tasks/" + task["id"]
        api("POST", path + "/links", {"ref": "team/web!126", "role": "reference", "subtaskId": task["subtasks"][0]["id"]})
        initial = api("GET", path)
        (evidence / "ids.json").write_text(json.dumps(ids))
        with sync_playwright() as p:
            browser = p.chromium.launch()
            page = browser.new_page(viewport={"width": 1440, "height": 900})
            errors = []
            page.on("pageerror", lambda error: errors.append(str(error)))
            page.goto(URL)
            page.wait_for_load_state("networkidle")
            page.locator("#everythingView").click()
            page.locator("#search").fill(prefix)
            card = page.locator('[data-task-id="' + task["id"] + '"]')
            card.locator(".card-title").click()
            for width in [1440, 390]:
                page.set_viewport_size({"width": width, "height": 900})
                for view in ["allView", "projectView"]:
                    page.locator("#" + view).click()
                    links = card.locator("a.external-link")
                    expect(links).to_have_count(3)
                    assert links.evaluate_all("els => els.every(e => e.target === '_blank' && e.rel === 'noopener noreferrer' && e.draggable === false)")
                    expect(links.filter(has_text="api!175")).to_have_attribute("href", "https://gitlab.com/team/api/-/merge_requests/175")
                    expect(links.filter(has_text="web!126")).to_contain_text("reference")
                    expect(links.filter(has_text="PROJ-931")).to_have_attribute("href", "https://example.atlassian.net/browse/PROJ-931")
                    assert card.evaluate("e => e.scrollWidth <= e.clientWidth + 1"), "card overflow"
                    page.context.route("https://gitlab.com/**", lambda route: route.fulfill(status=200, body="destination verified"))
                    with page.expect_popup() as opened:
                        links.filter(has_text="api!175").click()
                    popup = opened.value
                    popup.wait_for_load_state()
                    assert popup.url == "https://gitlab.com/team/api/-/merge_requests/175"
                    popup.close()
                    expect(page.locator("#drawer")).not_to_have_class("drawer show")
                    expect(card.locator(".subtask-panel")).to_be_visible()
                    page.screenshot(path=str(evidence / f"{width}-{view}.png"), full_page=True)
            page.set_viewport_size({"width": 1440, "height": 900})
            card.locator("[data-open]").click()
            page.locator("#newLink").fill("PROJ-77")
            page.locator("#stageLink").click()
            page.locator("#fReconcileMode").select_option("manual")
            page.locator("#fTitle").fill(prefix + " cancelled")
            page.locator("#closeDrawer").click()
            assert api("GET", path) == initial, "cancel persisted draft"
            writes = []
            page.on("request", lambda req: writes.append((req.method, req.url)) if req.method != "GET" and "/api/" in req.url else None)
            card.locator("[data-open]").click()
            page.locator('[data-link-role="0"]').select_option("reference")
            page.locator('[data-link-scope="0"]').select_option(task["subtasks"][0]["id"])
            page.locator('[data-remove-link="2"]').click()
            page.locator("#newLink").fill("PROJ-77")
            page.locator("#stageLink").click()
            page.locator("#fReconcileMode").select_option("manual")
            page.locator("#fNotes").fill("Saved alongside draft links")
            page.locator("#saveTask").click()
            expect(page.locator("#drawer")).to_have_attribute("aria-hidden", "true")
            saved = api("GET", path)
            assert writes == [("PUT", URL + path)], writes
            assert saved["reconcileMode"] == "manual" and saved["notes"] == "Saved alongside draft links"
            assert saved["agentContext"] == initial["agentContext"]
            assert len(saved["links"]) == 3 and saved["links"][0]["subtaskId"] == task["subtasks"][0]["id"]
            assert saved["links"][0]["role"] == "reference"
            assert saved["links"][1] == initial["links"][1], "cached state or identity lost"
            expect(card).to_contain_text("Manual")
            card.locator("[data-open]").click()
            page.locator("#newLink").fill("PROJ-999")
            page.locator("#stageLink").click()
            api("PATCH", path, {"tag": "concurrent"})
            expect(page.locator("#saveTask")).to_be_disabled()
            expect(page.locator("#editConflict")).to_be_visible()
            page.locator("#closeDrawer").click()
            assert all(l["ref"] != "PROJ-999" for l in api("GET", path)["links"])
            page.set_viewport_size({"width": 390, "height": 900})
            page.locator("#newTask").click()
            page.locator("#fTitle").fill(prefix + " new")
            page.locator("#newSubtask").fill("Unsaved child")
            page.locator("#newSubtask").press("Enter")
            page.locator("#newLink").fill("PROJ-932")
            page.locator("#stageLink").click()
            expect(page.locator('[data-link-scope="0"] option')).to_have_count(1)
            page.screenshot(path=str(evidence / "mobile-drawer.png"), full_page=True)
            page.locator("#saveTask").click()
            expect(page.locator("#drawer")).to_have_attribute("aria-hidden", "true")
            added = next(t for t in api("GET", "/api/tasks")["tasks"] if t["title"] == prefix + " new")
            ids.append(added["id"])
            assert added["links"][0]["ref"] == "PROJ-932" and len(added["subtasks"]) == 1
            assert not errors, errors
            browser.close()

        receipt = api("POST", path + "/work-log", {"kind": "review_delivered", "text": "Finalized test review", "subtaskId": task["subtasks"][0]["id"]})
        persisted = api("GET", path)
        subprocess.run(["docker", "compose", "restart", "board"], cwd=ROOT, check=True)
        for attempt in range(60):
            try:
                reloaded = api("GET", path)
                break
            except (OSError, ConnectionError):
                if attempt == 59:
                    raise
                time.sleep(0.25)
        assert reloaded == persisted, "restart lost metadata"
        history = api("GET", path + "/history")
        assert any(r["id"] == receipt["id"] and r["workLog"]["kind"] == "review_delivered" for r in history["records"])
        print("PASS: both layouts at desktop/390px; external destinations; cancel; one-PUT draft save; roles/scopes/cache/memo; stale draft; mobile new card; container restart persistence and receipt.")
        print("Evidence:", evidence)
    finally:
        current = api("GET", "/api/tasks")["tasks"]
        for task in current:
            if task["title"] == prefix + " new" and task["id"] not in baseline and task["id"] not in ids:
                ids.append(task["id"])
        for task_id in ids:
            api("DELETE", "/api/tasks/" + task_id)
        after = {t["id"]: t for t in api("GET", "/api/tasks")["tasks"]}
        for task_id, original in baseline.items():
            assert after[task_id] == original, "Real card changed: " + task_id
        print("Removed only disposable cards through API; original cards unchanged.")


if __name__ == "__main__":
    main()
