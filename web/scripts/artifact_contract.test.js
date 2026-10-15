// Contract between the Go API and the UI: run against a live server with
// CARTOGRAPHER_URL (go test ./internal/server sets it to an httptest server
// over the fixture). Ported from the admin build's data/ contract; the checks
// against the Elixir atlas input and the private-sample markers were dropped
// because there is no static build any more.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
const base = process.env.CARTOGRAPHER_URL;
const skip = base ? false : 'set CARTOGRAPHER_URL to a running `cartographer serve` (go test ./internal/server does)';
const get = async path => {const r = await fetch(new URL(path, base)); assert.ok(r.ok, `${path}: ${r.status}`); return r.json()};
let data;
async function load() {
  if (data) return data;
  const [catalog, overview] = await Promise.all([get('api/catalog'), get('api/overview')]);
  const shards = new Map();
  for (const s of new Set(catalog.map(e => e.shard).filter(Boolean))) shards.set(s, await get('api/entity/' + encodeURIComponent(s)));
  return data = {catalog, entries: new Map(catalog.map(e => [e.id, e])), overview, shards};
}

test('all entity IDs and shard references resolve; labels stay meaningful', {skip}, async () => {
  const {catalog, entries, shards} = await load();
  assert.equal(entries.size, catalog.length, 'duplicate catalog ids');
  for (const e of catalog) {
    if (e.kind === 'module') assert.ok(!['py','js','mjs','sh','ex'].includes(e.label), e.id);
    if (e.kind === 'function') {
      if (e.parent) assert.ok(entries.has(e.parent), e.id);
      assert.ok(shards.get(e.shard).functions[e.id], e.id);
    }
    if (e.icon) assert.ok(fs.existsSync(new URL(`../assets/${e.icon}.svg`, import.meta.url)), e.icon);
    for (const id of e.members || []) assert.ok(entries.has(id), `${e.id} member ${id}`);
  }
});

test('call links survive sharding in both directions without dropping sites', {skip}, async () => {
  const {shards} = await load();
  const functions = new Map([...shards.values()].flatMap(d => Object.entries(d.functions || {})));
  assert.ok(functions.size > 0);
  for (const [id, f] of functions) for (const edge of f.out) {
    assert.ok(functions.has(edge.id), edge.id);
    assert.equal(functions.get(edge.id).in.find(e => e.id === id)?.sites, edge.sites, `${id} -> ${edge.id}`);
    assert.ok(['precise', 'heuristic', 'candidate'].includes(edge.precision), edge.precision);
  }
});

test('schema relationships and code mentions resolve to catalog entities', {skip}, async () => {
  const {catalog, entries, shards} = await load();
  for (const e of catalog.filter(e => e.kind === 'table')) {
    const d = shards.get(e.shard);
    assert.equal(d.columns.length, e.count, e.id);
    for (const r of d.relationships) { assert.ok(entries.has(r.from), r.from); assert.ok(entries.has(r.to), r.to); }
    for (const r of d.references) { assert.equal(r.precision, 'candidate'); if (r.function) assert.ok(entries.has(r.function), r.function); }
  }
});

test('overview graph, services and contexts reference catalog entities', {skip}, async () => {
  const {entries, overview} = await load();
  for (const view of Object.values(overview.views)) {
    const ids = new Set(view.nodes.map(n => n.id));
    for (const id of ids) assert.ok(entries.has(id), id);
    for (const edge of view.edges) { assert.ok(ids.has(edge.from), edge.from); assert.ok(ids.has(edge.to), edge.to); }
  }
  for (const s of overview.services) for (const id of [...(s.modules || []), ...(s.tables || [])]) assert.ok(entries.has(id), id);
  for (const c of overview.contexts) {
    assert.ok(entries.has(c.id), c.id);
    for (const id of c.modules) assert.ok(entries.has(id), id);
    for (const t of c.tables) assert.ok(entries.has(t.id), t.id);
  }
});

test('source revisions are pinned and the initial payload stays bounded', {skip}, async () => {
  const {overview, shards} = await load();
  const sources = overview.manifest.sources;
  for (const rev of Object.values(sources)) assert.match(rev, /^[a-f0-9]{40}$/);
  for (const d of shards.values()) for (const f of Object.values(d.functions || {})) if (f.url) assert.ok(f.url.includes(`/blob/${sources[f.repo]}/`), f.url);
  assert.ok(JSON.stringify(overview).length < 40000);
  const app = fs.readFileSync(new URL('../app.js', import.meta.url), 'utf8');
  assert.ok(!app.includes("'data/") && !app.includes('detail/model.json'), 'the UI must load the API, not static data files');
});
