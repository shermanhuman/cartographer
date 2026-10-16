// Run: mise exec -- node --test plugins/extract-javascript/test/
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {execFileSync, spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {extract, parseArgs} from '../extract.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const fixture = path.join(here, 'fixture');
const entry = path.join(here, '..', 'extract.mjs');
const fn = (doc, name) => doc.functions.find(f => f.name === name);
const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), 'extract-js-'));

test('inventories functions, callbacks and methods with arity and min_arity', () => {
  const doc = extract({repo: 'shop', root: fixture});
  assert.deepEqual(doc.producer, {name: 'extract-javascript', version: '0.1.0', call_precision: 'heuristic'});
  assert.deepEqual(doc.modules.map(m => m.id), ['shop:assets/js/app.js', 'shop:assets/js/util.mjs']);
  assert.deepEqual(doc.notes, []);

  assert.equal(fn(doc, 'render').id, 'shop:assets/js/app.js:render/2');
  assert.deepEqual([fn(doc, 'render').arity, fn(doc, 'render').min_arity, fn(doc, 'render').kind], [2, 1, 'function']);
  assert.deepEqual([fn(doc, 'format').kind, fn(doc, 'format').min_arity], ['arrow', 1]);
  assert.equal(fn(doc, 'Hooks.Chart.mounted').kind, 'method');
  assert.deepEqual([fn(doc, 'Widget.update').arity, fn(doc, 'Widget.update').min_arity], [2, 1]);
  assert.equal(fn(doc, 'debounce').module, 'shop:assets/js/util.mjs');
  assert.ok(doc.functions.some(f => f.name.startsWith('anonymous@') && f.kind === 'arrow'));
  assert.ok(doc.functions.every(f => /^[0-9a-f]{64}$/.test(f.fingerprint) && f.clauses[0] === f.line));
});

test('calls keep their uncertainty: js_local and js_dynamic targets only', () => {
  const doc = extract({repo: 'shop', root: fixture});
  assert.ok(doc.calls.every(c => c.kind === 'js_local' || c.kind === 'js_dynamic'));
  assert.ok(doc.calls.every(c => !('to' in c)));
  const from = (name) => doc.calls.filter(c => c.from === fn(doc, name).id);
  assert.deepEqual(from('render').map(c => [c.target.name, c.kind]).sort(), [['items', 'js_local'], ['map', 'js_dynamic']]);
  assert.deepEqual(from('Widget.update')[0].target, {module: 'console', name: 'log', arity: 2});
  assert.deepEqual(from('Hooks.Chart.mounted')[0].target, {name: 'handleEvent', arity: 2});
});

test('fingerprints ignore whitespace and comments but follow the body', () => {
  const root = tmp();
  try {
    fs.cpSync(fixture, root, {recursive: true});
    const file = path.join(root, 'assets/js/app.js');
    const before = extract({repo: 'shop', root});
    const original = fs.readFileSync(file, 'utf8');
    fs.writeFileSync(file, original.replace('function items(el) {\n  return [...el.children];', 'function items( el ) {\n  // children\n\n  return [ ...el.children ];'));
    const spaced = extract({repo: 'shop', root});
    assert.equal(fn(spaced, 'items').fingerprint, fn(before, 'items').fingerprint);
    fs.writeFileSync(file, original.replace('[...el.children]', '[...el.childNodes]'));
    const changed = extract({repo: 'shop', root});
    assert.notEqual(fn(changed, 'items').fingerprint, fn(before, 'items').fingerprint);
    assert.equal(fn(changed, 'render').fingerprint, fn(before, 'render').fingerprint);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('fingerprints follow regex and BigInt literals', () => {
  const root = tmp();
  try {
    fs.mkdirSync(path.join(root, 'assets/js'), {recursive: true});
    // Same function name in every file, so only the literal differs.
    const bodies = ['/ab+c/g.test(s)', '/ab+d/g.test(s)', '/ab+c/i.test(s)', '1n', '1'];
    bodies.forEach((body, i) => fs.writeFileSync(path.join(root, `assets/js/f${i}.js`), `function f(s) { return ${body}; }`));
    const doc = extract({repo: 'x', root});
    const prints = doc.functions.map(f => f.fingerprint);
    assert.equal(prints.length, bodies.length);
    assert.equal(new Set(prints).size, bodies.length);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('syntax failures are notes, not silent omissions or crashes', () => {
  const root = tmp();
  try {
    fs.mkdirSync(path.join(root, 'assets/js'), {recursive: true});
    fs.writeFileSync(path.join(root, 'assets/js/bad.js'), 'export function {');
    fs.writeFileSync(path.join(root, 'assets/js/good.js'), 'function ok() { return 1; }');
    const doc = extract({repo: 'x', root});
    assert.equal(doc.notes.length, 1);
    assert.equal(doc.notes[0].kind, 'parse_error');
    assert.equal(doc.notes[0].file, 'assets/js/bad.js');
    assert.deepEqual(doc.functions.map(f => f.name), ['ok']);
    assert.deepEqual(doc.files.map(f => f.path), ['assets/js/bad.js', 'assets/js/good.js']);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('symlinks and node_modules are not read', () => {
  const root = tmp();
  const outside = tmp();
  try {
    fs.mkdirSync(path.join(root, 'assets/js/node_modules/dep'), {recursive: true});
    fs.writeFileSync(path.join(root, 'assets/js/node_modules/dep/index.js'), 'function dep() {}');
    fs.writeFileSync(path.join(outside, 'secret.js'), 'function secret() {}');
    fs.symlinkSync(path.join(outside, 'secret.js'), path.join(root, 'assets/js/link.js'));
    const doc = extract({repo: 'x', root});
    assert.deepEqual(doc.functions, []);
    assert.deepEqual(doc.notes.map(n => [n.kind, n.file]), [['symlink_skipped', 'assets/js/link.js']]);
  } finally { fs.rmSync(root, {recursive: true}); fs.rmSync(outside, {recursive: true}); }
});

test('arguments are validated', () => {
  assert.throws(() => parseArgs(['--root', fixture]), /--repo is required/);
  assert.throws(() => parseArgs(['--repo', 'a:b', '--root', fixture]), /plain name/);
  assert.throws(() => parseArgs(['--repo', 'a', '--root', fixture, '--source-roots', '../x']), /relative/);
  assert.deepEqual(parseArgs(['--repo', 'a', '--root', fixture, '--source-roots', 'src, lib']).sourceRoots, ['src', 'lib']);
  const bad = spawnSync(process.execPath, [entry, '--bogus'], {encoding: 'utf8'});
  assert.equal(bad.status, 2);
  assert.equal(bad.stdout, '');
});

test('CLI output is deterministic, also through a symlinked entry path', () => {
  const root = tmp();
  try {
    const link = path.join(root, 'extract.mjs');
    fs.symlinkSync(entry, link);
    const args = ['--repo', 'shop', '--root', fixture, '--revision', 'abc'];
    const first = execFileSync(process.execPath, [entry, ...args], {encoding: 'utf8'});
    const second = execFileSync(process.execPath, [link, ...args], {encoding: 'utf8'});
    assert.equal(first, second);
    assert.equal(JSON.parse(first).revision, 'abc');
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('deep call chains do not overflow the walk', () => {
  const root = tmp();
  try {
    fs.mkdirSync(path.join(root, 'assets/js'), {recursive: true});
    const chain = 'q' + '.then(step)'.repeat(5000);
    fs.writeFileSync(path.join(root, 'assets/js/deep.js'), `function run(q) { return ${chain}; }\n`);
    fs.writeFileSync(path.join(root, 'assets/js/ok.js'), 'function ok() { return 1; }');
    const doc = extract({repo: 'x', root});
    assert.deepEqual(doc.notes, []);
    assert.deepEqual(doc.functions.map(f => f.name), ['run', 'ok']);
    const calls = doc.calls.filter(c => c.from === fn(doc, 'run').id);
    // q.then(step) has a receiver module; the 4999 chained calls do not.
    assert.deepEqual(calls.map(c => c.target), [{name: 'then', arity: 1}, {module: 'q', name: 'then', arity: 1}]);
  } finally { fs.rmSync(root, {recursive: true}); }
});
