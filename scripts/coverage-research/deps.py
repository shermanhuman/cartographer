import sqlite3,os,re,collections,sys,glob
# Store and snapshot: CARTO_DB (default the user store), CARTO_SNAPSHOT
# (default the newest snapshot holding the repo).
db=sqlite3.connect('file:'+os.path.expanduser(os.environ.get('CARTO_DB','~/.local/share/cartographer/carto.db'))+'?mode=ro',uri=True)
repo=sys.argv[1]
S=os.environ.get('CARTO_SNAPSHOT') or db.execute("select f.snapshot_id from files f join snapshots s on s.id=f.snapshot_id where f.repo=? order by s.created_at desc limit 1",(repo,)).fetchone()[0]; deps=sys.argv[2]
names=collections.Counter(); ex=collections.defaultdict(list)
for kind,target,file,line in db.execute("select kind,target,file,line from calls where snapshot_id=? and repo=? and to_id is null and kind in ('local_or_import','component') and instr(target,'.')=0",(S,repo)):
    n=target.rsplit('/',1)[0]; names[n]+=1; ex[n].append((target,file,line))
depnames=set()
pat=re.compile(r'^\s*(?:def|defmacro|defdelegate|defguard)\s+([a-z_][A-Za-z0-9_?!]*)',re.M)
pat2=re.compile(r'^\s*(?:def|defmacro)\s+unquote\(',re.M)
for root in deps.split(','):
  for f in glob.glob(root+'/**/*.ex',recursive=True):
    try: t=open(f,errors='replace').read()
    except: continue
    depnames.update(pat.findall(t))
# elixir stdlib ex_unit etc
miss=[(c,n) for n,c in names.items() if n not in depnames]
miss.sort(reverse=True)
print('distinct names',len(names),'sites',sum(names.values()),'names not defined in deps',len(miss),'sites',sum(c for c,n in miss))
for c,n in miss[:80]:
    print(f"  {c:5d} {n:30s} {ex[n][0]}")
