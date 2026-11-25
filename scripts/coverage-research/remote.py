# Classify a repo's call rows that have no in-repo target and name a module:
# is the module.function/arity provable from the dependency index (name-level,
# as index_deps.exs builds it), the Elixir runtime's exports, or OTP?
import json, sqlite3, sys, collections
repo, idx_file, std_file, db_file, snap = sys.argv[1:6]
idx = json.load(open(idx_file)); std = json.load(open(std_file))
ex, otp = std['elixir'], std['otp']
db = sqlite3.connect('file:' + db_file + '?mode=ro', uri=True)
mods = {r[0] for r in db.execute("select name from modules where snapshot_id=? and repo=?", (snap, repo))} if True else set()
rows = db.execute("select target, kind, file, line from calls where snapshot_id=? and repo=? and to_id is null and kind in ('remote','capture','apply','import','component')", (snap, repo)).fetchall()
c = collections.Counter(); ex_c = collections.defaultdict(collections.Counter)
for t, kind, f, l in rows:
    head, _, ar = t.rpartition('/')
    if '.' not in head: c['bare (no module)'] += 1; continue
    mod, name = head.rsplit('.', 1); ar = int(ar)
    if mod.startswith(':'):
        m = mod[1:]
        k = 'erlang: OTP module' if m in otp else 'erlang: not OTP (an Erlang dependency or unknown)'
    elif mod in mods: k = 'project module, function not found'
    elif mod in ex:
        e = ex[mod]; k = 'elixir runtime: export found' if [name, ar] in e['f'] or [name, ar] in e['m'] else 'elixir runtime: module found, function/arity NOT exported'
    elif mod in idx:
        e = idx[mod]; k = 'dependency index: export found' if any(n == name and lo <= ar <= hi for n, lo, hi in e['exports']) else 'dependency index: module found, function/arity NOT in source defs'
    else: k = 'module in neither index (not project, not dependency, not runtime)'
    c[k] += 1; ex_c[k][t] += 1
print(repo, len(rows), 'rows with no in-repo target, kinds remote/capture/apply/import/component')
for k, n in c.most_common():
    print(f'{n:6d} {k}')
    if 'NOT' in k or 'neither' in k or 'not OTP' in k or 'project module' in k:
        print('        ' + '; '.join(f'{t} x{m}' for t, m in ex_c[k].most_common(14)))
