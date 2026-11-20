// Routes and HTTP links between the Go API and the UI, run against a server
// over the facts-v2 fixture (CARTOGRAPHER_V2_URL; go test ./internal/server
// sets it): sales' adminClient.Check reaches admin's route
// POST /api/v1/sales/cold/check through an HTTP link, and the route leads to
// SalesColdController.check.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {functionNeighborhood, edgeLabel, hopLabel} from '../graph-model.js';
const base = process.env.CARTOGRAPHER_V2_URL;
const skip = base ? false : 'set CARTOGRAPHER_V2_URL to `cartographer serve` over testdata/workspace-v2 (go test ./internal/server does)';
const get = async path => {const r = await fetch(new URL(path, base)); assert.ok(r.ok, `${path}: ${r.status}`); return r.json()};
const route = 'admin:route:AdminWeb.Router:POST /api/v1/sales/cold/check';
const caller = 'sales:internal/dialer.adminClient.Check/1';
const handler = 'admin:AdminWeb.SalesColdController.check/2';
const start = 'sales:internal/dialer.Server.campaignEnrollSave/2';

test('a route is a catalog entity with a shard naming its handler and HTTP callers', {skip}, async () => {
  const catalog = await get('api/catalog');
  const entries = new Map(catalog.map(e => [e.id, e]));
  const r = entries.get(route);
  assert.ok(r, 'route in the catalog');
  assert.equal(r.kind, 'route');
  assert.equal(r.label, 'POST /api/v1/sales/cold/check');
  assert.equal(r.count, 1, 'one HTTP caller');
  const d = await get('api/entity/' + encodeURIComponent(r.shard));
  assert.equal(d.kind, 'route');
  assert.equal(d.method, 'POST');
  assert.equal(d.handler.id, handler);
  assert.ok(entries.has(d.handler.id));
  assert.equal(d.callers.length, 1);
  assert.equal(d.callers[0].id, caller);
  assert.equal(d.callers[0].precision, 'heuristic');
  assert.match(d.callers[0].provenance, /config\.yaml cold\.admin_url/);
  // The shard draws like a function: HTTP callers in, handler out.
  const g = functionNeighborhood(route, d);
  assert.deepEqual(g.edges.map(e => [e.from, e.to, edgeLabel(e)]), [[caller, route, '1 HTTP link'], [route, handler, 'route handler']]);
});

test('function shards carry HTTP and route edges both ways', {skip}, async () => {
  const sales = await get('api/entity/' + encodeURIComponent(caller));
  const out = sales.functions[caller].out.find(e => e.id === route);
  assert.deepEqual([out.kinds, out.precision], [['http'], 'heuristic']);
  assert.match(out.provenance, /cold\.admin_url/);
  const admin = await get('api/entity/' + encodeURIComponent(handler));
  const into = admin.functions[handler].in.find(e => e.id === route);
  assert.deepEqual(into.kinds, ['route']);
});

test('the overview draws the cross-repository HTTP edge', {skip}, async () => {
  const overview = await get('api/overview');
  const edge = overview.views.home.edges.find(e => e.from === 's:sales' && e.to === 's:admin');
  assert.ok(edge, JSON.stringify(overview.views.home.edges));
  assert.equal(edge.label, '1 HTTP link');
  assert.deepEqual(edge.kinds, ['http']);
  assert.equal(edge.precision, 'heuristic');
});

test('the path view names each hop kind, forward and reverse', {skip}, async () => {
  for (const [from, to, reverse] of [[start, handler, false], [handler, start, true]]) {
    const p = await get('api/path?' + new URLSearchParams({from, to}));
    assert.equal(p.reverse, reverse);
    assert.deepEqual(p.steps.map(s => s.id), [start, 'sales:internal/dialer.Server.coldVerdicts/1', caller, route, handler]);
    assert.deepEqual(p.steps.slice(1).map(s => [hopLabel(s), s.precision]), [['call', 'precise'], ['dispatch', 'heuristic'], ['http', 'heuristic'], ['route', 'precise']]);
    assert.match(p.summary, /⇢ dispatch \(heuristic\) → adminClient\.Check ⇢ http POST \/api\/v1\/sales\/cold\/check \(heuristic: config\.yaml cold\.admin_url → admin\) → SalesColdController\.check$/);
  }
});
