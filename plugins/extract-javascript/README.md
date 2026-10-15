# extract-javascript

Writes one [facts v1](../../docs/facts-format.md) document that inventories
a repository's JavaScript to stdout. [Acorn](https://github.com/acornjs/acorn)
(pinned in `package.json`) parses the code; project code is never run.

```bash
mise exec -- npm ci --ignore-scripts --prefix plugins/extract-javascript
mise exec -- node plugins/extract-javascript/extract.mjs \
  --repo shop-app --root /path/to/snapshot [--revision "$SHA"] [--source-roots assets/js]
```

| Argument | Default | Meaning |
|---|---|---|
| `--repo` | required | Repository name used in ids. |
| `--root` | required | Directory to read (no git operations). |
| `--revision` | none | Copied to `revision`. |
| `--source-roots` | `assets/js` | Comma-separated directories; `.js`, `.mjs`, `.cjs` files are parsed. `node_modules`, dot-directories and symlinks are skipped. |

Exit status 2 means bad arguments.

## Output

- `modules`: one per file. `id` is `<repo>:<path>` and `name` is the path.
- `functions`: every function, arrow and method, including callbacks.
  - `id` is `<repo>:<path>:<name>/<arity>`; a repeated name in a file gets `#2`, `#3`, ...
  - `name` is qualified by class or object binding (`Hooks.Chart.mounted`).
    Anonymous functions are `anonymous@<line>:<column>`.
  - `kind` is `function`, `arrow` or `method`. `arity` counts the parameters;
    `min_arity` leaves out defaults and rest parameters.
  - `fingerprint` is the sha256 of the function's token stream, so
    whitespace and comment edits don't change it.
- `calls`: `js_local` is a bare identifier call; its `target.module` is the
  file path. `js_dynamic` is a member or computed call; `target.module` is the
  receiver identifier when there is one. Both are name-only evidence: the core
  must not resolve them without lexical binding analysis. Producer
  `call_precision` is `heuristic`.
- `notes`: `parse_error` (module, then script parse both failed) and
  `symlink_skipped`.

TypeScript, JSX and JavaScript embedded in templates (HEEx) are not
extracted. Calls made at module top level have no calling function, so they
are left out.

## Tests

```bash
mise exec -- node --test plugins/extract-javascript/test/
```
