import data from './journey-data.js';
export const journeys = Object.assign(Object.create(null), data.journeys);
export const domains = Object.assign(Object.create(null), data.domains);
export function resolveJourney(view, context) {return journeys[view] ? view : journeys[context] ? context : '';}
export function resolveStep(journey, id) {return journeys[journey]?.steps.find(s=>s.id===id) || journeys[journey]?.steps[0];}
