import {journeys, resolveStep} from './journey-model.js';
const esc = value => String(value ?? '').replace(/[&<>"']/g, c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const tag = (text, tone='') => `<span class="badge ${tone}">${esc(text)}</span>`;
const statusText = s => s==='reviewed'?'Reviewed':s==='needs_review'?'Needs review':'Not reviewed';
const statusTag = s => tag(statusText(s), s==='reviewed'?'gray':'amber');

// Generic journey workspace: a rail of steps and, for the selected step, its
// explanation, review status (from cited fingerprints), the next/previous
// handoff and the cited code with pinned source links.
export function createJourneyBrowser({host, byId, detail}) {
  let token = 0;
  const entityButton = (id, sub='') => `<button class="journey-entity" data-go="${esc(id)}"><span class="entity-kind">${esc(byId(id)?.kind || 'source')}</span><strong>${esc(byId(id)?.label || id)}</strong>${sub?`<small>${esc(sub)}</small>`:''}<span class="entity-arrow">→</span></button>`;
  async function render({journey, step, focus}){
    const run=++token,j=journeys[journey],s=resolveStep(journey,step);if(!j||!s)return;
    const active=host.contains(document.activeElement)?document.activeElement:null;
    const focusAttribute=['data-step','data-go'].find(attr=>active?.hasAttribute(attr));
    const focusValue=focusAttribute?active.getAttribute(focusAttribute):null;
    const railScroll=host.querySelector('.journey-rail')?.scrollTop||0;
    const priorKey=host.dataset.detailKey, key=`${journey}:${s.id}`;
    const contentScroll=priorKey===key?host.querySelector('.journey-detail')?.scrollTop||0:0;
    host.dataset.detailKey=key;
    const sources=await Promise.all(s.cites.map(async id=>{const e=byId(id);if(!e)return {id,url:''};try{const d=await detail(e);return {id,url:d?.url||''}}catch{return {id,url:''}}}));if(run!==token)return;
    const index=j.steps.indexOf(s), prev=j.steps[index-1], next=j.steps[index+1];
    const handoff=(other,dir)=>`<div class="flow-connection"><span class="connection-type">${dir==='next'?'next step':'previous step'}</span><button data-step="${esc(other.id)}">${dir==='next'?'→':'←'} ${esc(other.title)}</button></div>`;
    const warning=s.status==='reviewed'?'':`<div class="journey-review-warning"><strong>${s.status==='needs_review'?'Cited code changed or is missing since review':'This step has not been reviewed'}</strong><p>${esc(s.reasons.length?s.reasons.join(' · '):'No reviewer recorded fingerprints for the cited code, so this explanation is unverified.')}</p><button class="text-link" data-freshness-details>Inspect sources and freshness →</button></div>`;
    const cited=s.cites.map(id=>byId(id)?entityButton(id,byId(id).full):`<div class="journey-note">${esc(id)} is cited but not in this snapshot.</div>`).join('')||'<div class="journey-note">This step cites no code or data. Its explanation cannot be checked against the map.</div>';
    const body=`<p class="step-summary">${esc(s.body)||'<em>No explanation recorded.</em>'}</p><h3>Follow the flow</h3><div class="flow-connections">${prev?handoff(prev,'prev'):''}${next?handoff(next,'next'):''}${!prev&&!next?'<p>Single-step journey.</p>':''}</div><h3>Cited code and data (${s.cites.length})</h3>${cited}`;
    host.innerHTML=`<div class="journey-workspace"><nav class="journey-rail" aria-label="Journey steps"><div class="rail-heading"><span>FOLLOW THE FLOW</span>${tag('Pinned snapshot','gray')}</div><section class="flow-group">${j.steps.map((x,i)=>`<button class="flow-step ${x.id===s.id?'selected':''}" data-step="${esc(x.id)}" ${x.id===s.id?'aria-current="step"':''}><span class="step-number">${i+1}</span><span><strong>${esc(x.title)}</strong><small>${esc(statusText(x.status))}</small></span><span class="step-arrow">→</span></button>`).join('')}</section><div class="rail-scope"><b>About this journey</b><p>${esc(j.question||j.description||j.scope)}</p></div></nav><section class="journey-detail" aria-label="Workflow step details"><div class="step-heading"><div class="step-kicker">STEP ${index+1} OF ${j.steps.length}</div><h2>${esc(s.title)}</h2>${statusTag(s.status)}</div><div class="step-body">${warning}${body}<details class="step-sources" ${document.body.dataset.print?'open':''}><summary>Source evidence (${sources.length})</summary>${sources.map(ref=>ref.url?`<a href="${esc(ref.url)}" target="_blank" rel="noreferrer">${esc(byId(ref.id)?.full||ref.id)} ↗</a>`:entityButton(ref.id,'Source location unavailable')).join('')}<small>Links are pinned to the snapshot's repository revisions. Review status compares cited fingerprints with the reviewed ones; it does not establish runtime behaviour.</small></details></div></section></div>`;
    host.querySelector('.journey-rail').scrollTop=railScroll;host.querySelector('.journey-detail').scrollTop=contentScroll;
    host.querySelectorAll('[data-go]').forEach(el=>el.classList.toggle('entity-selected',el.dataset.go===focus));
    if(focusAttribute)Array.from(host.querySelectorAll(`[${focusAttribute}]`)).find(el=>el.getAttribute(focusAttribute)===focusValue)?.focus({preventScroll:true});
  }
  return {render};
}
