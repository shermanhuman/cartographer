import {test} from 'node:test';
import assert from 'node:assert/strict';
import {functionNeighborhood, layeredLayout} from '../graph-model.js';

test('reciprocal calls retain both directed edges and one neighbor node', () => {
  const graph = functionNeighborhood('a', {
    in: [{id: 'b', sites: 2}, {id: 'c', sites: 1}],
    out: [{id: 'b', sites: 3}, {id: 'd', sites: 1}]
  });
  assert.deepEqual(graph.edges, [
    {from: 'b', to: 'a', sites: 2}, {from: 'c', to: 'a', sites: 1},
    {from: 'a', to: 'b', sites: 3}, {from: 'a', to: 'd', sites: 1}
  ]);
  assert.deepEqual([...graph.incoming, ...graph.outgoingOnly].map(c => c.id), ['b', 'c', 'd']);
});

test('self recursion is counted once and excluded from loop rendering', () => {
  const graph = functionNeighborhood('a', {in: [{id: 'a', sites: 2}], out: [{id: 'a', sites: 2}]});
  assert.equal(graph.selfSites, 2);
  assert.deepEqual(graph.edges, []);
  assert.deepEqual(graph.incoming, []);
  assert.deepEqual(graph.outgoingOnly, []);
});

test('edge precision is carried through when the API provides it', () => {
  const graph = functionNeighborhood('a', {in: [{id: 'b', sites: 1, precision: 'heuristic'}], out: []});
  assert.deepEqual(graph.edges, [{from: 'b', to: 'a', sites: 1, precision: 'heuristic'}]);
});

test('layered layout puts callers left, callees right and centres columns', () => {
  const nodes = layeredLayout({nodes: [{id: 'f', depth: 0}, {id: 'c1', depth: -1}, {id: 'c2', depth: -1}, {id: 'x', depth: 1}, {id: 'cc', depth: -2}]});
  const at = Object.fromEntries(nodes.map(n => [n.id, n]));
  assert.ok(at.cc.x < at.c1.x && at.c1.x < at.f.x && at.f.x < at.x.x);
  assert.equal(at.c1.x, at.c2.x);
  assert.equal(at.f.y, 79, 'a single node is centred against the tallest column');
  assert.equal(at.c2.y - at.c1.y, 158);
});
