# extract-javascript

Writes one [facts](../../docs/facts-format.md) document (version 3) to stdout,
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

Exit status 2 means bad arguments, and 1 any other failure (the compiler couldn't be loaded, for one).

## Reading

**The fence.** The compiler sees only the snapshot (with symlinks resolved, and refused when they lead out of it) and its own lib directory. A path that matches a file only by case (`./Foo` for `foo.js` on a case-insensitive disk) is refused, as Linux would. A `node_modules`, a `tsconfig.json` or any file above `--root` can't change the result, so the same commit gives the same facts on every machine.

**Configs.**
- Each file is read with the nearest `tsconfig.json` or `jsconfig.json` above it, inside the snapshot.
- Only module and JSX options are taken from it (`paths`, `baseUrl`, `jsx`, `module`, `moduleResolution`, `target`, `lib` and similar). Its `include`, `exclude` and `references` are ignored.
- A config that can't be read whole falls back to the defaults, with a `config_fallback` note. The usual cause is an `extends` naming a package that isn't installed.
- The defaults are: `allowJs`, bundler resolution, ESNext, JSX preserved, and no ambient `@types`.

**Dependencies.** No dependencies are installed. A call into a package import that doesn't resolve counts as `external`.

**Skipped files.**
- A file with a line over 10,000 characters is treated as generated (a vendored SDK) and skipped, with a `minified_skipped` note.
- A minified, too-deep or unparsable file doesn't exist for the compiler either, so a call into it is counted `not_extracted` (a package that isn't installed is `external`).
- A file whose expressions nest more than 250 deep is inventoried without resolution, with a `too_deep` note.

## Output

### `modules`

One per file. The `id` is `<repo>:<path>`. `language` is `javascript` or `typescript`.

### `functions`

Every function, arrow, method, accessor and constructor with a body.

- **`id`** is `<repo>:<path>:<name>/<arity>`, the same as the 0.1 Acorn plugin gave; `test/corpus` holds the expected ids. A repeated name in a file gets `#2`, `#3`, …
- **`name`** is qualified by its class or object binding (`Hooks.Chart.mounted`). Anonymous functions are `anonymous@<line>:<column>`.
- **`kind`** is `function`, `arrow` or `method`.
- **`arity`** counts the parameters; TypeScript's `this` parameter isn't one. `min_arity` leaves out defaults, rest parameters and optional (`?`) parameters.
- **`(top)/0`**, kind `top`, is the file's top-level code. It exists only when that code makes a call (a JSX element like `<div/>` isn't one). It is appended after every real function, so a real function named `(top)` keeps its id and the synthetic one becomes `(top)/0#2`.
- **`fingerprint`** is the sha256 of the function's tokens. Whitespace, comments and quote style don't change it.

### `calls`

A call is one of three kinds:

- **`call`, or `jsx` for `<Component />`, resolved.** It names the function the compiler resolved it to, in `to`. Precision:
  - `precise` for an identifier, an import, a namespace member or an IIFE;
  - `heuristic` for a member call through an inferred type.
  - Inside one object literal (a LiveView hook), `this.x()` also resolves to a function assigned as `this.x = …` in that object's methods, `heuristic`.
- **`unresolved`.** It has a `target` (name and arity) and no `to`. Examples: a call on an untyped value (`this.pushEvent`), on a parameter, or to something that isn't a function.
- **Not listed.** Calls into the standard library or a package, or into a file in the snapshot that isn't extracted (`external`), and constructions of a class with no constructor (`not_extracted`). These are counted in `stats` only.

### `http_refs`

These are `fetch` calls whose URL the plugin can read: a literal, a template
(`${id}` becomes `:id`) or a concatenation. The query and fragment are
dropped. The method comes from the init object, `GET` when there is none.

- **No host** (`/dial/me`): `origin: "same"`, linked to the caller's own
  repository.
- **Absolute URL:** `base` names its source, the constant it came from
  (`ADMIN = process.env.ADMIN || "http://…"`) or the origin.
- **A parameter that fills a whole segment after a literal `/`**
  (`/users/${id}`) **or sits in the query** (`"/search?q=" + term`) is an
  interpolation: the function makes the request itself.
- **Wrapper:** a function whose `fetch` path is carried by its parameter
  (`fetch(path)`, `"/api" + path`, `BASE + path`), like `api(method, path)`. Each
  call to it is a request from the caller, one level deep, with the method
  too when it is a parameter. A wrapper no call reaches counts as
  `not_extracted`.

A fetch with no path to link (`form.action`, `BASE + compute()`) is counted
in the `http` stats entry as `not_extracted`.

### `events`

Any `x.pushEvent("name", …)` or `x.pushEventTo(selector, "name", …)` is a
send. When it is inside a hook object (an object literal's method, or an
arrow within one), it carries that object's registry names.

- `hooks` lists every name the object is registered under in
  `new LiveSocket(url, Socket, {hooks: …})`. The registry may be an object
  literal (spreads followed), a variable holding one plus `Hooks.Name = {...}`
  assignments in its file, or a namespace import. Each entry is followed
  through imports, `export default` and `const A = B` aliases.
- `pushEventTo` has target `dynamic` and is never linked: it goes to
  whatever its selector matches.
- A name built at runtime is `dynamic`.
- The core links a send through the Elixir extractor's `hook_mounts`.

### `stats`

Counts per language: call sites, in-repo, external, unresolved and not
extracted. An `http` entry counts the fetch sites (`refs` with a path,
`not_extracted` without).

### `notes`

The note kinds are `parse_error`, `config_fallback`, `minified_skipped`, `too_deep` and `symlink_skipped`. Their messages hold repo-relative paths only.

## Tests

```bash
CARTOGRAPHER_TYPESCRIPT="$(mise where npm:typescript@6.0.3)" mise exec -- node --test plugins/extract-javascript/test/
```
