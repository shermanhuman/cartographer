// Search over the entity catalog, kept free of the DOM so it can be tested.

// With nothing typed, the All chip lists only the big landmarks (modules,
// tables, services, contexts) so the dialog opens on something browsable.
// A specific chip lists that kind even when nothing is typed.
const LANDMARKS = ['module', 'table', 'service', 'context'];

export function searchRows(catalog, query, kind = 'all') {
  const q = String(query ?? '').trim().toLowerCase();
  const words = q.split(/\s+/).filter(Boolean);
  let rows = catalog.filter(e => (kind === 'all' || e.kind === kind) &&
    words.every(w => (e.full + ' ' + e.repo + ' ' + (e.file || '')).toLowerCase().includes(w)));
  if (!q && kind === 'all') rows = rows.filter(e => LANDMARKS.includes(e.kind));
  const score = e => (e.label.toLowerCase() === q ? 4 : e.label.toLowerCase().startsWith(q) ? 2 : 0) + (e.kind === 'module' ? 1 : 0);
  return rows.sort((a, b) => score(b) - score(a) || a.full.localeCompare(b.full));
}
