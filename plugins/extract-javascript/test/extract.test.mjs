// Run: CARTOGRAPHER_TYPESCRIPT=$(mise where npm:typescript@6.0.3) mise exec -- node --test plugins/extract-javascript/test/
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {execFileSync, spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {extract, parseArgs, loadTypeScript, TOP} from '../extract.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const fixture = path.join(here, 'fixture');
const corpus = path.join(here, 'corpus');
const entry = path.join(here, '..', 'extract.mjs');
const fn = (doc, name) => doc.functions.find(f => f.name === name);
const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), 'extract-js-'));
const ts = loadTypeScript();
const run = (opts) => extract({typescript: ts, ...opts});
// write lays out files under a fresh root: {"assets/js/a.js": "..."}.
function write(files, root = tmp()) {
  for (const [rel, text] of Object.entries(files)) {
    fs.mkdirSync(path.dirname(path.join(root, rel)), {recursive: true});
    fs.writeFileSync(path.join(root, rel), text);
  }
  return root;
}
const short = (id) => id.split(':').slice(1).join(':');
const edges = (doc) => doc.calls.filter(c => c.to).map(c => [c.from.split(':').slice(1).join(':'), c.to.split(':').slice(1).join(':'), c.precision]);

test('function ids match the v0.2.0 (Acorn) extractor on every naming rule', () => {
  const want = JSON.parse(fs.readFileSync(path.join(corpus, 'expected-ids.json'), 'utf8'));
  const doc = run({repo: 'corpus', root: corpus});
  assert.deepEqual(doc.notes, []);
  const real = doc.functions.filter(f => f.kind !== 'top').map(f => ({id: f.id, name: f.name, arity: f.arity, min_arity: f.min_arity, kind: f.kind, line: f.line}));
  assert.deepEqual(real, want);
  // A real function named "(top)" keeps its id; the synthetic one moves.
  assert.ok(want.some(f => f.id === `corpus:assets/js/names.js:${TOP}/0`));
  assert.deepEqual(doc.functions.filter(f => f.kind === 'top').map(f => f.id), [`corpus:assets/js/names.js:${TOP}/0#2`]);
});

test('inventories functions with arity and min_arity', () => {
  const doc = run({repo: 'shop', root: fixture});
  assert.deepEqual(doc.producer, {name: 'extract-javascript', version: '0.3.0', call_precision: 'heuristic'});
  assert.equal(doc.facts_version, 3);
  assert.deepEqual(doc.modules.map(m => m.id), ['shop:assets/js/app.js', 'shop:assets/js/util.mjs']);
  assert.deepEqual(doc.notes, []);
  assert.equal(fn(doc, 'render').id, 'shop:assets/js/app.js:render/2');
  assert.deepEqual([fn(doc, 'render').arity, fn(doc, 'render').min_arity, fn(doc, 'render').kind], [2, 1, 'function']);
  assert.deepEqual([fn(doc, 'format').kind, fn(doc, 'format').min_arity], ['arrow', 1]);
  assert.equal(fn(doc, 'Hooks.Chart.mounted').kind, 'method');
  assert.deepEqual([fn(doc, 'Widget.update').arity, fn(doc, 'Widget.update').min_arity], [2, 1]);
  assert.ok(doc.functions.every(f => /^[0-9a-f]{64}$/.test(f.fingerprint) && f.clauses[0] === f.line));
});

test('calls resolve to functions, with precision; the rest are counted', () => {
  const doc = run({repo: 'shop', root: fixture});
  assert.deepEqual(edges(doc).sort(), [
    ['assets/js/app.js:anonymous@16:33/1', 'assets/js/app.js:render/2', 'precise'],
    ['assets/js/app.js:anonymous@27:32/0', 'assets/js/app.js:Widget.update/2', 'heuristic'],
    ['assets/js/app.js:anonymous@4:23/1', 'assets/js/app.js:format/2', 'precise'],
    ['assets/js/app.js:render/2', 'assets/js/app.js:items/1', 'precise'],
  ]);
  // this.handleEvent: LiveView's API on an untyped `this`, never guessed.
  const unresolved = doc.calls.filter(c => c.kind === 'unresolved').map(c => c.target.name).sort();
  assert.deepEqual(unresolved, ['apply', 'handleEvent']);
  assert.deepEqual(doc.stats, [{language: 'javascript', call_sites: 12, in_repo: 4, dispatch: 0, external: 5, unresolved: 2, refs: 0, not_extracted: 1}]);
  // Top-level code calls from the file's synthetic function.
  assert.equal(fn(doc, TOP).id, `shop:assets/js/app.js:${TOP}/0`);
  assert.equal(doc.functions.filter(f => f.name === TOP).length, 1); // util.mjs has no top-level call
});

test('hooks: object methods, this-assigned functions, aliases, IIFE locals', () => {
  const root = write({
    'assets/js/hooks.js': `
export const Pane = {
  mounted() {
    this._calc = () => this.el.offsetHeight;
    this._onResize = () => this._calc();
    this.helper();
  },
  helper() { return this._calc(); },
};
export const Alias = Pane;
`,
    'assets/js/app.js': `
import {Pane, Alias} from "./hooks";
import {LiveSocket} from "phoenix_live_view";
(function () {
  function api(method, path) { return fetch(path, {method}); }
  api("GET", "/x");
})();
Alias.helper();
new LiveSocket("/live", {hooks: {Pane, Alias}});
`,
  });
  try {
    const doc = run({repo: 'x', root});
    assert.deepEqual(edges(doc).sort(), [
      ['assets/js/app.js:(top)/0', 'assets/js/app.js:anonymous@4:1/0', 'precise'],
      ['assets/js/app.js:(top)/0', 'assets/js/hooks.js:Pane.helper/0', 'heuristic'],
      ['assets/js/app.js:anonymous@4:1/0', 'assets/js/app.js:api/2', 'precise'],
      ['assets/js/hooks.js:Pane.helper/0', 'assets/js/hooks.js:this._calc/0', 'heuristic'],
      ['assets/js/hooks.js:Pane.mounted/0', 'assets/js/hooks.js:Pane.helper/0', 'heuristic'],
      ['assets/js/hooks.js:this._onResize/0', 'assets/js/hooks.js:this._calc/0', 'heuristic'],
    ]);
    // new LiveSocket from an uninstalled package: external, not unresolved.
    const app = doc.stats.find(s => s.language === 'javascript');
    assert.equal(app.external >= 2, true); // fetch (lib.dom) and LiveSocket
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('TypeScript, TSX and .mts are read; declaration files are not', () => {
  const root = write({
    'src/util.mts': 'export function twice(n: number): number { return n * 2; }\n',
    'src/types.d.ts': 'declare function ambient(): void;\n',
    'src/Button.tsx': `
import {twice} from "./util.mjs";
export function Button(props: {n: number}) { return <b>{twice(props.n)}</b>; }
export function Page() { return <Button n={1} />; }
`,
    'src/main.ts': `
import {Page} from "./Button";
class Svc { run(this: Svc, a: string, b?: number) { return a; } }
function overloaded(a: string): string;
function overloaded(a: number): number;
function overloaded(a: any) { return a; }
export function start() { new Svc().run("x"); overloaded(1); return Page(); }
`,
  });
  try {
    const doc = run({repo: 'x', root, sourceRoots: ['src']});
    assert.deepEqual(doc.files.map(f => [f.path, f.language]), [
      ['src/Button.tsx', 'typescript'], ['src/main.ts', 'typescript'], ['src/util.mts', 'typescript']]);
    assert.deepEqual(edges(doc).sort(), [
      ['src/Button.tsx:Button/1', 'src/util.mts:twice/1', 'precise'],
      ['src/Button.tsx:Page/0', 'src/Button.tsx:Button/1', 'precise'],
      ['src/main.ts:start/0', 'src/Button.tsx:Page/0', 'precise'],
      ['src/main.ts:start/0', 'src/main.ts:Svc.run/2', 'heuristic'],
      ['src/main.ts:start/0', 'src/main.ts:overloaded/1', 'precise'],
    ]);
    assert.equal(doc.calls.find(c => c.kind === 'jsx' && c.to.endsWith('Button/1')).from, 'x:src/Button.tsx:Page/0');
    // The `this` parameter isn't an argument; `b?` is optional.
    assert.deepEqual([fn(doc, 'Svc.run').arity, fn(doc, 'Svc.run').min_arity], [2, 1]);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('a call to a non-function is unresolved; into an unread file, external', () => {
  const root = write({
    'assets/vendor/topbar.js': 'export function show() {}\n',
    'assets/js/a.js': `
import {show} from "../vendor/topbar.js";
const settings = {retries: 3};
export function go(cb) { show(); settings.retries(); cb(); }
`,
  });
  try {
    const doc = run({repo: 'x', root});
    assert.deepEqual(edges(doc), []);
    assert.deepEqual(doc.calls.filter(c => c.kind === 'unresolved').map(c => c.target.name).sort(), ['cb', 'retries']);
    assert.equal(doc.stats[0].external, 1);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('the fence: nothing outside the snapshot changes the facts', () => {
  const outer = tmp();
  try {
    write({
      'repo/assets/js/a.js': 'import {dep} from "dep";\nexport class Local { helper() {} }\nexport function go() { dep((x) => x.helper()); }\n',
      'repo/assets/tsconfig.json': '{"extends": "@missing/tsconfig", "compilerOptions": {"baseUrl": ".", "paths": {"*": ["../deps/*"]}}}\n',
    }, outer);
    const clean = JSON.stringify(run({repo: 'x', root: path.join(outer, 'repo')}));
    // A package above the snapshot whose types would type `x` as Local and
    // so link x.helper(): the fence must keep it invisible.
    write({
      'node_modules/dep/package.json': '{"name": "dep", "types": "index.d.ts"}\n',
      'node_modules/dep/index.d.ts': 'import type {Local} from "../../repo/assets/js/a.js";\nexport function dep(cb: (x: Local) => void): void;\n',
    }, outer);
    const polluted = JSON.stringify(run({repo: 'x', root: path.join(outer, 'repo')}));
    assert.equal(polluted, clean);
    const doc = JSON.parse(clean);
    assert.deepEqual(edges(doc), []);
    assert.deepEqual(doc.notes.map(n => [n.kind, n.file]), [['config_fallback', 'assets/tsconfig.json']]);
    // A symlinked root reads the same; a link leaving the root is refused.
    const link = path.join(outer, 'link');
    fs.symlinkSync(path.join(outer, 'repo'), link);
    assert.equal(JSON.stringify(run({repo: 'x', root: link})), clean);
    fs.symlinkSync(path.join(outer, 'node_modules/dep/index.d.ts'), path.join(outer, 'repo/assets/js/near.js'));
    const linked = run({repo: 'x', root: path.join(outer, 'repo')});
    assert.deepEqual(linked.notes.map(n => [n.kind, n.file]), [['symlink_skipped', 'assets/js/near.js'], ['config_fallback', 'assets/tsconfig.json']]);
  } finally { fs.rmSync(outer, {recursive: true}); }
});

test('facts are the same from two different roots', () => {
  const a = write({'assets/js/a.js': 'function f() { g(); }\nfunction g() {}\nbroken(;\n', 'assets/js/b.js': 'export const x = () => 1;\n'});
  const b = write({'deeper/still/x': ''});
  try {
    fs.cpSync(a, path.join(b, 'deeper/still/repo'), {recursive: true});
    const one = JSON.stringify(run({repo: 'x', root: a}));
    const two = JSON.stringify(run({repo: 'x', root: path.join(b, 'deeper/still/repo')}));
    assert.equal(one, two);
    assert.ok(!one.includes(os.tmpdir()), 'facts carry no absolute path');
  } finally { fs.rmSync(a, {recursive: true}); fs.rmSync(b, {recursive: true}); }
});

test('minified files are listed and skipped with a note', () => {
  const root = write({'assets/js/sdk.js': 'function a(){}' + ';'.repeat(10001) + '\n', 'assets/js/ok.js': 'function ok() {}\n'});
  try {
    const doc = run({repo: 'x', root});
    assert.deepEqual(doc.functions.map(f => f.name), ['ok']);
    assert.deepEqual(doc.files.map(f => f.path), ['assets/js/ok.js', 'assets/js/sdk.js']);
    assert.deepEqual(doc.notes.map(n => [n.kind, n.file]), [['minified_skipped', 'assets/js/sdk.js']]);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('fingerprints ignore whitespace and comments but follow the body', () => {
  const root = tmp();
  try {
    fs.cpSync(fixture, root, {recursive: true});
    const file = path.join(root, 'assets/js/app.js');
    const before = run({repo: 'shop', root});
    const original = fs.readFileSync(file, 'utf8');
    fs.writeFileSync(file, original.replace('function items(el) {\n  return [...el.children];', 'function items( el ) {\n  // children\n  /** doc */\n\n  return [ ...el.children ];'));
    const spaced = run({repo: 'shop', root});
    assert.equal(fn(spaced, 'items').fingerprint, fn(before, 'items').fingerprint);
    fs.writeFileSync(file, original.replace('[...el.children]', '[...el.childNodes]'));
    const changed = run({repo: 'shop', root});
    assert.notEqual(fn(changed, 'items').fingerprint, fn(before, 'items').fingerprint);
    assert.equal(fn(changed, 'render').fingerprint, fn(before, 'render').fingerprint);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('fingerprints follow regex, BigInt and string literals', () => {
  const root = tmp();
  try {
    fs.mkdirSync(path.join(root, 'assets/js'), {recursive: true});
    const bodies = ['/ab+c/g.test(s)', '/ab+d/g.test(s)', '/ab+c/i.test(s)', '1n', '1', '"a"', "'b'"];
    bodies.forEach((body, i) => fs.writeFileSync(path.join(root, `assets/js/f${i}.js`), `function f(s) { return ${body}; }`));
    const doc = run({repo: 'x', root});
    const prints = doc.functions.map(f => f.fingerprint);
    assert.equal(prints.length, bodies.length);
    assert.equal(new Set(prints).size, bodies.length);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('syntax failures are notes, not silent omissions or crashes', () => {
  const root = write({'assets/js/bad.js': 'export function {', 'assets/js/good.js': 'function ok() { return 1; }'});
  try {
    const doc = run({repo: 'x', root});
    assert.deepEqual(doc.notes.map(n => [n.kind, n.file]), [['parse_error', 'assets/js/bad.js']]);
    assert.match(doc.notes[0].message, /^line 1: /);
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
    const doc = run({repo: 'x', root});
    assert.deepEqual(doc.functions, []);
    assert.deepEqual(doc.notes.map(n => [n.kind, n.file]), [['symlink_skipped', 'assets/js/link.js']]);
  } finally { fs.rmSync(root, {recursive: true}); fs.rmSync(outside, {recursive: true}); }
});

test('the compiler is found in either mise layout, at the pinned version only', () => {
  assert.throws(() => loadTypeScript(''), /no TypeScript/);
  const dir = tmp();
  try {
    assert.throws(() => loadTypeScript(dir), /no TypeScript package under/);
    const real = path.dirname(path.dirname(fs.realpathSync(path.join(ts.getDefaultLibFilePath({})))));
    fs.mkdirSync(path.join(dir, 'lib/node_modules'), {recursive: true});
    fs.symlinkSync(real, path.join(dir, 'lib/node_modules/typescript'));
    assert.equal(loadTypeScript(dir).version, ts.version);
  } finally { fs.rmSync(dir, {recursive: true}); }
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

test('deep call chains do not overflow', () => {
  const root = write({'assets/js/deep.js': `function run(q) { return q${'\n.then(step)'.repeat(5000)}; }\n`, 'assets/js/ok.js': 'function ok() { return 1; }'});
  try {
    const doc = run({repo: 'x', root});
    assert.deepEqual(doc.notes.map(n => [n.kind, n.file]), [['too_deep', 'assets/js/deep.js']]);
    assert.deepEqual(doc.functions.map(f => f.name), ['run', 'ok']);
    assert.equal(doc.stats[0].call_sites, 5000);
    assert.equal(doc.stats[0].unresolved, 5000);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('arity is clamped to the facts format bound of 255', () => {
  const params = Array.from({length: 300}, (_, i) => `p${i}`).join(', ');
  const root = write({'assets/js/wide.js': `function wide(${params}) { return p0; }`});
  try {
    const doc = run({repo: 'x', root});
    assert.deepEqual([fn(doc, 'wide').arity, fn(doc, 'wide').min_arity], [255, 255]);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('fetch: literal, template and concatenated URLs, one level of wrapper', () => {
  const root = write({
    'assets/js/dial.js': `
(function () {
  async function api(method, path, body) {
    const res = await fetch(path, {method, credentials: "same-origin"});
    return res.json();
  }
  function track(name) { fetch("/dial/events", {method: "POST", body: name}); }
  function getJSON(url) { return fetch(url).then(r => r.json()); }
  async function start(id, source) {
    await api("GET", "/dial/me");
    await api("POST", \`/dial/calls/\${id}/end\`);
    await api("POST", "/dial/greeting/audio?source=" + source);
    getJSON("/greetings/voices?q=" + source);
    fetch(document.querySelector("form").action, {method: "POST"});
    fetch("//cdn.example.com/x.js");
  }
})();
`,
    'e2e/check.mjs': `
const ADMIN = process.env.ADMIN || "http://localhost:7889";
const BASE = process.env.BASE ?? "http://localhost:7871";
function admin(path, body) { return fetch(ADMIN + path, {method: "POST", body}); }
await admin("/api/v1/sales/cold/check", {});
await fetch(\`\${BASE}/greetings/audio/tait.mp3\`);
await fetch(BASE + computeLater());
await fetch("https://api.example.com/v2/items?x=1", {method: "delete"});
`,
  });
  try {
    const doc = run({repo: 'sales', root, sourceRoots: ['assets/js', 'e2e']});
    const refs = doc.http_refs.map(r => [short(r.from), r.method, r.path, r.origin ?? '', r.base ?? '', r.line]);
    assert.deepEqual(refs, [
      ['assets/js/dial.js:track/1', 'POST', '/dial/events', 'same', '', 7],
      ['assets/js/dial.js:start/2', 'GET', '/dial/me', 'same', '', 10],
      ['assets/js/dial.js:start/2', 'POST', '/dial/calls/:id/end', 'same', '', 11],
      ['assets/js/dial.js:start/2', 'POST', '/dial/greeting/audio', 'same', '', 12],
      ['assets/js/dial.js:start/2', 'GET', '/greetings/voices', 'same', '', 13],
      ['e2e/check.mjs:(top)/0', 'POST', '/api/v1/sales/cold/check', '', 'ADMIN', 5],
      ['e2e/check.mjs:(top)/0', 'GET', '/greetings/audio/tait.mp3', '', 'BASE', 6],
      ['e2e/check.mjs:(top)/0', 'DELETE', '/v2/items', '', 'https://api.example.com', 8],
    ]);
    // form.action, a protocol-relative URL and BASE + a computed path are
    // fetches with no path to link: counted, not invented.
    const http = doc.stats.find(s => s.language === 'http');
    assert.deepEqual([http.call_sites, http.refs, http.not_extracted], [11, 8, 3]);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('hook pushEvent: the registry names a hook object is bound to', () => {
  const root = write({
    'assets/js/hooks/sig.js': `
export const DocumentSignature = {
  mounted() {
    this.handleEvent("document:sign", () => { this.pushEvent("passkey-signed", {}); });
    this.pushEventTo("#x", "aimed", {});
    this.pushEvent(this.el.dataset.event, {});
  },
};
export const PasskeyAssertion = DocumentSignature;
export const Unregistered = { mounted() { this.pushEvent("lost", {}); } };
`,
    'assets/js/app.js': `
import {LiveSocket} from "phoenix_live_view";
import {DocumentSignature, PasskeyAssertion} from "./hooks/sig";
const hooks = {DocumentSignature, PasskeyAssertion};
new LiveSocket("/live", Socket, {hooks: {...colocated, Sig: DocumentSignature, PasskeyAssertion}});
new LiveSocket("/other", Socket, {hooks});
`,
  });
  try {
    const doc = run({repo: 'x', root});
    const ev = doc.events.map(e => [e.event ?? 'DYN', e.via, (e.hooks ?? []).join(','), e.target ?? '', e.line]);
    assert.deepEqual(ev, [
      ['passkey-signed', 'pushEvent', 'DocumentSignature,PasskeyAssertion,Sig', '', 4],
      ['aimed', 'pushEventTo', 'DocumentSignature,PasskeyAssertion,Sig', 'dynamic', 5],
      ['DYN', 'pushEvent', 'DocumentSignature,PasskeyAssertion,Sig', '', 6],
      ['lost', 'pushEvent', '', '', 10],
    ]);
    assert.ok(doc.events.every(e => e.precision === 'heuristic' && e.from.startsWith('x:assets/js/hooks/sig.js:')));
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('a call into an unread file (syntax error, minified, too deep) is external, never a dangling id', () => {
  const root = write({
    'src/a.js': 'import {foo} from "./b.js";\nimport {bar} from "./min.js";\nimport {baz} from "./deep.js";\nexport function run() { return foo(1) + bar() + baz(); }\n',
    'src/b.js': 'export function foo(x) { return x; }\nexport function broken( {\n',
    'src/min.js': 'export function bar() {}' + ';'.repeat(10001) + '\n',
    'src/deep.js': `export function baz() { return q${'.then(s)'.repeat(400)}; }\n`,
  });
  try {
    const doc = run({repo: 't', root, sourceRoots: ['src']});
    assert.deepEqual(doc.notes.map(n => [n.kind, n.file]), [['parse_error', 'src/b.js'], ['too_deep', 'src/deep.js'], ['minified_skipped', 'src/min.js']]);
    assert.deepEqual(edges(doc), []);
    const ids = new Set(doc.functions.map(f => f.id));
    assert.ok(doc.calls.every(c => !c.to || ids.has(c.to)));
    // In-repo files the plugin doesn't read: not extracted, not external.
    assert.equal(doc.stats.find(s => s.language === 'javascript').not_extracted, 3);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('fetch: a parameter inside a readable path is the function\'s own request; unreached wrappers are counted', () => {
  const root = write({
    'src/api.js': `
export function loadUser(id) { return fetch(\`/api/users/\${id}\`); }
export function search(term) { return fetch("/api/search?q=" + term, {method: "POST"}); }
export function unused(path) { return fetch(path); }
const BASE = "https://api.example.com";
export function remote(path) { return fetch(BASE + path); }
export function go() { remote("/v1/items"); prefixed("/orders"); versioned("/orders"); }
function prefixed(path) { return fetch("/api" + path); }
const API = "https://api.example.com/v1";
function versioned(path) { return fetch(API + path); }
`,
  });
  try {
    const doc = run({repo: 't', root, sourceRoots: ['src']});
    assert.deepEqual(doc.http_refs.map(r => [short(r.from), r.method, r.path, r.origin ?? r.base]), [
      ['src/api.js:loadUser/1', 'GET', '/api/users/:id', 'same'],
      ['src/api.js:search/1', 'POST', '/api/search', 'same'],
      ['src/api.js:go/0', 'GET', '/api/orders', 'same'],
      ['src/api.js:go/0', 'GET', '/v1/items', 'BASE'],
      ['src/api.js:go/0', 'GET', '/v1/orders', 'API'],
    ]);
    const http = doc.stats.find(s => s.language === 'http');
    assert.deepEqual([http.call_sites, http.refs, http.not_extracted], [6, 5, 1]);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('hook registries: member assignments, default exports, spreads and namespace imports', () => {
  const root = write({
    'assets/js/hooks/a.js': 'export default { mounted() { this.pushEvent("from_default", {}); } };\n',
    'assets/js/hooks/b.js': 'export const B = { mounted() { this.pushEvent("from_ns", {}); } };\n',
    'assets/js/app.js': `
import A from "./hooks/a.js";
import * as Group from "./hooks/b.js";
let Hooks = {};
Hooks.Chart = { mounted() { this.pushEvent("from_member", {}); } };
const extra = {A};
new LiveSocket("/live", Socket, {hooks: {...Hooks, ...extra}});
new LiveSocket("/other", Socket, {hooks: Group});
`,
  });
  try {
    const doc = run({repo: 'x', root});
    assert.deepEqual(doc.events.map(e => [e.event, (e.hooks ?? []).join(',')]).sort(), [['from_default', 'A'], ['from_member', 'Chart'], ['from_ns', 'B']]);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('a call through a reassignable binding is heuristic', () => {
  const root = write({'src/a.js': 'function a() {}\nfunction b() {}\nexport function run(f, cb = a) {\n  let h = a;\n  if (f) h = b;\n  h();\n  cb();\n  const k = a;\n  k();\n  a();\n}\n'});
  try {
    const doc = run({repo: 't', root, sourceRoots: ['src']});
    assert.deepEqual(edges(doc).map(e => e[2]).sort(), ['heuristic', 'heuristic', 'precise', 'precise']);
  } finally { fs.rmSync(root, {recursive: true}); }
});

test('review round 3: no directory listed for a config, spread fetch options, empty event names', () => {
  const outer = tmp();
  try {
    write({
      'repo/src/a.js': 'export function go(opts) { fetch("/orders", {...opts}); fetch("/items", {...opts, method: "PUT"}); }\nexport const Hook = { mounted() { this.pushEvent("", {}); } };\n',
      'repo/src/tsconfig.json': '{"include": ["../../**/*"], "compilerOptions": {"allowJs": true}}\n',
      'elsewhere/x.js': 'export function x() {}\n',
    }, outer);
    // Every directory listed during extraction is inside the snapshot.
    const listed = [];
    const real = fs.readdirSync;
    fs.readdirSync = function (p, ...rest) { listed.push(String(p)); return real.call(this, p, ...rest); };
    let doc;
    try { doc = run({repo: 't', root: path.join(outer, 'repo'), sourceRoots: ['src']}); } finally { fs.readdirSync = real; }
    const rootReal = fs.realpathSync(path.join(outer, 'repo'));
    assert.deepEqual(listed.filter(p => !fs.realpathSync(p).startsWith(rootReal)), []);
    assert.deepEqual(doc.http_refs.map(r => [r.path, r.method]), [['/orders', ''], ['/items', 'PUT']]);
    assert.deepEqual(doc.events.map(e => [e.event ?? 'DYN', !!e.dynamic]), [['DYN', true]]);
  } finally { fs.rmSync(outer, {recursive: true}); }
});

test('fetch wrappers are decided by their callers, never by the URL shape', () => {
  const root = write({
    'src/w.js': `
const BASE = "https://api.example.com";
function joined(path) { return fetch(\`\${BASE}/\${path}\`); }
function slash(path) { return fetch("/" + path); }
function json(id) { return fetch("/users/" + id + ".json"); }
function req(id = "me") { return fetch("/people/" + id); }
export function go() { joined("orders"); slash("api/x"); req("one"); req(); }
`,
  });
  try {
    const doc = run({repo: 't', root, sourceRoots: ['src']});
    assert.deepEqual(doc.http_refs.map(r => [short(r.from), r.path, r.origin ?? r.base]).sort(), [
      ['src/w.js:go/0', '/api/x', 'same'],
      ['src/w.js:go/0', '/orders', 'BASE'],
      ['src/w.js:go/0', '/people/:id', 'same'],
      ['src/w.js:go/0', '/people/one', 'same'],
      ['src/w.js:json/1', '/users/:id.json', 'same'],
    ]);
  } finally { fs.rmSync(root, {recursive: true}); }
});

// Decision 16: a call on a value that comes from a package is external.
test('values from packages are external: chromium pages, helpers, callbacks', () => {
  const root = write({
    'e2e/smoke.mjs': `import { chromium } from 'playwright';
async function login(page) { await page.fill('#email', 'a'); await page.click('button'); }
async function main() {
  const browser = await chromium.launch();
  const page = await browser.newPage();
  await page.locator('a').click();
  await login(page);
  page.on('response', (r) => r.url());
  const { request } = page;
  await request.get('/x');
  await browser.close();
}
main();
`,
  });
  const doc = run({repo: 'shop', root, sourceRoots: ['e2e']});
  const unresolved = doc.calls.filter(c => c.kind === 'unresolved').map(c => c.target.name);
  assert.deepEqual(unresolved, []);
  const js = doc.stats.find(s => s.language === 'javascript');
  assert.equal(js.unresolved, 0);
  assert.ok(js.external >= 9, `external ${js.external}`);
});

test('standard-library callbacks and project values never taint', () => {
  const root = write({
    'assets/js/app.js': `import { thing } from 'some-pkg';
function make() { return { render() {} }; }
function fetchX() { return new Promise(r => r({ x() {} })); }
function helper(v) { v.go(); }
export function run(items) {
  items.forEach(i => i.render());
  fetchX().then(r => r.x());
  helper(make());
  helper(thing);
}
`,
  });
  const doc = run({repo: 'shop', root});
  const unresolved = doc.calls.filter(c => c.kind === 'unresolved').map(c => c.target.name).sort();
  // render and x are on project values, forEach on an untyped parameter;
  // go is reached once with a project value, so the helper's parameter
  // isn't a package value.
  assert.deepEqual(unresolved, ['forEach', 'go', 'render', 'x']);
});

// Round 1: H1-M5 (a helper reached twice), H1-m4 (require), H1-M6 (a
// package function's callbacks take the caller's values).
test('a helper called twice with package values, and require(), are external', () => {
  const root = write({
    'e2e/twice.cjs': `const { chromium } = require('playwright');
async function login(page) { await page.fill('#email', 'a'); }
async function main() {
  const browser = await chromium.launch();
  const a = await browser.newPage();
  const b = await browser.newPage();
  await login(a);
  await login(a);
  await login(b);
}
main();
`,
  });
  const doc = run({repo: 'shop', root, sourceRoots: ['e2e']});
  // require() itself has no declaration without Node's types: unresolved,
  // as before; what it returns is the package.
  assert.deepEqual(doc.calls.filter(c => c.kind === 'unresolved').map(c => c.target.name), ['require']);
});

test("a package function's callbacks take the caller's values", () => {
  const root = write({
    'assets/js/app.js': `import _ from 'lodash';
function make() { return [{ render() {} }]; }
export function run() {
  _.each(make(), (i) => i.render());
}
`,
  });
  const doc = run({repo: 'shop', root});
  assert.deepEqual(doc.calls.filter(c => c.kind === 'unresolved').map(c => c.target.name), ['render']);
});
