import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
const root=new URL('../',import.meta.url);
const read=name=>JSON.parse(fs.readFileSync(new URL(name,root)));
const cat=read('data/catalog.json'), entries=new Map(cat.map(e=>[e.id,e]));
const overview=read('data/overview.json');
const details=new Map([...new Set(cat.map(e=>e.shard).filter(Boolean))].map(name=>[name,read('data/'+name)]));
const atlas=process.env.SYSTEM_MAP_ATLAS ? JSON.parse(fs.readFileSync(process.env.SYSTEM_MAP_ATLAS)) : null;

test('all entity IDs and shard references resolve; filenames keep meaningful labels',()=>{
  assert.equal(entries.size,cat.length);
  for(const e of cat){
    if(e.kind==='module')assert.ok(!['py','js','mjs','sh','ex'].includes(e.label),e.id);
    if(e.kind==='function'){assert.ok(entries.has(e.parent),e.id);assert.ok(details.get(e.shard).functions[e.id],e.id);}
    if(e.icon)assert.ok(fs.existsSync(new URL(`assets/${e.icon}.svg`,root)),e.icon);
  }
  if(atlas)assert.deepEqual(new Set(cat.filter(e=>e.kind==='function').map(e=>e.id)),new Set(atlas.functions.filter(f=>f.kind!=='compiler_generated').map(f=>f.id)));
});

test('call links survive sharding in both directions without dropping sites',()=>{
  const functions=new Map([...details.values()].flatMap(d=>Object.entries(d.functions||{})));
  for(const [id,f] of functions)for(const edge of f.out){
    assert.ok(functions.has(edge.id));
    assert.equal(functions.get(edge.id).in.find(e=>e.id===id)?.sites,edge.sites);
  }
  if(atlas){
    const expected=new Map();
    for(const c of atlas.calls)if(functions.has(c.from)&&functions.has(c.to)){const key=JSON.stringify([c.from,c.to]);expected.set(key,(expected.get(key)||0)+1);}
    const actual=new Map([...functions].flatMap(([id,f])=>f.out.map(e=>[JSON.stringify([id,e.id]),e.sites])));
    assert.deepEqual(actual,expected);
  }
});

test('schema columns and physical/logical relationships retain their source meaning',()=>{
  for(const e of cat.filter(e=>e.kind==='table')){
    const d=details.get(e.shard);
    for(const r of d.relationships){assert.ok(entries.has(r.from));assert.ok(entries.has(r.to));}
    if(atlas){const t=atlas.schema.tables.find(t=>t.id===e.id);assert.deepEqual(d.columns,t.columns);assert.deepEqual(d.relationships,atlas.schema.relationships.filter(r=>[r.from,r.to].includes(e.id)));}
  }
});

test('curated links, graph endpoints and service attachments resolve',()=>{
  for(const view of Object.values(overview.views)){
    const ids=new Set(view.nodes.map(n=>n.id));
    for(const id of ids)assert.ok(entries.has(id),id);
    for(const edge of view.edges){assert.ok(ids.has(edge.from));assert.ok(ids.has(edge.to));}
  }
  for(const r of [...overview.changes,...overview.evidence])for(const id of r.entities)assert.ok(entries.has(id),id);
  for(const r of overview.changes)assert.match(r.status,/not implemented/);
  for(const s of overview.services)for(const id of [...(s.modules||[]),...(s.tables||[])])assert.ok(entries.has(id),id);
});

test('source revisions are pinned and initial payload stays bounded',()=>{
  for(const rev of Object.values(overview.manifest.sources))assert.match(rev,/^[a-f0-9]{40}$/);
  if(atlas)assert.deepEqual(overview.manifest.sources,atlas.manifest.sources);
  assert.ok(fs.statSync(new URL('data/overview.json',root)).size<40000);
  assert.ok(fs.statSync(new URL('data/catalog.json',root)).size<3000000);
  assert.ok(!fs.readFileSync(new URL('app.js',root),'utf8').includes('detail/model.json'));
});

test('private audit samples are absent from published metadata',()=>{
  for(const name of fs.readdirSync(new URL('data/',root)).filter(n=>n.endsWith('.json'))){
    const text=fs.readFileSync(new URL('data/'+name,root),'utf8');
    for(const marker of ['coverage-audit-private','packet.js','raw_samples'])assert.ok(!text.includes(marker),name);
  }
});
