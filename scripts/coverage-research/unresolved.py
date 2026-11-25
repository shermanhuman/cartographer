# The calls a map still leaves unresolved in one repo, by target, with an
# example site: the "real gaps" list. CARTO_DB and CARTO_SNAPSHOT as in
# bare.py. Usage: python3 unresolved.py <repo> [limit]
import sqlite3, os, sys, collections
db = sqlite3.connect('file:' + os.path.expanduser(os.environ.get('CARTO_DB', '~/.local/share/cartographer/carto.db')) + '?mode=ro', uri=True)
repo = sys.argv[1]
limit = int(sys.argv[2]) if len(sys.argv) > 2 else 40
S = os.environ.get('CARTO_SNAPSHOT') or db.execute(
    "select f.snapshot_id from files f join snapshots s on s.id=f.snapshot_id where f.repo=? order by s.created_at desc limit 1", (repo,)).fetchone()[0]
rows = db.execute("""select kind, target, file, line from calls
  where snapshot_id=? and repo=? and to_id is null and kind not in ('external','dynamic')""", (S, repo)).fetchall()
by = collections.Counter((k, t) for k, t, _, _ in rows)
ex = {}
for k, t, f, l in rows:
    ex.setdefault((k, t), (f, l))
kinds = collections.Counter(k for k, _, _, _ in rows)
print(f"{repo} {S}: {len(rows)} unresolved call rows; by kind: {dict(kinds.most_common())}")
for (k, t), n in by.most_common(limit):
    f, l = ex[(k, t)]
    print(f"  {n:5d} {k:16s} {t:45s} e.g. {f}:{l}")
