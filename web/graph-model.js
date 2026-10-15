// Keep direction separate from node placement: reciprocal calls share a node
// but retain both arrows. Self calls are counted explicitly, not drawn as loops.
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
      ...incoming.map(call => ({from: call.id, to: id, sites: call.sites})),
      ...outgoing.map(call => ({from: id, to: call.id, sites: call.sites}))
    ]
  };
}
