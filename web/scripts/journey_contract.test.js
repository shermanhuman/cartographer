import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {journeys, domains, resolveJourney, resolveStep} from '../journey-model.js';
import {investigations, investigationState, investigationHTML} from '../investigations.js';
const data = name => JSON.parse(fs.readFileSync(new URL(`../data/${name}`, import.meta.url)));
const catalog=new Map(data('catalog.json').map(e=>[e.id,e]));
const overview=data('overview.json');
const freshness=data('freshness.json');
const reviewRequired=s=>assert.notEqual(freshness.steps[s]?.status,'reviewed',s+' must disclose missing evidence');

test('reviewed steps resolve to real code, schema, tests and pinned source locations',()=>{
  assert.deepEqual(JSON.parse(JSON.stringify(journeys)),JSON.parse(fs.readFileSync(new URL('../model/journeys.json',import.meta.url))).journeys);
  const files=data('files.json').repositories.find(r=>r.id==='breakdown-admin').files;
  for(const [jid,j] of Object.entries(journeys))for(const s of j.steps){
    assert.ok(s.sources.length, s.title+' has no evidence');
    for(const id of [...s.code,...s.data.map(r=>r.entity),...s.sources])if(!catalog.has(id))reviewRequired(jid+'/'+s.id);
    for(const id of s.sources){if(!catalog.has(id))continue;const e=catalog.get(id),shard=data(e.shard),d=e.kind==='function'?shard.functions[id]:shard;const url=d.url||d.sources?.[0]?.url;assert.ok(url.includes(overview.manifest.sources['breakdown-admin']), id+' source revision');}
    for(const id of s.changes)assert.ok(overview.changes.some(c=>c.id===id),id);
    for(const ref of s.tests)if(!files.some(f=>f.path===ref.file))reviewRequired(jid+'/'+s.id);
  }
  for(const name of Object.keys(domains))if(!catalog.has(`public.${name}`))assert.equal(freshness.status,'attention');else assert.equal(catalog.get(`public.${name}`).kind,'table');
});

test('every handoff has resolvable endpoints and source evidence',()=>{
  for(const [jid,j] of Object.entries(journeys)){
    assert.equal(new Set(j.steps.map(s=>s.id)).size,j.steps.length);
    for(const s of j.steps)assert.ok(j.groups.some(g=>g.id===s.group));
    for(const e of j.edges){assert.ok(j.steps.some(s=>s.id===e.from));assert.ok(j.steps.some(s=>s.id===e.to));if(!catalog.has(e.source)){reviewRequired(jid+'/'+e.from);reviewRequired(jid+'/'+e.to);}assert.ok(e.type);}
  }
  const j=journeys.sync;
  assert.equal(j.steps.find(s=>s.id==='codes').group,j.steps.find(s=>s.id==='concepts').group);
  assert.ok(!j.edges.some(e=>e.from==='codes'&&e.to==='concepts'));
  assert.ok(!j.edges.some(e=>e.from==='concepts'&&e.to==='codes'));
  assert.equal(j.steps.find(s=>s.id==='rebuild').group,'maintenance');
});

test('deep links retain a valid journey through code/schema drill-down and recover stale step IDs',()=>{
  assert.equal(resolveJourney('sync',''),'sync');
  assert.equal(resolveJourney('tables','sync'),'sync');
  assert.equal(resolveJourney('entity','search'),'search');
  assert.equal(resolveJourney('verify','sync'),'verify');
  assert.equal(resolveJourney('tables','deleted-journey'),'');
  assert.equal(resolveStep('sync','concepts').id,'concepts');
  assert.equal(resolveStep('sync','deleted-step').id,'source');
  assert.equal(resolveStep('deleted-journey','source'),undefined);
});

test('investigation checkpoints resolve to existing steps and distinguish observed from source-only findings',()=>{
  assert.equal(Object.keys(investigations).length,3);
  for(const investigation of Object.values(investigations)){
    assert.ok(investigation.basis);
    for(const check of investigation.checks){
      assert.ok(journeys[check.view]?.steps.some(s=>s.id===check.step));
      assert.ok(['architecture','failure','coverage','performance','changes'].includes(check.lens));
    }
  }
  assert.ok(investigations['false-pass'].checks.some(c=>c.step==='compare'));
  assert.match(investigations['missed-edit'].basis,/not a newly observed incident/);
  assert.deepEqual(investigationState('false-pass',99),{view:'verify',step:'compare',lens:'changes',context:'verify',investigation:'false-pass',checkpoint:3});
  assert.equal(investigationState('unknown'),null);
  assert.equal(investigationState('slow-search',NaN).checkpoint,0);
  assert.ok(!('answer' in investigationState('false-pass',0)));
});

test('prototype names in fragment inputs are treated as unknown',()=>{for(const key of ['constructor','__proto__','toString']){assert.equal(resolveJourney(key,key),'');assert.equal(resolveStep(key,'source'),undefined);assert.equal(investigationState(key),null);assert.equal(investigationHTML({investigation:key}), '');}});
