// Contexts: module clusters by namespace, with the tables their code
// mentions and the docs that mention their modules.
import {esc} from './util.js';
export function createContextBrowser({host,contexts,byId,selectContext,openEntity}){
  let query='',current='';
  const map=new Map(contexts.map(c=>[c.id,c]));
  const repos=[...new Set(contexts.map(c=>c.repo))].sort();
  const open=new Set(repos);
  host.innerHTML=`<div class="file-browser schema-browser"><section class="file-tree-pane" aria-label="Contexts"><div class="file-tree-tools"><input id="context-filter" type="search" placeholder="Find a context or module…" aria-label="Filter contexts or modules"><div class="tree-commands"><span id="context-match-count"></span><span>Namespace clusters</span></div></div><div id="context-tree" class="repository-tree"></div></section><section class="file-detail-pane" aria-label="Selected context"><div id="context-detail"></div></section></div>`;
  const tree=host.querySelector('#context-tree'),content=host.querySelector('#context-detail');
  function drawTree(){
    const words=query.toLowerCase().trim().split(/\s+/).filter(Boolean);
    const matches=contexts.filter(c=>words.every(w=>(c.name+' '+c.repo+' '+c.modules.map(id=>byId(id)?.full||id).join(' ')).toLowerCase().includes(w)));
    host.querySelector('#context-match-count').textContent=`${matches.length} / ${contexts.length} contexts`;
    tree.innerHTML=repos.map(repo=>{const subset=matches.filter(c=>c.repo===repo).sort((a,b)=>a.name.localeCompare(b.name));return subset.length?`<details data-repo="${esc(repo)}" ${query||open.has(repo)?'open':''}><summary><span class="folder-name">${esc(repo)}</span><small>${subset.length}</small></summary><div class="tree-children">${subset.map(c=>`<button class="tree-file ${c.id===current?'selected':''}" data-context="${esc(c.id)}" aria-label="Inspect context ${esc(c.name)}"><span class="file-glyph">◫</span><span>${esc(c.name)}</span><small>${c.modules.length}</small></button>`).join('')}</div></details>`:''}).join('')||'<p class="empty-state">No matching context or module.</p>';
    for(const el of tree.querySelectorAll('details'))el.addEventListener('toggle',()=>{if(el.open)open.add(el.dataset.repo);else open.delete(el.dataset.repo)});
  }
  function showContext(id){
    current=id;const c=map.get(id);
    if(!c){content.innerHTML='<div class="file-welcome"><span>◫</span><h2>Explore by context</h2><p>A context groups modules that share a namespace prefix. Select one to see its modules, the tables its code mentions, and the docs that describe it.</p><div class="note">Contexts are derived from module names, not from reviewed architecture boundaries.</div></div>';return}
    const module=id=>{const m=byId(id);return `<button class="relationship-row" data-go="${esc(id)}"><span><b>${esc(m?.full||id)}</b><small>${esc(m?.file||'')}</small></span><span class="badge">${esc(m?.count??0)} functions</span></button>`};
    const table=t=>`<button class="relationship-row" data-go="${esc(t.id)}"><span><b>${esc(byId(t.id)?.label||t.id)}</b><small>${esc(t.id)}</small></span><span class="badge amber">${t.count} mention${t.count===1?'':'s'} · candidate</span></button>`;
    const doc=d=>`<a class="relationship-row" href="${esc(d.url)}" target="_blank" rel="noreferrer"><span><b>${esc(d.title)}</b><small>${esc(d.repo)} / ${esc(d.path)}:${esc(d.line)}</small></span><span class="badge blue">doc ↗</span></a>`;
    content.innerHTML=`<div class="file-heading"><div class="file-path">CONTEXT / ${esc(c.repo)}</div><h2>${esc(c.name)}</h2><div class="file-tags"><span class="badge">${c.modules.length} modules</span><span class="badge amber">${c.tables.length} tables mentioned</span><span class="badge blue">${c.docs.length} docs</span></div><button class="secondary" data-context-graph="${esc(id)}">Open context graph ↗</button></div>
    <section class="schema-section"><h3>Modules</h3>${c.modules.map(module).join('')||'<p class="schema-hint">No modules.</p>'}</section>
    <section class="schema-section"><h3>Tables mentioned <small>textual mentions in this context's functions · candidate</small></h3>${c.tables.map(table).join('')||'<p class="schema-hint">No table mentions found in this context. That does not prove it never touches data.</p>'}</section>
    <section class="schema-section"><h3>Linked docs</h3>${c.docs.map(doc).join('')||'<p class="schema-hint">No tracked Markdown mentions these modules.</p>'}</section>`;
  }
  host.querySelector('#context-filter').oninput=e=>{query=e.target.value;drawTree()};
  host.addEventListener('click',e=>{const b=e.target.closest('button');if(!b)return;if(b.dataset.context)selectContext(b.dataset.context);else if(b.dataset.contextGraph)openEntity(b.dataset.contextGraph)});
  return {update(id){const changed=current!==id;const scroll=tree.scrollTop;if(changed||!content.children.length){showContext(id);drawTree()}else if(!tree.children.length)drawTree();tree.scrollTop=scroll}};
}
