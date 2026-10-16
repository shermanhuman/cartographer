// extract-javascript: writes one facts v1 document describing the JavaScript
// under a repository's source roots to stdout. Acorn parses the code; this
// plugin never executes project code. Usage: see README.md.
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {parse} from './vendor/acorn.mjs'; // acorn 8.15.0, MIT (vendor/ACORN-LICENSE)

export const VERSION = '0.1.0';
const EXTENSIONS = new Set(['.js', '.mjs', '.cjs']);
const FUNCTION_TYPES = new Set(['FunctionDeclaration', 'FunctionExpression', 'ArrowFunctionExpression']);
const USAGE = 'usage: node extract.mjs --repo NAME --root DIR [--revision SHA] [--source-roots assets/js]';

export function parseArgs(argv) {
  const opts = {sourceRoots: ['assets/js']};
  const flags = {'--repo': 'repo', '--root': 'root', '--revision': 'revision', '--source-roots': 'sourceRoots'};
  for (let i = 0; i < argv.length; i++) {
    const key = flags[argv[i]];
    if (!key || i + 1 >= argv.length) throw new Error(`unknown or incomplete argument: ${argv[i]}`);
    const value = argv[++i];
    opts[key] = key === 'sourceRoots' ? value.split(',').map(s => s.trim()).filter(Boolean) : value;
  }
  if (!opts.repo) throw new Error('--repo is required');
  if (!opts.root) throw new Error('--root is required');
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(opts.repo)) throw new Error('--repo must be a plain name (letters, digits, . _ -)');
  if (!fs.statSync(opts.root, {throwIfNoEntry: false})?.isDirectory()) throw new Error(`--root ${opts.root} is not a directory`);
  if (opts.sourceRoots.length === 0) throw new Error('--source-roots is empty');
  for (const r of opts.sourceRoots) {
    if (path.isAbsolute(r) || r.split(/[\\/]/).includes('..')) throw new Error(`--source-roots entries must be relative paths inside --root: ${r}`);
  }
  return opts;
}

// Regular files with a JavaScript extension. Symlinks are never followed;
// node_modules and dot-directories are skipped.
export function listFiles(root, roots) {
  const files = new Set();
  const notes = [];
  const walk = (rel) => {
    const abs = path.join(root, rel);
    const stat = fs.lstatSync(abs, {throwIfNoEntry: false});
    if (!stat) return;
    if (stat.isSymbolicLink()) {
      notes.push({kind: 'symlink_skipped', file: toPosix(rel), message: 'symlinked source is not read'});
    } else if (stat.isDirectory()) {
      for (const name of fs.readdirSync(abs).sort()) {
        if (name === 'node_modules' || name.startsWith('.')) continue;
        walk(path.join(rel, name));
      }
    } else if (stat.isFile() && EXTENSIONS.has(path.extname(rel))) {
      files.add(toPosix(rel));
    }
  };
  for (const r of roots) {
    // A symlinked component above the root itself is also refused.
    const parts = path.normalize(r).split(path.sep);
    let linked = false;
    for (let i = 1; i < parts.length; i++) {
      if (fs.lstatSync(path.join(root, ...parts.slice(0, i)), {throwIfNoEntry: false})?.isSymbolicLink()) linked = true;
    }
    if (linked) notes.push({kind: 'symlink_skipped', file: toPosix(r), message: 'symlinked source root is not read'});
    else walk(path.normalize(r));
  }
  return {files: [...files].sort(), notes};
}

const toPosix = (p) => p.split(path.sep).join('/');

function parseSource(source) {
  let first;
  for (const sourceType of ['module', 'script']) {
    const tokens = [];
    try {
      const ast = parse(source, {ecmaVersion: 'latest', sourceType, locations: true, allowHashBang: true, onToken: tokens});
      return {ast, tokens};
    } catch (e) {
      first ??= e;
    }
  }
  throw first;
}

const keyName = (key) => key?.type === 'Identifier' || key?.type === 'PrivateIdentifier' ? key.name : key?.type === 'Literal' ? String(key.value) : null;

// Iterative, like the walk: a.b.c... may be thousands deep.
function memberName(node) {
  const parts = [];
  for (; node?.type === 'MemberExpression' && !node.computed; node = node.object) parts.unshift(node.property.name);
  if (node?.type === 'Identifier') parts.unshift(node.name);
  else if (node?.type === 'ThisExpression') parts.unshift('this');
  return parts.length ? parts.join('.') : null;
}

// Name of the thing an object literal or class is bound to, if any.
function bindingName(node, parent) {
  if (node.id?.name) return node.id.name;
  if (!parent) return null;
  if (parent.type === 'VariableDeclarator' && parent.id.type === 'Identifier') return parent.id.name;
  if (parent.type === 'AssignmentExpression') return memberName(parent.left);
  if ((parent.type === 'Property' || parent.type === 'PropertyDefinition') && !parent.computed) return keyName(parent.key);
  if (parent.type === 'AssignmentPattern' && parent.left.type === 'Identifier') return parent.left.name;
  if (parent.type === 'ExportDefaultDeclaration') return 'default';
  return null;
}

// Qualified name for a function at stack[i]: methods and object properties
// are prefixed with their class or object binding (Hooks.Chart.mounted).
function functionName(stack, i) {
  const node = stack[i];
  const parent = stack[i - 1];
  if (node.id?.name) return node.id.name;
  if (parent && !parent.computed && (parent.type === 'MethodDefinition' || parent.type === 'PropertyDefinition')) {
    // Method/field < ClassBody < ClassDeclaration|ClassExpression.
    const owner = stack[i - 3] ? bindingName(stack[i - 3], stack[i - 4]) : null;
    return owner ? `${owner}.${keyName(parent.key)}` : keyName(parent.key);
  }
  if (parent?.type === 'Property' && !parent.computed) {
    const owner = ownerQualifier(stack, i - 2);
    return owner ? `${owner}.${keyName(parent.key)}` : keyName(parent.key);
  }
  return bindingName(node, parent);
}

// Nested object literals: {a: {b: {c() {}}}} bound to X gives X.a.b.c.
function ownerQualifier(stack, owner) {
  const parts = [];
  let j = owner;
  while (j >= 0 && stack[j]?.type === 'ObjectExpression') {
    const parent = stack[j - 1];
    const name = bindingName(stack[j], parent);
    if (!name) break;
    parts.unshift(name);
    if (parent?.type === 'Property' && stack[j - 2]?.type === 'ObjectExpression') j -= 2;
    else break;
  }
  return parts.length ? parts.join('.') : null;
}

function kindOf(node, parent) {
  if (node.type === 'ArrowFunctionExpression') return 'arrow';
  if (parent?.type === 'MethodDefinition') return 'method';
  if (parent?.type === 'Property' && (parent.method || parent.kind === 'get' || parent.kind === 'set')) return 'method';
  return 'function';
}

// A token's value as text. Regex tokens carry {pattern, flags, value} and
// BigInt literals a bigint, so neither may go through plain interpolation
// ("[object Object]", or 1n printing like 1).
function tokenText(t) {
  const v = t.value;
  if (v == null) return '';
  if (t.type.label === 'regexp') return `/${v.pattern}/${v.flags}`;
  if (typeof v === 'bigint') return `${v}n`;
  if (typeof v === 'object') return JSON.stringify(v);
  return String(v);
}

// sha256 over the function's token stream: whitespace, comments and quote
// style do not change it.
function fingerprint(tokens, node) {
  const hash = crypto.createHash('sha256');
  let lo = 0, hi = tokens.length;
  while (lo < hi) { const mid = (lo + hi) >> 1; if (tokens[mid].start < node.start) lo = mid + 1; else hi = mid; }
  for (let k = lo; k < tokens.length && tokens[k].end <= node.end; k++) {
    const t = tokens[k];
    hash.update(`${t.type.label}\u0000${tokenText(t)}\u0001`);
  }
  return hash.digest('hex');
}

function callTarget(node, file) {
  const c = node.callee;
  const arity = node.arguments.length;
  if (c.type === 'Identifier') return {target: {module: file, name: c.name, arity}, kind: 'js_local'};
  if (c.type === 'MemberExpression') {
    const name = !c.computed ? c.property.name : c.property.type === 'Literal' ? String(c.property.value) : '<dynamic>';
    const target = c.object.type === 'Identifier' ? {module: c.object.name, name, arity} : {name, arity};
    return {target, kind: 'js_dynamic'};
  }
  return {target: {name: '<dynamic>', arity}, kind: 'js_dynamic'};
}

export function extract({repo, root, revision, sourceRoots = ['assets/js']}) {
  const {files, notes} = listFiles(root, sourceRoots);
  const modules = [];
  const functions = [];
  const calls = [];

  for (const file of files) {
    let parsed;
    try {
      parsed = parseSource(fs.readFileSync(path.join(root, file), 'utf8'));
    } catch (e) {
      notes.push({kind: 'parse_error', file, message: e.loc ? `line ${e.loc.line}: ${e.message}` : e.message});
      continue;
    }
    const moduleId = `${repo}:${file}`;
    modules.push({id: moduleId, name: file, namespace: file.replace(/\.[^./]+$/, '').split('/'), file, line: 1, language: 'javascript'});
    const seen = new Map();
    const stack = [];

    // Iterative pre-order walk: a long call or member chain nests the AST
    // thousands deep, which would overflow a recursive visit.
    const work = [{node: parsed.ast, caller: null}];
    while (work.length) {
      const item = work.pop();
      if (item.exit) { stack.pop(); continue; }
      const {node, caller} = item;
      if (!node || typeof node.type !== 'string') continue;
      stack.push(node);
      let current = caller;
      if (FUNCTION_TYPES.has(node.type)) {
        const i = stack.length - 1;
        const {line, column} = node.loc.start;
        const name = functionName(stack, i) ?? `anonymous@${line}:${column}`;
        const arity = node.params.length;
        const base = `${moduleId}:${name}/${arity}`;
        const n = (seen.get(base) ?? 0) + 1;
        seen.set(base, n);
        const id = n === 1 ? base : `${base}#${n}`;
        functions.push({
          id, module: moduleId, name, arity,
          min_arity: node.params.filter(p => p.type !== 'AssignmentPattern' && p.type !== 'RestElement').length,
          kind: kindOf(node, stack[i - 1]), file, line, clauses: [line],
          fingerprint: fingerprint(parsed.tokens, node),
        });
        current = id;
      }
      if (node.type === 'CallExpression' && current) {
        calls.push({from: current, ...callTarget(node, file), file, line: node.loc.start.line});
      }
      work.push({exit: true});
      const children = [];
      for (const [key, value] of Object.entries(node)) {
        if (key === 'loc' || key === 'start' || key === 'end') continue;
        if (Array.isArray(value)) children.push(...value);
        else if (value && typeof value === 'object') children.push(value);
      }
      for (let k = children.length - 1; k >= 0; k--) work.push({node: children[k], caller: current});
    }
  }

  const cmp = (...keys) => (a, b) => {
    for (const k of keys) { const x = k(a), y = k(b); if (x < y) return -1; if (x > y) return 1; }
    return 0;
  };
  const uniqueCalls = [...new Map(calls.map(c => [JSON.stringify(c), c])).values()];
  const doc = {
    facts_version: 1,
    producer: {name: 'extract-javascript', version: VERSION, call_precision: 'heuristic'},
    repo,
    ...(revision ? {revision} : {}),
    files: files.map(p => ({path: p, language: 'javascript', category: /(^|\/)(test|tests|__tests__)\/|\.(test|spec)\.[cm]?js$/.test(p) ? 'test' : 'source'})),
    modules: modules.sort(cmp(m => m.id)),
    functions: functions.sort(cmp(f => f.id)),
    calls: uniqueCalls.sort(cmp(c => c.file, c => c.line, c => c.from, c => c.target.module ?? '', c => c.target.name, c => c.target.arity, c => c.kind)),
    notes: notes.sort(cmp(n => n.file ?? '', n => n.kind, n => n.message)),
  };
  return doc;
}

if (process.argv[1] && import.meta.url === pathToFileURL(fs.realpathSync(process.argv[1])).href) {
  let opts;
  try {
    opts = parseArgs(process.argv.slice(2));
  } catch (e) {
    process.stderr.write(`${e.message}\n${USAGE}\n`);
    process.exit(2);
  }
  process.stdout.write(JSON.stringify(extract(opts)));
}
