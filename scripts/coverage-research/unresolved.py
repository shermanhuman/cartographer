# The calls a map still leaves unresolved in one repo, by target, with an
# example site: the "real gaps" list. Component calls are left out
# (coverage counts them external, though a project component that doesn't
# resolve is a real gap), and so are calls of kind external and dynamic.
# Captures and apply/3 of use-generated functions keep their kinds and
# still appear here. CARTO_DB and CARTO_SNAPSHOT as in
# bare.py. Usage: python3 unresolved.py <repo> [limit]
import sqlite3, os, sys, collections
db = sqlite3.connect('file:' + os.path.expanduser(os.environ.get('CARTO_DB', '~/.local/share/cartographer/carto.db')) + '?mode=ro', uri=True)
repo = sys.argv[1]
limit = int(sys.argv[2]) if len(sys.argv) > 2 else 40
S = os.environ.get('CARTO_SNAPSHOT') or db.execute(
    "select f.snapshot_id from files f join snapshots s on s.id=f.snapshot_id where f.repo=? order by s.created_at desc limit 1", (repo,)).fetchone()[0]
rows = db.execute("""select kind, target, file, line from calls
  where snapshot_id=? and repo=? and to_id is null and kind not in ('external','dynamic','component')""", (S, repo)).fetchall()
# Library calls keep kind 'remote' with a module outside the project: not
# gaps. Keep bare targets and targets in a project module.
project = {m for (m,) in db.execute("select name from modules where snapshot_id=? and repo=?", (S, repo))}
def gap(t):
    head = t.rsplit('/', 1)[0]
    return '.' not in head or head.rsplit('.', 1)[0] in project
rows = [r for r in rows if gap(r[1])]
by = collections.Counter((k, t) for k, t, _, _ in rows)
ex = {}
for k, t, f, l in rows:
    ex.setdefault((k, t), (f, l))
kinds = collections.Counter(k for k, _, _, _ in rows)
print(f"{repo} {S}: {len(rows)} unresolved project or bare call rows; by kind: {dict(kinds.most_common())}")
for (k, t), n in by.most_common(limit):
    f, l = ex[(k, t)]
    print(f"  {n:5d} {k:16s} {t:45s} e.g. {f}:{l}")
