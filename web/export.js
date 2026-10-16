// Print and export helpers: a standalone SVG of the live graph (edges from
// the live <svg> with computed styles inlined, nodes redrawn as SVG) and the
// provenance footer every export carries.
import {esc} from './util.js';
const trunc=(s,n)=>{s=String(s??'');return s.length>n?s.slice(0,n-1)+'…':s};
const STYLE=['fill','stroke','stroke-width','stroke-dasharray','stroke-linejoin','stroke-linecap','paint-order','font-size','font-family','font-weight','opacity'];
const FONT='-apple-system, BlinkMacSystemFont, &quot;Segoe UI&quot;, sans-serif';

export function footerText(status, now = new Date()) {
  if (!status) return '';
  const repos = (status.repos || []).map(r => `${r.name}@${String(r.revision || '').slice(0, 7)}`).join(' ');
  return `cartographer · map ${status.snapshot}${repos ? ' · ' + repos : ''} · generated ${now.toISOString().replace(/\.\d+Z$/, 'Z')}`;
}

function inlineStyles(source, clone) {
  const from = [source, ...source.querySelectorAll('*')], to = [clone, ...clone.querySelectorAll('*')];
  from.forEach((el, i) => {
    const cs = getComputedStyle(el);
    to[i].setAttribute('style', STYLE.map(p => `${p}:${cs.getPropertyValue(p)}`).filter(d => !d.endsWith(':')).join(';'));
  });
}

// exportSVG returns a self-contained SVG document for the drawn graph.
// nodes: [{x, y, label, meta, path, badge, full, focused}] in stage space.
export function exportSVG({edges, nodes, width, height, title, subtitle, footer}) {
  const top = 54, bottom = footer ? 34 : 8, W = Math.max(width, 640), H = height + top + bottom;
  let edgeMarkup = '';
  if (edges) {
    const clone = edges.cloneNode(true);
    inlineStyles(edges, clone);
    const xs = new XMLSerializer();
    edgeMarkup = [...clone.childNodes].map(n => xs.serializeToString(n)).join('');
  }
  const node = n => `<g transform="translate(${n.x},${n.y})"><title>${esc(n.full)}</title><rect width="235" height="126" rx="8" fill="#ffffff" stroke="${n.focused ? '#3d866c' : '#dce4df'}" stroke-width="${n.focused ? 2 : 1}"/><text x="15" y="30" font-size="13" font-weight="600" fill="#243340">${esc(trunc(n.label, 30))}</text><text x="15" y="50" font-size="9" fill="#85948c">${esc(trunc(n.meta, 44))}</text>${n.path ? `<text x="15" y="66" font-size="9" fill="#9aa79d">${esc(trunc(n.path, 44))}</text>` : ''}<text x="15" y="110" font-size="9" fill="#6b8375">${esc(trunc(n.badge, 40))}</text></g>`;
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}" font-family="${FONT}" role="img" aria-label="${esc(title)}">`
    + `<rect width="${W}" height="${H}" fill="#f5f7f7"/>`
    + `<text x="20" y="26" font-size="15" font-weight="600" fill="#243340">${esc(title)}</text>`
    + (subtitle ? `<text x="20" y="44" font-size="10" fill="#71808c">${esc(trunc(subtitle, 160))}</text>` : '')
    + `<g transform="translate(0,${top})">${edgeMarkup}${nodes.map(node).join('')}</g>`
    + (footer ? `<text x="20" y="${H - 12}" font-size="9" fill="#71808c" class="export-footer-text">${esc(footer)}</text>` : '') + '</svg>';
}
