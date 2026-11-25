import sqlite3,os,re,collections,sys
# Store and snapshot: CARTO_DB (default the user store), CARTO_SNAPSHOT
# (default the newest snapshot holding the repo).
db=sqlite3.connect('file:'+os.path.expanduser(os.environ.get('CARTO_DB','~/.local/share/cartographer/carto.db'))+'?mode=ro',uri=True)
repo=sys.argv[1]
S=os.environ.get('CARTO_SNAPSHOT') or db.execute("select f.snapshot_id from files f join snapshots s on s.id=f.snapshot_id where f.repo=? order by s.created_at desc limit 1",(repo,)).fetchone()[0]
fns=db.execute("select module,name,arity,min_arity,kind,file from functions where snapshot_id=? and repo=?",(S,repo)).fetchall()
defs=collections.defaultdict(list)
for module,name,arity,mina,kind,file in fns:
    if kind in ('def','defmacro','defdelegate','defguard'):
        for a in range(mina,arity+1): defs[(name,a)].append((module,file))
rows=db.execute("select kind,target,file,line,from_id from calls where snapshot_id=? and repo=? and to_id is null and kind in ('local_or_import','component') and instr(target,'.')=0",(S,repo)).fetchall()
tot=0; hit=collections.Counter(); hits=collections.defaultdict(list); test=0; testhit=0
for kind,target,file,line,frm in rows:
    name,ar=target.rsplit('/',1); ar=int(ar)
    tot+=1
    istest=file.startswith('test/')
    test+=istest
    if (name,ar) in defs:
        hit[target]+=1; hits[target].append((file,line)); testhit+=istest
print(repo,'bare unresolved',tot,'in test files',test,'name/arity defined publicly by some project module',sum(hit.values()),'of which in tests',testhit)
for t,c in hit.most_common(70):
    mods=sorted(set(m.split(':',1)[1] for m,f in defs[(t.rsplit('/',1)[0],int(t.rsplit('/',1)[1]))]))
    print(f"  {c:5d} {t:35s} defined in {len(mods)}: {', '.join(mods[:3])}  e.g. {hits[t][0]}")
