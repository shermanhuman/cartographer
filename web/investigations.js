// Investigations come from /api/annotations (kind "investigation"): each
// step is a checkpoint question (title) and answer (body); a checkpoint opens
// the first entity it cites.
export const investigations = Object.create(null);
export function setInvestigations(list) {
  for (const key of Object.keys(investigations)) delete investigations[key];
  for (const a of list || []) {
    if (a?.kind !== 'investigation' || !a.id) continue;
    const steps = a.steps?.length ? a.steps : [{id: a.id, title: a.title, body: a.body, cites: a.cites, status: a.status}];
    investigations[a.id] = {title: a.title || a.id, basis: a.summary || '', finding: a.body || '', status: a.status || 'unreviewed', checks: steps.map(s => ({id: s.id, question: s.title || s.id, answer: s.body || '', cites: s.cites || [], status: s.status || 'unreviewed'}))};
  }
}
const esc=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const clamp=(index,item)=>Number.isFinite(index)?Math.max(0,Math.min(Math.trunc(index),item.checks.length-1)):0;
export function investigationState(id,index=0){
  const item=Object.hasOwn(investigations,id)?investigations[id]:null;if(!item||!item.checks.length)return null;
  index=clamp(index,item);
  const target=item.checks[index].cites[0]||'';
  return target?{view:'entity',entity:target,focus:target,investigation:id,checkpoint:index}:{view:'home',entity:'',focus:'',investigation:id,checkpoint:index};
}
export function investigationHTML(state){
  const selected=Object.hasOwn(investigations,state.investigation)?investigations[state.investigation]:null;
  if(selected&&selected.checks.length){const index=clamp(state.checkpoint,selected),check=selected.checks[index];return `<section class="investigation-panel" aria-label="Guided investigation"><div><span class="investigation-eyebrow">INVESTIGATION · ${esc(selected.title)} · ${index+1} / ${selected.checks.length}${check.status==='reviewed'?'':' · '+esc(check.status.replace('_',' '))}</span><h2>${esc(check.question)}</h2><p>${esc(check.answer)}</p><small>${esc(selected.basis)}</small></div><div class="investigation-controls"><button data-investigation="${esc(state.investigation)}" data-checkpoint="${index}" class="secondary">Show checkpoint</button><button data-investigation="${esc(state.investigation)}" data-checkpoint="${index-1}" class="secondary" ${index===0?'disabled':''}>←</button><button data-investigation="${esc(state.investigation)}" data-checkpoint="${index+1}" class="secondary" ${index===selected.checks.length-1?'disabled':''}>Next →</button><button data-close-investigation class="text-link">Close guide</button></div></section>`}
  const all=Object.entries(investigations);
  if(state.view==='home'&&all.length)return `<div class="investigation-intro">INVESTIGATE A QUESTION</div><div class="investigation-cards">${all.map(([id,i])=>`<button data-investigation="${esc(id)}"><strong>${esc(i.title)} →</strong><small>${esc(i.basis||i.finding)}</small></button>`).join('')}</div>`;
  return '';
}
