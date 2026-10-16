import {investigationHTML, investigationState, setInvestigations} from './investigations.js';
import {journeys, resolveJourney, resolveStep, setJourneys, setDomains} from './journey-model.js';
import {createJourneyBrowser} from './journey-browser.js';
import {createFileBrowser} from './file-browser.js';
import {createSchemaBrowser} from './schema-browser.js';
import {createContextBrowser} from './context-browser.js';
import {functionNeighborhood, layeredLayout} from './graph-model.js';
import {exportSVG, footerText} from './export.js';
import {esc, badge, reviewText, text} from './util.js';
const $ = s => document.querySelector(s);
const errorOf = async r => {try{return (await r.json()).error}catch{return ''}};
const getJSON = async url => {const r=await fetch(url);if(!r.ok)throw new Error(await errorOf(r)||`Could not load ${url} (${r.status})`);return r.json()};
const getText = async url => {const r=await fetch(url);if(!r.ok)throw new Error(await errorOf(r)||`Could not load ${url} (${r.status})`);return r.text()};
// Every API call carries the snapshot chosen in the deep link (default newest).
const SNAPSHOT = new URLSearchParams(location.hash.slice(1)).get('snapshot') || '';
const api = (path, params={}) => {const p=new URLSearchParams(params);if(SNAPSHOT)p.set('snapshot',SNAPSHOT);const q=p.toString();return `api/${path}${q?'?'+q:''}`};
let overview, catalog, entities, state, status, snapshots, currentGraph, renderToken=0, searchKind='all', listMode=false;
let fileBrowser, schemaBrowser, contextBrowser, fileManifest, schemaIndex, journeyBrowser;
let transform={x:0,y:0,z:1}, dimensions={w:1000,h:600}, lastLayoutKey='', drawn=[];
const cache = new Map();
const kinds = {module:'Module',function:'Function',table:'Table',service:'Repository',external:'External source',file:'Tracked file',context:'Context',annotation:'Annotation'};
const views = ['home','modules','tables','contexts','entity'];
const shortRepo=r=>r||'database';
const icon=e=>`<span class="tech-icon">${e?.icon?`<img src="assets/${esc(e.icon)}.svg" alt="">`:e?.kind==='function'?'ƒ':e?.kind==='table'?'▤':e?.kind==='module'?'◇':e?.kind==='context'?'◫':e?.kind==='annotation'?'✎':'↗'}</span>`;
const link=(url,label)=>url?`<a href="${esc(url)}" target="_blank" rel="noreferrer">${esc(label)} ↗</a>`:esc(label);
const action=(id,label,sub='')=>`<button class="inspector-link" data-go="${esc(id)}">${esc(label)}${sub?`<small>${esc(sub)}</small>`:''}</button>`;
const fmtDate=s=>{const d=new Date(s);return isNaN(d)?String(s||''):d.toLocaleString()};
function freshnessSummary(){const f=status?.freshness;return typeof f==='string'?f:f?.summary||''}
function showFreshness(){if(!status)return;const f=status.freshness,stale=Boolean(f&&typeof f==='object'&&f.stale);const review=Object.entries(status.annotations||{}).filter(([k])=>k!=='reviewed').reduce((n,[,v])=>n+v,0);$('#freshness-title').textContent=`Snapshot ${status.snapshot}`;$('.freshness-detail').textContent=[`built ${fmtDate(status.created_at)}`,(status.repos||[]).map(r=>`${r.name}@${String(r.revision).slice(0,7)}`).join(' '),freshnessSummary()||'working-copy freshness not checked',review?`${review} annotation${review===1?'':'s'} need review`:''].filter(Boolean).join(' · ');$('.freshness').classList.toggle('needs-review',stale||review>0)}
function byId(id){return entities.get(id)}
function parent(e){return e?.parent?byId(e.parent):e}
function journeyOf(view){return Object.hasOwn(journeys,view)?journeys[view]:null}
async function detail(e){if(!e?.shard)return e||{};if(!cache.has(e.shard))cache.set(e.shard,getJSON(api('entity/'+encodeURIComponent(e.shard))).catch(error=>{cache.delete(e.shard);throw error}));const d=await cache.get(e.shard);return e.kind==='function'?(d.functions?.[e.id]||{}):d}
function parse(){const p=new URLSearchParams(location.hash.slice(1));const print=p.get('print');return {view:p.get('view')||'home',entity:p.get('entity')||'',focus:p.get('focus')||'',tab:p.get('tab')||'overview',page:Math.max(0,Number(p.get('page'))||0),trail:p.get('trail')||'',filter:p.get('filter')||'',file:p.get('file')||'',table:p.get('table')||'',cluster:p.get('cluster')||'',context:p.get('context')||'',step:p.get('step')||'',investigation:p.get('investigation')||'',checkpoint:Math.max(0,Number(p.get('checkpoint'))||0),depth:Math.max(0,Math.min(4,Number(p.get('depth'))||0)),snapshot:p.get('snapshot')||'',print:['page','snippet'].includes(print)?print:''}}
function hashOf(s){const p=new URLSearchParams();for(const [k,v]of Object.entries(s))if(v!==''&&v!==0)p.set(k,v);return p.toString()}
function navigate(next,replace=false){const merged={...state,...next};history[replace?'replaceState':'pushState']({},'',`${location.pathname}${location.search}#${hashOf(merged)}`);lastHash=location.hash;state=merged;render()}
function investigate(id,index=0){const check=investigationState(id,index);if(!check)return;navigate({context:'',step:'',tab:'overview',page:0,trail:'',...check})}
function journey(view){listMode=false;const j=journeyOf(view), keep=['modules','tables','contexts'].includes(view);navigate({view,investigation:'',checkpoint:0,entity:'',focus:'',tab:'overview',page:0,trail:'',filter:'',depth:0,context:j?view:keep?state.context:'',step:j?j.steps[0].id:keep?state.step:''})}
function chooseStep(id){const context=resolveJourney(state.view,state.context),step=resolveStep(context,id);if(step)navigate({view:context,context,step:step.id,focus:step.code[0]||'',entity:'',tab:'overview',page:0})}
function returnToJourney(){const context=resolveJourney(state.view,state.context);if(context)navigate({view:context,context,entity:'',page:0})}
async function browseSource(id){const e=byId(id);if(!e)return;if(e.kind==='table'){navigate({view:'tables',table:id,focus:id,entity:'',tab:'data'});return}if(e.kind==='context'){navigate({view:'contexts',cluster:id,focus:id,entity:'',tab:'overview'});return}const p=parent(e);if(p?.file){await ensureBrowser('modules');const file=fileManifest.repositories.flatMap(r=>r.files).find(f=>f.repo===p.repo&&f.path===p.file);if(file)navigate({view:'modules',filter:'',file:file.id,focus:id,entity:'',tab:e.kind==='function'?'code':'overview'});else openGraph(id)}}
async function browseRepository(repo){if(!overview.manifest.sources[repo])return;await ensureBrowser('modules');fileBrowser.filterRepository(repo);navigate({view:'modules',filter:repo,file:'',entity:'',focus:'',tab:'overview',page:0})}
function openGraph(id){if(!byId(id))return;listMode=false;navigate({view:'entity',entity:id,focus:id,tab:'code',page:0,trail:state.context||(['modules','tables','contexts'].includes(state.view)?state.view:state.trail)})}
function explore(id){const e=byId(id);if(!e)return toast('This entity is not in the selected snapshot.');if(journeyOf(state.view)){navigate({focus:id,tab:e.kind==='function'?'code':e.kind==='table'?'data':'overview'});return}if(['module','table'].includes(e.kind)){browseSource(id);return}openGraph(id)}
function select(id){navigate({focus:id,tab:state.tab||'overview'})}
function toast(message){$('#toast').textContent=message;$('#toast').classList.add('visible');clearTimeout(toast.timer);toast.timer=setTimeout(()=>$('#toast').classList.remove('visible'),2700)}

function grid(ids,offsetY=50){return ids.map((id,i)=>({id,x:25+(i%3)*290,y:offsetY+Math.floor(i/3)*180}))}
async function ensureBrowser(view){
  if(view==='modules'&&!fileBrowser){
    fileManifest=await getJSON(api('files'));
    for(const f of fileManifest.repositories.flatMap(r=>r.files))entities.set(f.id,{...f,kind:'file',label:f.name,full:f.path,file:f.path});
    fileBrowser=createFileBrowser({host:$('#file-browser-host'),manifest:fileManifest,byId,detail,filterChanged:filter=>navigate({filter},true),selectFile:f=>navigate({file:f.id,focus:f.modules[0]||f.id,tab:'overview'}),selectEntity:id=>navigate({focus:id,tab:'code'}),openEntity:openGraph});
  }
  if(view==='tables'&&!schemaBrowser){schemaIndex=await getJSON(api('schema'));schemaBrowser=createSchemaBrowser({host:$('#schema-browser-host'),tables:schemaIndex,byId,detail,selectTable:id=>navigate({table:id,focus:id,tab:'overview'}),openEntity:openGraph})}
  if(view==='contexts'&&!contextBrowser)contextBrowser=createContextBrowser({host:$('#context-browser-host'),contexts:overview.contexts,byId,selectContext:id=>navigate({cluster:id,focus:id,tab:'overview'}),openEntity:openGraph});
}
async function graph(){
  const j=journeyOf(state.view);
  if(j){state.context=state.view;const step=resolveStep(state.view,state.step);state.step=step.id;return {title:j.title,subtitle:j.question,nodes:[],edges:[],type:'PINNED SNAPSHOT · REVIEW STATUS ATTACHED',note:`${j.scope}. Review status compares the fingerprints recorded at review with the snapshot's code.`,journey:true}}
  if(Object.hasOwn(overview.views,state.view))return {...overview.views[state.view],type:'REPOSITORIES · RESOLVED CALLS'};
  if(state.view==='modules'){await ensureBrowser('modules');return {title:'Code explorer',subtitle:'Browse repositories and folders. Select a file, inspect its functions, then open a relationship graph.',nodes:[],edges:[],type:'TRACKED FILES · PINNED REVISIONS',note:`${fileManifest.repositories.reduce((n,r)=>n+r.count,0).toLocaleString()} files across ${fileManifest.repositories.length} repositories. Function details cover the files an extractor parsed.`,browser:true}}
  if(state.view==='tables'){await ensureBrowser('tables');return {title:'Data structure',subtitle:'Find a table or column. Inspect columns, keys, indexes, relationships and code mentions in one place.',nodes:[],edges:[],type:'SCHEMA SNAPSHOT',note:`All ${schemaIndex.length} tables are browsable. Foreign keys are schema facts; code mentions are textual candidates. No live database is queried.`,browser:true}}
  if(state.view==='contexts'){await ensureBrowser('contexts');return {title:'Contexts',subtitle:'Module clusters by namespace: their modules, the tables their code mentions, and the docs that describe them.',nodes:[],edges:[],type:'MODULE NAMESPACE CLUSTERS',note:`${overview.contexts.length} contexts across ${new Set(overview.contexts.map(c=>c.repo)).size} repositories. Contexts are derived from module names, not reviewed boundaries.`,browser:true}}
  const e=byId(state.entity);if(!e)return {title:'Entity not found',subtitle:'This link refers to an entity outside the selected snapshot.',nodes:[],edges:[],note:'Use search to find a current entity or return to System overview.',type:'UNKNOWN ENTITY',error:`entity ${state.entity} not found in snapshot ${status.snapshot}`};
  const d=await detail(e), title=e.label;
  let nodes=[],edges=[],note='',pages=1,total=0;
  if(e.kind==='module'){
    const neighbors=new Map();
    for(const fn of Object.values(d.functions))for(const [direction,calls]of [['in',fn.in],['out',fn.out]])for(const call of calls||[]){const target=parent(byId(call.id));if(target?.kind==='module'&&target.id!==e.id){const key=direction+':'+target.id;const old=neighbors.get(key)||{id:target.id,direction,sites:0,precision:call.precision};old.sites+=call.sites;if(call.precision==='precise')old.precision='precise';neighbors.set(key,old)}}
    const incoming=[...neighbors.values()].filter(n=>n.direction==='in'),outgoing=[...neighbors.values()].filter(n=>n.direction==='out');
    const ids=[...new Set([...incoming,...outgoing].map(n=>n.id))];nodes=[{id:e.id,x:315,y:20},...grid(ids,225)];
    edges=[...incoming.map(n=>({from:n.id,to:e.id,label:n.sites+' resolved call sites',precision:n.precision})),...outgoing.map(n=>({from:e.id,to:n.id,label:n.sites+' resolved call sites',precision:n.precision}))];
    note='All resolved module dependencies in the extracted call graph. These are calls across module boundaries, not execution order. Dynamic and unresolved calls are not represented.';
  }else if(e.kind==='function'&&state.depth>1){
    const g=await getJSON(api('graph',{id:e.id,depth:state.depth}));
    nodes=layeredLayout(g);edges=g.edges.map(x=>({from:x.from,to:x.to,label:`${x.sites} call site${x.sites===1?'':'s'}`,precision:x.precision}));
    note=`Call neighbourhood to depth ${g.depth}: callers left, callees right.${g.hidden?` ${g.hidden} more nodes are hidden; run cartographer impact ${e.id}.`:''} ${esc(d.external_or_unresolved_sites)} external or unresolved call sites from this function are not drawn.`;
  }else if(e.kind==='function'){
    const neighborhood=functionNeighborhood(e.id,d),{incoming,outgoingOnly,selfSites}=neighborhood;const height=Math.max(incoming.length,outgoingOnly.length,1)*158;
    nodes=[{id:e.id,x:345,y:(height-126)/2},...incoming.map((n,i)=>({id:n.id,x:20,y:i*158})),...outgoingOnly.map((n,i)=>({id:n.id,x:675,y:i*158}))];
    edges=neighborhood.edges.map(n=>({from:n.from,to:n.to,label:`${n.sites} call site${n.sites===1?'':'s'}`,precision:n.precision}));
    note=`Focused neighbourhood: ${d.in.length} resolved callers, ${d.out.length} resolved callees; reciprocal calls keep both arrows. ${selfSites} self-recursive call sites are listed in Code but not drawn as loops. ${esc(d.external_or_unresolved_sites)} external, generated or unresolved call sites are not drawn. Add &depth=2 to the link for a wider view.`;
  }else if(e.kind==='table'){
    const relationships=d.relationships.filter(r=>byId(r.from)&&byId(r.to));const ids=[...new Set(relationships.flatMap(r=>[r.from,r.to]))].filter(id=>id!==e.id);total=ids.length;
    const mentions=[...new Set((d.references||[]).map(r=>r.function).filter(id=>id&&byId(id)))];
    nodes=[{id:e.id,x:315,y:20},...grid([...ids,...mentions],225)];const shown=new Set(nodes.map(n=>n.id));
    edges=[...relationships.filter(r=>shown.has(r.from)&&shown.has(r.to)).map(r=>({from:r.from,to:r.to,label:'foreign key'})),...mentions.map(id=>({from:id,to:e.id,label:'mentions',precision:'candidate'}))];
    note='Foreign keys come from the schema snapshot. Functions that mention the table are textual candidates, not proven reads or writes.';
  }else{
    const ids=(e.members||e.modules||e.tables||[]).filter(id=>byId(id));nodes=[{id:e.id,x:315,y:20},...grid(ids,225)];edges=nodes.slice(1).map(n=>({from:e.id,to:n.id,label:e.kind==='annotation'?'cites':'contains'}));
    note=e.kind==='annotation'?'Entities this annotation cites. Review status is attached in Docs.':e.kind==='context'?'Modules in this namespace cluster. Use Contexts for tables and docs.':'Largest modules or tables in this repository. Use Code explorer or Data structure for the full inventory.';
  }
  return {title,subtitle:e.full+(e.kind==='function'?' · Focused call neighbourhood':` · ${kinds[e.kind]}`),nodes,edges,note,pages,total,type:e.kind==='function'?'EXTRACTED CALL SITES':e.kind==='table'?'FOREIGN KEYS + CODE MENTIONS':'STRUCTURAL DRILL-DOWN'};
}

function badgeOf(e){
  if(e.kind==='annotation')return [reviewText(e.status),e.status==='reviewed'?'gray':'amber'];
  return [e.kind==='module'?`${e.count} functions`:e.kind==='table'?`${e.count} columns`:e.kind==='function'?'Source function':e.kind==='context'?`${e.count} modules`:e.kind==='service'?`${e.count} ${e.repo?'modules':'tables'}`:e.kind==='external'?'External dependency':shortRepo(e.repo),''];
}
function nodeBadge(e){const [text,tone]=badgeOf(e);return badge(text,tone)}
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
    const label=[x.label,x.precision&&x.precision!=='precise'?x.precision:''].filter(Boolean).join(' · ');
    return `<path d="${d}" class="${x.precision?'p-'+esc(x.precision):''}" marker-end="url(#arrow)"/><text x="${(x1+x2)/2}" y="${(y1+y2)/2-9}" text-anchor="middle">${esc(label)}</text>`;
  }).join('');
  stage.innerHTML=`<svg class="edges" width="${dimensions.w}" height="${dimensions.h}" aria-hidden="true"><defs><marker id="arrow" markerWidth="6" markerHeight="6" refX="5" refY="3" orient="auto"><path d="M0,0 L6,3 L0,6" style="fill:#9eb7a9;stroke:none"/></marker></defs>${paths}</svg>`+ns.map(n=>`<button class="map-node ${state.focus===n.id?'focused':''}" style="left:${n.x}px;top:${n.y}px" data-select="${esc(n.id)}" title="${esc(n.e.full)}" aria-label="Inspect ${esc(n.e.full)}"><div class="node-top">${icon(n.e)}<span class="node-name">${esc(n.label||n.e.label)}</span></div><div class="node-meta">${esc(n.e.language || kinds[n.e.kind])} · ${esc(shortRepo(n.e.repo))}</div>${n.e.file?`<div class="node-path" title="${esc(n.e.file)}">${esc(n.e.file)}</div>`:''}<div class="node-foot">${nodeBadge(n.e)}<span aria-hidden="true">↗</span></div></button>`).join('');
  drawn=ns;
  $('#minimap').innerHTML=`<svg viewBox="0 0 ${dimensions.w} ${dimensions.h}" aria-hidden="true">${ns.map(n=>`<rect x="${n.x}" y="${n.y}" width="235" height="126" rx="14" fill="${n.id===state.focus?'#70a58a':'#d8e5dc'}"/>`).join('')}</svg>`;
  $('#list-view').innerHTML=ns.map(n=>`<button class="entity-row" data-select="${esc(n.id)}">${icon(n.e)}<span><strong>${esc(n.label||n.e.label)}</strong><small>${esc(n.e.full)}</small></span>${nodeBadge(n.e)}</button>`).join('')||'<div class="empty-state">No entities in this view.</div>';
  $('#canvas').hidden=listMode;$('#list-view').hidden=!$('#canvas').hidden;$('.map-controls').hidden=$('#canvas').hidden;$('#minimap').hidden=$('#canvas').hidden;$('.legend').hidden=$('#canvas').hidden;
  $('#list-toggle').textContent=listMode?'Map view':'List view';
  $('#entity-count').textContent=`${ns.length} entities in view`;
  const layoutKey=JSON.stringify(ns.map(n=>[n.id,n.x,n.y]));
  if(layoutKey!==lastLayoutKey){lastLayoutKey=layoutKey;requestAnimationFrame(fit)}
}
function applyTransform(){ $('#stage').style.transform=`translate(${transform.x}px,${transform.y}px) scale(${transform.z})`;$('#zoom-level').textContent=`${Math.round(transform.z*100)}%`}
function fit(){const c=$('#canvas');if(c.hidden||!currentGraph)return;transform.z=Math.min(1.05,(c.clientWidth-40)/dimensions.w,(c.clientHeight-28)/dimensions.h);transform.z=Math.max(.15,transform.z);transform.x=(c.clientWidth-dimensions.w*transform.z)/2;transform.y=(c.clientHeight-dimensions.h*transform.z)/2;applyTransform()}
function zoom(factor){const c=$('#canvas'),z=Math.max(.15,Math.min(2,transform.z*factor)),ratio=z/transform.z;transform.x=c.clientWidth/2-(c.clientWidth/2-transform.x)*ratio;transform.y=c.clientHeight/2-(c.clientHeight/2-transform.y)*ratio;transform.z=z;applyTransform()}

function expandableItems(items,render,label,visible=30){const first=items.slice(0,visible).map(render).join('');if(items.length<=visible)return first;return first+`<details class="additional-references"><summary>${items.length-visible} more ${esc(label)} (${items.length} total)</summary>${items.slice(visible).map(render).join('')}</details>`}
function relatedTableLinks(ids){return `<h3>Related tables (${ids.length})</h3>`+expandableItems(ids,id=>action(id,byId(id)?.label||id,'Textual mention · candidate'),'tables')}
function sectionList(label,items){return `<h3>${esc(label)} (${items.length})</h3>`+(items.length?items.map(n=>action(n.id,byId(n.id)?.label||n.id,`${byId(n.id)?.full||n.id} · ${n.sites} call site${n.sites===1?'':'s'}${n.precision?' · '+n.precision:''}`)).join(''):'<p>None resolved in the snapshot.</p>')}
function docsList(d){
  const docs=d.docs||[],anns=d.annotations||[];
  let html=`<h3>Docs mentioning it (${docs.length})</h3>`+(docs.length?docs.map(x=>`<div class="inspector-link">${link(x.url,x.title)}<small>${esc(x.repo)} / ${esc(x.path)}:${esc(x.line)}</small></div>`).join(''):'<p>No tracked Markdown mentions it.</p>');
  html+=`<h3>Annotations citing it (${anns.length})</h3>`+(anns.length?anns.map(a=>a.kind==='journey'&&journeyOf(a.annotation)?`<button class="inspector-link" data-journey-target="${esc(a.annotation)}">${esc(a.title)}<small>journey · ${esc(reviewText(a.status))}</small></button>`:action(a.id,a.title,`${a.kind} · ${reviewText(a.status)}`)).join(''):'<p>No reviewed explanation cites it.</p>');
  if(d.steps)html+=`<h3>Steps (${d.steps.length})</h3>`+d.steps.map(s=>`<div class="inspector-link">${esc(s.title||s.id)}<small>${esc(reviewText(s.status))}${s.reasons?.length?' · '+esc(s.reasons.join('; ')):''}</small></div>`).join('');
  return html;
}
// The authored text of an annotation beyond its summary: body, a table
// note's role and rule, an investigation's basis.
function annotationDetail(d){return [text(d.body)&&`<p>${esc(d.body)}</p>`,text(d.role)&&`<div class="kv"><span>Role</span><span>${esc(d.role)}</span></div>`,text(d.rule)&&`<div class="kv"><span>Rule</span><span>${esc(d.rule)}</span></div>`,text(d.basis)&&`<div class="note">${esc(d.basis)}</div>`].filter(Boolean).join('')}
async function inspect(e,token){
  $('#inspector-header').innerHTML=`<div class="inspector-head"><div class="inspector-eyebrow">${e?esc(kinds[e.kind]):'YOUR SYSTEM MAP'}</div><div class="inspector-title">${icon(e)}<h2>${esc(e?.label||'Explore a connected system')}</h2></div><p class="subtext">${esc(e?.kind==='function'?parent(e)?.full||e.repo:e?.full||e?.repo||'Select a node to see its context')}</p></div>`;
  const ctx=resolveJourney(state.view,state.context),step=resolveStep(ctx,state.step);
  if(ctx&&step){$('#inspector-header').insertAdjacentHTML('afterbegin',`<div class="inspector-context">Inspecting within ${esc(journeys[ctx].title)}<button data-return-journey>↩ ${esc(step.title)}</button></div>`)}
  if(e&&['module','function','table','context'].includes(e.kind))$('#inspector-header').insertAdjacentHTML('beforeend',`<div class="inspector-tools"><button data-browse-source="${esc(e.id)}">${e.kind==='table'?'Open full schema':e.kind==='context'?'Open in Contexts':'Open file tree'} ↗</button><button data-open-graph="${esc(e.id)}">${e.kind==='table'?'Relationships':e.kind==='module'?'Module dependencies':e.kind==='context'?'Context graph':'Call neighbourhood'} ↗</button></div>`);
  const tabs=['overview','code','data','docs'];$('#inspector-tabs').innerHTML=tabs.map(t=>`<button data-tab="${t}" class="${state.tab===t?'selected':''}">${t[0].toUpperCase()+t.slice(1)}</button>`).join('');
  if(!e){$('#inspector-content').innerHTML=['modules','tables','contexts'].includes(state.view)?'<p>Select a file, function, table or context to inspect its source, data and docs here.</p><div class="note">Browsing does not imply complete function extraction or a completed design review.</div>':`<p>Start with a repository, a journey, or search (⌘K). Every node links code, data and docs at pinned revisions.</p><div class="note">This map is an imported snapshot served from this machine. ${esc(freshnessSummary()||'Working-copy freshness has not been checked.')}</div>`;return}
  if(e.kind==='file'){$('#inspector-content').innerHTML=`<p>${esc(e.file)}</p><div class="kv"><span>Category</span><span>${esc(e.category)}</span></div><div class="kv"><span>Source revision</span><span>${esc(overview.manifest.sources[e.repo]?.slice(0,10)||'snapshot')}</span></div><p>${link(e.url,'Open source at snapshot revision')}</p><div class="note">This file has no indexed function details. That is an extraction limit, not a claim that it contains no behaviour.</div>`;return}
  $('#inspector-content').innerHTML='<p>Loading source details…</p>';const d=await detail(e);if(token!==renderToken)return;
  let html='';const go=`<button class="primary" ${['module','function','table','context'].includes(e.kind)?'data-browse-source':'data-open-graph'}="${esc(e.id)}">${e.kind==='table'?'Open full schema':['module','function'].includes(e.kind)?'Open source file':e.kind==='context'?'Open in Contexts':'Explore graph'} →</button>`;
  const ctxId=d.context||e.context, nDocs=(d.docs||[]).length+(d.annotations||[]).length;
  if(state.tab==='overview'){
    html=`<p>${esc(e.summary||(e.kind==='function'?'Source-defined function. Explore its callers and callees; dynamic calls may be unresolved.':e.kind==='table'?`Table in ${e.domain}. Inspect columns and links, then follow code mentions.`:e.kind==='context'?'Modules that share a namespace prefix.':e.kind==='annotation'?'A human-written explanation that cites map entities.':'Source module inventory.'))}</p><div class="kv"><span>Repository</span><span>${esc(e.repo||'—')}</span></div><div class="kv"><span>Source revision</span><span>${esc(overview.manifest.sources[e.repo]?.slice(0,10)||'snapshot')}</span></div>${d.file&&d.line?`<div class="kv"><span>Location</span><span>${link(d.url,`${d.file}:${d.line}`)}</span></div>`:''}${ctxId&&byId(ctxId)?`<div class="kv"><span>Context</span><span><button class="text-link inline" data-go="${esc(ctxId)}">${esc(byId(ctxId).label)}</button></span></div>`:''}${e.count?`<div class="kv"><span>${e.kind==='table'?'Columns':e.kind==='module'?'Source functions':'Members'}</span><span>${esc(e.count)}</span></div>`:''}${e.kind==='annotation'?annotationDetail(d)+`<div class="kv"><span>Review</span><span>${esc(reviewText(e.status))}</span></div>`:''}${go}<h3>Attached context</h3><button class="inspector-link" data-tab="docs">${nDocs} docs and annotations <small>Markdown that mentions it · reviewed explanations that cite it</small></button>${e.kind==='function'&&e.parent?action(e.parent,'Back to '+parent(e).label,'Containing module'):''}`;
  }else if(state.tab==='code'){
    const source=d.url;html=source?`<p>${link(source,'Open source at snapshot revision')}</p>`:'';
    if(e.kind==='function')html+=`<p>${esc(d.file)}:${esc(d.line)}<br>${esc(d.kind)} · ${esc(d.language||'')}</p>${sectionList('Resolved callers',d.in||[])}${sectionList('Resolved callees',d.out||[])}<div class="note">${esc(d.external_or_unresolved_sites)} call sites point outside the map or could not be resolved. Absence of a static edge does not prove absence of a runtime call.</div>`;
    else if(e.kind==='module')html+=`<h3>Defined functions (${Object.keys(d.functions).length})</h3>${Object.values(d.functions).sort((a,b)=>a.name.localeCompare(b.name)).map(f=>action(f.id,byId(f.id)?.label||f.id,`${f.kind} · line ${f.line}`)).join('')}`;
    else if(e.kind==='table')html+=`<div class="note">These are textual mentions, including comments and query fragments (candidate). They are not proven read/write edges.</div><h3>Code mentions (${(d.references||[]).length})</h3>${expandableItems(d.references||[],r=>`<div class="inspector-link">${r.function&&byId(r.function)?action(r.function,byId(r.function).label):link(r.url,r.file+':'+r.line)}<small>${esc(r.repo)} · ${esc((r.text||'').trim())}</small></div>`,'references',35)||'<p>No code mentions.</p>'}`;
    else html+=(e.members||e.modules||e.tables||[]).filter(id=>byId(id)).map(id=>action(id,byId(id).label,byId(id).full)).join('')||'<p>No members.</p>';
  }else if(state.tab==='data'){
    if(e.kind==='table')html=`<p>Schema snapshot · ${esc(d.schema)}.${esc(d.name)}</p><h3>Columns (${d.columns.length})</h3><table class="columns-table"><tbody>${d.columns.map(c=>`<tr><td>${esc(c.name)}</td><td>${esc(c.type)}${c.nullable?'':' · required'}</td></tr>`).join('')}</tbody></table><h3>Relationships (${d.relationships.length})</h3>${d.relationships.map(r=>action(r.from===e.id?r.to:r.from,byId(r.from===e.id?r.to:r.from)?.label||r.to,r.kind+' · '+(r.definition||r.name))).join('')||'<p>No foreign keys.</p>'}<h3>Indexes (${d.indexes.length})</h3>${d.indexes.map(i=>`<div class="inspector-link">${esc(i.name)}<small>${esc(i.definition)}</small></div>`).join('')}`;
    else {const tables=(d.tables||e.tables||[]).map(t=>typeof t==='string'?t:t.id);html=`<div class="note">Textual mentions in source, not proven reads/writes. Comments and dynamic SQL may appear.</div>${tables.length?relatedTableLinks(tables):'<p>No table mentions attached. This does not establish that the code never accesses data.</p>'}`}
  }else if(state.tab==='docs')html=docsList(d);
  const appearances=Object.entries(journeys).flatMap(([journey,j])=>j.steps.filter(s=>s.code.includes(e.id)).map(s=>({journey,j,s})));
  if(e.kind==='service'&&overview.manifest.sources[e.repo])html+=`<h3>Repository inventory</h3><button class="primary" data-browse-repository="${esc(e.repo)}">Browse repository files →</button><p class="subtext">${esc(e.repo)} · every file the extractors reported.</p>`;
  if(appearances.length)html+=`<h3>Used in journeys</h3>${appearances.map(({journey,j,s})=>`<button class="inspector-link" data-journey-target="${esc(journey)}" data-target-step="${esc(s.id)}">${esc(s.title)}<small>${esc(j.title)} · ${esc(reviewText(s.status))}</small></button>`).join('')}`;
  $('#inspector-content').innerHTML=html;
}
function crumbs(){
  let h='<button data-view="home">System Map</button>';const ctx=resolveJourney(state.view,state.context),step=resolveStep(ctx,state.step),e=byId(state.entity);
  if(ctx){h+=`<span>/</span><button data-return-journey>${esc(journeys[ctx].title)}</button><span>/</span><button data-return-journey>${esc(step.title)}</button>`;if(!journeyOf(state.view))h+=`<span>/</span><span>${esc(e?.label||currentGraph.title)}</span>`}
  else {if(state.trail&&state.view==='entity')h+=`<span>/</span><button data-view="${esc(state.trail)}">${esc(overview.views[state.trail]?.title||({modules:'Code explorer',tables:'Data structure',contexts:'Contexts'}[state.trail])||state.trail)}</button>`;if(e?.parent)h+=`<span>/</span><button data-go="${esc(e.parent)}">${esc(parent(e).label)}</button>`;if(state.view!=='home')h+=`<span>/</span><span>${esc(e?.label||currentGraph.title)}</span>`}
  $('#breadcrumbs').innerHTML=h;
}
function journeyCards(){return Object.entries(journeys).map(([id,j])=>`<button class="journey" data-view="${esc(id)}"><span class="journey-icon">⇢</span><span><strong>${esc(j.title)}</strong><small>${esc(j.question||j.scope)}</small></span><span class="arrow">↗</span></button>`).join('')}
async function render(){
  const token=++renderToken;
  try{
    if(!['overview','code','data','docs'].includes(state.tab))state.tab='overview';
    const g=await graph();if(token!==renderToken)return;currentGraph=g;
    // An export waits for data-ready and fails on data-error.
    if(g.error)document.body.dataset.error=g.error;else delete document.body.dataset.error;
    if(!state.focus)state.focus=journeyOf(state.view)?resolveStep(state.view,state.step).code[0]||'':state.view==='home'?overview.services[0]?.id||'':state.view==='entity'?state.entity:state.view==='modules'&&state.file?(fileBrowser.fileMap.get(state.file)?.modules[0]||state.file):state.view==='tables'?state.table:state.view==='contexts'?state.cluster:g.nodes[0]?.id||'';
    $('#investigation-host').innerHTML=investigationHTML(state);
    $('#title').textContent=g.title;$('#subtitle').textContent=g.subtitle||'';$('#map-scope').textContent=g.title.toUpperCase();$('#map-type').textContent=g.type;$('#map-note').textContent=g.note;crumbs();
    document.querySelectorAll('.nav-item').forEach(b=>b.classList.toggle('active',b.dataset.view===(state.context&&state.view==='entity'?state.context:state.view)));
    $('#pagination').innerHTML=g.pages>1?`<button class="secondary" data-page="${state.page-1}" ${!state.page?'disabled':''}>←</button><span>${state.page+1} / ${g.pages}</span><button class="secondary" data-page="${state.page+1}" ${state.page+1>=g.pages?'disabled':''}>→</button>`:'';
    $('#journeys').innerHTML=state.view==='home'?journeyCards():'';$('#journeys').hidden=!$('#journeys').innerHTML;
    $('.workbench').classList.toggle('browsing',Boolean(g.browser));
    $('.workbench').classList.toggle('journey-mode',Boolean(g.journey));
    $('.map-panel').setAttribute('aria-label',g.journey?'Journey workspace':g.browser?g.title:'Interactive architecture map');
    $('#journey-host').hidden=!g.journey;
    const ctx=resolveJourney(state.view,state.context),step=resolveStep(ctx,state.step);
    $('#context-strip').hidden=!ctx||Boolean(g.journey);
    $('#context-strip').innerHTML=ctx&&step?`<div>Exploring from <strong>${esc(journeys[ctx].title)} / ${esc(step.title)}</strong></div><div class="context-actions"><button data-return-journey>↩ Return to journey</button><button data-clear-context>Leave journey</button></div>`:'';
    $('#file-browser-host').hidden=state.view!=='modules';$('#schema-browser-host').hidden=state.view!=='tables';$('#context-browser-host').hidden=state.view!=='contexts';
    $('#list-toggle').hidden=Boolean(g.browser||g.journey);
    $('#toolbar-note').textContent=g.browser||g.journey?'':'Edges: precise = resolved by the extractor · heuristic = matched by name across repositories · candidate = textual';
    if(g.journey||g.browser){$('#canvas').hidden=true;$('#list-view').hidden=true;$('.map-controls').hidden=true;$('#minimap').hidden=true;$('.legend').hidden=true;drawn=[]}
    if(g.journey){
      $('#entity-count').textContent=journeyOf(state.view).steps.length+' steps · cited code and review status';
      if(!journeyBrowser)journeyBrowser=createJourneyBrowser({host:$('#journey-host'),byId,detail});
      await journeyBrowser.render({journey:state.view,step:state.step,focus:state.focus});
    }else if(g.browser){
      $('#entity-count').textContent=state.view==='modules'?'All tracked files · no pagination':state.view==='tables'?`All ${schemaIndex.length} tables · no pagination`:`${overview.contexts.length} contexts`;
      if(state.view==='modules')await fileBrowser.update({file:state.file,selected:state.focus,filter:state.filter});else if(state.view==='tables')await schemaBrowser.update(state.table);else contextBrowser.update(state.cluster);
    }else draw(g);
    $('#download-svg').disabled=!canExportSVG();
    if(token!==renderToken)return;
    await inspect(byId(state.focus),token);
  }catch(error){if(token===renderToken){$('#inspector-content').innerHTML=`<div class="error-box">${esc(error.message)}<p>Try returning to System overview or reloading.</p></div>`;document.body.dataset.error=error.message;console.error(error)}}
}

function canExportSVG(){return Boolean(currentGraph&&!currentGraph.journey&&!currentGraph.browser&&drawn.length)}
function buildSVG(withFooter=true){return exportSVG({edges:$('#stage svg.edges'),nodes:drawn.map(n=>({x:n.x,y:n.y,label:n.label||n.e.label,full:n.e.full,meta:`${n.e.language||kinds[n.e.kind]} · ${shortRepo(n.e.repo)}`,path:n.e.file||'',badge:badgeOf(n.e)[0],focused:n.id===state.focus})),width:dimensions.w,height:dimensions.h,title:currentGraph.title,subtitle:currentGraph.subtitle,footer:withFooter?footerText(status):''})}
// The print view: the live graph becomes a scalable SVG and every export
// carries the provenance footer. Snippet mode prints only the graph and
// details panel.
function preparePrint(mode){document.body.dataset.print=mode;$('#export-footer').textContent=footerText(status);if(state.print)document.title=`cartographer map ${status.snapshot} ${(status.repos||[]).map(r=>`${r.name}@${String(r.revision).slice(0,7)}`).join(' ')} - ${String(currentGraph?.title||'').replace(/[^\x20-\x7e]/g,'')}`;$('#print-graph').innerHTML=canExportSVG()?buildSVG(false):''}
window.cartographerSVG=()=>canExportSVG()?buildSVG():'';
window.addEventListener('beforeprint',()=>{if(!document.body.dataset.print)preparePrint('page')});
window.addEventListener('afterprint',()=>{if(!state.print)delete document.body.dataset.print});

function search(){const q=$('#global-search').value.trim().toLowerCase();const words=q.split(/\s+/).filter(Boolean);let rows=catalog.filter(e=>(searchKind==='all'||e.kind===searchKind)&&words.every(w=>(e.full+' '+e.repo+' '+(e.file||'')).toLowerCase().includes(w)));if(!q)rows=rows.filter(e=>['module','table','service','context'].includes(e.kind));rows.sort((a,b)=>{const score=e=>(e.label.toLowerCase()===q?4:e.label.toLowerCase().startsWith(q)?2:0)+(e.kind==='module'?1:0);return score(b)-score(a)||a.full.localeCompare(b.full)});$('#search-results').innerHTML=rows.length?`<p>${rows.length.toLocaleString()} matches${rows.length>40?' · showing 40; refine your search':''}</p>`+rows.slice(0,40).map(e=>`<button class="entity-row" data-search-result="${esc(e.id)}">${icon(e)}<span><strong>${esc(e.label)}</strong><small>${esc(e.full)} · ${esc(shortRepo(e.repo))}</small></span>${badge(kinds[e.kind],'gray')}</button>`).join(''):'<p>No matches. Try a module, function or table name.</p>'}
function openSearch(){$('#search-dialog').showModal();search();$('#global-search').focus()}
function sources(){const m=overview.manifest;$('#sources-content').innerHTML=`<section class="freshness-report"><h3>Snapshot</h3><p><code>${esc(m.snapshot)}</code> · workspace ${esc(m.workspace)} · built ${esc(fmtDate(m.created_at))}</p><h3>Freshness</h3><p>${esc(freshnessSummary()||'Not checked against working copies. Run cartographer status for commits behind per repository.')}</p><p>Annotation review: ${esc(Object.entries(status.annotations||{}).map(([k,v])=>`${v} ${reviewText(k).toLowerCase()}`).join(', ')||'no annotations')}</p></section><h3>Repository revisions</h3>`+m.repos.map(r=>`<p><b>${esc(r.name)}</b><br>${r.url?link(`${r.url.replace(/\.git$/,'')}/tree/${r.revision}`,r.revision.slice(0,12)):esc(r.revision.slice(0,12))}</p>`).join('')+`<h3>Data structure</h3><p>${esc(m.schema_basis)}</p><h3>Extraction boundaries</h3><ul>${m.coverage.map(s=>`<li>${esc(s)}</li>`).join('')}</ul><h3>Rendering & icons</h3><p>A lightweight HTML/SVG renderer served from this machine on 127.0.0.1. Technology icons are bundled from @likec4/icons (MIT); no external CDN is called.</p>`;$('#sources-dialog').showModal()}
function showSnapshots(){const sel=$('#snapshot-picker');sel.innerHTML=snapshots.map(s=>`<option value="${esc(s.id)}" ${s.id===status.snapshot?'selected':''}>${esc(s.id)}</option>`).join('');sel.disabled=snapshots.length<2;sel.onchange=()=>{const p=new URLSearchParams(location.hash.slice(1));if(sel.value===status.default_snapshot)p.delete('snapshot');else p.set('snapshot',sel.value);history.replaceState({},'',`${location.pathname}${location.search}#${p}`);location.reload()}}
// An authored overview annotation supplies the home subtitle and text and
// the summaries of the repositories it describes (matched by repo).
function applyOverview(a){if(!a)return;const home=overview.views.home;if(home){if(text(a.summary))home.subtitle=a.summary;if(text(a.body))home.note=`${a.body} ${home.note||''}`.trim()}for(const s of Array.isArray(a.services)?a.services:[]){const e=s&&typeof s==='object'&&text(s.repo)?byId('s:'+s.repo):null;if(!e)continue;if(text(s.summary))e.summary=s.summary;if(text(s.label))e.full=`${s.label} · ${e.repo} repository`}}
function showJourneyNav(){const host=$('#journey-nav');const items=Object.entries(journeys);host.hidden=!items.length;host.insertAdjacentHTML('beforeend',items.map(([id,j])=>`<button class="nav-item" data-view="${esc(id)}"><span>⇢</span>${esc(j.title)}${j.status!=='reviewed'?'<b>review</b>':''}</button>`).join(''))}

document.addEventListener('click',event=>{const b=event.target.closest('button,a');if(!b)return;
  if(b.dataset.view){event.preventDefault();journey(b.dataset.view)}
  else if(b.dataset.journeyTarget){const step=resolveStep(b.dataset.journeyTarget,b.dataset.targetStep);navigate({view:b.dataset.journeyTarget,context:b.dataset.journeyTarget,step:step?.id||'',entity:'',focus:state.focus,tab:'overview',page:0})}
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
  else if(b.dataset.page!==undefined)navigate({page:Number(b.dataset.page),focus:''});
  else if(b.dataset.searchResult){$('#search-dialog').close();explore(b.dataset.searchResult)}
  else if(b.dataset.searchKind){searchKind=b.dataset.searchKind;document.querySelectorAll('[data-search-kind]').forEach(el=>el.classList.toggle('selected',el===b));search()}
});
$('#stage').addEventListener('dblclick',event=>{const node=event.target.closest('[data-select]');if(node)explore(node.dataset.select)});
$('#search-open').onclick=openSearch;$('#global-search').oninput=search;$('#provenance').onclick=sources;$('#source-button').onclick=sources;$('#close-sources').onclick=()=>$('#sources-dialog').close();
$('#global-search').onkeydown=event=>{if(event.key==='Enter'){event.preventDefault();const row=$('[data-search-result]');if(row)row.click()}};
document.addEventListener('keydown',event=>{if((event.metaKey||event.ctrlKey)&&event.key.toLowerCase()==='k'){event.preventDefault();if(!$('#search-dialog').open)openSearch()}});
$('#copy-link').onclick=async()=>{try{const {print,...rest}=state;await navigator.clipboard.writeText(location.href.split('#')[0]+'#'+hashOf({...rest,snapshot:SNAPSHOT||status.snapshot}));toast('Link copied · opens this snapshot, view and selection')}catch{toast('Copy this page’s address from the address bar.')}};
$('#copy-embed').onclick=async()=>{const id=state.focus||state.entity,e=byId(id);if(!e||!['function','module','table','context'].includes(e.kind))return toast('Select a function, module, table or context to copy its embed.');try{const md=await getText(api('embed',{id}));await navigator.clipboard.writeText(md);toast('Embed copied · paste into a GitHub issue or PR')}catch(error){toast(error.message||'Could not copy the embed.')}};
$('#save-pdf').onclick=()=>{preparePrint($('#export-mode').value);window.print()};
$('#download-svg').onclick=()=>{if(!canExportSVG())return toast('Open a graph view to download it as SVG.');const url=URL.createObjectURL(new Blob([buildSVG()],{type:'image/svg+xml'}));const a=document.createElement('a');a.href=url;a.download=`cartographer-${String(currentGraph.title).replace(/[^A-Za-z0-9._-]+/g,'-')}.svg`;document.body.append(a);a.click();a.remove();setTimeout(()=>URL.revokeObjectURL(url),1000)};
$('#fit').onclick=fit;$('#minimap').onclick=fit;$('#zoom-in').onclick=()=>zoom(1.2);$('#zoom-out').onclick=()=>zoom(1/1.2);$('#list-toggle').onclick=()=>{listMode=!listMode;draw(currentGraph)};
let drag=null;$('#canvas').addEventListener('pointerdown',event=>{if(event.target.closest('button'))return;drag={x:event.clientX,y:event.clientY,tx:transform.x,ty:transform.y};$('#canvas').setPointerCapture(event.pointerId);$('#canvas').classList.add('dragging')});$('#canvas').addEventListener('pointermove',event=>{if(!drag)return;transform.x=drag.tx+event.clientX-drag.x;transform.y=drag.ty+event.clientY-drag.y;applyTransform()});for(const name of ['pointerup','pointercancel','lostpointercapture'])$('#canvas').addEventListener(name,()=>{drag=null;$('#canvas').classList.remove('dragging')});$('#canvas').addEventListener('wheel',event=>{if(event.ctrlKey||event.metaKey){event.preventDefault();zoom(event.deltaY<0?1.08:1/1.08)}},{passive:false});
// Back/forward fires popstate and, when the fragment changes, hashchange
// too; render once per new location. A link to another snapshot reloads the
// page, so every API call (SNAPSHOT) and all loaded data follow it.
let lastHash=location.hash;
function locationChanged(){if(location.hash===lastHash)return;lastHash=location.hash;const next=parse();if(next.snapshot!==SNAPSHOT){location.reload();return}state=next;render()}
window.addEventListener('popstate',locationChanged);window.addEventListener('hashchange',locationChanged);new ResizeObserver(()=>requestAnimationFrame(fit)).observe($('#canvas'));
const frame=()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
try{
  let annotations;[overview,catalog,status,snapshots,annotations]=await Promise.all([getJSON(api('overview')),getJSON(api('catalog')),getJSON(api('status')),getJSON('api/snapshots'),getJSON(api('annotations'))]);
  setJourneys(annotations);setDomains(annotations);setInvestigations(annotations);
  entities=new Map(catalog.map(e=>[e.id,e]));applyOverview(annotations.find(a=>a.kind==='overview'));state=parse();if(state.print)document.body.dataset.print=state.print;
  showFreshness();showSnapshots();showJourneyNav();
  $('#workspace-name').textContent=String(overview.manifest.workspace||'workspace').toUpperCase();$('#workspace-title').textContent=overview.manifest.workspace||'Workspace';$('#sidebar-snapshot').textContent=`${status.snapshot} · served on 127.0.0.1 only`;document.title=`System Map · ${overview.manifest.workspace} · cartographer`;
  $('#footer-counts').textContent=`${catalog.filter(e=>e.kind==='module').length} modules · ${catalog.filter(e=>e.kind==='function').length.toLocaleString()} functions · ${catalog.filter(e=>e.kind==='table').length} tables · ${status.provenance}`;
  await render();
  if(state.print)preparePrint(state.print);
  await document.fonts?.ready;await frame();
  document.documentElement.dataset.ready='true';document.body.dataset.ready='1';
}catch(error){$('#subtitle').textContent='The map could not load.';$('#inspector-content').innerHTML=`<div class="error-box">${esc(error.message)}</div>`;document.body.dataset.error=error.message;document.body.dataset.ready='1';console.error(error)}
