import {test} from 'node:test';
import assert from 'node:assert/strict';
import {functionNeighborhood} from '../graph-model.js';

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
