import {journeys, resolveStep} from './journey-model.js';
import {esc, badge, reviewText, reviewBadge} from './util.js';

// Generic journey workspace: a rail of steps (in the journey's authored
// groups) and, for the selected step, its explanation, the authored owner,
// mode, boundary, outcome, data, risks, unknowns and tests, review status
// (from cited fingerprints), the authored flow edges (or next/previous) and
// the cited code with pinned source links.
export function createJourneyBrowser({host, byId, detail}) {
  let token = 0;
  const entityButton = (id, sub='') => `<button class="journey-entity" data-go="${esc(id)}"><span class="entity-kind">${esc(byId(id)?.kind || 'source')}</span><strong>${esc(byId(id)?.label || id)}</strong>${sub?`<small>${esc(sub)}</small>`:''}<span class="entity-arrow">→</span></button>`;
  const stepButton = (s, i, selected) => `<button class="flow-step ${selected?'selected':''}" data-step="${esc(s.id)}" ${selected?'aria-current="step"':''}><span class="step-number">${i+1}</span><span><strong>${esc(s.title)}</strong><small>${esc([s.owner, reviewText(s.status)].filter(Boolean).join(' · '))}</small></span><span class="step-arrow">→</span></button>`;
  function rail(j, s) {
    const index = new Map(j.steps.map((x, i) => [x.id, i]));
    const known = new Set(j.groups.map(g => g.id));
    const sections = j.groups.map(g => ({...g, steps: j.steps.filter(x => x.group === g.id)})).filter(g => g.steps.length);
    const rest = j.steps.filter(x => !known.has(x.group));
    if (rest.length) sections.push({id: '', label: sections.length ? 'Other steps' : '', note: '', steps: rest});
    return sections.map(g => `<section class="flow-group">${g.label?`<h3>${esc(g.label)}</h3>`:''}${g.note?`<p>${esc(g.note)}</p>`:''}${g.steps.map(x => stepButton(x, index.get(x.id), x.id === s.id)).join('')}</section>`).join('');
  }
  function connections(j, s) {
    const title = id => j.steps.find(x => x.id === id)?.title || id;
    if (j.edges.length) {
      const edges = [...j.edges.filter(e => e.to === s.id).map(e => ({e, other: e.from, dir: 'from'})), ...j.edges.filter(e => e.from === s.id).map(e => ({e, other: e.to, dir: 'to'}))];
      return edges.map(({e, other, dir}) => `<div class="flow-connection"><span class="connection-type">${dir==='to'?'next':'previous'}${e.type?' · '+esc(e.type):''}</span><button data-step="${esc(other)}">${dir==='to'?'→':'←'} ${esc(title(other))}</button>${e.label?`<p>${esc(e.label)}</p>`:''}</div>`).join('') || '<p>No authored flow edges touch this step.</p>';
    }
    const index = j.steps.indexOf(s), prev = j.steps[index-1], next = j.steps[index+1];
    const handoff = (other, dir) => `<div class="flow-connection"><span class="connection-type">${dir==='next'?'next step':'previous step'}</span><button data-step="${esc(other.id)}">${dir==='next'?'→':'←'} ${esc(other.title)}</button></div>`;
    return `${prev?handoff(prev,'prev'):''}${next?handoff(next,'next'):''}${!prev&&!next?'<p>Single-step journey.</p>':''}`;
  }
  function authored(s) {
    let html = '';
    if (s.owner || s.mode) html += `<p>${s.owner?badge(s.owner,'blue'):''} ${s.mode?badge(s.mode,'gray'):''}</p>`;
    if (s.boundary) html += `<div class="boundary-card"><h3>Boundary</h3><p>${esc(s.boundary)}</p></div>`;
    if (s.outcome) html += `<div class="boundary-card"><h3>Outcome</h3><p>${esc(s.outcome)}</p></div>`;
    if (s.data.length) html += `<h3>Data (${s.data.length})</h3>${s.data.map(d => `<div class="lineage-card">${badge(d.verb || 'uses', 'blue')}${byId(d.entity)?entityButton(d.entity, d.meaning):`<strong>${esc(d.entity)}</strong> <small>${esc(d.meaning)} · not in this snapshot</small>`}</div>`).join('')}`;
    html += s.risks.map(r => `<div class="failure-card"><span class="failure-marker">!</span><div><h3>${esc(r.title || 'Risk')}</h3>${r.body?`<p>${esc(r.body)}</p>`:''}</div></div>`).join('');
    if (s.unknown) html += `<div class="journey-unknown"><strong>Not established</strong><p>${esc(s.unknown)}</p></div>`;
    if (s.tests.length) html += `<h3>Tests (${s.tests.length})</h3>${s.tests.map(t => `<div class="test-source">${esc(t.label || t.file)}${t.file?`<small>${esc(t.file)}${t.line?':'+esc(t.line):''}</small>`:''}</div>`).join('')}`;
    return html;
  }
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
    const index=j.steps.indexOf(s);
    const warning=s.status==='reviewed'?'':`<div class="journey-review-warning"><strong>${s.status==='needs_review'?'Cited code changed or is missing since review':'This step has not been reviewed'}</strong><p>${esc(s.reasons.length?s.reasons.join(' · '):'No reviewer recorded fingerprints for the cited code, so this explanation is unverified.')}</p><button class="text-link" data-freshness-details>Inspect sources and freshness →</button></div>`;
    const cited=s.cites.map(id=>byId(id)?entityButton(id,byId(id).full):`<div class="journey-note">${esc(id)} is cited but not in this snapshot.</div>`).join('')||'<div class="journey-note">This step cites no code or data. Its explanation cannot be checked against the map.</div>';
    const body=`<p class="step-summary">${esc(s.body)||'<em>No explanation recorded.</em>'}</p>${authored(s)}<h3>Follow the flow</h3><div class="flow-connections">${connections(j,s)}</div><h3>Cited code and data (${s.cites.length})</h3>${cited}`;
    const about=[j.description,j.about].filter(Boolean).map(t=>`<p>${esc(t)}</p>`).join('')||`<p>${esc(j.question||j.scope)}</p>`;
    host.innerHTML=`<div class="journey-workspace"><nav class="journey-rail" aria-label="Journey steps"><div class="rail-heading"><span>FOLLOW THE FLOW</span>${badge('Pinned snapshot','gray')}</div>${rail(j,s)}<div class="rail-scope"><b>About this journey</b>${about}</div></nav><section class="journey-detail" aria-label="Workflow step details"><div class="step-heading"><div class="step-kicker">STEP ${index+1} OF ${j.steps.length}</div><h2>${esc(s.title)}</h2>${reviewBadge(s.status)}</div><div class="step-body">${warning}${body}<details class="step-sources" ${document.body.dataset.print?'open':''}><summary>Source evidence (${sources.length})</summary>${sources.map(ref=>ref.url?`<a href="${esc(ref.url)}" target="_blank" rel="noreferrer">${esc(byId(ref.id)?.full||ref.id)} ↗</a>`:entityButton(ref.id,'Source location unavailable')).join('')}<small>Links are pinned to the snapshot's repository revisions. Review status compares cited fingerprints with the reviewed ones; it does not establish runtime behaviour.</small></details></div></section></div>`;
    host.querySelector('.journey-rail').scrollTop=railScroll;host.querySelector('.journey-detail').scrollTop=contentScroll;
    host.querySelectorAll('[data-go]').forEach(el=>el.classList.toggle('entity-selected',el.dataset.go===focus));
    if(focusAttribute)Array.from(host.querySelectorAll(`[${focusAttribute}]`)).find(el=>el.getAttribute(focusAttribute)===focusValue)?.focus({preventScroll:true});
  }
  return {render};
}
