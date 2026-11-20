# extract-elixir

Writes one [facts v2](../../docs/facts-format.md) document for an Elixir
repository to stdout. It parses source with `Code.string_to_quoted/2` and
never compiles or runs project code. It needs Elixir 1.19+ and no
dependencies.

Parsing does not create atoms for names found in the repository. A static
atoms encoder maps a name to an existing atom when there is one and keeps
it as a string otherwise, so a large or hostile repository cannot exhaust
the VM's atom table. As a backstop, a file is skipped with an `atom_budget`
note if the atom table is ever 90% full.

```bash
mise exec -- elixir plugins/extract-elixir/extract.exs \
  --repo shop-app --root /path/to/snapshot --revision "$SHA" \
  [--source-roots lib,test] \
  [--schema-facts schema.json] [--ref-roots lib,priv/repo/migrations,scripts]
```

| Argument | Default | Meaning |
|---|---|---|
| `--repo` | required | Repository name; ids are `<repo>:<Module>` and `<repo>:<Module>.<fun>/<arity>`. |
| `--root` | required | Directory to read. No git operations; the core passes a `git archive` snapshot. |
| `--revision` | none | Copied to `revision`. |
| `--source-roots` | `lib` | Comma-separated directories (relative to root) whose `.ex`/`.exs` files are parsed. Paths are literal (no globbing); symlinks are never followed; dot-files and dot-directories are skipped. |
| `--schema-facts` | none | A facts document from `extract-postgres-schema`. Enables `table_refs`. |
| `--ref-roots` | `lib,priv/repo/migrations,scripts` | Directories scanned for table names (`.ex .exs .sql .sh`). |

Exit status 2 means bad arguments; nothing is written to stdout.

## Output

- `files`: every parsed or scanned file; category `source`, `test` (under
  `test/`), `migration` or `script`.
- `modules`: `namespace` is the module name split on `.`. `defprotocol P`
  is module `P`. `defimpl P, for: T` is module `P.T` (one per entry of a
  `for:` list, and `for:` defaults to the enclosing module), and its
  functions belong to it, not to the module it is nested in.
- `functions`: clauses merged per id. `min_arity` accounts for default
  arguments, `clauses` lists clause lines, and `fingerprint` is the sha256 of
  a structural rendering of each clause's AST with metadata stripped.
  Whitespace and comment edits keep it; body changes do not.
- `calls`: a `target` (`module`, `name`, `arity`) and never `to`. The core
  resolves targets. Kinds:

  | Kind | Meaning | `target.module` |
  |---|---|---|
  | `remote` | `Mod.fun(...)` after alias expansion; Erlang modules are `:mod` | yes |
  | `local` | unqualified call to a function defined in the caller's module | caller's module |
  | `import` | unqualified call matched to exactly one imported module in this repo, or to `Kernel`, using the effective import in its lexical scope as Elixir computes it: a later `import` of a module replaces the earlier one, `only:` lists and `:functions`/`:macros`/`:sigils` filter by name/arity and by function vs macro, and `except:` removes names from the earlier import (Kernel's implicit one included). Non-literal options block attribution | yes |
  | `local_or_import` | unqualified call the walker cannot attribute (imports from dependencies, `use`-injected functions) | omitted |
  | `capture` | `&Mod.fun/1` or `&fun/1` (attributed like local/import) | when known |
  | `delegate` | `defdelegate ... to:` | yes |
  | `apply` | `apply(Mod, :fun, [args])` with literal parts | yes |
  | `dynamic` | `var.fun()`, `fun.()`, `apply/2,3` with non-literal parts | omitted; never resolved |

  Operators, special forms, string-interpolation `to_string`, field access
  (`map.field`) and `quote` bodies are not calls. A pipe adds one to the
  arity.
- `table_refs` (with `--schema-facts`): lines naming a captured table,
  plus Ecto `schema "table"` declarations. `#` and `--` comment lines are
  skipped. In `.sql` and `.sh` files any word counts. In `.ex`/`.exs` files
  `@moduledoc`/`@doc`/`@typedoc`/`@shortdoc` (heredocs included) are
  skipped, and only code forms count: an atom (`:t`, `table(:t)`,
  `references(:t)`), a whole string (`"t"`, `from x in "t"`), or a word
  after a SQL keyword (`FROM`, `JOIN`, `INTO`, `UPDATE`, `TABLE`,
  `REFERENCES`). `function` is set when the line is inside a function
  clause. These are text matches (`candidate` precision).
- `routes`: Phoenix routes from router modules (`use Phoenix.Router` or
  `use <App>Web, :router`). Each `get`/`post`/`put`/`patch`/`delete`/
  `options`/`head` call with a literal path, inside any nesting of `scope`
  blocks (`scope "/p"`, `scope "/p", Alias`, `scope "/p", Alias, opts`,
  `scope path: ..., alias: ...`, and inside `if`/`live_session` blocks), is
  one route: `id` `<repo>:route:<METHOD> <path>`, `path` the joined scope
  and route paths (`:id` segments as written). `handler` is
  `<Controller>.<action>/2` when this document defines it (the controller
  is expanded through the lexical aliases and prefixed by the scope
  aliases, as Phoenix does), and then `precision` is `precise`. The same
  method and path a second time is left out with a `duplicate_route` note
  (the first in file order is kept). Not extracted, only counted in
  `stats`: a non-literal path, and any other router macro whose first
  argument is a `/` path (`live`, `forward`, `resources`, `match`,
  `live_dashboard`, ...).
- `http_refs`: requests through Req wrappers. A *carrier* is a function, in
  a module that calls `Req.request`/`get`/`post`/`put`/`patch`/`delete`/
  `head`, that passes one of its parameters on to such a call as the URL or
  as the options' `url:`, directly or through `Req.new`, `Req.merge`,
  `Req.Request.*`, `Keyword.put`/`merge`/..., `++`, variables and other
  carriers in the same module (a fixpoint; return values are not followed).
  A call to a carrier, anywhere in the repository, whose path argument is a
  string literal, an interpolated string, `"/path?" <> query`, or a keyword
  list whose `url:` is one of those, is a ref on the calling function:
  `path` with the query string dropped and each segment holding an
  interpolation replaced by `:p1`, `:p2`, ... (a path with parameters must
  also have a literal segment), `method` from the carrier's name (`get` ->
  `GET`), a literal `method:` option, the method every request it passes the
  path to agrees on (`fetch` -> `get` -> `GET`), or a literal verb argument
  (`request(:post, path)`), and `base` the carrier's module. A string that
  already names a path (`"/subscriptions/#{id}"`) starts a request where it
  is written, so a helper that builds one is where the ref is; so is a Req
  call with such a literal URL. Not extracted, only counted: a piped path
  (`path |> ApiClient.get()`), a variable or call (a path built in another
  function), a capture of a carrier, and a Req call whose URL comes from no
  parameter and no literal (`url:` reaching `Req.new` through a pipe in
  another function, a module attribute).
- `stats`: one entry, language `elixir`. `call_sites` counts `calls`;
  `in_repo` those whose target is a function in this document, `external`
  those naming a module outside it (Kernel and Erlang included),
  `unresolved` the rest (dynamic calls, unattributed unqualified calls, a
  function missing from a module that is here). `not_extracted` counts the
  routes and refs above that were not extracted. `dispatch` and `refs` are
  0.
- `notes`:

  | Kind | Meaning |
  |---|---|
  | `parse_error` | The file is listed but adds nothing else. |
  | `read_error` | A file or directory could not be read. |
  | `invalid_utf8` | The file had invalid UTF-8; it was parsed with U+FFFD in its place. Every string in the output is valid UTF-8. |
  | `atom_budget` | The file was not parsed because the atom table passed its budget. |
  | `dynamic_definition` | A definition the walker cannot name, e.g. `def unquote(name)()` or `defimpl P, for: some_var`. |
  | `duplicate_module` / `duplicate_definition` | The same module or function id in a second place. |
  | `symlink_skipped` | A symlinked file or directory, noted once and never followed. |
  | `empty_root` | A source or ref root has no matching files (or does not exist). |
  | `unknown_table` | An Ecto schema whose table is not in the schema facts. |
  | `duplicate_route` | A route with the method and path of an earlier one. |

Every array is sorted, and keys are written in a fixed order, so the same
input gives byte-identical output.

## Tests

```bash
mise exec -- elixir plugins/extract-elixir/test/extract_test.exs
mise exec -- go test ./internal/facts -run Contract
```

`test/fixture` is the v1 shop fixture; `test/web_fixture/{admin,rosearch,sites}`
are small copies of a Phoenix admin app's routers, a client app's Req
wrapper, and a shared library whose one request is built across functions.
