#!/usr/bin/env python3
"""Fold project and tag spellings already on the board. Dry run by default; --apply writes."""

import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

ACTOR = "backfill-projects"
MAX_RESPONSE = 16 * 1024 * 1024


class BackfillError(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise BackfillError("redirect refused")


def fold_key(value):
    return re.sub(r"[\s_-]+", "", value.lower())


def base_url():
    base = os.environ.get("TODO_URL", "http://127.0.0.1:7337").rstrip("/")
    parsed = urllib.parse.urlsplit(base)
    if (
        parsed.scheme != "http"
        or parsed.hostname not in ("localhost", "127.0.0.1", "::1")
        or parsed.username
        or parsed.path
        or parsed.query
        or parsed.fragment
    ):
        raise BackfillError("TODO_URL must be a loopback HTTP origin")
    return base


def request(method, path, body=None, revision=None):
    headers = {"Content-Type": "application/json", "X-Actor": ACTOR}
    if revision is not None:
        headers["If-Match"] = str(revision)
    req = urllib.request.Request(base_url() + path, data=body, method=method, headers=headers)
    with urllib.request.build_opener(NoRedirect).open(req, timeout=10) as response:
        raw = response.read(MAX_RESPONSE + 1)
    if len(raw) > MAX_RESPONSE:
        raise BackfillError("response too large")
    return json.loads(raw)


def spellings_by_key(tasks, field):
    """Map each fold key to its spellings, most used first, then lowercase, then alphabetical."""
    groups = {}
    for task in tasks:
        value = task.get(field) or ""
        if value:
            counts = groups.setdefault(fold_key(value), {})
            counts[value] = counts.get(value, 0) + 1
    return {
        key: sorted(counts.items(), key=lambda item: (-item[1], item[0] != item[0].lower(), item[0]))
        for key, counts in groups.items()
    }


def near_duplicates(groups):
    """Cluster keys where one is a prefix of another or they share a segment of 4+ characters."""
    keys = sorted(groups)
    parent = {key: key for key in keys}

    def root(key):
        while parent[key] != key:
            key = parent[key]
        return key

    for i, left in enumerate(keys):
        for right in keys[i + 1 :]:
            shared = set(left.split("-")) & set(right.split("-"))
            if left.startswith(right) or right.startswith(left) or any(len(part) >= 4 for part in shared):
                parent[root(right)] = root(left)
    clusters = {}
    for key in keys:
        clusters.setdefault(root(key), []).append(key)
    return [members for _, members in sorted(clusters.items()) if len(members) > 1]


def describe(groups, key):
    return ", ".join("%s (%d)" % item for item in groups[key])


def card_ref(task):
    return "#%d" % task["number"] if task.get("number") else task["id"]


def print_changes(changes):
    if not changes:
        print("No drifted spellings.")
        return
    rows = [("CARD", "FIELD", "BEFORE", "AFTER", "TITLE")]
    rows += [(card_ref(task), field, before, after, task["title"][:60]) for task, field, before, after in changes]
    widths = [max(len(row[i]) for row in rows) for i in range(4)]
    for row in rows:
        print("  ".join(cell.ljust(widths[i]) for i, cell in enumerate(row[:4])) + "  " + row[4])


def apply_changes(by_card):
    failed = 0
    for task, body in by_card.values():
        path = "/api/tasks/" + urllib.parse.quote(task["id"], safe="")
        try:
            updated = request("PATCH", path, json.dumps(body).encode(), task["revision"])
        except urllib.error.HTTPError as error:
            failed += 1
            print("%s: %s not updated (HTTP %d); re-run to retry" % (ACTOR, card_ref(task), error.code), file=sys.stderr)
            continue
        if any(updated.get(field) != value for field, value in body.items()):
            failed += 1
            stored = json.dumps({field: updated.get(field) for field in body})
            print("%s: %s stored %s" % (ACTOR, card_ref(task), stored), file=sys.stderr)
    print("\nApplied: %d of %d card(s) updated." % (len(by_card) - failed, len(by_card)))
    return failed


def main(args):
    if args not in ([], ["--dry-run"], ["--apply"]):
        raise BackfillError("usage: backfill-projects.py [--dry-run | --apply]")
    tasks = request("GET", "/api/tasks").get("tasks")
    if not isinstance(tasks, list):
        raise BackfillError("invalid task response")
    tasks.sort(key=lambda task: (task.get("number") or 0, task["id"]))
    changes, near = [], []
    for field in ("project", "tag"):
        groups = spellings_by_key(tasks, field)
        for key in sorted(groups):
            if len(groups[key]) > 1:
                print("%s: %s -> %s" % (field, describe(groups, key), groups[key][0][0]))
        target = {key: spellings[0][0] for key, spellings in groups.items()}
        for task in tasks:
            value = task.get(field) or ""
            if value and target[fold_key(value)] != value:
                changes.append((task, field, value, target[fold_key(value)]))
        near += [(field, "  ~  ".join(describe(groups, key) for key in members)) for members in near_duplicates(groups)]
    print()
    print_changes(changes)
    if near:
        print("\nNear-duplicates (report only, not changed):")
        for field, members in near:
            print("  %s: %s" % (field, members))
    by_card = {}
    for task, field, _, after in changes:
        by_card.setdefault(task["id"], (task, {}))[1][field] = after
    if args != ["--apply"]:
        print("\nDry run: %d card(s) would change. Re-run with --apply to write." % len(by_card))
        return 0
    return 1 if apply_changes(by_card) else 0


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except (BackfillError, KeyError, TypeError, ValueError, OSError) as error:
        print("%s: %s" % (ACTOR, error), file=sys.stderr)
        sys.exit(1)
