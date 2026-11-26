# Compiler oracle verdicts on the extractor's external calls (map-coverage-2
# decisions 21, 24, 26). Usage: compare.py <extractor json with ext> <oracle.json>
import json, sys, collections
ext_doc, oracle = json.load(open(sys.argv[1])), json.load(open(sys.argv[2]))
project = {m['name'] for m in ext_doc['modules']}
ctx = {(c['module'], c['name'], c['arity']): c['context'] for c in oracle['contexts']}
events = collections.defaultdict(set)
for e in oracle['calls']:
    events[(e['file'], e['line'], e['name'], e['arity'])].add(e['module'])
verdict = collections.Counter(); wrong = []; nov = collections.Counter(); diff = collections.Counter()
for c in ext_doc['calls']:
    if not c.get('ext'): continue
    t = c['target']; mod = t.get('module')
    key = (c['file'], c['line'], t['name'], t['arity'])
    R = events.get(key)
    if not R:
        cause = 'test file (not compiled)' if c['file'].startswith('test/') else ('query vocabulary' if mod in ('Ecto.Query.API', 'Ecto.Query.WindowAPI') else 'other')
        nov[cause] += 1; continue
    if mod is not None and mod in R and mod not in project:
        verdict['right'] += 1; continue
    projs = [r for r in R if r in project]
    if projs:
        p = projs[0]; g = ctx.get((p, t['name'], t['arity']))
        if g and g not in project: verdict['right: generated into a project module by ' + ('library' )] += 1
        else:
            verdict['WRONG: a project function'] += 1; wrong.append((c['file'], c['line'], mod, t['name'], t['arity'], sorted(R)))
        continue
    if mod is None or mod in R: verdict['right'] += 1
    else:
        verdict['different library module'] += 1; diff[(mod, tuple(sorted(R)))] += 1
total = sum(verdict.values()) + sum(nov.values())
print(f"{total} external rows: {dict(verdict)}")
print(f"no verdict: {dict(nov)}")
for w in wrong[:10]: print("  WRONG", w)
for (m, R), n in diff.most_common(12): print(f"  diff {n:4d} ours={m} compiler={list(R)}")
