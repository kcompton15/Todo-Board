"""Live UI polish regression on a throwaway board. Uses disposable API records."""

import json
import re
import sys
import tempfile
import urllib.parse
import uuid
from pathlib import Path

from acceptance import URL, api
from playwright.sync_api import TimeoutError as PlaywrightTimeout
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


DROPDOWN_GEOMETRY = """() => {
  const box = (id) => {
    const r = document.getElementById(id).getBoundingClientRect();
    return { left: r.left, top: r.top, right: r.right, bottom: r.bottom };
  };
  return {
    button: box("projectFilterButton"),
    list: box("projectFilterList"),
    width: innerWidth,
    height: innerHeight,
  };
}"""

ACTIVE_OPTION = """() => {
  const id = document.getElementById("projectFilterButton")
    .getAttribute("aria-activedescendant");
  return id ? document.getElementById(id).textContent : null;
}"""

SELECTION = """() => {
  const items = [...document.querySelectorAll("#projectFilterList [role=option]")];
  const pick = (test) => items.filter(test).map((item) => item.textContent);
  return {
    selected: pick((item) => item.getAttribute("aria-selected") === "true"),
    committed: pick((item) => item.classList.contains("committed")),
  };
}"""


def dropdown_parts(page):
    return (
        page.locator("#projectFilterButton"),
        page.locator("#projectFilterList"),
        page.locator("#projectFilter"),
    )


def expect_closed(button, listbox):
    expect(button).to_have_attribute("aria-expanded", "false")
    assert not listbox.evaluate("e => e.matches(':popover-open')")


def expect_selection(page, selected, committed, label):
    state = page.evaluate(SELECTION)
    expected = {"selected": [selected], "committed": [committed]}
    assert state == expected, f"{label}: selection {state}, wanted {expected}"


def expect_within_window(page, label):
    geo = page.evaluate(DROPDOWN_GEOMETRY)
    list_box = geo["list"]
    assert list_box["top"] >= 7.5, f"{label}: {geo}"
    assert list_box["left"] >= 7.5, f"{label}: {geo}"
    assert list_box["bottom"] <= geo["height"] - 7.5, f"{label}: {geo}"
    assert list_box["right"] <= geo["width"] - 7.5, f"{label}: {geo}"


def check_project_dropdown(browser, evidence, ids):
    prefix = ids["prefix"]
    billing = f"{prefix}-Billing"
    for width, scheme in [(1280, "light"), (1280, "dark"), (390, "light"), (390, "dark")]:
        label = f"dropdown {width} {scheme}"
        context, page, errors = open_page(browser, width, scheme)
        button, listbox, select = dropdown_parts(page)
        expect(select).to_be_hidden()
        assert select.evaluate("e => e.tabIndex") == -1, label
        page.locator("#search").focus()
        page.keyboard.press("Tab")
        assert page.evaluate("document.activeElement.id") == "projectFilterButton", label
        padding, position = button.evaluate(
            "e => { const s = getComputedStyle(e); return [parseFloat(s.paddingRight), s.backgroundPosition]; }"
        )
        assert padding >= 28, f"{label}: padding-right {padding}"
        assert "16px" in position and "11px" in position, f"{label}: chevron at {position}"

        page.keyboard.press("ArrowDown")
        expect(button).to_have_attribute("aria-expanded", "true")
        assert listbox.evaluate("e => e.matches(':popover-open')"), label
        geo = page.evaluate(DROPDOWN_GEOMETRY)
        assert geo["button"]["bottom"] <= geo["list"]["top"] <= geo["button"]["bottom"] + 6, (
            f"{label}: list not under button {geo}"
        )
        assert geo["list"]["left"] >= 7.5, f"{label}: list left {geo}"
        assert geo["list"]["right"] <= geo["width"] - 7.5, f"{label}: list right {geo}"
        page.screenshot(
            path=str(evidence / f"dropdown-open-{width}-{scheme}.png"),
            animations="disabled",
        )
        expect_selection(page, "All projects", "All projects", label)
        first = button.get_attribute("aria-activedescendant")
        page.keyboard.press("ArrowDown")
        assert button.get_attribute("aria-activedescendant") != first, label
        expect_selection(page, page.evaluate(ACTIVE_OPTION), "All projects", label)
        page.keyboard.press("End")
        assert page.evaluate(ACTIVE_OPTION) == "Unassigned", label
        page.keyboard.press("Home")
        assert page.evaluate(ACTIVE_OPTION) == "All projects", label
        page.keyboard.press(prefix[0].lower())
        assert page.evaluate(ACTIVE_OPTION) == billing, page.evaluate(ACTIVE_OPTION)
        page.keyboard.press("Enter")
        expect_closed(button, listbox)
        assert page.evaluate("document.activeElement.id") == "projectFilterButton", label
        expect(button).to_have_text(billing)
        expect_selection(page, billing, billing, label)
        shown = page.locator(".card[data-task-id]").evaluate_all(
            "cards => cards.map(card => card.dataset.taskId)"
        )
        assert shown == [ids["billing"]], f"{label}: filtered cards {shown}"

        page.keyboard.press("ArrowDown")
        page.keyboard.press("ArrowDown")
        expect_selection(page, page.evaluate(ACTIVE_OPTION), billing, label)
        page.keyboard.press("Escape")
        expect_closed(button, listbox)
        assert select.evaluate("e => e.value") == billing, label
        expect_selection(page, billing, billing, label)

        page.keyboard.press("ArrowDown")
        page.keyboard.press("Home")
        expect_selection(page, "All projects", billing, label)
        page.keyboard.press("Tab")
        expect_closed(button, listbox)
        assert page.evaluate("document.activeElement.id") == "doneToggle", label
        assert select.evaluate("e => e.value") == billing, label
        expect_selection(page, billing, billing, label)

        button.click()
        expect(button).to_have_attribute("aria-expanded", "true")
        listbox.get_by_role("option", name="All projects", exact=True).click()
        expect_closed(button, listbox)
        assert select.evaluate("e => e.value") == "", label
        expect(button).to_have_text("All projects")

        button.click()
        expect(button).to_have_attribute("aria-expanded", "true")
        page.keyboard.press("ArrowDown")
        expect_selection(page, page.evaluate(ACTIVE_OPTION), "All projects", label)
        page.locator("#summary").click(position={"x": 4, "y": 4})
        expect_closed(button, listbox)
        assert select.evaluate("e => e.value") == "", label
        expect_selection(page, "All projects", "All projects", label)

        button.click()
        renamed = f"{prefix}-Renamed"
        api("PATCH", "/api/tasks/" + ids["blocked"], {"project": renamed})
        expect(listbox.get_by_role("option", name=renamed, exact=True)).to_be_visible(
            timeout=2000
        )
        expect(button).to_have_attribute("aria-expanded", "true")
        api("PATCH", "/api/tasks/" + ids["blocked"], {"project": ""})
        expect(listbox.get_by_role("option", name=renamed, exact=True)).to_have_count(0)
        page.keyboard.press("Escape")
        expect_closed(button, listbox)
        finish(context, errors, label)

    for height in [260, 180]:
        label = f"dropdown 1280x{height}"
        context, page, errors = open_page(browser, 1280, "light", height=height)
        button, listbox, _ = dropdown_parts(page)
        button.click()
        expect(button).to_have_attribute("aria-expanded", "true")
        expect_within_window(page, label)
        page.screenshot(
            path=str(evidence / f"dropdown-open-short-{height}.png"),
            animations="disabled",
        )
        finish(context, errors, label)


SELECT_STYLES = """() => [...document.querySelectorAll("select")]
  .filter((e) => e.getClientRects().length)
  .map((e) => {
    const s = getComputedStyle(e);
    return {
      name: e.id || e.getAttribute("aria-label") || e.className,
      appearance: s.appearance,
      image: s.backgroundImage,
      padding: parseFloat(s.paddingRight),
    };
  })"""

FOCUS_STYLE = """() => {
  const color = (token) => {
    const probe = document.createElement("span");
    probe.style.cssText = `transition: none; color: var(${token})`;
    document.body.append(probe);
    const value = getComputedStyle(probe).color;
    probe.remove();
    return value;
  };
  const ring = color("--ring"), accent = color("--accent");
  const el = document.activeElement;
  const s = getComputedStyle(el);
  const r = el.getBoundingClientRect();
  return {
    outline: s.outlineStyle !== "none" && parseFloat(s.outlineWidth) > 0,
    halo: s.boxShadow.includes(ring) && s.borderTopColor === accent,
    box: { x: r.x, y: r.y, width: r.width, height: r.height },
  };
}"""


def tab_to(page, selector, label, limit=120):
    for _ in range(limit):
        page.keyboard.press("Tab")
        if page.evaluate("s => document.activeElement.matches(s)", selector):
            return
    raise AssertionError(f"{label}: Tab never reached {selector}")


def expect_focus_ring(page, evidence, name, label):
    try:
        page.wait_for_function(
            f"() => {{ const s = ({FOCUS_STYLE})(); return s.outline || s.halo; }}",
            timeout=2000,
        )
    except PlaywrightTimeout:
        raise AssertionError(
            f"{label}: no focus ring on {name} {page.evaluate(FOCUS_STYLE)}"
        ) from None
    style = page.evaluate(FOCUS_STYLE)
    box, pad = style["box"], 10
    viewport = page.viewport_size
    x, y = max(box["x"] - pad, 0), max(box["y"] - pad, 0)
    page.screenshot(
        path=str(evidence / f"focus-{name}-{label.split()[-1]}.png"),
        clip={
            "x": x,
            "y": y,
            "width": min(box["width"] + 2 * pad, viewport["width"] - x),
            "height": min(box["height"] + 2 * pad, viewport["height"] - y),
        },
        animations="disabled",
    )


def check_controls_style(browser, evidence, ids):
    billing = ids["billing"]
    for scheme in SCHEMES:
        label = f"controls 1280 {scheme}"
        context, page, errors = open_page(browser, 1280, scheme)
        card = page.locator(f'[data-task-id="{billing}"]')
        card.locator(".card-title").click()
        expect(card.locator(".sub-lane").first).to_be_visible()
        page.locator("body").click(position={"x": 2, "y": 2})

        page.evaluate("document.activeElement.blur()")
        for name, selector in [
            ("search", "#search"),
            ("project", "#projectFilterButton"),
            ("done", "#doneToggle"),
            ("card-title", ".card-title"),
            ("lane-add", ".lane-add input"),
        ]:
            tab_to(page, selector, label)
            expect_focus_ring(page, evidence, name, label)

        card.locator("[data-open]").click()
        expect(page.locator("#fTitle")).to_be_focused()
        page.keyboard.press("Tab")
        expect(page.locator("#fLane")).to_be_focused()
        expect_focus_ring(page, evidence, "fLane", label)

        selects = page.evaluate(SELECT_STYLES)
        assert len(selects) >= 8, f"{label}: only {len(selects)} visible selects"
        for item in selects:
            assert item["appearance"] == "none", f"{label}: {item}"
            assert "linear-gradient" in item["image"], f"{label}: no chevron {item}"
            assert item["padding"] >= 24, f"{label}: tight chevron {item}"
        radius = page.evaluate(
            "() => ['workLogKind', 'fLane'].map((id) => getComputedStyle(document.getElementById(id)).borderRadius)"
        )
        assert radius[0] == radius[1], f"{label}: work log radius {radius}"

        page.locator("#drawer").screenshot(
            path=str(evidence / f"controls-drawer-{scheme}.png"), animations="disabled"
        )
        page.locator("#workLogKind").scroll_into_view_if_needed()
        page.locator("#drawer").screenshot(
            path=str(evidence / f"controls-drawer-links-{scheme}.png"),
            animations="disabled",
        )
        page.keyboard.press("Escape")
        expect(page.locator("#drawer")).to_have_attribute("aria-hidden", "true")
        card.screenshot(
            path=str(evidence / f"controls-card-{scheme}.png"), animations="disabled"
        )
        finish(context, errors, label)


LINK_ROWS = """() => [...document.querySelectorAll("#draftLinks .link-row")].map((row) => {
  const box = (selector) => {
    const r = row.querySelector(selector).getBoundingClientRect();
    return { top: r.top, bottom: r.bottom, middle: (r.top + r.bottom) / 2 };
  };
  return {
    ref: box(".link-ref"),
    scope: box("[data-link-scope]"),
    role: box("[data-link-role]"),
    remove: box("[data-remove-link]"),
    text: row.querySelector(".link-ref").textContent,
  };
})"""

LONGEST_TEXT = """(root) => {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  let longest = "";
  while (walker.nextNode()) {
    const text = walker.currentNode.textContent.trim();
    if (text.length > longest.length) longest = text;
  }
  return longest;
}"""


def middle(locator):
    box = locator.bounding_box()
    return box["y"] + box["height"] / 2


def check_drawer(browser, evidence, ids):
    billing = ids["billing"]
    for width, scheme in [(1280, "light"), (1280, "dark"), (390, "light"), (390, "dark")]:
        label = f"drawer {width} {scheme}"
        context, page, errors = open_page(browser, width, scheme)
        drawer = page.locator("#drawer")
        assert drawer.evaluate("e => e.inert && e.hasAttribute('inert')"), label
        page.locator(".lane-add input").last.focus()
        for step in range(60):
            page.keyboard.press("Tab")
            inside = page.evaluate(
                "document.getElementById('drawer').contains(document.activeElement)"
            )
            assert not inside, f"{label}: Tab {step + 1} focused the closed drawer"

        edit = page.locator(f'[data-task-id="{billing}"] [data-open]')
        edit.click()
        expect(page.locator("#fTitle")).to_be_focused()
        assert not drawer.evaluate("e => e.inert"), label
        page.locator("#drawer").screenshot(
            path=str(evidence / f"drawer-top-{width}-{scheme}.png"), animations="disabled"
        )

        rows = page.evaluate(LINK_ROWS)
        assert len(rows) == 2, f"{label}: link rows {rows}"
        for row in rows:
            if width >= 1280:
                spread = [row[part]["middle"] for part in ("ref", "scope", "role", "remove")]
                assert max(spread) - min(spread) <= 4, f"{label}: link row on many lines {row}"
            else:
                assert abs(row["scope"]["top"] - row["role"]["top"]) <= 4, f"{label}: {row}"
                assert row["ref"]["bottom"] <= row["scope"]["top"], f"{label}: {row}"
                assert row["remove"]["bottom"] <= row["scope"]["top"], f"{label}: {row}"
        page.locator("#draftLinks").scroll_into_view_if_needed()
        page.locator("#drawer").screenshot(
            path=str(evidence / f"drawer-links-{width}-{scheme}.png"), animations="disabled"
        )

        kind, add = page.locator("#workLogKind"), page.locator("#addWorkLog")
        assert abs(middle(kind) - middle(add)) <= 4, f"{label}: work log controls split"
        assert page.locator("#workLogText").bounding_box()["y"] < kind.bounding_box()["y"], label
        entry_text = f"Vendor sandbox down ({scheme} {width})"
        page.locator("#workLogText").fill(entry_text)
        kind.select_option("blocker")
        add.click()
        entry = page.locator("#workLog li").filter(has=page.get_by_text(entry_text, exact=True))
        expect(entry).to_have_count(1)
        badge = entry.locator('.log-kind[data-kind="blocker"]')
        expect(badge).to_be_visible()
        expect(badge).to_have_text("blocker")
        expect(page.locator('#workLog .log-kind[data-kind="progress"]')).to_have_count(1)
        page.locator("#workLog").scroll_into_view_if_needed()
        page.locator("#drawer").screenshot(
            path=str(evidence / f"drawer-worklog-{width}-{scheme}.png"), animations="disabled"
        )

        activity = page.locator("#taskActivity")
        expect(activity.locator(".activity-list")).to_be_visible()
        longest = activity.evaluate(LONGEST_TEXT)
        assert len(longest) <= 200, f"{label}: activity text {len(longest)} chars"
        clipped = activity.locator("span[title]").first
        expect(clipped).to_have_text(re.compile(r"^.{140}…$"))
        assert len(clipped.get_attribute("title")) > 140, f"{label}: clipped title"

        page.keyboard.press("Escape")
        expect(drawer).to_have_attribute("aria-hidden", "true")
        assert drawer.evaluate("e => e.inert"), label
        expect(edit).to_be_focused()

        if (width, scheme) == (1280, "light"):
            edit.click()
            expect(page.locator("#fTitle")).to_be_focused()
            page.locator('[data-link-role="0"]').select_option("reference")
            page.locator("#saveTask").click()
            expect(drawer).to_have_attribute("aria-hidden", "true")
            saved = api("GET", "/api/tasks/" + billing)
            roles = [link["role"] for link in saved.get("task", saved)["links"]]
            assert roles == ["reference", "required"], f"{label}: saved roles {roles}"
        finish(context, errors, label)


CHECKS = {
    "header_fit": check_header_fit,
    "project_dropdown": check_project_dropdown,
    "controls_style": check_controls_style,
    "drawer": check_drawer,
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


def seed(prefix, ids):
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
    ids.update({role: task["id"] for role, task in zip(inputs, created)})
    for entry in [
        {"kind": "progress", "text": "Failing test committed"},
        {"kind": "decision", "text": "Generate key client-side"},
    ]:
        api("POST", f"/api/tasks/{ids['billing']}/work-log", entry)


def cleanup(ids, baseline):
    created = [value for key, value in ids.items() if key != "prefix"]
    problems = []
    for task_id in created:
        try:
            api("DELETE", "/api/tasks/" + task_id)
        except (OSError, ValueError) as error:
            problems.append(f"DELETE {task_id}: {error}")
    try:
        current = {task["id"]: task for task in api("GET", "/api/tasks")["tasks"]}
    except (OSError, ValueError) as error:
        return problems + [f"GET /api/tasks: {error}"]
    remaining = sorted(set(created) & set(current))
    if remaining:
        problems.append(f"QA cards remain: {remaining}")
    for task_id, original in baseline.items():
        if current.get(task_id) != original:
            problems.append("Real task changed: " + task_id)
    return problems


def main():
    refuse_real_boards()
    names = selected_checks()
    prefix = "QA-Polish-" + uuid.uuid4().hex[:10]
    evidence = Path(tempfile.mkdtemp(prefix="todo-ui-polish-"))
    baseline = {task["id"]: task for task in api("GET", "/api/tasks")["tasks"]}
    ids = {"prefix": prefix}
    try:
        seed(prefix, ids)
        (evidence / "ids.json").write_text(json.dumps(ids, indent=2))
        with sync_playwright() as p:
            browser = p.chromium.launch()
            for name in names:
                CHECKS[name](browser, evidence, ids)
            browser.close()
        print("PASS:", ", ".join(names))
        print("Evidence:", evidence)
    finally:
        problems = cleanup(ids, baseline)
        for problem in problems:
            print("Cleanup problem:", problem, file=sys.stderr)
        if not problems:
            print("Exact-ID QA cleanup complete; original tasks unchanged.")
    if problems:
        sys.exit(1)


if __name__ == "__main__":
    main()
