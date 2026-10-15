import {journeys, domains, resolveStep} from './journey-model.js';
const esc = value => String(value ?? '').replace(/[&<>"']/g, c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const tag = (text, tone='') => `<span class="badge ${tone}">${esc(text)}</span>`;
const tabs = [['architecture','Flow'],['data','Data & ownership'],['failure','Failure & recovery'],['coverage','Coverage'],['performance','Performance'],['changes','Proposed changes']];

export function createJourneyBrowser({host, byId, detail, overview, freshness}) {
  let token = 0;
  const entityButton = (id, sub='') => `<button class="journey-entity" data-go="${esc(id)}"><span class="entity-kind">${esc(byId(id)?.kind || 'source')}</span><strong>${esc(byId(id)?.label || id)}</strong>${sub?`<small>${esc(sub)}</small>`:''}<span class="entity-arrow">→</span></button>`;
  const sourceUrl = ref => `https://github.com/Applesauce-Labs/breakdown-admin/blob/${overview.manifest.sources['breakdown-admin']}/${ref.file}#L${esc(ref.line)}`;
  const evidenceCard = e => `<article class="journey-evidence">${tag(e.status,'amber')}<h3>${esc(e.title)}</h3><strong class="journey-metric">${esc(e.value)}</strong><p>${esc(e.body)}</p><small>${esc(e.date)} · recorded evidence, not live monitoring</small>${e.url?`<a href="${esc(e.url)}" target="_blank" rel="noreferrer">Read underlying audit ↗</a>`:`<p class="note">${esc(e.unavailable_reason||"Historical audit retained locally; raw samples are not included.")}</p>`}</article>`;
  function stepIds(s){return new Set([...s.code,...s.data.map(r=>r.entity),...s.code.map(id=>byId(id)?.parent).filter(Boolean)])}
  function evidenceForStep(j, s, kind){const ids=stepIds(s);return overview.evidence.filter(e=>e.kind===kind && e.entities.some(id=>ids.has(id)))}
  function changeCards(s){return s.changes.map(id=>overview.changes.find(c=>c.id===id)).filter(Boolean).map(c=>`<article class="journey-change">${tag(`${c.id} · Proposed`, 'purple')}<h3>${esc(c.title)}</h3><p>${esc(c.summary)}</p><p><b>Reason:</b> ${esc(c.why)}</p><p><b>Required checks:</b> ${esc(c.guard)}</p><details><summary>Affected modules (${c.entities.length})</summary>${c.entities.map(id=>entityButton(id)).join('')}</details><a href="${esc(c.url)}" target="_blank" rel="noreferrer">Review proposal ↗</a></article>`).join('')}
  async function render({journey, step, lens, focus}){
    const run=++token,j=journeys[journey],s=resolveStep(journey,step);if(!j||!s)return;
    const active=host.contains(document.activeElement)?document.activeElement:null;
    const focusAttribute=['data-step','data-lens','data-go'].find(attr=>active?.hasAttribute(attr));
    const focusValue=focusAttribute?active.getAttribute(focusAttribute):null;
    const railScroll=host.querySelector('.journey-rail')?.scrollTop||0;
    const priorKey=host.dataset.detailKey, key=`${journey}:${s.id}:${lens}`;
    const contentScroll=priorKey===key?host.querySelector('.journey-detail')?.scrollTop||0:0;
    host.dataset.detailKey=key;
    const sources=await Promise.all(s.sources.map(async id=>{const d=await detail(byId(id));return {id,url:d.url||d.sources?.[0]?.url}}));if(run!==token)return;
    const connections=j.edges.filter(e=>e.from===s.id||e.to===s.id);
    const edgeHtml=connections.map(e=>{const other=j.steps.find(x=>x.id===(e.from===s.id?e.to:e.from));return `<div class="flow-connection"><span class="connection-type">${esc(e.type)}</span><button data-step="${esc(other.id)}">${e.from===s.id?'→':'←'} ${esc(other.title)}</button><p>${esc(e.label)}</p><button class="text-link" data-go="${esc(e.source)}">Inspect source evidence →</button></div>`}).join('');
    const review=freshness?.steps[journey+'/'+s.id], reviewed=review?.status==='reviewed';
    const reviewReasons=[];
    if(!review||review.status==='unknown')reviewReasons.push('Source or review evidence is unavailable, so this explanation cannot be confirmed.');
    if(review?.input_changed)reviewReasons.push('The generated source snapshot or shared explanation inputs differ from the last recorded review. Regeneration does not renew that review.');
    if(review?.annotation_changed)reviewReasons.push('This step’s explanation has changed since its last recorded review.');
    if(review?.source_change_count>0)reviewReasons.push(`${review.source_change_count} relevant source paths changed in the compared checkouts after this artifact was generated.`);
    const reviewReason=reviewReasons.join(' ')||'The recorded review does not cover this explanation.';
    let body='';
    if(lens==='data'){
      body=`<p class="section-intro">Reviewed operations for this step. These labels describe the cited source path; the broader table reference index also includes unclassified textual references.</p>`+(s.data.length?s.data.map(r=>{const d=domains[r.entity.replace('public.','')];return `<article class="lineage-card"><div>${tag(r.verb,'blue')}${tag(d?.role||'Source / derived schema','gray')}</div>${entityButton(r.entity,r.meaning)}${d?`<p>${esc(d.meaning)}</p><p class="invariant"><b>Rule:</b> ${esc(d.rule)}</p>`:''}</article>`}).join(''):'<div class="journey-note">No reviewed table operations are attached to this step. This is a mapping gap, not proof that it performs no database work.</div>');
    }else if(lens==='failure'){
      body=s.risks.map(r=>`<article class="failure-card"><span class="failure-marker">!</span><div><h3>${esc(r.title)}</h3><p>${esc(r.body)}</p></div></article>`).join('')+`<div class="boundary-card"><h3>Transaction / execution boundary</h3><p>${esc(s.boundary)}</p></div><h3>Related proposed work</h3>${changeCards(s)||'<p>No linked proposal for this step.</p>'}`;
    }else if(lens==='coverage'||lens==='performance'){
      const direct=evidenceForStep(j,s,lens), related=overview.evidence.filter(e=>e.kind===lens&&e.flows.includes(journey)&&!direct.includes(e));
      body=direct.map(evidenceCard).join('')||`<div class="journey-note">No ${lens==='coverage'?'coverage result':'performance measurement'} is attached directly to this step. This is unknown, not a pass.</div>`;
      if(lens==='coverage')body+=`<h3>Relevant test source</h3><p class="section-intro">These are test definitions at the snapshot revision. They were not executed by this map and are not live pass/fail results.</p>${s.tests.map(ref=>`<a class="test-source" href="${esc(sourceUrl(ref))}" target="_blank" rel="noreferrer">${esc(ref.label)} ↗<small>${esc(ref.file)}:${esc(ref.line)}</small></a>`).join('')||'<p>No test mapping reviewed for this step.</p>'}`;
      if(related.length)body+=`<details class="related-evidence"><summary>Elsewhere in this journey (${related.length})</summary>${related.map(evidenceCard).join('')}</details>`;
    }else if(lens==='changes'){
      const proposal=s.proposal?`<article class="journey-change">${tag('Architecture proposal V3','purple')}<h3>${esc(s.proposal.title)}</h3><ol class="proposed-sequence">${s.proposal.steps.map(text=>`<li>${esc(text)}</li>`).join('')}</ol><p><b>Before rollout:</b> ${esc(s.proposal.gate)}</p><a href="${esc(overview.proposal)}" target="_blank" rel="noreferrer">Read complete proposal ↗</a></article>`:'';
      body=`<div class="proposal-boundary">Proposed behavior is not implemented in this source snapshot. The flow at left continues to show the current system.</div>${proposal}${changeCards(s)||'<p>No structural proposal is linked to this step.</p>'}<h3>Impact starting points</h3><p class="section-intro">Directly associated code and tables. This is a review starting set, not a complete transitive impact analysis.</p>${[...new Set([...s.code,...s.data.map(r=>r.entity)])].map(id=>entityButton(id)).join('')}`;
    }else{
      body=`<p class="step-summary">${esc(s.summary)}</p><div class="boundary-card"><h3>Where the boundary is</h3><p>${esc(s.boundary)}</p></div><h3>What becomes true</h3><p>${esc(s.outcome)}</p><h3>Follow the handoff</h3><div class="flow-connections">${edgeHtml}</div><h3>Inspect implementation</h3>${s.code.map(id=>entityButton(id)).join('')}<button class="journey-inline-link" data-lens="data">Inspect ${s.data.length} reviewed data operations →</button><button class="journey-inline-link" data-lens="failure">Inspect ${s.risks.length} failure cases →</button>`;
    }
    host.innerHTML=`<div class="journey-workspace"><nav class="journey-rail" aria-label="Journey steps"><div class="rail-heading"><span>FOLLOW THE FLOW</span>${tag('Pinned snapshot','gray')}</div>${j.groups.map(g=>`<section class="flow-group ${g.id==='processors'?'parallel-group':''}"><h3>${esc(g.label)}</h3>${g.note?`<p>${esc(g.note)}</p>`:''}${j.steps.filter(s=>s.group===g.id).map(s=>`<button class="flow-step ${s.id===step?'selected':''}" data-step="${esc(s.id)}" ${s.id===step?'aria-current="step"':''}><img src="assets/${esc(s.icon)}.svg" alt=""><span><strong>${esc(s.title)}</strong><small>${esc(s.mode)}</small></span><span class="step-arrow">→</span></button>`).join('')}</section>`).join('')}<div class="rail-scope"><b>Mapping scope</b><p>${esc(j.scope)}</p></div></nav><section class="journey-detail" aria-label="Workflow step details"><div class="step-heading"><div class="step-kicker">${esc(s.owner)}</div><h2>${esc(s.title)}</h2>${tag(s.mode,'blue')} ${tag(reviewed?'Reviewed snapshot path':'Review required',reviewed?'gray':'amber')}</div><div class="journey-tabs" aria-label="Journey view">${tabs.map(([id,label])=>`<button data-lens="${id}" class="${lens===id?'selected':''}" aria-pressed="${lens===id}">${label}</button>`).join('')}</div><div class="step-body">${!reviewed?`<div class="journey-review-warning"><strong>Source review needs attention</strong><p>${esc(reviewReason)}</p><button class="text-link" data-freshness-details>Inspect freshness comparison →</button></div>`:''}${body}<div class="journey-unknown"><strong>Still unknown / incomplete</strong><p>${esc(s.unknown)}</p></div><details class="step-sources"><summary>Source evidence (${sources.length})</summary>${sources.map(ref=>ref.url?`<a href="${esc(ref.url)}" target="_blank" rel="noreferrer">${esc(byId(ref.id)?.full)} ↗</a>`:entityButton(ref.id,'Source location unavailable')).join('')}<small>Source links reference admin ${esc(overview.manifest.sources['breakdown-admin'].slice(0,10))}. Explanation review is tracked separately. Static evidence does not establish runtime execution or deployment.</small></details></div></section></div>`;
    host.querySelector('.journey-rail').scrollTop=railScroll;host.querySelector('.journey-detail').scrollTop=contentScroll;
    host.querySelectorAll('[data-go]').forEach(el=>el.classList.toggle('entity-selected',el.dataset.go===focus));
    if(focusAttribute)Array.from(host.querySelectorAll(`[${focusAttribute}]`)).find(el=>el.getAttribute(focusAttribute)===focusValue)?.focus({preventScroll:true});
  }
  return {render};
}
