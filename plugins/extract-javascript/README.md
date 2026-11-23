# extract-javascript

Writes one [facts](../../docs/facts-format.md) document (version 2) to stdout,
describing a repository's JavaScript and TypeScript. The TypeScript compiler
API parses the code and resolves its calls. The compiler is TypeScript 6.0.3,
pinned exactly; cartographer passes its mise install with `--typescript`.
Project code is never run, and nothing is installed.

```bash
mise exec -- node plugins/extract-javascript/extract.mjs \
  --repo shop-app --root /path/to/snapshot [--revision "$SHA"] [--source-roots assets/js] \
  --typescript "$(mise where npm:typescript@6.0.3)"
```

| Argument | Default | Meaning |
|---|---|---|
| `--repo` | required | Repository name used in ids. |
| `--root` | required | Directory to read (no git operations). |
| `--revision` | none | Copied to `revision`. |
| `--source-roots` | `assets/js` | Comma-separated directories. These files are read: `.js .mjs .cjs .jsx .ts .mts .cts .tsx` (declaration files aside). `node_modules`, dot-directories and symlinks are skipped. |
| `--typescript` | `$CARTOGRAPHER_TYPESCRIPT` | The `mise where npm:typescript@6.0.3` directory. It may use aube's layout or npm's. Any other compiler version is refused. |

Exit status 2 means bad arguments, and 1 means the compiler couldn't be loaded.

## Reading

**The fence.** The compiler sees only the snapshot (with symlinks resolved, and refused when they lead out of it) and its own lib directory. A `node_modules`, a `tsconfig.json` or any file above `--root` can't change the result, so the same commit gives the same facts on every machine.

**Configs.**
- Each file is read with the nearest `tsconfig.json` or `jsconfig.json` above it, inside the snapshot.
- Only module and JSX options are taken from it (`paths`, `baseUrl`, `jsx`, `module`, `moduleResolution`, `target`, `lib` and similar). Its `include`, `exclude` and `references` are ignored.
- A config that can't be read whole falls back to the defaults, with a `config_fallback` note. The usual cause is an `extends` naming a package that isn't installed.
- The defaults are: `allowJs`, bundler resolution, ESNext, JSX preserved, and no ambient `@types`.

**Dependencies.** No dependencies are installed. A call into a package import that doesn't resolve counts as `external`.

**Skipped files.**
- A file with a line over 10,000 characters is treated as generated (a vendored SDK) and skipped, with a `minified_skipped` note.
- A file whose expressions nest more than 1,000 deep is inventoried without resolution, with a `too_deep` note.

## Output

### `modules`

One per file. The `id` is `<repo>:<path>`. `language` is `javascript` or `typescript`.

### `functions`

Every function, arrow, method, accessor and constructor with a body.

- **`id`** is `<repo>:<path>:<name>/<arity>`, the same as the 0.1 Acorn plugin gave; `test/corpus` holds the expected ids. A repeated name in a file gets `#2`, `#3`, …
- **`name`** is qualified by its class or object binding (`Hooks.Chart.mounted`). Anonymous functions are `anonymous@<line>:<column>`.
- **`kind`** is `function`, `arrow` or `method`.
- **`arity`** counts the parameters; TypeScript's `this` parameter isn't one. `min_arity` leaves out defaults, rest parameters and optional (`?`) parameters.
- **`(top)/0`**, kind `top`, is the file's top-level code. It exists only when that code makes a call. It is appended after every real function, so a real function named `(top)` keeps its id and the synthetic one becomes `(top)/0#2`.
- **`fingerprint`** is the sha256 of the function's tokens. Whitespace, comments and quote style don't change it.

### `calls`

A call is one of three kinds:

- **`call`, or `jsx` for `<Component />`, resolved.** It names the function the compiler resolved it to, in `to`. Precision:
  - `precise` for an identifier, an import, a namespace member or an IIFE;
  - `heuristic` for a member call through an inferred type.
  - Inside one object literal (a LiveView hook), `this.x()` also resolves to a function assigned as `this.x = …` in that object's methods, `heuristic`.
- **`unresolved`.** It has a `target` (name and arity) and no `to`. Examples: a call on an untyped value (`this.pushEvent`), on a parameter, or to something that isn't a function.
- **Not listed.** Calls into the standard library or a package, or into a file in the snapshot that isn't extracted (`external`), and constructions of a class with no constructor (`not_extracted`). These are counted in `stats` only.

### `stats`

Counts per language: call sites, in-repo, external, unresolved and not extracted.

### `notes`

The note kinds are `parse_error`, `config_fallback`, `minified_skipped`, `too_deep` and `symlink_skipped`. Their messages hold repo-relative paths only.

## Tests

```bash
CARTOGRAPHER_TYPESCRIPT="$(mise where npm:typescript@6.0.3)" mise exec -- node --test plugins/extract-javascript/test/
```
