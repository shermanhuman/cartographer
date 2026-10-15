import {investigations, investigationHTML, investigationState} from './investigations.js';
import {journeys, resolveJourney, resolveStep} from './journey-model.js';
import {createJourneyBrowser} from './journey-browser.js';
import {createFileBrowser} from './file-browser.js';
import {createSchemaBrowser} from './schema-browser.js';
import {functionNeighborhood} from './graph-model.js';
const $ = s => document.querySelector(s);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const getJSON = async url => {const r=await fetch(url);if(!r.ok)throw new Error(`Could not load ${url} (${r.status})`);return r.json()};
let overview, catalog, entities, state, freshness, currentGraph, renderToken=0, searchKind='all', listMode=false;
let fileBrowser, schemaBrowser, fileManifest, schemaIndex, journeyBrowser;
let transform={x:0,y:0,z:1}, dimensions={w:1000,h:600}, lastLayoutKey='';
const cache = new Map();
const kinds = {module:'Module',function:'Function',table:'Table',service:'Service',external:'External source',file:'Tracked file'};
const subtitles={modules:'Browse source modules across three repositories. Open one to explore its functions.',tables:'Explore migration-derived tables, columns and physical or inferred relationships.',changes:'Proposed structural improvements, attached to the code they affect.'};
const shortRepo=r=>r?.replace('breakdown-','')||'source';
const badge=(text,tone='')=>`<span class="badge ${tone}">${esc(text)}</span>`;
const icon=e=>`<span class="tech-icon">${e?.icon?`<img src="assets/${esc(e.icon)}.svg" alt="">`:e?.kind==='function'?'ƒ':e?.kind==='table'?'▤':e?.kind==='module'?'◇':'↗'}</span>`;
const link=(url,label)=>`<a href="${esc(url)}" target="_blank" rel="noreferrer">${esc(label)} ↗</a>`;
const action=(id,label,sub='')=>`<button class="inspector-link" data-go="${esc(id)}">${esc(label)}${sub?`<small>${esc(sub)}</small>`:''}</button>`;
function showFreshness(){if(!freshness)return;const overdue=freshness.observation_mode==='watch'&&Date.now()-Date.parse(freshness.checked_at)>120000;const n=Object.values(freshness.steps).filter(s=>s.status!=='reviewed').length;$('.freshness-detail').textContent=`${overdue?'Freshness check overdue':freshness.status==='current'?'Matches compared source revisions':'Source comparison needs attention'} · ${n} steps need review${freshness.checked_at?' · checked '+new Date(freshness.checked_at).toLocaleString():''} · deployment unknown`;$('.freshness').classList.toggle('needs-review',overdue||freshness.status!=='current')}
function byId(id){return entities.get(id)}
function parent(e){return e?.parent?byId(e.parent):e}
function changesFor(e){const p=parent(e);return overview.changes.filter(c=>c.entities.includes(p?.id)||e?.id==='s:admin'||e?.id==='s:oban'&&c.id==='SM-02')}
function evidenceFor(e){const p=parent(e);return overview.evidence.filter(x=>x.entities.includes(p?.id)||e?.id==='s:admin'||e?.id==='s:rosearch'&&x.flows.includes('search')||e?.id==='s:postgres'&&x.entities.some(id=>id.startsWith('public.')))}
async function detail(e){if(!e?.shard)return e||{};if(!cache.has(e.shard))cache.set(e.shard,getJSON(`data/${e.shard}`).catch(error=>{cache.delete(e.shard);throw error}));const d=await cache.get(e.shard);return e.kind==='function'?d.functions[e.id]:d}
function parse(){const p=new URLSearchParams(location.hash.slice(1));return {view:p.get('view')||'home',entity:p.get('entity')||'',focus:p.get('focus')||'',lens:p.get('lens')||'architecture',tab:p.get('tab')||'overview',page:Math.max(0,Number(p.get('page'))||0),trail:p.get('trail')||'',filter:p.get('filter')||'',file:p.get('file')||'',table:p.get('table')||'',context:p.get('context')||'',step:p.get('step')||'',investigation:p.get('investigation')||'',checkpoint:Math.max(0,Number(p.get('checkpoint'))||0)}}
function navigate(next,replace=false){const merged={...state,...next};const p=new URLSearchParams();for(const [k,v]of Object.entries(merged))if(v!==''&&v!==0)p.set(k,v);history[replace?'replaceState':'pushState']({},'',`${location.pathname}${location.search}#${p}`);state=merged;render()}
function investigate(id,index=0){const check=investigationState(id,index);if(!check)return;navigate({...check,focus:'',entity:'',tab:check.lens==='coverage'||check.lens==='performance'?'evidence':check.lens==='changes'?'changes':'overview',page:0})}
function journey(view){listMode=false;const j=(Object.hasOwn(journeys,view)?journeys[view]:null), keep=['modules','tables','changes'].includes(view);navigate({view,investigation:'',checkpoint:0,entity:'',focus:'',tab:'overview',page:0,trail:'',filter:'',context:j?view:keep?state.context:'',step:j?j.steps[0].id:keep?state.step:'',lens:j||keep?state.lens:'architecture'})}
function chooseStep(id){const context=resolveJourney(state.view,state.context),step=resolveStep(context,id);if(step)navigate({view:context,context,step:step.id,focus:step.code[0]||'',entity:'',tab:'overview',page:0})}
function returnToJourney(){const context=resolveJourney(state.view,state.context);if(context)navigate({view:context,context,entity:'',page:0})}
async function browseSource(id){const e=byId(id);if(!e)return;if(e.kind==='table'){navigate({view:'tables',table:id,focus:id,entity:'',tab:'data'});return}const p=parent(e);if(p?.file){await ensureBrowser('modules');const file=fileManifest.repositories.flatMap(r=>r.files).find(f=>f.repo===p.repo&&f.path===p.file);if(file)navigate({view:'modules',filter:'',file:file.id,focus:id,entity:'',tab:e.kind==='function'?'code':'overview'})}}
async function browseRepository(repo){if(!overview.manifest.sources[repo])return;await ensureBrowser('modules');fileBrowser.filterRepository(repo);navigate({view:'modules',filter:repo,file:'',entity:'',focus:'',tab:'overview',page:0})}
function openGraph(id){if(!byId(id))return;listMode=false;navigate({view:'entity',entity:id,focus:id,tab:'code',page:0,trail:state.context||(['modules','tables'].includes(state.view)?state.view:state.trail)})}
function explore(id){const e=byId(id);if(!e)return toast('This entity is not in the source snapshot.');if((Object.hasOwn(journeys,state.view)?journeys[state.view]:null)){navigate({focus:id,tab:e.kind==='function'?'code':e.kind==='table'?'data':'overview'});return}if(['module','table'].includes(e.kind)){browseSource(id);return}openGraph(id)}
function select(id){navigate({focus:id,tab:state.tab||'overview'})}
function toast(message){$('#toast').textContent=message;$('#toast').classList.add('visible');clearTimeout(toast.timer);toast.timer=setTimeout(()=>$('#toast').classList.remove('visible'),2700)}

function grid(ids,offsetY=50){return ids.map((id,i)=>({id,x:25+(i%3)*290,y:offsetY+Math.floor(i/3)*180}))}
async function ensureBrowser(view){
  if(view==='modules'&&!fileBrowser){
    fileManifest=await getJSON('data/files.json');
    for(const f of fileManifest.repositories.flatMap(r=>r.files))entities.set(f.id,{...f,kind:'file',label:f.name,full:f.path,file:f.path});
    fileBrowser=createFileBrowser({host:$('#file-browser-host'),manifest:fileManifest,byId,detail,filterChanged:filter=>navigate({filter},true),selectFile:f=>navigate({file:f.id,focus:f.modules[0]||f.id,tab:'overview'}),selectEntity:id=>navigate({focus:id,tab:'code'}),openEntity:openGraph});
  }
  if(view==='tables'&&!schemaBrowser){schemaIndex=await getJSON('data/schema-index.json');schemaBrowser=createSchemaBrowser({host:$('#schema-browser-host'),tables:schemaIndex,byId,detail,selectTable:id=>navigate({table:id,focus:id,tab:'overview'}),openEntity:openGraph})}
}
async function graph(){
  if((Object.hasOwn(journeys,state.view)?journeys[state.view]:null)){const j=(Object.hasOwn(journeys,state.view)?journeys[state.view]:null);state.context=state.view;const step=resolveStep(state.view,state.step);state.step=step.id;return {title:j.title,subtitle:j.question,nodes:[],edges:[],type:'PINNED SOURCE · REVIEW STATUS ATTACHED',note:j.scope,journey:true}}
  if(Object.hasOwn(overview.views,state.view))return {...overview.views[state.view],type:'CURATED RELATIONSHIPS'};
  if(state.view==='modules'){await ensureBrowser('modules');return {title:'Code explorer',subtitle:'Browse repositories and folders. Select a file, inspect its functions, then open a relationship graph.',nodes:[],edges:[],type:'TRACKED FILES · SOURCE SNAPSHOT',note:`${fileManifest.repositories.reduce((n,r)=>n+r.count,0).toLocaleString()} tracked files across three repositories. All paths are browsable; function details cover the indexed subset. Developer tooling is labeled separately.`,browser:true}}
  if(state.view==='tables'){await ensureBrowser('tables');return {title:'Data structure',subtitle:'Find a table or column. Inspect columns, keys, indexes and relationships in one place.',nodes:[],edges:[],type:'MIGRATION-DERIVED SCHEMA',note:`All ${schemaIndex.length} tables are browsable. Physical foreign keys, reviewed logical links and inferred relationships remain distinct. No production counts or live queries are run.`,browser:true}}
  if(state.view==='changes')return {title:'Structural improvements',subtitle:subtitles.changes,nodes:grid([...new Set(overview.changes.flatMap(c=>c.entities))]),edges:[],type:'PROPOSED · NOT IMPLEMENTED',note:'Recommendations are recorded on issue #120. These are selected source-review findings, not a completed line-by-line review of every inventoried function.'};
  const e=byId(state.entity);if(!e)return {title:'Entity not found',subtitle:'This link refers to an entity outside the loaded snapshot.',nodes:[],edges:[],note:'Use search to find a current snapshot entity or return to System overview.',type:'UNKNOWN ENTITY'};
  const d=await detail(e), title=e.kind==='function'?e.label:e.label;
  let nodes=[],edges=[],note='',pages=1,total=0;
  if(e.kind==='module'){
    const neighbors=new Map();
    for(const fn of Object.values(d.functions))for(const [direction,calls]of [['in',fn.in],['out',fn.out]])for(const call of calls||[]){const target=parent(byId(call.id));if(target?.kind==='module'&&target.id!==e.id){const key=direction+':'+target.id;const old=neighbors.get(key)||{id:target.id,direction,sites:0};old.sites+=call.sites;neighbors.set(key,old)}}
    const incoming=[...neighbors.values()].filter(n=>n.direction==='in'),outgoing=[...neighbors.values()].filter(n=>n.direction==='out');
    const ids=[...new Set([...incoming,...outgoing].map(n=>n.id))];nodes=[{id:e.id,x:315,y:20},...grid(ids,225)];
    edges=[...incoming.map(n=>({from:n.id,to:e.id,label:n.sites+' resolved call sites'})),...outgoing.map(n=>({from:e.id,to:n.id,label:n.sites+' resolved call sites'}))];
    note='All resolved module dependencies in the extracted call graph. These are calls across module boundaries, not execution order. Open the source tree for function membership. Dynamic and unresolved calls are not represented.';
  }else if(e.kind==='function'){
    const neighborhood=functionNeighborhood(e.id,d),{incoming,outgoingOnly,selfSites}=neighborhood;const height=Math.max(incoming.length,outgoingOnly.length,1)*158;
    nodes=[{id:e.id,x:345,y:(height-126)/2},...incoming.map((n,i)=>({id:n.id,x:20,y:i*158})),...outgoingOnly.map((n,i)=>({id:n.id,x:675,y:i*158}))];
    edges=neighborhood.edges.map(n=>({from:n.from,to:n.to,label:`${n.sites} call site${n.sites===1?'':'s'}`}));
    note=`Focused neighborhood: ${d.in.length} resolved callers, ${d.out.length} resolved callees; all other resolved neighbors and both directions of reciprocal calls are shown. ${selfSites} self-recursive call sites are listed in Code but not drawn as loops. ${esc(d.external_or_unresolved_sites)} external, generated or unresolved call sites are not drawn. Static edges are not a complete runtime graph.`;
  }else if(e.kind==='table'){
    const relationships=d.relationships.filter(r=>byId(r.from)&&byId(r.to));const ids=[...new Set(relationships.flatMap(r=>[r.from,r.to]))].filter(id=>id!==e.id);total=ids.length;const subset=ids;
    nodes=[{id:e.id,x:315,y:20},...grid(subset,225)];const shown=new Set(nodes.map(n=>n.id));edges=relationships.filter(r=>shown.has(r.from)&&shown.has(r.to)).map(r=>({from:r.from,to:r.to,label:r.kind==='physical_fk'?'physical FK':r.kind?.includes('candidate')?'inferred candidate':'logical relationship'}));note='Physical foreign keys and logical/inferred relationships remain distinct. The schema comes from local migrations, not a live production drift check.';
  }else{
    const ids=e.modules||e.tables||[];nodes=[{id:e.id,x:315,y:20},...grid(ids,225)];edges=nodes.slice(1).map(n=>({from:e.id,to:n.id,label:'contains / owns'}));note='Selected entry points for this service. Use Code explorer or Data structure to browse the full inventory.';
  }
  return {title,subtitle:e.full+(e.kind==='function'?' · Focused call neighborhood':` · ${kinds[e.kind]}`),nodes,edges,note,pages,total,type:e.kind==='function'?'EXTRACTED CALL SITES':e.kind==='table'?'PHYSICAL + LOGICAL LINKS':'STRUCTURAL DRILL-DOWN'};
}

function nodeBadge(e){
  if(state.lens==='changes'){const n=changesFor(e).length;return badge(n?`${n} proposed improvement${n===1?'':'s'}`:'No linked proposal',n?'purple':'gray')}
  if(['coverage','performance'].includes(state.lens)){const a=evidenceFor(e).filter(x=>x.kind===state.lens);return badge(a.length?`${a.length} evidence item${a.length===1?'':'s'}`:state.lens==='performance'&&e.cost?`${e.cost} unmeasured candidate${e.cost===1?'':'s'}`:'No attached measurement',a.length?'amber':'gray')}
  return badge(e.kind==='module'?`${e.count} functions`:e.kind==='table'?`${e.count} columns`:e.kind==='function'?'Source function':e.kind==='external'?'External dependency':shortRepo(e.repo));
}
function draw(g){
  const stage=$('#stage'), ns=g.nodes.map(n=>({...n,e:byId(n.id)})).filter(n=>n.e);const minX=Math.min(...ns.map(n=>n.x),0), minY=ns.length?Math.min(...ns.map(n=>n.y)):0;for(const n of ns){n.x-=minX;n.y-=minY-20}const byNode=new Map(ns.map(n=>[n.id,n]));dimensions={w:Math.max(600,...ns.map(n=>n.x+260)),h:Math.max(300,...ns.map(n=>n.y+150))};
  const directedPairs=new Set(g.edges.map(x=>JSON.stringify([x.from,x.to])));
  const paths=g.edges.filter(x=>byNode.has(x.from)&&byNode.has(x.to)).map(x=>{
    const a=byNode.get(x.from),b=byNode.get(x.to);let x1=a.x+235,y1=a.y+63,x2=b.x,y2=b.y+63;
    if(Math.abs(a.x-b.x)<50){x1=a.x+117;y1=a.y+126;x2=b.x+117;y2=b.y}
    if(b.x<a.x-50){x1=a.x;y1=a.y+63;x2=b.x+235;y2=b.y+63}
    if(directedPairs.has(JSON.stringify([x.to,x.from]))){
      if(Math.abs(a.x-b.x)<50){const lane=a.y<b.y?-12:12;x1+=lane;x2+=lane}
      else {const lane=a.x<b.x?-12:12;y1+=lane;y2+=lane}
    }
    const d=Math.abs(a.x-b.x)<50?`M${x1},${y1} C${x1},${(y1+y2)/2} ${x2},${(y1+y2)/2} ${x2},${y2}`:`M${x1},${y1} C${(x1+x2)/2},${y1} ${(x1+x2)/2},${y2} ${x2},${y2}`;
    return `<path d="${d}" marker-end="url(#arrow)"/><text x="${(x1+x2)/2}" y="${(y1+y2)/2-9}" text-anchor="middle">${esc(x.label||'')}</text>`;
  }).join('');
  stage.innerHTML=`<svg class="edges" width="${dimensions.w}" height="${dimensions.h}" aria-hidden="true"><defs><marker id="arrow" markerWidth="6" markerHeight="6" refX="5" refY="3" orient="auto"><path d="M0,0 L6,3 L0,6" style="fill:#9eb7a9;stroke:none"/></marker></defs>${paths}</svg>`+ns.map(n=>`<button class="map-node ${state.focus===n.id?'focused':''} ${state.lens==='changes'&&changesFor(n.e).length?'has-change':''}" style="left:${n.x}px;top:${n.y}px" data-select="${esc(n.id)}" title="${esc(n.e.full)}" aria-label="Inspect ${esc(n.e.full)}"><div class="node-top">${icon(n.e)}<span class="node-name">${esc(n.label||n.e.label)}</span></div><div class="node-meta">${esc(n.e.language || kinds[n.e.kind])} · ${esc(shortRepo(n.e.repo))}</div>${n.e.file?`<div class="node-path" title="${esc(n.e.file)}">${esc(n.e.file)}</div>`:''}<div class="node-foot">${nodeBadge(n.e)}<span aria-hidden="true">↗</span></div></button>`).join('');
  $('#minimap').innerHTML=`<svg viewBox="0 0 ${dimensions.w} ${dimensions.h}" aria-hidden="true">${ns.map(n=>`<rect x="${n.x}" y="${n.y}" width="235" height="126" rx="14" fill="${n.id===state.focus?'#70a58a':'#d8e5dc'}"/>`).join('')}</svg>`;
  $('#list-view').innerHTML=ns.map(n=>`<button class="entity-row" data-select="${esc(n.id)}">${icon(n.e)}<span><strong>${esc(n.label||n.e.label)}</strong><small>${esc(n.e.full)}</small></span>${nodeBadge(n.e)}</button>`).join('')||'<div class="empty-state">No entities in this view.</div>';
  if(state.view==='changes')$('#list-view').innerHTML=`<div class="change-directory">${overview.changes.map(c=>changeCard(c,true)).join('')}</div>`;
  $('#canvas').hidden=listMode||state.view==='changes';$('#list-view').hidden=!$('#canvas').hidden;$('.map-controls').hidden=$('#canvas').hidden;$('#minimap').hidden=$('#canvas').hidden;$('.legend').hidden=$('#canvas').hidden;
  $('#list-toggle').textContent=listMode?'Map view':'List view';$('#list-toggle').disabled=state.view==='changes';
  $('#entity-count').textContent=`${ns.length} entities in view`;
  const layoutKey=JSON.stringify(ns.map(n=>[n.id,n.x,n.y]));
  if(layoutKey!==lastLayoutKey){lastLayoutKey=layoutKey;requestAnimationFrame(fit)}
}
function applyTransform(){ $('#stage').style.transform=`translate(${transform.x}px,${transform.y}px) scale(${transform.z})`;$('#zoom-level').textContent=`${Math.round(transform.z*100)}%`}
function fit(){const c=$('#canvas');if(c.hidden||!currentGraph)return;transform.z=Math.min(1.05,(c.clientWidth-40)/dimensions.w,(c.clientHeight-28)/dimensions.h);transform.z=Math.max(.15,transform.z);transform.x=(c.clientWidth-dimensions.w*transform.z)/2;transform.y=(c.clientHeight-dimensions.h*transform.z)/2;applyTransform()}
function zoom(factor){const c=$('#canvas'),z=Math.max(.15,Math.min(2,transform.z*factor)),ratio=z/transform.z;transform.x=c.clientWidth/2-(c.clientWidth/2-transform.x)*ratio;transform.y=c.clientHeight/2-(c.clientHeight/2-transform.y)*ratio;transform.z=z;applyTransform()}

function evidenceCard(e){return `<article class="evidence-card">${badge(e.status,['Unknown','Finding'].includes(e.status)?'amber':'blue')}<div class="metric">${esc(e.value)}</div><strong>${esc(e.title)}</strong><p>${esc(e.body)}</p><p>Observed: ${esc(e.date)} · snapshot evidence</p>${e.url?link(e.url,'Open underlying audit'):'<small>Historical audit retained locally; raw samples are not included in this map.</small>'}</article>`}
function changeCard(c,full=false){return `<article class="change-card"><span class="change-id">${esc(c.id)} · ${esc(c.priority)}</span>${badge('Proposed','purple')}<strong>${esc(c.title)}</strong><p>${esc(c.summary)}</p>${full?`<p><b>Why:</b> ${esc(c.why)}</p><p><b>Preserve:</b> ${esc(c.guard)}</p>${c.entities.map(id=>action(id,byId(id).label,'Open affected module')).join('')}`:`<p>${esc(c.why)}</p>`}${link(c.url,'Recorded on issue #120')}</article>`}
function expandableItems(items,render,label,visible=30){const first=items.slice(0,visible).map(render).join('');if(items.length<=visible)return first;return first+`<details class="additional-references"><summary>${items.length-visible} more ${esc(label)} (${items.length} total)</summary>${items.slice(visible).map(render).join('')}</details>`}
function relatedTableLinks(ids){return `<h3>Related tables (${ids.length})</h3>`+expandableItems(ids,id=>action(id,byId(id)?.label||id,'Textual reference in this module’s file · inspect eligibility'),'tables')}
function sectionList(label,items){return `<h3>${esc(label)} (${items.length})</h3>`+(items.length?items.map(n=>action(n.id,byId(n.id)?.label||n.id,`${byId(n.id)?.full} · ${n.sites} call site${n.sites===1?'':'s'}`)).join(''):'<p>None resolved in the static inventory.</p>')}
async function inspect(e,token){
  $('#inspector-header').innerHTML=`<div class="inspector-head"><div class="inspector-eyebrow">${e?esc(kinds[e.kind]):'YOUR SYSTEM MAP'}</div><div class="inspector-title">${icon(e)}<h2>${esc(e?.label||'Explore a connected system')}</h2></div><p class="subtext">${esc(e?.kind==='function'?parent(e).full:e?.repo||'Select a node to see its context')}</p></div>`;
  const ctx=resolveJourney(state.view,state.context),step=resolveStep(ctx,state.step);
  if(ctx&&step){$('#inspector-header').insertAdjacentHTML('afterbegin',`<div class="inspector-context">Inspecting within ${esc(journeys[ctx].title)}<button data-return-journey>↩ ${esc(step.title)}</button></div>`)}
  if(e&&['module','function','table'].includes(e.kind))$('#inspector-header').insertAdjacentHTML('beforeend',`<div class="inspector-tools"><button data-browse-source="${esc(e.id)}">${e.kind==='table'?'Open full schema':'Open file tree'} ↗</button><button data-open-graph="${esc(e.id)}">${e.kind==='table'?'Relationships':e.kind==='module'?'Module dependencies':'Call neighborhood'} ↗</button></div>`);
  const tabs=['overview','code','data','evidence','changes'];$('#inspector-tabs').innerHTML=tabs.map(t=>`<button data-tab="${t}" class="${state.tab===t?'selected':''}">${t[0].toUpperCase()+t.slice(1)}</button>`).join('');
  if(!e){$('#inspector-content').innerHTML=['modules','tables'].includes(state.view)?'<p>Select a file, function or table to inspect its source, audit evidence and proposed improvements here.</p><div class="note">Browsing does not imply complete function extraction or a completed design review.</div>':`<p>Start with one product journey. Every node connects code, data, evidence and proposed improvements.</p><h3>Try this path</h3><button class="primary" data-view="sync">Explore Sync & enrichment →</button><div class="note">This is a local source snapshot. Deployed-code tracking and live monitoring are not connected.</div>`;return}
  if(e.kind==='file'){$('#inspector-content').innerHTML=`<p>${esc(e.file)}</p><div class="kv"><span>Category</span><span>${esc(e.category)}</span></div><div class="kv"><span>Source revision</span><span>${esc(overview.manifest.sources[e.repo].slice(0,10))}</span></div><p>${link(e.url,'Open source at snapshot revision')}</p><div class="note">This file has no indexed function details. That is an extraction limit, not a claim that it contains no behavior.</div>`;return}
  $('#inspector-content').innerHTML='<p>Loading source details…</p>';const d=await detail(e);if(token!==renderToken)return;
  let html='';const changes=changesFor(e),ev=evidenceFor(e),go=`<button class="primary" ${['module','function','table'].includes(e.kind)?'data-browse-source':'data-open-graph'}="${esc(e.id)}">${e.kind==='table'?'Open full schema':['module','function'].includes(e.kind)?'Open source file':'Explore service'} →</button>`;
  if(state.tab==='overview'){
    const summaries={CodeTags:'Extracts trouble-code tags and maintains tenant/shop count summaries.',ConceptTags:'Stores versioned concept tags, pending work and completion state.',ConceptTagBackfill:'Runs concept tagging in bounded chunks, drains changed records and retires older versions.',CodeTagBackfill:'Drains changed repair orders and rebuilds due tenant code counts.',SearchTsvBackfill:'Backfills searchable full-text vectors for the four text source tables.',VerifyNightly:'Selects due shops and dispatches nightly verification jobs.',VerifyWeekly:'Dispatches verification using a weekly window and cadence policy.',VerifyMonthly:'Dispatches verification for the prior calendar month.',VerifyShop:'Runs per-shop verification and records its result.',RepairOrders:'Synchronizes repair orders and child data, then queues enrichment.',Facets:'Builds search facets and baseline summaries.',Search:'Coordinates search execution, matching, counts, logging and related helpers.',Policy:'Classifies impersonation HTTP routes with default-deny behavior.'};
    html=`<p>${esc(e.summary||summaries[e.label]||(e.kind==='function'?'Source-defined function. Explore its callers and callees; dynamic paths may be unresolved.':e.kind==='table'?`Part of ${e.domain}. Inspect columns and links, then follow source references.`:'Source-derived module inventory. Its responsibility description has not yet been reviewed.'))}</p><div class="kv"><span>Repository</span><span>${esc(e.repo)}</span></div><div class="kv"><span>Source revision</span><span>${esc(overview.manifest.sources[e.repo]?.slice(0,10)||'snapshot')}</span></div><div class="kv"><span>Runtime freshness</span><span>Not connected</span></div>${e.count?`<div class="kv"><span>${e.kind==='table'?'Columns':'Source functions'}</span><span>${esc(e.count)}</span></div>`:''}${go}<h3>Attached context</h3><button class="inspector-link" data-tab="evidence">${ev.length} evidence items <small>Measured performance, sampled coverage and unknowns</small></button><button class="inspector-link" data-tab="changes">${changes.length} structural improvements <small>Proposed changes · not implemented</small></button>${e.kind==='function'&&e.parent?action(e.parent,'Back to '+parent(e).label,'Containing module'):''}`;
  }else if(state.tab==='code'){
    const source=d.url||d.sources?.[0]?.url;html=source?`<p>${link(source,'Open source at snapshot revision')}</p>`:'<p>Open a contained module to inspect its source.</p>';
    if(e.kind==='function')html+=`<p>${esc(d.file)}:${esc(d.line)}<br>${esc(d.kind)} · ${esc(d.language)}</p>${sectionList('Resolved callers',d.in)}${sectionList('Resolved callees',d.out)}<div class="note">${esc(d.external_or_unresolved_sites)} call sites point outside the source inventory or could not be resolved. Absence of a static edge does not prove absence of a runtime call.</div>`;
    else if(e.kind==='module')html+=`<h3>Defined functions (${Object.keys(d.functions).length})</h3>${Object.values(d.functions).sort((a,b)=>a.name.localeCompare(b.name)).map(f=>action(f.id,byId(f.id).label,`${f.kind} · line ${f.line}`)).join('')}`;
    else if(e.kind==='table')html+=`<div class="note">These are textual references, including comments and query fragments. They are not proven read/write edges.</div><h3>Source references (${(d.references||[]).length})</h3>${expandableItems(d.references||[],r=>`<div class="inspector-link">${r.function&&byId(r.function)?action(r.function,byId(r.function).label):link(r.url,r.file+':'+r.line)}<small>${esc(r.kind)}</small></div>`,'references',35)||'<p>No references resolved.</p>'}`;
    else html+=(e.modules||[]).map(id=>action(id,byId(id).label,byId(id).full)).join('');
  }else if(state.tab==='data'){
    if(e.kind==='table')html=`<p>Migration-derived schema · ${esc(d.schema)}.${esc(d.name)}</p><h3>Columns (${d.columns.length})</h3><table class="columns-table"><tbody>${d.columns.map(c=>`<tr><td>${esc(c.name)}</td><td>${esc(c.type)}${c.nullable?'':' · required'}</td></tr>`).join('')}</tbody></table><h3>Relationships (${d.relationships.length})</h3>${d.relationships.map(r=>action(r.from===e.id?r.to:r.from,byId(r.from===e.id?r.to:r.from)?.label||r.to,r.kind+' · '+(r.definition||r.name||'Logical source relationship'))).join('')}<h3>Indexes (${d.indexes.length})</h3>${d.indexes.map(i=>`<div class="inspector-link">${esc(i.name)}<small>${esc(i.definition)}</small></div>`).join('')}`;
    else {const tables=e.tables||d.tables||[];html=`<div class="note">${e.tables?'Selected schema entry points for this service.':'File-level textual references, not proven function-level reads/writes. Comments and dynamic SQL may appear; relationships require review.'}</div>${tables.length?relatedTableLinks(tables):'<p>No table references attached. This does not establish that the code never accesses data.</p>'}`}
  }else if(state.tab==='evidence'){
    html=ev.length?(e.kind==='function'?'<p>Context attached through the containing module; these are not necessarily measurements of this individual function.</p>':'')+ev.map(evidenceCard).join(''):'<div class="note">No measurement is attached to this entity. This is unknown, not a passing coverage or performance result.</div>';
    if(e.cost||d.cost)html+=`<h3>Static cost candidates</h3><p>${e.kind==='function'?esc(d.cost?.tags?.join(' · ')||'Database work candidate'):`${e.cost} functions flagged for cost review.`} A source marker does not establish runtime expense.</p>`;
    html+='<div class="note">Evidence belongs to its recorded revision, scope and sample. These badges are not live health signals.</div>';
  }else if(state.tab==='changes')html=changes.length?changes.map(c=>changeCard(c,true)).join(''):'<p>No structural improvement is linked to this entity yet. That does not mean it has passed a complete design review.</p>';
  const appearances=Object.entries(journeys).flatMap(([journey,j])=>j.steps.filter(s=>s.code.includes(e.id)||s.data.some(r=>r.entity===e.id)).map(s=>({journey,j,s})));
  if(e.kind==='service'&&overview.manifest.sources[e.repo])html+=`<h3>Repository inventory</h3><button class="primary" data-browse-repository="${esc(e.repo)}">Browse repository files →</button><p class="subtext">${esc(e.repo)} · all tracked paths, including configuration and infrastructure.</p>`;
  if(appearances.length)html+=`<h3>Used in reviewed journeys</h3>${appearances.map(({journey,j,s})=>`<button class="inspector-link" data-journey-target="${journey}" data-target-step="${s.id}">${esc(s.title)}<small>${esc(j.title)}</small></button>`).join('')}`;
  $('#inspector-content').innerHTML=html;
}
function crumbs(){
  let h='<button data-view="home">System Map</button>';const ctx=resolveJourney(state.view,state.context),step=resolveStep(ctx,state.step),e=byId(state.entity);
  if(ctx){h+=`<span>/</span><button data-return-journey>${esc(journeys[ctx].title)}</button><span>/</span><button data-return-journey>${esc(step.title)}</button>`;if(!(Object.hasOwn(journeys,state.view)?journeys[state.view]:null))h+=`<span>/</span><span>${esc(e?.label||currentGraph.title)}</span>`}
  else {if(state.trail&&state.view==='entity')h+=`<span>/</span><button data-view="${esc(state.trail)}">${esc(overview.views[state.trail]?.title||({modules:'Code explorer',tables:'Data structure'}[state.trail])||state.trail)}</button>`;if(e?.parent)h+=`<span>/</span><button data-go="${esc(e.parent)}">${esc(parent(e).label)}</button>`;if(state.view!=='home')h+=`<span>/</span><span>${esc(e?.label||currentGraph.title)}</span>`}
  $('#breadcrumbs').innerHTML=h;
}
async function render(){
  const token=++renderToken;
  try{
    if(!['overview','code','data','evidence','changes'].includes(state.tab))state.tab='overview';if(!['architecture','data','failure','coverage','performance','changes'].includes(state.lens))state.lens='architecture';
    const g=await graph();if(token!==renderToken)return;currentGraph=g;
    if(!state.focus)state.focus=(Object.hasOwn(journeys,state.view)?journeys[state.view]:null)?resolveStep(state.view,state.step).code[0]:state.view==='home'?'s:admin':state.view==='entity'?state.entity:state.view==='modules'&&state.file?(fileBrowser.fileMap.get(state.file)?.modules[0]||state.file):state.view==='tables'?state.table:g.nodes[0]?.id||'';
    $('#investigation-host').innerHTML=investigationHTML(state);
    $('#title').textContent=g.title;$('#subtitle').textContent=g.subtitle||'';$('#map-scope').textContent=g.title.toUpperCase();$('#map-type').textContent=g.type;$('#map-note').textContent=g.note;crumbs();
    document.querySelectorAll('.nav-item').forEach(b=>b.classList.toggle('active',b.dataset.view===(state.context&&state.view==='entity'?state.context:state.view)));document.querySelectorAll('[data-lens]').forEach(b=>b.classList.toggle('selected',b.dataset.lens===state.lens));
    const old=$('#inventory-filter');if(old)old.remove();
    $('#pagination').innerHTML=g.pages>1?`<button class="secondary" data-page="${state.page-1}" ${!state.page?'disabled':''}>←</button><span>${state.page+1} / ${g.pages}</span><button class="secondary" data-page="${state.page+1}" ${state.page+1>=g.pages?'disabled':''}>→</button>`:'';
    $('#journeys').innerHTML=state.view==='home'?[['sync','⇄','A repair order changes','When is the edit searchable? What can fail?'],['search','⌕','A shop searches','How do matching, counts and facets work?'],['verify','◷','A shop is verified','What does a pass actually prove?']].map(([v,i,t,s])=>`<button class="journey" data-view="${v}"><span class="journey-icon">${i}</span><span><strong>${t}</strong><small>${s}</small></span><span class="arrow">↗</span></button>`).join(''):'';
    $('.workbench').classList.toggle('browsing',Boolean(g.browser));
    $('.workbench').classList.toggle('journey-mode',Boolean(g.journey));
    $('.map-panel').setAttribute('aria-label',g.journey?'Journey workspace':g.browser?g.title:'Interactive architecture map');
    $('#journey-host').hidden=!g.journey;
    const ctx=resolveJourney(state.view,state.context),step=resolveStep(ctx,state.step);
    $('#context-strip').hidden=!ctx||Boolean(g.journey);
    $('#context-strip').innerHTML=ctx&&step?`<div>Exploring from <strong>${esc(journeys[ctx].title)} / ${esc(step.title)}</strong></div><div class="context-actions"><button data-return-journey>↩ Return to journey</button><button data-clear-context>Leave journey</button></div>`:'';
    $('#file-browser-host').hidden=state.view!=='modules';$('#schema-browser-host').hidden=state.view!=='tables';
    $('#list-toggle').hidden=Boolean(g.browser||g.journey);$('#lenses').hidden=Boolean(g.browser||g.journey);
    if(g.journey){
      $('#canvas').hidden=true;$('#list-view').hidden=true;$('.map-controls').hidden=true;$('#minimap').hidden=true;$('.legend').hidden=true;
      $('#entity-count').textContent=(Object.hasOwn(journeys,state.view)?journeys[state.view]:null).steps.length+' steps · source, failure and change context';
      if(!journeyBrowser)journeyBrowser=createJourneyBrowser({freshness,host:$('#journey-host'),byId,detail,overview});
      await journeyBrowser.render({journey:state.view,step:state.step,lens:state.lens,focus:state.focus});
    }else if(g.browser){
      $('#canvas').hidden=true;$('#list-view').hidden=true;$('.map-controls').hidden=true;$('#minimap').hidden=true;$('.legend').hidden=true;
      $('#entity-count').textContent=state.view==='modules'?'All tracked files · no pagination':`All ${schemaIndex.length} tables · no pagination`;
      if(state.view==='modules')await fileBrowser.update({file:state.file,selected:state.focus,filter:state.filter});else await schemaBrowser.update(state.table);
    }else draw(g);
    if(token!==renderToken)return;
    await inspect(byId(state.focus),token);
  }catch(error){if(token===renderToken){$('#inspector-content').innerHTML=`<div class="error-box">${esc(error.message)}<p>Try returning to System overview or reloading the preview.</p></div>`;console.error(error)}}
}

function search(){const q=$('#global-search').value.trim().toLowerCase();const words=q.split(/\s+/).filter(Boolean);let rows=catalog.filter(e=>(searchKind==='all'||e.kind===searchKind)&&words.every(w=>(e.full+' '+e.repo+' '+(e.file||'')).toLowerCase().includes(w)));if(!q)rows=rows.filter(e=>['module','table','service'].includes(e.kind));rows.sort((a,b)=>{const score=e=>(e.label.toLowerCase()===q?4:e.label.toLowerCase().startsWith(q)?2:0)+(e.kind==='module'?1:0);return score(b)-score(a)||a.full.localeCompare(b.full)});$('#search-results').innerHTML=rows.length?`<p>${rows.length.toLocaleString()} matches${rows.length>40?' · showing 40; refine your search':''}</p>`+rows.slice(0,40).map(e=>`<button class="entity-row" data-search-result="${esc(e.id)}">${icon(e)}<span><strong>${esc(e.label)}</strong><small>${esc(e.full)} · ${esc(shortRepo(e.repo))}</small></span>${badge(kinds[e.kind],'gray')}</button>`).join(''):'<p>No matches. Try a module, function or table name.</p>'}
function openSearch(){$('#search-dialog').showModal();search();$('#global-search').focus()}
function sources(){const m=overview.manifest;$('#sources-content').innerHTML='<div class="note">Packaged source snapshot. Generation uses explicit repository revisions and a migration-derived schema; this page does not scan databases. Live deployment tracking is not connected.</div><h3>Repository revisions</h3>'+Object.entries(m.sources).map(([r,sha])=>`<p><b>${esc(r)}</b><br>${link(`https://github.com/Applesauce-Labs/${r}/tree/${sha}`,sha.slice(0,12))}</p>`).join('')+`<h3>Data structure</h3><p>${esc(m.schema_basis)}</p><h3>Extraction boundaries</h3><ul>${m.coverage.map(s=>`<li>${esc(s)}</li>`).join('')}</ul><h3>Rendering & icons</h3><p>The map uses a lightweight HTML/SVG renderer over the extracted graph. Technology icons are rendered locally from @likec4/icons; no external CDN or production endpoint is called.</p><p>${link(overview.proposal,'Complete proposal')} · ${link(overview.improvements,'Structural improvements')}</p>`;$('#sources-content').insertAdjacentHTML('afterbegin',`<section class="freshness-report"><h3>Explanation review</h3><p>${esc(freshness.review?.reviewer||"Reviewer not recorded")}<br>${esc(freshness.review?.reviewed_at||"Review date unknown")}</p><h3>Freshness check</h3><p>${esc(freshness.checked_at || "Snapshot comparison")} · ${esc(freshness.comparison)}</p>${freshness.repositories.map(r=>`<div class="freshness-repo"><b>${esc(r.repo)} · ${esc(r.status)}</b><p>Reviewed: ${esc(freshness.review?.sources?.[r.repo]?.slice(0,12)||'not recorded')}<br>Artifact: ${esc(r.snapshot_revision.slice(0,12))}<br>Compared: ${esc(r.compared_revision?.slice(0,12)||'unavailable')}<br>${r.changed_paths.length} changed paths · ${r.uncommitted_source_paths.length} uncommitted source paths</p></div>`).join('')}<p>Schema: ${esc(freshness.schema.status)} · Deployed revision: unknown</p><p>${esc(freshness.policy)}</p></section>`);$('#sources-dialog').showModal()}

document.addEventListener('click',event=>{const b=event.target.closest('button,a');if(!b)return;
  if(b.dataset.view){event.preventDefault();journey(b.dataset.view)}
  else if(b.dataset.journeyTarget){const step=resolveStep(b.dataset.journeyTarget,b.dataset.targetStep);navigate({view:b.dataset.journeyTarget,context:b.dataset.journeyTarget,step:step.id,entity:'',focus:state.focus,tab:'overview',page:0})}
  else if(b.hasAttribute('data-freshness-details'))sources();
  else if(b.dataset.investigation)investigate(b.dataset.investigation,Number(b.dataset.checkpoint)||0);
  else if(b.hasAttribute('data-close-investigation'))navigate({investigation:'',checkpoint:0});
  else if(b.dataset.step)chooseStep(b.dataset.step);
  else if(b.hasAttribute('data-return-journey'))returnToJourney();
  else if(b.hasAttribute('data-clear-context'))navigate({context:'',step:'',trail:''});
  else if(b.dataset.browseRepository)browseRepository(b.dataset.browseRepository);
  else if(b.dataset.browseSource)browseSource(b.dataset.browseSource);
  else if(b.dataset.openGraph)openGraph(b.dataset.openGraph);
  else if(b.dataset.go)explore(b.dataset.go);
  else if(b.dataset.select)select(b.dataset.select);
  else if(b.dataset.tab)navigate({tab:b.dataset.tab});
  else if(b.dataset.lens){const lens=b.dataset.lens;navigate({lens,tab:({architecture:'overview',data:'data',failure:'overview',changes:'changes'}[lens]||'evidence')})}
  else if(b.dataset.page!==undefined)navigate({page:Number(b.dataset.page),focus:''});
  else if(b.dataset.searchResult){$('#search-dialog').close();explore(b.dataset.searchResult)}
  else if(b.dataset.searchKind){searchKind=b.dataset.searchKind;document.querySelectorAll('[data-search-kind]').forEach(el=>el.classList.toggle('selected',el===b));search()}
});
$('#stage').addEventListener('dblclick',event=>{const node=event.target.closest('[data-select]');if(node)explore(node.dataset.select)});
$('#search-open').onclick=openSearch;$('#global-search').oninput=search;$('#provenance').onclick=sources;$('#source-button').onclick=sources;$('#close-sources').onclick=()=>$('#sources-dialog').close();
$('#global-search').onkeydown=event=>{if(event.key==='Enter'){event.preventDefault();const row=$('[data-search-result]');if(row)row.click()}};
document.addEventListener('keydown',event=>{if((event.metaKey||event.ctrlKey)&&event.key.toLowerCase()==='k'){event.preventDefault();if(!$('#search-dialog').open)openSearch()}});
$('#copy-link').onclick=async()=>{try{const p=new URLSearchParams();for(const [k,v]of Object.entries(state))if(v!==''&&v!==0)p.set(k,v);await navigator.clipboard.writeText(location.href.split('#')[0]+'#'+p);toast('Link copied · opens this view, selection and lens')}catch{toast('Copy this page’s address from the address bar.')}};
$('#fit').onclick=fit;$('#minimap').onclick=fit;$('#zoom-in').onclick=()=>zoom(1.2);$('#zoom-out').onclick=()=>zoom(1/1.2);$('#list-toggle').onclick=()=>{listMode=!listMode;draw(currentGraph)};
let drag=null;$('#canvas').addEventListener('pointerdown',event=>{if(event.target.closest('button'))return;drag={x:event.clientX,y:event.clientY,tx:transform.x,ty:transform.y};$('#canvas').setPointerCapture(event.pointerId);$('#canvas').classList.add('dragging')});$('#canvas').addEventListener('pointermove',event=>{if(!drag)return;transform.x=drag.tx+event.clientX-drag.x;transform.y=drag.ty+event.clientY-drag.y;applyTransform()});for(const name of ['pointerup','pointercancel','lostpointercapture'])$('#canvas').addEventListener(name,()=>{drag=null;$('#canvas').classList.remove('dragging')});$('#canvas').addEventListener('wheel',event=>{if(event.ctrlKey||event.metaKey){event.preventDefault();zoom(event.deltaY<0?1.08:1/1.08)}},{passive:false});
window.addEventListener('popstate',()=>{state=parse();render()});window.addEventListener('hashchange',()=>{state=parse();render()});new ResizeObserver(()=>requestAnimationFrame(fit)).observe($('#canvas'));
try{[overview,catalog,freshness]=await Promise.all([getJSON('data/overview.json'),getJSON('data/catalog.json'),getJSON('data/freshness.json')]);showFreshness();entities=new Map(catalog.map(e=>[e.id,e]));state=parse();$('#proposal-link').href=overview.proposal;document.querySelectorAll('[data-change-count]').forEach(el=>el.textContent=overview.changes.length);$('#footer-counts').textContent=`${catalog.filter(e=>e.kind==='module').length} modules · ${catalog.filter(e=>e.kind==='function').length.toLocaleString()} source functions · ${catalog.filter(e=>e.kind==='table').length} tables`;await render();document.documentElement.dataset.ready='true'}catch(error){$('#subtitle').textContent='The local map could not load.';$('#inspector-content').innerHTML=`<div class="error-box">${esc(error.message)}</div>`;console.error(error)}

// Poll only the prepared metadata report; the browser never scans repos or counts data.
setInterval(async()=>{if(document.hidden||!freshness||freshness.observation_mode!=='watch')return;showFreshness();try{const r=await fetch('data/freshness.json',{cache:'no-store'});if(!r.ok)return;const next=await r.json();const changed=JSON.stringify(next.steps)!==JSON.stringify(freshness.steps)||next.status!==freshness.status;Object.assign(freshness,next);showFreshness();if(changed)render()}catch{/* Last successful check remains timestamped. */}},30000);
