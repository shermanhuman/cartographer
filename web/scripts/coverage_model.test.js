import {test} from 'node:test';
import assert from 'node:assert/strict';
import {coverageHTML} from '../coverage-model.js';

test('a row shows files, call sites in repo with the rest, and functions linked', () => {
  const html = coverageHTML([{repo: 'breakdown-sales', language: 'go', files_extracted: 133, files_tracked: 133, call_sites: 12328, in_repo: 4494, dispatch: 112, external: 7722, unresolved: 0, not_extracted: 10, functions: 1443, functions_linked: 1300}]);
  assert.match(html, /breakdown-sales<\/td><td>go<\/td><td>133\/133/);
  assert.match(html, /4,494 of 12,328 \(36%\)/);
  assert.match(html, /dispatch 112 · external 7,722 · not extracted 10/);
  assert.match(html, /<td>90%<\/td>/);
});

test('a note replaces the call figures', () => {
  const html = coverageHTML([{repo: 'breakdown-infra', language: 'python', files_extracted: 0, files_tracked: 14, call_sites: 0, in_repo: 0, functions: 0, functions_linked: 0, note: 'no extractor'}]);
  assert.match(html, /0\/14<\/td><td>no extractor<\/td><td>—/);
});

test('no rows says so', () => {
  assert.match(coverageHTML([]), /No coverage figures/);
});

test('event and http rows show sends and requests linked', () => {
  const html = coverageHTML([
    {repo: 'breakdown-admin', language: 'events', files_extracted: 0, files_tracked: 0, sends: 288, sends_linked: 278, sends_dynamic: 2, handler_clauses: 250, handler_clauses_linked: 240},
    {repo: 'breakdown-sales', language: 'http', files_extracted: 0, files_tracked: 0, http_refs: 101, http_linked: 54, not_extracted: 8},
  ]);
  assert.match(html, /events<\/td><td>—<\/td><td>278 of 288 sends \(97%\)<br><small>dynamic 2 · unlinked 8<\/small><\/td><td>96% of handle_event clauses/);
  assert.match(html, /54 of 101 requests linked to a route \(53%\)<br><small>no path 8/);
});

test('dynamic calls and outside services get their own counts', () => {
  const html = coverageHTML([
    {repo: 'breakdown-admin', language: 'elixir', files_extracted: 900, files_tracked: 900, call_sites: 40000, in_repo: 9000, external: 30000, unresolved: 226, dynamic: 490, functions: 10, functions_linked: 9},
    {repo: 'breakdown-admin', language: 'http', files_extracted: 0, files_tracked: 0, http_refs: 1, http_linked: 0, http_external: 34},
  ]);
  assert.match(html, /external 30,000 · unresolved 226 · dynamic 490/);
  assert.match(html, /0 of 1 requests linked to a route \(0%\)<br><small>external 34<\/small>/);
});
