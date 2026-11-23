// extract-javascript: writes one facts document describing the JavaScript and
// TypeScript under a repository's source roots to stdout. The TypeScript 6
// compiler API parses the code and resolves calls; this plugin never executes
// project code and never reads outside --root. Usage: see README.md.
import crypto from 'node:crypto';
import fs from 'node:fs';
import {createRequire} from 'node:module';
import path from 'node:path';
import {pathToFileURL} from 'node:url';

export const VERSION = '0.2.0';
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
const MAX_DEPTH = 1000;
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
function fence(ts, rootReal, libDir) {
  const inside = (real) => real === rootReal || real.startsWith(rootReal + '/') || real.startsWith(libDir + '/');
  const real = (p) => {
    try { return fs.realpathSync.native(p); } catch { return null; }
  };
  const allowed = (p) => {
    const r = real(path.resolve(p));
    return r !== null && inside(r) ? r : null;
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
    readDirectory: (p, exts, excl, incl, depth) => ts.sys.readDirectory(p, exts, excl, incl, depth).filter(f => allowed(f) !== null),
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
// whole (an `extends` outside the fence) falls back to the defaults.
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
  const host0 = fence(ts, rootReal, libDir);
  const {files: listed, notes} = listFiles(root, sourceRoots);

  // Minified files are listed but not read.
  const files = [];
  for (const file of listed) {
    const text = fs.readFileSync(path.join(root, file), 'utf8');
    if (text.split('\n').some(l => l.length > MINIFIED_LINE)) {
      notes.push({kind: 'minified_skipped', file, message: `a line is over ${MINIFIED_LINE} characters; treated as generated code and not read`});
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
  const stat = (lang) => { if (!stats.has(lang)) stats.set(lang, {language: lang, call_sites: 0, in_repo: 0, dispatch: 0, external: 0, unresolved: 0, refs: 0, not_extracted: 0}); return stats.get(lang); };
  const extracted = new Set(files);

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
      walkFile({ts, K, N, sf, file, lang, repo, checker, relOf, extracted, modules, functions, calls, stat});
    }
  }

  for (const {file, sf} of deep) {
    walkFile({ts, K, N, sf, file, lang: languageOf(file), repo, checker: null, relOf: null, extracted, modules, functions, calls, stat});
  }

  const cmp = (...keys) => (a, b) => {
    for (const k of keys) { const x = k(a), y = k(b); if (x < y) return -1; if (x > y) return 1; }
    return 0;
  };
  const uniqueCalls = [...new Map(calls.map(c => [JSON.stringify(c), c])).values()];
  return {
    facts_version: 2,
    producer: {name: 'extract-javascript', version: VERSION, call_precision: 'heuristic'},
    repo,
    ...(revision ? {revision} : {}),
    files: listed.map(p => ({path: p, language: languageOf(p), category: /(^|\/)(test|tests|__tests__|e2e)\/|\.(test|spec)\.[cm]?[jt]sx?$/.test(p) ? 'test' : 'source'})),
    modules: modules.sort(cmp(m => m.id)),
    functions: functions.sort(cmp(f => f.id)),
    calls: uniqueCalls.sort(cmp(c => c.file, c => c.line, c => c.from, c => c.to ?? '', c => c.target?.module ?? '', c => c.target?.name ?? '', c => c.target?.arity ?? 0, c => c.kind)),
    notes: notes.sort(cmp(n => n.file ?? '', n => n.kind, n => n.message)),
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

function walkFile({ts, K, N, sf, file, lang, repo, checker, relOf, extracted, modules, functions, calls, stat}) {
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
    if (!encl) topUsed = true;
    const line = sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1;
    const jsx = node.kind === K.JsxOpeningElement || node.kind === K.JsxSelfClosingElement;
    const callee = N.skip(jsx ? node.tagName : node.expression);
    if (jsx && callee.kind === K.Identifier && /^[a-z]/.test(callee.text)) continue; // <div>: an element, not a call
    const arity = jsx ? 1 : Math.min(node.arguments?.length ?? 0, MAX_ARITY);
    s.call_sites++;
    const r = resolve({ts, K, N, node, callee, jsx, checker, relOf, extracted, repo});
    if (r.to) {
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
    calls.push({from, target: {name: calleeName(K, N, callee), arity}, kind: 'unresolved', file, line});
  }

  if (topUsed) {
    const top = idx.top;
    functions.push({id: top.id, module: moduleId, name: TOP, arity: 0, min_arity: 0, kind: 'top', file, line: 1, clauses: [1],
      fingerprint: fingerprint(ts, tokens, starts, 0, sf.end)});
  }
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
    // An import that doesn't resolve names a package that isn't installed.
    if (aliasFailed) return {outcome: 'external'};
    decl = sym?.valueDeclaration ?? sym?.declarations?.[0];
  }
  if (!decl) {
    // An import from a package that isn't installed: a call into a dependency.
    if (aliasFailed || importedFromPackage(ts, K, callee, checker)) return {outcome: 'external'};
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
  const direct = callee.kind === K.Identifier || jsx || N.isFn(callee) || isNamespaceMember(ts, K, callee, checker);
  return {to: entry.id, precision: direct ? 'precise' : 'heuristic'};
}

const isClassLike = (K, n) => n.kind === K.ClassDeclaration || n.kind === K.ClassExpression;

function isNamespaceMember(ts, K, callee, checker) {
  if (callee.kind !== K.PropertyAccessExpression || callee.expression.kind !== K.Identifier) return false;
  // ns.f() through `import * as ns`: the module's own function.
  const s = checker.getSymbolAtLocation(callee.expression);
  return !!s && (s.flags & ts.SymbolFlags.Alias) !== 0 && (s.declarations ?? []).some(d => d.kind === K.NamespaceImport);
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
