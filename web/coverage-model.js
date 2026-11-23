// Coverage per repository and language, for the Sources dialog: files
// extracted out of those tracked, call sites resolved in the repository, and
// functions with a resolved edge. Rows come from /api/status `coverage`.
import {esc} from './util.js';

const pct = (n, d) => (d ? `${Math.round((100 * n) / d)}%` : '—');

export function coverageHTML(rows) {
  if (!rows || !rows.length) return '<p>No coverage figures in this snapshot.</p>';
  const body = rows.map(r => {
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
    '<p class="subtext">Files: extracted of those tracked with the language\'s extensions. Call sites in repo: calls that resolve to a function in the same repository; calls into libraries are counted as external. Functions linked: functions with at least one resolved call in or out.</p>';
}
