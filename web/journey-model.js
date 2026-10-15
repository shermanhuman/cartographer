// Journeys come from /api/annotations (kind "journey"); each step carries its
// review status from the store plus the fields its author wrote (group,
// owner, mode, boundary, outcome, risks, unknown, data, tests). View names
// the UI owns are never journeys. Table notes (kind "table-note") give a
// table its reviewed meaning, keyed by the table id they cite first.
import {text, array, objects} from './util.js';
export const journeys = Object.create(null);
export const domains = Object.create(null);
const reserved = new Set(['home','modules','tables','contexts','entity']);
const strings = v => array(v).filter(x => typeof x === 'string');
const risk = r => typeof r === 'string' ? {title: r, body: ''} : {title: text(r?.title), body: text(r?.body)};
const test = t => typeof t === 'string' ? {label: t, file: '', line: 0} : {label: text(t?.label), file: text(t?.file), line: Number(t?.line) || 0};
export function setJourneys(list) {
  for (const key of Object.keys(journeys)) delete journeys[key];
  for (const a of list || []) {
    if (a?.kind !== 'journey' || !a.id || reserved.has(a.id)) continue;
    const raw = a.steps?.length ? a.steps : [{id: a.id, title: a.title, body: a.body, cites: a.cites, status: a.status, reasons: a.reasons}];
    const steps = raw.map(s => ({
      id: s.id, title: s.title || s.id, body: s.body || '', cites: strings(s.cites), code: strings(s.cites),
      status: s.status || 'unreviewed', reasons: strings(s.reasons),
      group: text(s.group), owner: text(s.owner), mode: text(s.mode), boundary: text(s.boundary), outcome: text(s.outcome),
      unknown: text(s.unknown), risks: array(s.risks).map(risk).filter(r => r.title || r.body), tests: array(s.tests).map(test).filter(t => t.label || t.file),
      data: objects(s.data).map(d => ({verb: text(d.verb), entity: text(d.entity), meaning: text(d.meaning)})).filter(d => d.entity),
    }));
    const ids = new Set(steps.map(s => s.id));
    const groups = objects(a.groups).map(g => ({id: text(g.id), label: text(g.label) || text(g.id), note: text(g.note)})).filter(g => g.id);
    const edges = objects(a.edges).map(e => ({from: text(e.from), to: text(e.to), type: text(e.type), label: text(e.label)})).filter(e => ids.has(e.from) && ids.has(e.to));
    journeys[a.id] = {id: a.id, title: a.title || a.id, question: a.summary || '', description: a.body || '', about: text(a.scope),
      scope: `${steps.length} step${steps.length === 1 ? '' : 's'} · ${(a.status || 'unreviewed').replace('_', ' ')}`, status: a.status || 'unreviewed', steps, groups, edges};
  }
}
export function setDomains(list) {
  for (const key of Object.keys(domains)) delete domains[key];
  for (const a of list || []) {
    const table = strings(a?.cites)[0];
    if (a?.kind !== 'table-note' || !table || Object.hasOwn(domains, table)) continue;
    domains[table] = {id: a.id, title: text(a.title), role: text(a.role), rule: text(a.rule), meaning: text(a.summary), body: text(a.body), status: a.status || 'unreviewed', reasons: strings(a.reasons)};
  }
}
export function resolveJourney(view, context) {return journeys[view] ? view : journeys[context] ? context : '';}
export function resolveStep(journey, id) {return journeys[journey]?.steps.find(s=>s.id===id) || journeys[journey]?.steps[0];}
