// Coverage per repository and language, for the Sources dialog: files
// extracted out of those tracked, call sites resolved in the repository, and
// functions with a resolved edge. Rows come from /api/status `coverage`.
import {esc} from './util.js';

const pct = (n, d) => (d ? `${Math.round((100 * n) / d)}%` : '—');

export function coverageHTML(rows) {
  if (!rows || !rows.length) return '<p>No coverage figures in this snapshot.</p>';
  const body = rows.map(r => {
    // LiveView events and HTTP requests have no files of their own: the
    // figures are sends and requests linked.
    if (r.language === 'events') {
      const extra = [
        r.sends_dynamic ? `dynamic ${r.sends_dynamic}` : '',
        `unlinked ${(r.sends || 0) - (r.sends_linked || 0) - (r.sends_dynamic || 0)}`,
      ].filter(Boolean).join(' · ');
      return `<tr><td>${esc(r.repo)}</td><td>events</td><td>—</td><td>${(r.sends_linked || 0).toLocaleString()} of ${(r.sends || 0).toLocaleString()} sends (${pct(r.sends_linked, r.sends)})<br><small>${esc(extra)}</small></td><td>${pct(r.handler_clauses_linked, r.handler_clauses)} of handle_event clauses</td></tr>`;
    }
    if (r.language === 'http') {
      const extra = r.not_extracted ? `<br><small>no path ${r.not_extracted}</small>` : '';
      return `<tr><td>${esc(r.repo)}</td><td>http</td><td>—</td><td>${(r.http_linked || 0).toLocaleString()} of ${(r.http_refs || 0).toLocaleString()} requests linked to a route (${pct(r.http_linked, r.http_refs)})${extra}</td><td>—</td></tr>`;
    }
    const calls = r.note ? esc(r.note) : r.call_sites
      ? `${r.in_repo.toLocaleString()} of ${r.call_sites.toLocaleString()} (${pct(r.in_repo, r.call_sites)})`
      : 'no call sites';
    const extra = r.note ? '' : [
      r.dispatch ? `dispatch ${r.dispatch}` : '',
      r.external ? `external ${r.external.toLocaleString()}` : '',
      r.unresolved ? `unresolved ${r.unresolved}` : '',
      r.not_extracted ? `not extracted ${r.not_extracted}` : '',
    ].filter(Boolean).join(' · ');
    return `<tr><td>${esc(r.repo)}</td><td>${esc(r.language)}</td><td>${r.files_extracted}/${r.files_tracked}</td><td>${calls}${extra ? `<br><small>${esc(extra)}</small>` : ''}</td><td>${r.note ? '—' : pct(r.functions_linked, r.functions)}</td></tr>`;
  }).join('');
  return `<table class="coverage"><thead><tr><th>Repository</th><th>Language</th><th>Files</th><th>Call sites in repo</th><th>Functions linked</th></tr></thead><tbody>${body}</tbody></table>` +
    '<p class="subtext">Files: extracted of those tracked with the language\'s extensions. Call sites in repo: calls that resolve to a function in the same repository; calls into libraries are counted as external. Functions linked: functions with at least one resolved call in or out. Events: LiveView sends (phx-* attributes, JS.push, a hook\'s pushEvent) linked to a handle_event, and the handle_event clauses some send reaches. HTTP: requests whose path links to a route.</p>';
}
