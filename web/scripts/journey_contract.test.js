// Journeys and investigations are generic annotations. The model tests run
// on the fixture's annotation file; the contract test needs CARTOGRAPHER_URL.
// The admin-specific checks (journeys.json vs journey-data.js, domains,
// test mappings, the three hard-coded investigations) were dropped with that data.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {journeys, setJourneys, resolveJourney, resolveStep} from '../journey-model.js';
import {investigations, setInvestigations, investigationState, investigationHTML} from '../investigations.js';
const fixture = new URL('../../testdata/workspace-mini/snapshot/annotations/journeys.json', import.meta.url);
const annotations = JSON.parse(fs.readFileSync(fixture)).annotations;
const base = process.env.CARTOGRAPHER_URL;
const skip = base ? false : 'set CARTOGRAPHER_URL to a running `cartographer serve` (go test ./internal/server does)';
const get = async path => {const r = await fetch(new URL(path, base)); assert.ok(r.ok, `${path}: ${r.status}`); return r.json()};
const investigation = {kind: 'investigation', id: 'slow-checkout', title: 'Checkout is slow', summary: 'Source-only finding',
  steps: [{id: 'a', title: 'Where does it start?', body: 'At the controller.', cites: ['shop-app:ShopWeb.OrderController.create/2']},
          {id: 'b', title: 'What is unknown?', body: 'No measurement.', cites: []}]};

test('journeys load from annotations; reserved view names and other kinds are ignored', () => {
  setJourneys([...annotations, {kind: 'journey', id: 'tables', title: 'clash', steps: []}, investigation]);
  assert.deepEqual(Object.keys(journeys), ['checkout']);
  const j = journeys.checkout;
  assert.equal(j.steps.length, 2);
  assert.deepEqual(j.steps[0].code, ['shop-app:ShopWeb.OrderController.create/2', 'shop-app:Shop.Orders.create/2']);
  assert.equal(j.steps[1].status, 'unreviewed');
  setJourneys([{kind: 'journey', id: 'note', title: 'One step', cites: ['x'], status: 'reviewed'}]);
  assert.equal(journeys.note.steps[0].id, 'note');
  assert.equal(journeys.checkout, undefined, 'setJourneys replaces the previous set');
  setJourneys(annotations);
});

test('deep links retain a valid journey and recover stale step IDs', () => {
  setJourneys(annotations);
  assert.equal(resolveJourney('checkout', ''), 'checkout');
  assert.equal(resolveJourney('tables', 'checkout'), 'checkout');
  assert.equal(resolveJourney('entity', 'checkout'), 'checkout');
  assert.equal(resolveJourney('tables', 'deleted-journey'), '');
  assert.equal(resolveStep('checkout', 'charge').id, 'charge');
  assert.equal(resolveStep('checkout', 'deleted-step').id, 'place');
  assert.equal(resolveStep('deleted-journey', 'place'), undefined);
});

test('investigation checkpoints open their first cited entity and clamp indexes', () => {
  setInvestigations([...annotations, investigation]);
  assert.deepEqual(Object.keys(investigations), ['slow-checkout']);
  assert.deepEqual(investigationState('slow-checkout', 0), {view: 'entity', entity: 'shop-app:ShopWeb.OrderController.create/2', focus: 'shop-app:ShopWeb.OrderController.create/2', investigation: 'slow-checkout', checkpoint: 0});
  assert.deepEqual(investigationState('slow-checkout', 99), {view: 'home', entity: '', focus: '', investigation: 'slow-checkout', checkpoint: 1});
  assert.equal(investigationState('slow-checkout', NaN).checkpoint, 0);
  assert.equal(investigationState('unknown'), null);
  assert.ok(!('answer' in investigationState('slow-checkout', 0)));
  assert.match(investigationHTML({view: 'home'}), /Checkout is slow/);
  assert.match(investigationHTML({investigation: 'slow-checkout', checkpoint: 1}), /What is unknown\?/);
});

test('prototype names in fragment inputs are treated as unknown', () => {
  setJourneys(annotations); setInvestigations([investigation]);
  for (const key of ['constructor', '__proto__', 'toString']) {
    assert.equal(resolveJourney(key, key), '');
    assert.equal(resolveStep(key, 'place'), undefined);
    assert.equal(investigationState(key), null);
    assert.equal(investigationHTML({investigation: key}), '');
  }
});

test('served journey steps cite real entities or disclose that review is needed', {skip}, async () => {
  const [catalog, served] = await Promise.all([get('api/catalog'), get('api/annotations')]);
  const ids = new Set(catalog.map(e => e.id));
  setJourneys(served);
  assert.ok(Object.keys(journeys).length > 0);
  for (const j of Object.values(journeys)) {
    assert.equal(new Set(j.steps.map(s => s.id)).size, j.steps.length);
    for (const s of j.steps) {
      assert.ok(['reviewed', 'needs_review', 'unreviewed'].includes(s.status), s.status);
      if (s.cites.some(id => !ids.has(id))) assert.notEqual(s.status, 'reviewed', `${j.id}/${s.id} cites a missing entity`);
      if (s.status === 'needs_review') assert.ok(s.reasons.length, `${j.id}/${s.id} must say why`);
    }
  }
});
