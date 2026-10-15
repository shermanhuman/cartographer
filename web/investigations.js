export const investigations = {
  'missed-edit': {title:'An edit is missing from search',basis:'Source-confirmed failure path · not a newly observed incident',finding:'A committed edit can miss invalidation. Processor retries only help when pending work exists.',checks:[
    {view:'sync',step:'source',lens:'architecture',question:'Was the source committed before enrichment was marked?',answer:'The adapter writes repair orders and children before invoking the two enrichment helpers.'},
    {view:'sync',step:'queue',lens:'failure',question:'What happens if marking fails?',answer:'Helpers rescue and alert. The source write remains committed; an empty queue cannot establish completeness.'},
    {view:'sync',step:'concepts',lens:'coverage',question:'Do existing checks prove every edit was applied?',answer:'Sampled processing parity and backfill checkpoints do not prove complete update capture or semantic recall.'},
    {view:'sync',step:'queue',lens:'changes',question:'Which change closes the gap?',answer:'The durable source-change contract must record pending work atomically with the source write.'}
  ]},
  'slow-search': {title:'Search is slow for a large tenant',basis:'Recorded replica probes · limited, cache-sensitive samples',finding:'Page speed does not establish facet speed. Large-tenant concept facets reached the probe limit.',checks:[
    {view:'search',step:'request',lens:'architecture',question:'Which request and result shape are we investigating?',answer:'Identify page, count or facet work before attributing the latency. They have different entry points and costs.'},
    {view:'search',step:'results',lens:'performance',question:'Where is measured expense?',answer:'Open the facet audit: the large-tenant baseline reached five seconds. The contribution-read measurement is a different operation.'},
    {view:'search',step:'results',lens:'coverage',question:'What must stay correct when optimizing?',answer:'Preserve matching across query shapes. Existing parity samples omit important combinations; larger-tenant parity remains a gap.'}
  ]},
  'false-pass': {title:'Verification passed with differing partitions',basis:'Historical audit + source review · not a claim of current drift',finding:'The provider calculates partition differences but uses only total difference to decide the pass flag.',checks:[
    {view:'verify',step:'dispatch',lens:'architecture',question:'What time interval was compared?',answer:'Follow the job’s explicit start/end interval and cadence. An execution window is a separate policy.'},
    {view:'verify',step:'compare',lens:'architecture',question:'Where is pass/fail actually decided?',answer:'Tekmetric.Verification.verify/4 computes active/deleted deltas, but total_discrepancy only uses the total.'},
    {view:'verify',step:'compare',lens:'coverage',question:'Is there evidence beyond a hypothetical example?',answer:'The historical September 26 result shows recorded partition differences ignored by the pass criterion. It does not establish current drift or whether differing date/population definitions caused the discrepancy.'},
    {view:'verify',step:'compare',lens:'changes',question:'What is the fix and its limit?',answer:'First align API and database date/population definitions, then require comparable partitions to agree and handle errors explicitly. Equal counts still require deeper record/content reconciliation.'}
  ]}
};
const esc=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
export function investigationState(id,index=0){
  const item=Object.hasOwn(investigations,id)?investigations[id]:null;if(!item)return null;
  index=Number.isFinite(index)?Math.max(0,Math.min(Math.trunc(index),item.checks.length-1)):0;
  const {view,step,lens}=item.checks[index];
  return {view,step,lens,context:view,investigation:id,checkpoint:index};
}
export function investigationHTML(state){
  const selected=Object.hasOwn(investigations,state.investigation)?investigations[state.investigation]:null;
  if(selected){const index=Number.isFinite(state.checkpoint)?Math.max(0,Math.min(Math.trunc(state.checkpoint),selected.checks.length-1)):0,check=selected.checks[index];return `<section class="investigation-panel" aria-label="Guided investigation"><div><span class="investigation-eyebrow">INVESTIGATION · ${index+1} / ${selected.checks.length}</span><h2>${esc(check.question)}</h2><p>${esc(check.answer)}</p><small>${esc(selected.basis)}</small></div><div class="investigation-controls"><button data-investigation="${esc(state.investigation)}" data-checkpoint="${index}" class="secondary">Show checkpoint</button><button data-investigation="${esc(state.investigation)}" data-checkpoint="${index-1}" class="secondary" ${index===0?'disabled':''}>←</button><button data-investigation="${esc(state.investigation)}" data-checkpoint="${index+1}" class="secondary" ${index===selected.checks.length-1?'disabled':''}>Next →</button><button data-close-investigation class="text-link">Close guide</button></div></section>`}
  if(state.view==='home')return `<div class="investigation-intro">INVESTIGATE A QUESTION</div><div class="investigation-cards">${Object.entries(investigations).map(([id,i])=>`<button data-investigation="${id}"><strong>${esc(i.title)} →</strong><small>${esc(i.basis)}</small></button>`).join('')}</div>`;
  return '';
}
