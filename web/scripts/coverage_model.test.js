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
