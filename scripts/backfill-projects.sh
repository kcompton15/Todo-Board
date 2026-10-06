#!/usr/bin/env bash
set -euo pipefail
command -v python3 >/dev/null || { printf 'backfill-projects: python3 required\n' >&2; exit 1; }
exec python3 - "$@" <<'PY'
import json, os, re, sys, urllib.parse, urllib.request, urllib.error

def fail(message):
    raise ValueError(message)

def fold_key(value):
    return re.sub(r'[\s_-]+', '-', value.lower()).strip('-')

def base_url():
    base = os.environ.get('TODO_URL', 'http://127.0.0.1:7337').rstrip('/')
    parsed = urllib.parse.urlsplit(base)
    if parsed.scheme != 'http' or parsed.hostname not in ('localhost', '127.0.0.1', '::1') or parsed.username or parsed.path or parsed.query or parsed.fragment:
        fail('TODO_URL must be a loopback HTTP origin')
    return base

def request(method, path, body=None, revision=None):
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs):
            fail('redirect refused')
    headers = {'Content-Type': 'application/json', 'X-Actor': 'backfill-projects'}
    if revision is not None:
        headers['If-Match'] = str(revision)
    req = urllib.request.Request(base_url() + path, data=body, method=method, headers=headers)
    with urllib.request.build_opener(NoRedirect).open(req, timeout=10) as response:
        raw = response.read(16 * 1024 * 1024 + 1)
        if len(raw) > 16 * 1024 * 1024:
            fail('response too large')
        return json.loads(raw)

def canonical_names(tasks, field):
    groups = {}
    for task in tasks:
        value = task.get(field) or ''
        if value:
            counts = groups.setdefault(fold_key(value), {})
            counts[value] = counts.get(value, 0) + 1
    return {key: sorted(counts.items(), key=lambda item: (-item[1], item[0] != item[0].lower(), item[0])) for key, counts in groups.items()}

def near_duplicates(groups):
    keys = sorted(groups)
    parent = {key: key for key in keys}
    def root(key):
        while parent[key] != key:
            key = parent[key]
        return key
    for i, left in enumerate(keys):
        for right in keys[i + 1:]:
            shared = set(left.split('-')) & set(right.split('-'))
            if left.startswith(right) or right.startswith(left) or any(len(part) >= 4 for part in shared):
                parent[root(right)] = root(left)
    clusters = {}
    for key in keys:
        clusters.setdefault(root(key), []).append(key)
    return [members for _, members in sorted(clusters.items()) if len(members) > 1]

def describe(groups, key):
    return ', '.join('%s (%d)' % item for item in groups[key])

def ref(task):
    return '#%d' % task['number'] if task.get('number') else task['id']

def main():
    args = sys.argv[1:]
    if args not in ([], ['--dry-run'], ['--apply']):
        fail('usage: backfill-projects.sh [--dry-run | --apply]')
    apply = args == ['--apply']
    tasks = request('GET', '/api/tasks').get('tasks')
    if not isinstance(tasks, list):
        fail('invalid task response')
    tasks = sorted(tasks, key=lambda t: (t.get('number') or 0, t['id']))
    changes, near = [], []
    for field in ('project', 'tag'):
        groups = canonical_names(tasks, field)
        for key in sorted(groups):
            if len(groups[key]) > 1:
                print('%s: %s -> %s' % (field, describe(groups, key), groups[key][0][0]))
        target = {key: spellings[0][0] for key, spellings in groups.items()}
        for task in tasks:
            value = task.get(field) or ''
            if value and target[fold_key(value)] != value:
                changes.append((task, field, value, target[fold_key(value)]))
        near += [(field, '  ~  '.join(describe(groups, key) for key in members)) for members in near_duplicates(groups)]
    print()
    if changes:
        rows = [('CARD', 'FIELD', 'BEFORE', 'AFTER', 'TITLE')] + [(ref(t), f, b, a, t['title'][:60]) for t, f, b, a in changes]
        widths = [max(len(row[i]) for row in rows) for i in range(4)]
        for row in rows:
            print('  '.join(cell.ljust(widths[i]) for i, cell in enumerate(row[:4])) + '  ' + row[4])
    else:
        print('No drifted spellings.')
    if near:
        print('\nNear-duplicates (report only, not changed):')
        for field, members in near:
            print('  %s: %s' % (field, members))
    by_card = {}
    for task, field, _, after in changes:
        by_card.setdefault(task['id'], (task, {}))[1][field] = after
    if not apply:
        print('\nDry run: %d card(s) would change. Re-run with --apply to write.' % len(by_card))
        return
    failed = 0
    for task, body in by_card.values():
        try:
            updated = request('PATCH', '/api/tasks/' + urllib.parse.quote(task['id'], safe=''), json.dumps(body).encode(), task['revision'])
        except urllib.error.HTTPError as error:
            failed += 1
            print('backfill-projects: %s not updated (HTTP %d); re-run to retry' % (ref(task), error.code), file=sys.stderr)
            continue
        if any(updated.get(field) != value for field, value in body.items()):
            failed += 1
            print('backfill-projects: %s stored %s' % (ref(task), json.dumps({f: updated.get(f) for f in body})), file=sys.stderr)
    print('\nApplied: %d of %d card(s) updated.' % (len(by_card) - failed, len(by_card)))
    if failed:
        sys.exit(1)

try:
    main()
except (ValueError, KeyError, TypeError, OSError, urllib.error.URLError) as error:
    print('backfill-projects: ' + str(error), file=sys.stderr)
    sys.exit(1)
PY
