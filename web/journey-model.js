// Journeys come from /api/annotations (kind "journey"); each step carries its
// review status from the store. View names the UI owns are never journeys.
export const journeys = Object.create(null);
export const domains = Object.create(null); // reviewed table meanings; none in the generic annotation format
const reserved = new Set(['home','modules','tables','contexts','entity']);
export function setJourneys(list) {
  for (const key of Object.keys(journeys)) delete journeys[key];
  for (const a of list || []) {
    if (a?.kind !== 'journey' || !a.id || reserved.has(a.id)) continue;
    const raw = a.steps?.length ? a.steps : [{id: a.id, title: a.title, body: a.body, cites: a.cites, status: a.status, reasons: a.reasons}];
    const steps = raw.map(s => ({id: s.id, title: s.title || s.id, body: s.body || '', cites: s.cites || [], code: s.cites || [], data: [], status: s.status || 'unreviewed', reasons: s.reasons || []}));
    journeys[a.id] = {id: a.id, title: a.title || a.id, question: a.summary || '', description: a.body || '', scope: `${steps.length} step${steps.length === 1 ? '' : 's'} · ${(a.status || 'unreviewed').replace('_', ' ')}`, status: a.status || 'unreviewed', steps};
  }
}
export function resolveJourney(view, context) {return journeys[view] ? view : journeys[context] ? context : '';}
export function resolveStep(journey, id) {return journeys[journey]?.steps.find(s=>s.id===id) || journeys[journey]?.steps[0];}
