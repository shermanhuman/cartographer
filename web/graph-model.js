// Keep direction separate from node placement: reciprocal calls share a node
// but retain both arrows. Self calls are counted explicitly, not drawn as loops.
const withPrecision = (edge, call) => call.precision ? {...edge, precision: call.precision} : edge;
export function functionNeighborhood(id, detail) {
  const incoming = detail.in.filter(call => call.id !== id);
  const outgoing = detail.out.filter(call => call.id !== id);
  const left = new Set(incoming.map(call => call.id));
  return {
    incoming,
    outgoing,
    outgoingOnly: outgoing.filter(call => !left.has(call.id)),
    selfSites: detail.out.filter(call => call.id === id).reduce((sum, call) => sum + call.sites, 0),
    edges: [
      ...incoming.map(call => withPrecision({from: call.id, to: id, sites: call.sites}, call)),
      ...outgoing.map(call => withPrecision({from: id, to: call.id, sites: call.sites}, call))
    ]
  };
}
// Columns by call depth (callers negative, callees positive) for /api/graph
// neighbourhoods deeper than one hop; each column is centred vertically.
export function layeredLayout(graph, columnWidth = 330, rowHeight = 158) {
  const columns = new Map();
  for (const node of graph.nodes) {
    if (!columns.has(node.depth)) columns.set(node.depth, []);
    columns.get(node.depth).push(node.id);
  }
  const depths = [...columns.keys()].sort((a, b) => a - b);
  const tallest = Math.max(1, ...[...columns.values()].map(c => c.length));
  return depths.flatMap((depth, column) => {
    const ids = columns.get(depth);
    return ids.map((id, row) => ({id, x: 20 + column * columnWidth, y: (tallest - ids.length) * rowHeight / 2 + row * rowHeight}));
  });
}
