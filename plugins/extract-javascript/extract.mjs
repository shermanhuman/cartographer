// extract-javascript: writes one facts document describing the JavaScript and
// TypeScript under a repository's source roots to stdout. The TypeScript 6
// compiler API parses the code and resolves calls; this plugin never executes
// project code and never reads outside --root. Usage: see README.md.
import crypto from 'node:crypto';
import fs from 'node:fs';
import {createRequire} from 'node:module';
import path from 'node:path';
import {pathToFileURL} from 'node:url';

export const VERSION = '0.5.0';
// The compiler version every machine must run: a different one could resolve
// differently and change the map.
export const TYPESCRIPT_VERSION = '6.0.3';
const LANGUAGE = {'.js': 'javascript', '.mjs': 'javascript', '.cjs': 'javascript', '.jsx': 'javascript',
  '.ts': 'typescript', '.mts': 'typescript', '.cts': 'typescript', '.tsx': 'typescript'};
const MAX_ARITY = 255;
// A line this long is generated or minified code (a vendored SDK), not code a
// person maintains: it is listed and skipped with a note.
const MINIFIED_LINE = 10000;
// Nesting deeper than this (a 5,000-link call chain) overflows the
// compiler's recursive binder: such a file is inventoried without resolution.
const MAX_DEPTH = 250;
export const TOP = '(top)';
const USAGE = 'usage: node extract.mjs --repo NAME --root DIR [--revision SHA] [--source-roots assets/js] [--typescript DIR]';

export function parseArgs(argv) {
  const opts = {sourceRoots: ['assets/js']};
  const flags = {'--repo': 'repo', '--root': 'root', '--revision': 'revision', '--source-roots': 'sourceRoots', '--typescript': 'typescript'};
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

// loadTypeScript finds the pinned compiler in a mise install directory
// (`mise where npm:typescript@<version>`): aube installs put the package at
// node_modules/typescript, the npm backend at lib/node_modules/typescript.
export function loadTypeScript(dir = process.env.CARTOGRAPHER_TYPESCRIPT) {
  if (!dir) throw new Error(`no TypeScript: pass --typescript <mise where npm:typescript@${TYPESCRIPT_VERSION}> or set CARTOGRAPHER_TYPESCRIPT`);
  for (const rel of ['node_modules/typescript', 'lib/node_modules/typescript']) {
    const pkg = path.join(dir, rel, 'package.json');
    if (!fs.existsSync(pkg)) continue;
    const require = createRequire(pkg);
    const ts = require('./lib/typescript.js');
    if (ts.version !== TYPESCRIPT_VERSION) throw new Error(`TypeScript ${ts.version} at ${path.join(dir, rel)}; want ${TYPESCRIPT_VERSION}`);
    return ts;
  }
  throw new Error(`no TypeScript package under ${dir} (looked in node_modules/typescript and lib/node_modules/typescript)`);
}

const toPosix = (p) => p.split(path.sep).join('/');
const languageOf = (file) => file.endsWith('.d.ts') || file.endsWith('.d.mts') || file.endsWith('.d.cts') ? null : LANGUAGE[path.extname(file)] ?? null;

// Regular files with a JavaScript or TypeScript extension (declaration files
// aside). Symlinks are never followed; node_modules and dot-directories are
// skipped.
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
    } else if (stat.isFile() && languageOf(rel)) {
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

// fence is the only way the compiler sees the file system: paths inside the
// snapshot (symlinks resolved, and refused when they leave it) and the
// pinned compiler's own lib directory. Everything else does not exist, so a
// node_modules or tsconfig above the snapshot can't change the map.
function fence(ts, rootReal, libDir, hidden = new Set()) {
  const inside = (real) => real === rootReal || real.startsWith(rootReal + '/') || real.startsWith(libDir + '/');
  // The real path, refused when it differs from the asked path only by case
  // (a case-insensitive disk would otherwise find ./Foo as foo.js, which
  // Linux never does).
  const real = (p) => {
    let r, asked;
    try { r = fs.realpathSync.native(p); asked = fs.realpathSync(p); } catch { return null; }
    if (r !== asked && r.toLowerCase() === asked.toLowerCase()) return null;
    return r;
  };
  const allowed = (p) => {
    const r = real(path.resolve(p));
    return r !== null && inside(r) && !hidden.has(r) ? r : null;
  };
  return {
    fileExists: (p) => { const r = allowed(p); return r !== null && fs.statSync(r).isFile(); },
    directoryExists: (p) => { const r = allowed(p); return r !== null && fs.statSync(r).isDirectory(); },
    readFile: (p) => { const r = allowed(p); return r !== null && fs.statSync(r).isFile() ? fs.readFileSync(r, 'utf8') : undefined; },
    getDirectories: (p) => {
      const r = allowed(p);
      if (r === null) return [];
      return fs.readdirSync(r, {withFileTypes: true}).filter(e => e.isDirectory() || e.isSymbolicLink()).map(e => e.name).filter(n => { const x = allowed(path.join(p, n)); return x !== null && fs.statSync(x).isDirectory(); });
    },
    realpath: (p) => allowed(p) ?? p,
    // Input files come from the source roots, never from a config's
    // include patterns, so no directory is ever listed for the compiler
    // (a pattern reaching outside the snapshot would walk the host first).
    readDirectory: () => [],
    useCaseSensitiveFileNames: true,
  };
}

// The options a config may set. Everything else is fixed so every repo is
// read the same way: JS allowed, nothing emitted, no ambient @types.
const KEPT = ['baseUrl', 'paths', 'rootDirs', 'jsx', 'jsxFactory', 'jsxFragmentFactory', 'jsxImportSource', 'module', 'moduleResolution', 'target', 'lib', 'experimentalDecorators', 'allowImportingTsExtensions', 'customConditions', 'resolvePackageJsonExports', 'resolvePackageJsonImports', 'moduleSuffixes', 'allowArbitraryExtensions'];

function defaults(ts) {
  return {
    allowJs: true, checkJs: false, noEmit: true, noImplicitThis: true, types: [], skipLibCheck: true,
    jsx: ts.JsxEmit.Preserve, module: ts.ModuleKind.ESNext, moduleResolution: ts.ModuleResolutionKind.Bundler,
    target: ts.ScriptTarget.ESNext, allowNonTsExtensions: true,
  };
}

// configFor reads the nearest tsconfig.json or jsconfig.json above file, inside
// the snapshot, for its compiler options only. A config that can't be read
// whole (an `extends` outside the fence) keeps the options it could read,
// on top of the defaults, with a config_fallback note.
function configFor(ts, host, root, file, cache, notes) {
  for (let dir = path.posix.dirname(file); ; dir = path.posix.dirname(dir)) {
    for (const name of ['tsconfig.json', 'jsconfig.json']) {
      const rel = dir === '.' ? name : `${dir}/${name}`;
      const abs = path.join(root, rel);
      if (!host.fileExists(abs)) continue;
      if (!cache.has(rel)) cache.set(rel, readConfig(ts, host, root, rel, abs, notes));
      return cache.get(rel);
    }
    if (dir === '.' || dir === '') return cache.get('') ?? (cache.set('', {key: '', options: defaults(ts)}), cache.get(''));
  }
}

function readConfig(ts, host, root, rel, abs, notes) {
  const base = defaults(ts);
  const read = ts.readConfigFile(abs, host.readFile);
  let options = {};
  if (read.error) {
    notes.push({kind: 'config_fallback', file: rel, message: 'config could not be parsed; defaults used'});
  } else {
    const parsed = ts.parseJsonConfigFileContent(read.config, {...host, onUnRecoverableConfigFileDiagnostic() {}}, path.dirname(abs), undefined, abs);
    // 18003 (no inputs) is expected: files come from the source roots.
    if (parsed.errors.some(e => e.code !== 18003)) notes.push({kind: 'config_fallback', file: rel, message: 'part of the config could not be read (such as an extends outside the snapshot); only the options read are applied'});
    options = parsed.options;
  }
  for (const k of KEPT) if (options[k] !== undefined) base[k] = options[k];
  if (options.pathsBasePath) base.pathsBasePath = options.pathsBasePath;
  return {key: rel, options: base};
}

// ---- naming: the v0.2.0 (Acorn) id grammar, on the TypeScript AST --------

const TRANSPARENT = new Set(['ParenthesizedExpression', 'AsExpression', 'SatisfiesExpression', 'NonNullExpression', 'TypeAssertionExpression']);

export function naming(ts) {
  const K = ts.SyntaxKind;
  const transparent = (n) => n && TRANSPARENT.has(K[n.kind]);
  // The parent an ESTree walk would see: parentheses and type wrappers are
  // not nodes there.
  const up = (n) => { let p = n.parent; while (transparent(p)) p = p.parent; return p; };
  const isFn = (n) => n.kind === K.FunctionDeclaration || n.kind === K.FunctionExpression || n.kind === K.ArrowFunction ||
    n.kind === K.MethodDeclaration || n.kind === K.GetAccessor || n.kind === K.SetAccessor || n.kind === K.Constructor;
  const isClass = (n) => n && (n.kind === K.ClassDeclaration || n.kind === K.ClassExpression);
  const isObject = (n) => n && n.kind === K.ObjectLiteralExpression;
  const isAssign = (n) => n && n.kind === K.BinaryExpression && n.operatorToken.kind >= K.FirstAssignment && n.operatorToken.kind <= K.LastAssignment;
  const hasMod = (n, k) => (ts.canHaveModifiers(n) ? ts.getModifiers(n) : undefined)?.some(m => m.kind === k) ?? false;
  const computed = (name) => !name || name.kind === K.ComputedPropertyName;

  function keyName(key) {
    if (!key) return null;
    switch (key.kind) {
      case K.Identifier: return key.text;
      case K.PrivateIdentifier: return key.text.replace(/^#/, '');
      case K.StringLiteral: case K.NoSubstitutionTemplateLiteral: return key.text;
      case K.NumericLiteral: return String(Number(key.text));
      case K.BigIntLiteral: return key.text.replace(/n$/, '');
      default: return null;
    }
  }

  function memberName(node) {
    const parts = [];
    while (transparent(node)) node = node.expression;
    for (; node?.kind === K.PropertyAccessExpression; node = node.expression) {
      parts.unshift(keyName(node.name));
      while (transparent(node.expression)) node = node.expression;
    }
    if (node?.kind === K.Identifier) parts.unshift(node.text);
    else if (node?.kind === K.ThisKeyword) parts.unshift('this');
    return parts.length ? parts.join('.') : null;
  }

  // Name of the thing an object literal, class or function is bound to.
  function bindingName(node) {
    if ((isClass(node) || node.kind === K.FunctionExpression || node.kind === K.FunctionDeclaration) && node.name) return node.name.text;
    if ((node.kind === K.FunctionDeclaration || node.kind === K.ClassDeclaration) && hasMod(node, K.DefaultKeyword)) return 'default';
    const parent = up(node);
    if (!parent) return null;
    if (parent.kind === K.VariableDeclaration && parent.name.kind === K.Identifier && parent.initializer && skip(parent.initializer) === node) return parent.name.text;
    if (isAssign(parent) && skip(parent.right) === node) return memberName(parent.left);
    if ((parent.kind === K.PropertyAssignment || parent.kind === K.PropertyDeclaration) && skip(parent.initializer) === node && !computed(parent.name)) return keyName(parent.name);
    if ((parent.kind === K.Parameter || parent.kind === K.BindingElement) && parent.initializer && skip(parent.initializer) === node && parent.name.kind === K.Identifier) return parent.name.text;
    if (parent.kind === K.ExportAssignment && !parent.isExportEquals) return 'default';
    return null;
  }
  const skip = (n) => { while (transparent(n)) n = n.expression; return n; };

  // Nested object literals: {a: {b: {c() {}}}} bound to X gives X.a.b.c.
  function ownerQualifier(obj) {
    const parts = [];
    while (isObject(obj)) {
      const name = bindingName(obj);
      if (!name) break;
      parts.unshift(name);
      const parent = up(obj);
      if (parent?.kind === K.PropertyAssignment && isObject(up(parent))) obj = up(parent);
      else break;
    }
    return parts.length ? parts.join('.') : null;
  }

  // The qualified name, or null for an anonymous function.
  function functionName(node) {
    if ((node.kind === K.FunctionDeclaration || node.kind === K.FunctionExpression) && node.name) return node.name.text;
    const parent = up(node);
    const member = node.kind === K.MethodDeclaration || node.kind === K.GetAccessor || node.kind === K.SetAccessor || node.kind === K.Constructor;
    if (member && isClass(parent)) {
      if (node.kind !== K.Constructor && computed(node.name)) return null;
      const key = node.kind === K.Constructor ? 'constructor' : keyName(node.name);
      const owner = bindingName(parent);
      return owner ? `${owner}.${key}` : key;
    }
    if (parent?.kind === K.PropertyDeclaration && isClass(parent.parent) && skip(parent.initializer) === node) {
      if (computed(parent.name)) return null;
      const owner = bindingName(parent.parent);
      return owner ? `${owner}.${keyName(parent.name)}` : keyName(parent.name);
    }
    if (member && isObject(parent)) {
      if (computed(node.name)) return null;
      const owner = ownerQualifier(parent);
      return owner ? `${owner}.${keyName(node.name)}` : keyName(node.name);
    }
    if (parent?.kind === K.PropertyAssignment && skip(parent.initializer) === node && isObject(up(parent))) {
      if (computed(parent.name)) return null;
      const owner = ownerQualifier(up(parent));
      return owner ? `${owner}.${keyName(parent.name)}` : keyName(parent.name);
    }
    return bindingName(node);
  }

  function kindOf(node) {
    if (node.kind === K.ArrowFunction) return 'arrow';
    if (node.kind === K.FunctionDeclaration || node.kind === K.FunctionExpression) return 'function';
    return 'method';
  }

  // Where an ESTree function node starts: methods at their parameter list,
  // functions at `async` or `function` (never at `export`/`default`).
  function startOf(node, sf) {
    const kids = node.getChildren(sf);
    if (node.kind === K.MethodDeclaration || node.kind === K.GetAccessor || node.kind === K.SetAccessor || node.kind === K.Constructor) {
      const open = kids.find(k => k.kind === K.OpenParenToken);
      if (open) return open.getStart(sf);
    }
    if (node.kind === K.FunctionDeclaration || node.kind === K.FunctionExpression) {
      const mods = ts.canHaveModifiers(node) ? ts.getModifiers(node) ?? [] : [];
      const asyncMod = mods.find(m => m.kind === K.AsyncKeyword);
      if (asyncMod) return asyncMod.getStart(sf);
      const kw = kids.find(k => k.kind === K.FunctionKeyword);
      if (kw) return kw.getStart(sf);
    }
    return node.getStart(sf);
  }

  return {isFn, functionName, kindOf, startOf, up, skip, isObject, keyName};
}

// ---- tokens and fingerprints ----------------------------------------------

// Every token of a source file, in order: leaves of the syntax tree (JSDoc
// aside), so comments and whitespace never count.
function tokensOf(ts, sf) {
  const K = ts.SyntaxKind;
  const out = [];
  const work = [sf];
  while (work.length) {
    const n = work.pop();
    if (n.kind >= K.FirstJSDocNode && n.kind <= K.LastJSDocNode) continue;
    const kids = n.getChildren(sf);
    if (kids.length === 0) {
      if (n.kind !== K.EndOfFileToken && n.end > n.pos) out.push(n);
      continue;
    }
    for (let i = kids.length - 1; i >= 0; i--) work.push(kids[i]);
  }
  return out;
}

function tokenText(ts, t) {
  const K = ts.SyntaxKind;
  switch (t.kind) {
    case K.Identifier: case K.PrivateIdentifier: case K.NumericLiteral: case K.BigIntLiteral:
    case K.RegularExpressionLiteral: case K.JsxText:
      return t.text ?? t.getText();
    case K.StringLiteral: case K.NoSubstitutionTemplateLiteral: case K.TemplateHead: case K.TemplateMiddle: case K.TemplateTail:
      return JSON.stringify(t.text);
    default:
      return ts.tokenToString(t.kind) ?? t.getText();
  }
}

function fingerprint(ts, tokens, starts, lo0, end) {
  const hash = crypto.createHash('sha256');
  let lo = 0, hi = starts.length;
  while (lo < hi) { const mid = (lo + hi) >> 1; if (starts[mid] < lo0) lo = mid + 1; else hi = mid; }
  for (let k = lo; k < tokens.length && tokens[k].end <= end; k++) {
    hash.update(`${ts.SyntaxKind[tokens[k].kind]}\u0000${tokenText(ts, tokens[k])}\u0001`);
  }
  return hash.digest('hex');
}

// ---- extraction -----------------------------------------------------------

export function extract({repo, root, revision, sourceRoots = ['assets/js'], typescript}) {
  const ts = typeof typescript === 'object' ? typescript : loadTypeScript(typescript);
  const K = ts.SyntaxKind;
  const N = naming(ts);
  const rootReal = fs.realpathSync.native(path.resolve(root));
  const libDir = fs.realpathSync.native(path.dirname(ts.getDefaultLibFilePath({})));
  // Files the plugin doesn't read (minified, too deep, unparsable) don't
  // exist for the compiler either: an import of one is a call into an
  // unread file, never into functions the facts don't list.
  const hidden = new Set();
  const host0 = fence(ts, rootReal, libDir, hidden);
  const {files: listed, notes} = listFiles(root, sourceRoots);

  // Minified files are listed but not read.
  const files = [];
  for (const file of listed) {
    const text = fs.readFileSync(path.join(root, file), 'utf8');
    if (text.split('\n').some(l => l.length > MINIFIED_LINE)) {
      notes.push({kind: 'minified_skipped', file, message: `a line is over ${MINIFIED_LINE} characters; treated as generated code and not read`});
      hidden.add(fs.realpathSync.native(path.join(rootReal, file)));
    } else files.push(file);
  }

  // One program per config: each file is read with its nearest config.
  const configs = new Map();
  const groups = new Map();
  const deep = [];
  for (const file of files) {
    const sf = ts.createSourceFile(file, fs.readFileSync(path.join(root, file), 'utf8'), ts.ScriptTarget.ESNext, true, scriptKind(ts, file));
    if (depthOf(ts, sf) > MAX_DEPTH) {
      notes.push({kind: 'too_deep', file, message: `expressions nest more than ${MAX_DEPTH} deep; functions listed, calls not resolved`});
      deep.push({file, sf});
      hidden.add(fs.realpathSync.native(path.join(rootReal, file)));
      continue;
    }
    if (sf.parseDiagnostics?.length) {
      const d = sf.parseDiagnostics[0];
      const where = d.start !== undefined ? `line ${sf.getLineAndCharacterOfPosition(d.start).line + 1}: ` : '';
      notes.push({kind: 'parse_error', file, message: where + ts.flattenDiagnosticMessageText(d.messageText, ' ')});
      hidden.add(fs.realpathSync.native(path.join(rootReal, file)));
      continue;
    }
    const cfg = configFor(ts, host0, root, file, configs, notes);
    if (!groups.has(cfg.key)) groups.set(cfg.key, {cfg, files: []});
    groups.get(cfg.key).files.push(file);
  }

  const modules = [];
  const functions = [];
  const calls = [];
  const stats = new Map();
  // fetch: refs, wrappers (functions whose fetch URL is a parameter) and
  // the resolved calls that might reach a wrapper.
  const http = {refs: [], wrappers: new Map(), calls: [], sites: 0, unbuilt: 0};
  // LiveView hooks: the names each hook object is registered under, and
  // the pushEvent calls inside hook objects.
  const hooks = {registry: new Map(), pushes: []};
  // Values from packages (decision 16): resolved calls by target function
  // (for one level of parameters), and the unresolved member calls to
  // re-check once a program's files are walked.
  const taint = {callers: new Map(), pending: [], dropped: new Set(), memo: new Map(), memoParams: new Map()};
  const stat = (lang) => { if (!stats.has(lang)) stats.set(lang, {language: lang, call_sites: 0, in_repo: 0, dispatch: 0, external: 0, unresolved: 0, refs: 0, not_extracted: 0}); return stats.get(lang); };
  const extracted = new Set(files.filter(f => !hidden.has(fs.realpathSync.native(path.join(rootReal, f)))));
  for (const {file} of deep) extracted.add(file);

  for (const key of [...groups.keys()].sort()) {
    const {cfg, files: group} = groups.get(key);
    const options = cfg.options;
    const host = ts.createCompilerHost(options, true);
    Object.assign(host, {
      fileExists: host0.fileExists, readFile: host0.readFile, directoryExists: host0.directoryExists,
      getDirectories: host0.getDirectories, realpath: host0.realpath, useCaseSensitiveFileNames: () => true,
      getCurrentDirectory: () => rootReal,
      getSourceFile: (name, lang) => {
        const text = host0.readFile(name);
        return text === undefined ? undefined : ts.createSourceFile(name, text, lang, true);
      },
      writeFile() {},
    });
    const roots = group.map(f => path.join(rootReal, f));
    const program = ts.createProgram({rootNames: roots, options, host});
    const checker = program.getTypeChecker();
    // Functions of every extracted file this program sees: a call may
    // resolve into another group's file, which is only named, not walked.
    const relOf = (sf) => toPosix(path.relative(rootReal, fs.realpathSync.native(sf.fileName)));

    for (const file of group) {
      const sf = program.getSourceFile(path.join(rootReal, file));
      const lang = languageOf(file);
      if (!sf || sf.parseDiagnostics?.length) {
        const d = sf?.parseDiagnostics?.[0];
        const where = d && d.start !== undefined ? `line ${sf.getLineAndCharacterOfPosition(d.start).line + 1}: ` : '';
        notes.push({kind: 'parse_error', file, message: where + (d ? ts.flattenDiagnosticMessageText(d.messageText, ' ') : 'not read')});
        continue;
      }
      walkFile({ts, K, N, sf, file, lang, repo, checker, relOf, extracted, modules, functions, calls, stat, http, hooks, taint});
    }
    // A call on a value that comes from a package is a call into that
    // package: external, not unresolved.
    for (const p of taint.pending) {
      if (!tainted(ts, K, checker, p.expr, taint, 0, new Set(), true)) continue;
      p.s.unresolved--;
      p.s.external++;
      taint.dropped.add(p.call);
    }
    taint.pending = [];
    taint.callers = new Map();
    taint.memo = new Map();
    taint.memoParams = new Map();
  }

  for (const {file, sf} of deep) {
    walkFile({ts, K, N, sf, file, lang: languageOf(file), repo, checker: null, relOf: null, extracted, modules, functions, calls, stat, http, hooks, taint});
  }

  // One level of wrapper: a call to api("GET", "/dial/me") is a request
  // from the caller.
  const reached = new Set();
  for (const c of http.calls) for (const w of http.wrappers.get(c.to) ?? []) {
    reached.add(w);
    http.sites++;
    const parts = w.parts.flatMap(p => p.param !== undefined ? (c.args[p.param] ?? [{dyn: p.name}]).map(a => a.param !== undefined ? {dyn: a.name} : a) : [p]);
    let method = w.method;
    if (typeof method === 'object') {
      const a = c.args[method.param];
      method = a && a.length === 1 && a[0].lit !== undefined ? a[0].lit.toUpperCase() : '';
    }
    const ref = buildRef(parts, method, w.base);
    if (ref) http.refs.push({from: c.from, ...ref, file: c.file, line: c.line});
    else http.unbuilt++;
  }
  // A wrapper no resolved call reaches is its own request (or, with no
  // readable path, one with no path to link).
  for (const [from, ws] of http.wrappers) for (const w of ws) {
    if (reached.has(w)) continue;
    http.sites++;
    if (w.own) http.refs.push({from, ...w.own, file: w.file, line: w.line});
    else http.unbuilt++;
  }
  // Every ref's from is a listed function: (top) is listed when its file's
  // top-level code made the call.
  const known = new Set(functions.map(f => f.id));
  http.refs = [...new Map(http.refs.filter(r => known.has(r.from)).map(r => [JSON.stringify(r), r])).values()];
  if (http.sites > 0) {
    stats.set('http', {language: 'http', call_sites: http.sites, in_repo: 0, dispatch: 0, external: 0, unresolved: 0, refs: http.refs.length, not_extracted: http.unbuilt});
  }
  const events = hooks.pushes.filter(p => known.has(p.from)).map(p => ({
    from: p.from,
    ...(p.event === null ? {dynamic: true} : {event: p.event}),
    via: p.via,
    ...(p.via === 'pushEventTo' ? {target: 'dynamic'} : {}),
    ...(p.owner && hooks.registry.get(p.owner)?.size ? {hooks: [...hooks.registry.get(p.owner)].sort()} : {}),
    file: p.file, line: p.line, precision: 'heuristic',
  }));

  const cmp = (...keys) => (a, b) => {
    for (const k of keys) { const x = k(a), y = k(b); if (x < y) return -1; if (x > y) return 1; }
    return 0;
  };
  const uniqueCalls = [...new Map(calls.filter(c => !taint.dropped.has(c)).map(c => [JSON.stringify(c), c])).values()];
  return {
    facts_version: 3,
    producer: {name: 'extract-javascript', version: VERSION, call_precision: 'heuristic'},
    repo,
    ...(revision ? {revision} : {}),
    files: listed.map(p => ({path: p, language: languageOf(p), category: /(^|\/)(test|tests|__tests__|e2e)\/|\.(test|spec)\.[cm]?[jt]sx?$/.test(p) ? 'test' : 'source'})),
    modules: modules.sort(cmp(m => m.id)),
    functions: functions.sort(cmp(f => f.id)),
    calls: uniqueCalls.sort(cmp(c => c.file, c => c.line, c => c.from, c => c.to ?? '', c => c.target?.module ?? '', c => c.target?.name ?? '', c => c.target?.arity ?? 0, c => c.kind)),
    notes: notes.sort(cmp(n => n.file ?? '', n => n.kind, n => n.message)),
    ...(events.length ? {events: events.sort(cmp(e => e.file, e => e.line, e => e.from, e => e.event ?? ''))} : {}),
    http_refs: http.refs.sort(cmp(r => r.file, r => r.line, r => r.from, r => r.method, r => r.path)),
    stats: [...stats.values()].filter(s => s.call_sites > 0).sort(cmp(s => s.language)),
  };
}

function scriptKind(ts, file) {
  const ext = path.extname(file);
  return ext === '.tsx' ? ts.ScriptKind.TSX : ext === '.jsx' ? ts.ScriptKind.JSX : ['.ts', '.mts', '.cts'].includes(ext) ? ts.ScriptKind.TS : ts.ScriptKind.JS;
}

function depthOf(ts, sf) {
  let max = 0;
  const work = [[sf, 0]];
  while (work.length) {
    const [n, d] = work.pop();
    if (d > max) max = d;
    ts.forEachChild(n, (c) => { work.push([c, d + 1]); });
  }
  return max;
}

// fnIndex names every function in a source file, in walk order, so ids are
// the same whichever program reads the file.
const fnIndexCache = new WeakMap();
function fnIndex(ts, N, sf, moduleId) {
  if (fnIndexCache.has(sf)) return fnIndexCache.get(sf);
  const K = ts.SyntaxKind;
  const byNode = new Map();
  const list = [];
  const seen = new Map();
  const work = [sf];
  while (work.length) {
    const node = work.pop();
    if (N.isFn(node) && node.body) {
      const start = N.startOf(node, sf);
      const {line, character} = sf.getLineAndCharacterOfPosition(start);
      const name = N.functionName(node) ?? `anonymous@${line + 1}:${character}`;
      const params = node.parameters.filter(p => !(p.name.kind === K.Identifier && p.name.text === 'this'));
      const arity = Math.min(params.length, MAX_ARITY);
      const base = `${moduleId}:${name}/${arity}`;
      const n = (seen.get(base) ?? 0) + 1;
      seen.set(base, n);
      const entry = {node, id: n === 1 ? base : `${base}#${n}`, name, arity, line: line + 1, start,
        min_arity: Math.min(params.filter(p => !p.initializer && !p.dotDotDotToken && !p.questionToken).length, arity)};
      byNode.set(node, entry);
      list.push(entry);
    }
    const kids = [];
    ts.forEachChild(node, (c) => { kids.push(c); });
    for (let k = kids.length - 1; k >= 0; k--) work.push(kids[k]);
  }
  // The synthetic top-level function comes after every real one, so the
  // `#n` rule can never move a real id (a real function may be named "(top)").
  const topBase = `${moduleId}:${TOP}/0`;
  const top = {id: seen.has(topBase) ? `${topBase}#${seen.get(topBase) + 1}` : topBase};
  const out = {byNode, list, top};
  fnIndexCache.set(sf, out);
  return out;
}

function walkFile({ts, K, N, sf, file, lang, repo, checker, relOf, extracted, modules, functions, calls, stat, http, hooks, taint}) {
  const moduleId = `${repo}:${file}`;
  modules.push({id: moduleId, name: file, namespace: file.replace(/\.[^./]+$/, '').split('/'), file, line: 1, language: lang});
  const idx = fnIndex(ts, N, sf, moduleId);
  const tokens = tokensOf(ts, sf);
  const starts = tokens.map(t => t.getStart(sf));
  for (const e of idx.list) {
    functions.push({id: e.id, module: moduleId, name: e.name, arity: e.arity, min_arity: e.min_arity, kind: N.kindOf(e.node),
      file, line: e.line, clauses: [e.line], fingerprint: fingerprint(ts, tokens, starts, e.start, e.node.end)});
  }
  let topUsed = false;
  const s = stat(lang);

  // Functions assigned to `this.x` inside one object literal's methods
  // (LiveView hooks): `this.x()` there resolves to them, heuristically.
  const thisAssigned = new Map();
  const methodOwner = (fnNode) => {
    // The object literal whose method this function is, or that an arrow
    // inside such a method sees as `this`.
    let n = fnNode;
    while (n && n.kind === K.ArrowFunction) n = enclosingFn(n.parent);
    if (!n) return null;
    const p = N.up(n);
    if ((n.kind === K.MethodDeclaration || n.kind === K.GetAccessor || n.kind === K.SetAccessor) && N.isObject(p)) return p;
    if (n.kind === K.FunctionExpression && p?.kind === K.PropertyAssignment && N.isObject(N.up(p))) return N.up(p);
    return null;
  };
  const enclosingFn = (n) => { for (; n; n = n.parent) if (N.isFn(n) && n.body) return n; return null; };

  const work = [sf];
  const pending = [];
  while (work.length) {
    const node = work.pop();
    if (node.kind === K.BinaryExpression && node.operatorToken.kind === K.EqualsToken) {
      const left = N.skip(node.left);
      const right = N.skip(node.right);
      if (left.kind === K.PropertyAccessExpression && left.expression.kind === K.ThisKeyword && idx.byNode.has(right)) {
        const owner = methodOwner(enclosingFn(node));
        if (owner) {
          if (!thisAssigned.has(owner)) thisAssigned.set(owner, new Map());
          thisAssigned.get(owner).set(N.keyName(left.name), idx.byNode.get(right).id);
        }
      }
    }
    if (node.kind === K.CallExpression || node.kind === K.NewExpression || node.kind === K.JsxOpeningElement || node.kind === K.JsxSelfClosingElement) pending.push(node);
    const kids = [];
    ts.forEachChild(node, (c) => { kids.push(c); });
    for (let k = kids.length - 1; k >= 0; k--) work.push(kids[k]);
  }

  for (const node of pending) {
    const encl = enclosingFn(node.parent);
    const from = encl ? idx.byNode.get(encl).id : idx.top.id;
    const line = sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1;
    const jsx = node.kind === K.JsxOpeningElement || node.kind === K.JsxSelfClosingElement;
    const callee = N.skip(jsx ? node.tagName : node.expression);
    if (jsx && callee.kind === K.Identifier && /^[a-z]/.test(callee.text)) continue; // <div>: an element, not a call
    if (!encl) topUsed = true;
    const arity = jsx ? 1 : Math.min(node.arguments?.length ?? 0, MAX_ARITY);
    s.call_sites++;
    const r = resolve({ts, K, N, node, callee, jsx, checker, relOf, extracted, repo});
    if (checker && node.kind === K.NewExpression && calleeName(K, N, callee) === 'LiveSocket') {
      const opts = node.arguments?.[2] && N.skip(node.arguments[2]);
      const reg = opts?.kind === K.ObjectLiteralExpression && opts.properties.find(p => p.name && N.keyName(p.name) === 'hooks');
      if (reg?.kind === K.ShorthandPropertyAssignment) {
        // {hooks}: the variable it names.
        let sym;
        try { sym = checker.getShorthandAssignmentValueSymbol(reg); } catch { sym = undefined; }
        const obj = declObject(ts, K, N, checker, sym, 0);
        if (obj) registerHooks(ts, K, N, checker, obj, hooks);
      } else if (reg?.kind === K.PropertyAssignment) {
        registerHooks(ts, K, N, checker, reg.initializer, hooks);
      }
    }
    if (checker && !jsx && node.kind === K.CallExpression) hookCall({ts, K, N, checker, node, callee, encl, from, file, line, hooks, methodOwner});
    if (checker && !jsx && node.kind === K.CallExpression && isFetch(K, callee)) {
      fetchRef({ts, K, N, checker, node, encl, from, file, line, http});
    }
    if (r.to && r.target && node.kind === K.CallExpression) {
      if (!taint.callers.has(r.target)) taint.callers.set(r.target, []);
      taint.callers.get(r.target).push(node.arguments);
    }
    if (r.to) {
      // Every resolved call, with or without arguments: a wrapper's
      // defaulted parameter is still a request from the caller.
      if (checker && node.kind === K.CallExpression) {
        http.calls.push({from, to: r.to, file, line, args: node.arguments.map(a => valueParts(ts, K, N, checker, a, encl, 0))});
      }
      s.in_repo++;
      calls.push({from, to: r.to, kind: jsx ? 'jsx' : 'call', file, line, precision: r.precision});
      continue;
    }
    if (r.outcome === 'unresolved' && callee.kind === K.PropertyAccessExpression && callee.expression.kind === K.ThisKeyword) {
      const owner = methodOwner(encl);
      const to = owner && thisAssigned.get(owner)?.get(N.keyName(callee.name));
      if (to) {
        s.in_repo++;
        calls.push({from, to, kind: 'call', file, line, precision: 'heuristic'});
        continue;
      }
    }
    if (r.outcome === 'external') { s.external++; continue; }
    if (r.outcome === 'not_extracted') { s.not_extracted++; continue; }
    s.unresolved++;
    const call = {from, target: {name: calleeName(K, N, callee), arity}, kind: 'unresolved', file, line};
    calls.push(call);
    if (checker && callee.kind === K.PropertyAccessExpression && callee.expression.kind !== K.ThisKeyword) {
      taint.pending.push({s, call, expr: callee.expression});
    }
  }

  if (topUsed) {
    const top = idx.top;
    functions.push({id: top.id, module: moduleId, name: TOP, arity: 0, min_arity: 0, kind: 'top', file, line: 1, clauses: [1],
      fingerprint: fingerprint(ts, tokens, starts, 0, sf.end)});
  }
}

// ---- LiveView hooks ---------------------------------------------------------

// hookCall records `new LiveSocket(url, Socket, {hooks: {...}})` registries
// and `this.pushEvent("name")` sends inside a hook object.
function hookCall({ts, K, N, checker, node, callee, encl, from, file, line, hooks, methodOwner}) {
  if (callee.kind === K.PropertyAccessExpression && ['pushEvent', 'pushEventTo'].includes(callee.name.text)) {
    const via = callee.name.text;
    const arg = node.arguments[via === 'pushEventTo' ? 1 : 0];
    if (!arg) return;
    const parts = valueParts(ts, K, N, checker, arg, encl, 0);
    // An empty name is no name: counted as dynamic.
    const text = parts.length && parts.every(p => p.lit !== undefined && !p.base) ? parts.map(p => p.lit).join('') : '';
    const event = text === '' ? null : text;
    const owner = encl ? methodOwner(encl) : null;
    hooks.pushes.push({from, owner: owner ? nodeKey(owner) : null, event, via, file, line});
  }
}

// The hook objects a `hooks` registry names: {Name: obj} and {obj} entries,
// followed through imports and `const A = B` aliases to an object literal.
function registerHooks(ts, K, N, checker, registry, hooks) {
  for (const p of registryEntries(ts, K, N, checker, registry, 0)) {
    let name, value;
    if (p.kind === 'export') {
      name = p.name;
      value = declObject(ts, K, N, checker, p.sym, 0);
    } else if (p.kind === 'assigned') {
      name = p.name;
      value = objectOf(ts, K, N, checker, p.expr, 0);
    } else if (p.kind === K.ShorthandPropertyAssignment) {
      name = p.name.text;
      let sym;
      try { sym = checker.getShorthandAssignmentValueSymbol(p); } catch { sym = undefined; }
      value = declObject(ts, K, N, checker, sym, 0);
    } else if (p.kind === K.PropertyAssignment) {
      name = N.keyName(p.name);
      value = objectOf(ts, K, N, checker, p.initializer, 0);
    }
    if (!name || !value) continue;
    const key = nodeKey(value);
    if (!hooks.registry.has(key)) hooks.registry.set(key, new Set());
    hooks.registry.get(key).add(name);
  }
}

// A node's place, the same in every program that reads its file (the
// registry and a hook may be read under different configs).
const nodeKey = (node) => `${node.getSourceFile().fileName}:${node.pos}`;

// The {name: node} entries a registry expression names: an object literal
// (with spreads followed), a variable holding one plus `X.Name = {...}`
// assignments in its file, or a namespace import's exports.
function registryEntries(ts, K, N, checker, expr, depth) {
  expr = N.skip(expr);
  if (depth > 4) return [];
  if (expr.kind === K.ObjectLiteralExpression) {
    return expr.properties.flatMap(p => p.kind === K.SpreadAssignment ? registryEntries(ts, K, N, checker, p.expression, depth + 1) : [p]);
  }
  if (expr.kind !== K.Identifier) return [];
  let sym;
  try { sym = checker.getSymbolAtLocation(expr); } catch { sym = undefined; }
  if (!sym) return [];
  let target = sym;
  if (sym.flags & ts.SymbolFlags.Alias) {
    try { target = checker.getAliasedSymbol(sym); } catch { return []; }
  }
  if (target && target.flags & ts.SymbolFlags.ValueModule) {
    // import * as Hooks from "./hooks": each export is an entry.
    let exports = [];
    try { exports = checker.getExportsOfModule(target); } catch { exports = []; }
    return exports.map(e => ({kind: 'export', name: e.escapedName === 'default' ? null : e.escapedName, sym: e})).filter(e => e.name);
  }
  const d = target?.valueDeclaration ?? target?.declarations?.[0];
  if (!d || d.kind !== K.VariableDeclaration || !d.initializer) return [];
  const own = registryEntries(ts, K, N, checker, d.initializer, depth + 1);
  // Hooks.Chart = {...} anywhere in the declaring file.
  const assigned = [];
  const work = [d.getSourceFile()];
  while (work.length) {
    const n = work.pop();
    if (n.kind === K.BinaryExpression && n.operatorToken.kind === K.EqualsToken) {
      const left = N.skip(n.left);
      if (left.kind === K.PropertyAccessExpression && left.expression.kind === K.Identifier) {
        let s2;
        try { s2 = checker.getSymbolAtLocation(left.expression); } catch { s2 = undefined; }
        // Compared by declaration: a JS expando (`Hooks.X = ...`) can give
        // the reference a merged symbol.
        if (s2 && (s2 === target || (s2.valueDeclaration ?? s2.declarations?.[0]) === d)) assigned.push({kind: 'assigned', name: left.name.text, expr: n.right});
      }
    }
    ts.forEachChild(n, (c) => { work.push(c); });
  }
  return own.concat(assigned);
}

function objectOf(ts, K, N, checker, expr, depth) {
  expr = N.skip(expr);
  if (expr.kind === K.ObjectLiteralExpression) return expr;
  if (expr.kind !== K.Identifier || depth > 5) return null;
  let sym;
  try { sym = checker.getSymbolAtLocation(expr); } catch { sym = undefined; }
  return declObject(ts, K, N, checker, sym, depth);
}

function declObject(ts, K, N, checker, sym, depth) {
  if (!sym) return null;
  if (sym.flags & ts.SymbolFlags.Alias) {
    try { sym = checker.getAliasedSymbol(sym); } catch { return null; }
  }
  const d = sym?.valueDeclaration ?? sym?.declarations?.[0];
  if (d && d.kind === K.VariableDeclaration && d.initializer) return objectOf(ts, K, N, checker, d.initializer, depth + 1);
  // export default {...}
  if (d && d.kind === K.ExportAssignment) return objectOf(ts, K, N, checker, d.expression, depth + 1);
  // A JS expando member (`Hooks.Chart = {...}`): its assignment's value.
  const assign = d && (d.kind === K.BinaryExpression ? d : d.parent?.kind === K.BinaryExpression ? d.parent : null);
  if (assign && assign.operatorToken.kind === K.EqualsToken) return objectOf(ts, K, N, checker, assign.right, depth + 1);
  return null;
}

// ---- fetch -----------------------------------------------------------------

const isFetch = (K, callee) => (callee.kind === K.Identifier && callee.text === 'fetch') ||
  (callee.kind === K.PropertyAccessExpression && callee.name.text === 'fetch' && callee.expression.kind === K.Identifier && ['window', 'globalThis', 'self'].includes(callee.expression.text));

// valueParts evaluates a string expression into parts: {lit} text, {param}
// for a parameter of fn (a wrapper's input), {dyn} for anything else; a
// constant's value carries {base} naming it (an absolute URL's origin).
function valueParts(ts, K, N, checker, expr, fn, depth) {
  expr = N.skip(expr);
  if (depth > 3) return [{dyn: 'p'}];
  switch (expr.kind) {
    case K.StringLiteral: case K.NoSubstitutionTemplateLiteral:
      return [{lit: expr.text}];
    case K.TemplateExpression: {
      const out = [{lit: expr.head.text}];
      for (const span of expr.templateSpans) {
        out.push(...valueParts(ts, K, N, checker, span.expression, fn, depth + 1).map(p => p.lit !== undefined && !p.base ? p : p.param !== undefined ? p : p.base ? p : {dyn: nameOf(K, span.expression)}));
        out.push({lit: span.literal.text});
      }
      return out;
    }
    case K.BinaryExpression:
      if (expr.operatorToken.kind === K.PlusToken) return [...valueParts(ts, K, N, checker, expr.left, fn, depth + 1), ...valueParts(ts, K, N, checker, expr.right, fn, depth + 1)];
      if (expr.operatorToken.kind === K.BarBarToken || expr.operatorToken.kind === K.QuestionQuestionToken) {
        // process.env.X || "http://localhost:7790": the default.
        const right = valueParts(ts, K, N, checker, expr.right, fn, depth + 1);
        if (right.every(p => p.lit !== undefined)) return right;
      }
      return [{dyn: 'p'}];
    case K.Identifier: {
      let sym;
      try { sym = checker.getSymbolAtLocation(expr); } catch { sym = undefined; }
      const d = sym?.valueDeclaration ?? sym?.declarations?.[0];
      if (d && d.kind === K.Parameter && fn && d.parent === fn && d.name.kind === K.Identifier) {
        const params = fn.parameters.filter(p => !(p.name.kind === K.Identifier && p.name.text === 'this'));
        return [{param: params.indexOf(d), name: d.name.text}];
      }
      if (d && d.kind === K.VariableDeclaration && d.initializer && (ts.getCombinedNodeFlags(d) & ts.NodeFlags.Const)) {
        const v = valueParts(ts, K, N, checker, d.initializer, null, depth + 1);
        if (v.every(p => p.lit !== undefined)) return [{lit: v.map(p => p.lit).join(''), base: expr.text}];
      }
      return [{dyn: expr.text}];
    }
    default:
      return [{dyn: nameOf(K, expr)}];
  }
}

// {method} in an object literal: the variable it names.
function shorthandParts(ts, K, N, checker, prop, fn) {
  let sym;
  try { sym = checker.getShorthandAssignmentValueSymbol(prop); } catch { sym = undefined; }
  const d = sym?.valueDeclaration ?? sym?.declarations?.[0];
  if (d && d.kind === K.Parameter && fn && d.parent === fn && d.name.kind === K.Identifier) {
    const params = fn.parameters.filter(q => !(q.name.kind === K.Identifier && q.name.text === 'this'));
    return [{param: params.indexOf(d), name: d.name.text}];
  }
  return valueParts(ts, K, N, checker, prop.name, fn, 0);
}

const nameOf = (K, e) => e.kind === K.Identifier ? e.text : e.kind === K.PropertyAccessExpression ? e.name.text : 'p';

// buildRef turns parts into an http_refs item: a path from "/", the query
// and fragment dropped, interpolations as :name. A URL with no host is
// same-origin; an absolute one names its base. Anything else is no ref.
function buildRef(parts, method, base0) {
  while (parts.length && parts[0].lit === '') parts = parts.slice(1);
  if (!parts.length || parts[0].lit === undefined) return null;
  // Unknown parts are \u0000name\u0001 until the host is split off: a
  // path that starts unknown (BASE + somethingComputed) is no ref.
  let text = '';
  let base = base0 ?? parts.find(p => p.base)?.base ?? '';
  for (const p of parts) text += p.lit !== undefined ? p.lit : `\u0000${p.name ?? p.dyn ?? 'p'}\u0001`;
  text = text.split(/[?#]/)[0];
  const abs = /^https?:\/\/[^/\u0000]*(\u0000[^\u0001]*\u0001[^/\u0000]*)*/.exec(text);
  let origin;
  if (abs) {
    if (!base) base = abs[0].replace(/\u0000([^\u0001]*)\u0001/g, '${$1}');
    text = text.slice(abs[0].length);
    if (text === '' && parts.some(p => p.lit === undefined)) return null;
    text ||= '/';
  } else if (text.startsWith('/') && !text.startsWith('//')) {
    origin = 'same';
    base = '';
  } else return null;
  if (!text.startsWith('/')) return null;
  text = text.replace(/\u0000([^\u0001]*)\u0001/g, ':$1');
  return {method, path: text, ...(origin ? {origin} : {}), ...(base ? {base} : {})};
}

// fetchRef records one fetch(url, init): a ref, or, when the URL comes from
// the enclosing function's parameter, that function as a wrapper.
function fetchRef({ts, K, N, checker, node, encl, from, file, line, http}) {
  const [urlArg, init] = node.arguments;
  if (!urlArg) return false;
  const parts = valueParts(ts, K, N, checker, urlArg, encl, 0);
  let method = 'GET';
  const obj = init && N.skip(init);
  if (obj) {
    method = '';
    if (obj.kind === K.ObjectLiteralExpression) {
      // In order: a spread or a computed key may set the method, so it
      // makes the method unknown until a later explicit one.
      method = 'GET';
      for (const p of obj.properties) {
        if (p.kind === K.SpreadAssignment || (p.name && p.name.kind === K.ComputedPropertyName)) { method = ''; continue; }
        const key = p.name && N.keyName(p.name);
        if (key !== 'method') continue;
        const v = p.kind === K.ShorthandPropertyAssignment ? shorthandParts(ts, K, N, checker, p, encl)
          : p.kind === K.PropertyAssignment ? valueParts(ts, K, N, checker, p.initializer, encl, 0) : [{dyn: 'p'}];
        method = v.length === 1 && v[0].lit !== undefined ? v[0].lit.toUpperCase() : v.length === 1 && v[0].param !== undefined ? {param: v[0].param} : '';
      }
    }
  }
  // A URL that uses a parameter is decided by the callers, not by its
  // shape: each in-repo call that reaches the function is a request from
  // the caller with its argument in place; with no such call, it is the
  // function's own request, the parameter written :name. No request is lost.
  const own = buildRef(parts.map(p => (p.param !== undefined ? {dyn: p.name} : p)), typeof method === 'object' ? '' : method);
  if (parts.some(p => p.param !== undefined) && encl) {
    const base = parts.find(p => p.base)?.base;
    if (!http.wrappers.has(from)) http.wrappers.set(from, []);
    http.wrappers.get(from).push({parts, method, file, line, own, ...(base ? {base} : {})});
    return false;
  }
  http.sites++;
  const ref = own;
  if (!ref) { http.unbuilt++; return false; }
  http.refs.push({from, ...ref, file, line});
  return true;
}

function calleeName(K, N, callee) {
  if (callee.kind === K.Identifier) return callee.text;
  if (callee.kind === K.PropertyAccessExpression) return N.keyName(callee.name) ?? '<dynamic>';
  if (callee.kind === K.SuperKeyword) return 'super';
  return '<dynamic>';
}

// resolve finds the function a call runs: the checker's resolved signature,
// else the callee's symbol. The outcome says why there is no edge.
function resolve({ts, K, N, node, callee, jsx, checker, relOf, extracted, repo}) {
  if (!checker) return {outcome: 'unresolved'};
  let decl;
  try {
    decl = checker.getResolvedSignature(node)?.declaration;
  } catch { decl = undefined; }
  const nameNode = callee.kind === K.PropertyAccessExpression ? callee.name : callee;
  let sym;
  try { sym = checker.getSymbolAtLocation(nameNode); } catch { sym = undefined; }
  let aliasFailed = false;
  if (sym && sym.flags & ts.SymbolFlags.Alias) {
    const target = checker.getAliasedSymbol(sym);
    if (!target || target === checker.getUnknownSymbol?.() || target.flags === 0 || !target.declarations?.length) aliasFailed = true;
    else sym = target;
  }
  if (!decl || decl.kind === K.JSDocSignature || !decl.parent) {
    // An import that doesn't resolve names a package that isn't installed
    // (external), or an in-repo file the plugin doesn't read (not extracted).
    if (aliasFailed) return {outcome: importedFromPackage(ts, K, callee, checker) ? 'external' : 'not_extracted'};
    decl = sym?.valueDeclaration ?? sym?.declarations?.[0];
  }
  if (!decl) {
    // An import from a package that isn't installed: a call into a dependency.
    if (aliasFailed || importedFromPackage(ts, K, callee, checker)) return {outcome: 'external'};
    // ns.f() through `import * as ns from "./unread"`: an in-repo file the
    // plugin doesn't read.
    if (callee.kind === K.PropertyAccessExpression && importedFromPath(ts, K, callee, checker)) return {outcome: 'not_extracted'};
    return {outcome: 'unresolved'};
  }
  // A variable or property holding a function: that function.
  let target = decl;
  if ((target.kind === K.VariableDeclaration || target.kind === K.PropertyAssignment || target.kind === K.PropertyDeclaration) && target.initializer) {
    target = N.skip(target.initializer);
  }
  // An overload signature: its implementation.
  if (N.isFn(target) && !target.body && sym?.declarations) {
    target = sym.declarations.find(d => N.isFn(d) && d.body) ?? target;
  }
  const dsf = target.getSourceFile();
  if (dsf.isDeclarationFile || dsf.hasNoDefaultLib) return {outcome: 'external'};
  let rel;
  try { rel = relOf(dsf); } catch { return {outcome: 'external'}; }
  if (!extracted.has(rel)) return {outcome: 'external'};
  if (isClassLike(K, target)) return {outcome: 'not_extracted'};
  if (!N.isFn(target) || !target.body) return {outcome: 'unresolved'};
  const idx = fnIndex(ts, N, dsf, `${repo}:${rel}`);
  const entry = idx.byNode.get(target);
  if (!entry) return {outcome: 'unresolved'};
  const direct = (callee.kind === K.Identifier && stableBinding(ts, K, checker, callee)) || jsx || N.isFn(callee) || isNamespaceMember(ts, K, callee, checker);
  return {to: entry.id, precision: direct ? 'precise' : 'heuristic', target};
}

// tainted: the value comes from a package (decision 16). Taint starts at an
// identifier imported from a bare specifier (or required) and follows
// await, parentheses and casts, member access, calls and `new` on a
// tainted value, a declaration's initializer, destructuring, the parameters
// of a callback handed to a tainted call (page.on('response', r => …)),
// and one level of parameters: every in-repo call of the function passes a
// tainted value there. A standard-library value never taints: items.forEach
// on a project array stays the project's.
//
// Results are cached per node (and per whether parameters may be followed)
// for the program; a node already being evaluated answers false only to
// break a real cycle (round 1, H1-M5).
function tainted(ts, K, checker, n, taint, depth, onStack, params) {
  if (!n || depth > 64) return false;
  const memo = params ? taint.memoParams : taint.memo;
  if (memo.has(n)) return memo.get(n);
  if (onStack.has(n)) return false;
  onStack.add(n);
  const result = evaluate();
  onStack.delete(n);
  memo.set(n, result);
  return result;

  function evaluate() {
  // require('pkg') with a bare specifier is a package value too.
  if (n.kind === K.CallExpression && n.expression.kind === K.Identifier && n.expression.text === 'require' &&
      n.arguments[0]?.kind === K.StringLiteral && !/^[./]/.test(n.arguments[0].text)) return true;
  switch (n.kind) {
    case K.ParenthesizedExpression: case K.AsExpression: case K.NonNullExpression:
    case K.AwaitExpression: case K.TypeAssertionExpression: case K.SatisfiesExpression:
    case K.PropertyAccessExpression: case K.ElementAccessExpression:
    case K.CallExpression: case K.NewExpression:
      return next(n.expression);
    case K.Identifier: {
      if (importedFromPackage(ts, K, n, checker)) return true;
      let sym;
      try { sym = checker.getSymbolAtLocation(n); } catch { return false; }
      const d = sym?.valueDeclaration ?? sym?.declarations?.[0];
      if (!d) return false;
      return declTainted(d);
    }
    default:
      return false;
  }
  }
  function next(x, p = params) { return tainted(ts, K, checker, x, taint, depth + 1, onStack, p); }
  function declTainted(d) {
    if (d.kind === K.VariableDeclaration) return !!d.initializer && next(d.initializer);
    if (d.kind === K.BindingElement) {
      let p = d.parent;
      while (p && (p.kind === K.ObjectBindingPattern || p.kind === K.ArrayBindingPattern || p.kind === K.BindingElement)) p = p.parent;
      if (p?.kind === K.VariableDeclaration) return !!p.initializer && next(p.initializer);
      if (p?.kind === K.Parameter) return paramTainted(p);
      return false;
    }
    if (d.kind === K.Parameter) return paramTainted(d);
    return false;
  }
  function paramTainted(param) {
    const fn = param.parent;
    const i = fn.parameters?.indexOf(param) ?? -1;
    if (i < 0) return false;
    // A callback handed to a method of a package *value*
    // (page.on('response', r => ...)), never to a function the package
    // exports itself (_.each(items, i => ...)), whose callbacks take the
    // caller's own values (round 1, H1-M6; decision 28).
    const call = fn.parent;
    if ((fn.kind === K.ArrowFunction || fn.kind === K.FunctionExpression) && call?.kind === K.CallExpression &&
        call.arguments.includes(fn) && call.expression.kind === K.PropertyAccessExpression) {
      const owner = call.expression.expression;
      // The package's own namespace, reached through members only
      // (_.each, _.fp.map, Rx.operators.map): its functions, not a value.
      let root = owner;
      while (root.kind === K.PropertyAccessExpression) root = root.expression;
      const exported = root.kind === K.Identifier && importedFromPackage(ts, K, root, checker);
      if (!exported && next(owner)) return true;
    }
    if (!params) return false;
    // One level: every in-repo call passes a package value here.
    const callers = taint.callers.get(fn) ?? [];
    return callers.length > 0 && callers.every(args => args[i] && next(args[i], false));
  }
}

const isClassLike = (K, n) => n.kind === K.ClassDeclaration || n.kind === K.ClassExpression;

// A name that always holds what it was declared with: a function or class
// declaration, an import or a const. A let, var or parameter may be
// reassigned (or defaulted), so a call through it is heuristic.
function stableBinding(ts, K, checker, id) {
  let sym;
  try { sym = checker.getSymbolAtLocation(id); } catch { return false; }
  if (!sym) return false;
  if (sym.flags & ts.SymbolFlags.Alias) return true;
  const d = sym.valueDeclaration ?? sym.declarations?.[0];
  if (!d) return false;
  // A function or class declaration, or a named function or class
  // expression calling itself by its own name.
  if (d.kind === K.FunctionDeclaration || d.kind === K.ClassDeclaration || d.kind === K.FunctionExpression || d.kind === K.ClassExpression) return true;
  if (d.kind === K.VariableDeclaration) return (ts.getCombinedNodeFlags(d) & ts.NodeFlags.Const) !== 0;
  return false;
}

function isNamespaceMember(ts, K, callee, checker) {
  if (callee.kind !== K.PropertyAccessExpression || callee.expression.kind !== K.Identifier) return false;
  // ns.f() through `import * as ns`: the module's own function.
  const s = checker.getSymbolAtLocation(callee.expression);
  return !!s && (s.flags & ts.SymbolFlags.Alias) !== 0 && (s.declarations ?? []).some(d => d.kind === K.NamespaceImport);
}

// importedFromPath: the callee's root identifier is a namespace import of a
// relative path whose module didn't resolve (missing, or hidden).
function importedFromPath(ts, K, callee, checker) {
  let n = callee;
  while (n.kind === K.PropertyAccessExpression) n = n.expression;
  if (n.kind !== K.Identifier) return false;
  let s;
  try { s = checker.getSymbolAtLocation(n); } catch { return false; }
  const d = s?.declarations?.[0];
  if (!d || d.kind !== K.NamespaceImport) return false;
  const spec = d.parent.parent.moduleSpecifier.text;
  if (!spec.startsWith('.') && !spec.startsWith('/')) return false;
  let target;
  try { target = checker.getAliasedSymbol(s); } catch { return true; }
  return !target || !target.declarations?.length;
}

// importedFromPackage: the callee's root identifier is imported from a bare
// specifier (not ./ or /), which the fence never resolves.
function importedFromPackage(ts, K, callee, checker) {
  let n = callee;
  while (n.kind === K.PropertyAccessExpression) n = n.expression;
  if (n.kind !== K.Identifier) return false;
  const s = checker.getSymbolAtLocation(n);
  const d = s?.declarations?.[0];
  if (!d) return false;
  let p = d;
  while (p && p.kind !== K.ImportDeclaration && p.kind !== K.SourceFile) p = p.parent;
  if (p?.kind === K.ImportDeclaration) {
    const spec = p.moduleSpecifier.text;
    return !spec.startsWith('.') && !spec.startsWith('/');
  }
  // const x = require("pkg")
  if (d.kind === K.VariableDeclaration && d.initializer?.kind === K.CallExpression && d.initializer.expression.kind === K.Identifier &&
      d.initializer.expression.text === 'require' && d.initializer.arguments[0]?.kind === K.StringLiteral) {
    const spec = d.initializer.arguments[0].text;
    return !spec.startsWith('.') && !spec.startsWith('/');
  }
  return false;
}

if (process.argv[1] && import.meta.url === pathToFileURL(fs.realpathSync(process.argv[1])).href) {
  let opts;
  try {
    opts = parseArgs(process.argv.slice(2));
  } catch (e) {
    process.stderr.write(`${e.message}\n${USAGE}\n`);
    process.exit(2);
  }
  let doc;
  try {
    doc = extract(opts);
  } catch (e) {
    process.stderr.write(`${e.message}\n`);
    process.exit(1);
  }
  process.stdout.write(JSON.stringify(doc));
}
