# extract-elixir

Writes one [facts v3](../../docs/facts-format.md) document for an Elixir
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
  one route: `id` `<repo>:route:<Router module>:<METHOD> <path>`, `path` the joined scope
  and route paths (`:id` segments as written). `handler` is
  `<Controller>.<action>/2` when this document defines it (the controller
  is expanded through the lexical aliases and prefixed by the scope
  aliases, as Phoenix does), and then `precision` is `precise`. The same
  router, method and path a second time is left out with a `duplicate_route` note
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
  call with such a literal URL. A path piped into a carrier counts as its
  argument (`"/x/#{id}" |> ApiClient.get()`). Not extracted, only counted: a
  piped value that isn't such a path (`path |> ApiClient.get()`), a variable or call (a path built in another
  function), a capture of a carrier, and a Req call whose URL comes from no
  parameter and no literal (`url:` reaching `Req.new` through a pipe in
  another function, a module attribute).
- Templates. `~H` bodies, and `.heex` files under the source roots, are read
  with LiveView 1.2.15's tokenizer. The tokenizer is vendored in
  `heex_tokenizer.exs` (MIT, `LIVEVIEW-LICENSE.md`); nothing is compiled.
  - A template's owner is the function around its `~H`. A colocated
    `name.html.heex` beside `name.ex` is that module's `render/1`, and a file
    matched by `embed_templates "dir/*"` is `<Module>.<name>/1`. The last two
    are added as functions (kind `def`, `file` the template) unless the
    module defines them.
  - `<.fun>`, `<Mod.fun>` and `<.live_component module={X}>` (a call to
    `X.render/1`) are calls of kind `component`, attributed like any other
    call. Code in `{...}`, attribute values (event attributes included) and
    `<%= %>` is walked as code. An event attribute whose value is a call
    (`phx-click={show_modal("x")}`) counts as a dynamic send: what it returns
    isn't known.
  - `use X, :kind` (and `use X`) brings in the `import` and `alias` forms of
    X's helper quote, as Phoenix's `html_helpers` does. Only one level is
    followed, through `unquote(helper())` and a nested `use`. So `<.button>`
    resolves to the CoreComponents of its own app, and other calls through
    those imports resolve too.
  - A syntax error is a `heex_parse_error` note; the rest of the file still
    counts.
- `events`: LiveView sends.
  - **Sources.** An event attribute (`phx-click`, `phx-submit`, `phx-change`,
    `phx-window-keydown`, …) with a literal, a conditional's branch literals
    (never its condition's), or a `JS.push("name")`; a `JS.push` in any
    attribute; a colocated hook's `this.pushEvent("name")`. `lv:` events are
    LiveView's own and left out.
  - **`to`.** The `handle_event` functions that take the name:
    - `precise`: the LiveView's own template, or `phx-target={@myself}` in a
      LiveComponent;
    - `heuristic`: the LiveViews that render the sending function, found up
      the component call graph, or a component's `phx-click={@attr}` filled
      by a caller's literal attribute (one level). A clause that hands the
      event to `X.handle_event(event, …)` adds `X`'s function (one hop).
  - **Unlinked and dynamic.** A name no receiver takes is listed without
    `to`. A name built at runtime (`"filter_#{@n}"`, an assign no caller
    fills) is `dynamic`.
- `event_handlers`: the names each `handle_event/3` clause takes (`head`,
  `guard` `when e in ~w(...)`, `attribute` `when e in @events`), and
  `delegate` for a catch-all that calls another module's `handle_event`.
- `hook_mounts`: each `phx-hook` (a colocated `.Name` as `<Module>.Name`), with
  the `handle_event` functions of the LiveViews that render it as
  `receivers`. The core links a JavaScript hook's `pushEvent` through these.
- Library calls are `external` (kind `external`). An unqualified call is
  external when no project module defines that name and arity publicly,
  its own module doesn't define it, and a library is imported (admitting
  the name under `only:`/`except:`) or `use`d in scope. The target names
  the library only when it is the one candidate and the scope has no
  project `use` the walk can't expand and no project import that passes a
  library on. A call to a project module that `use`s a library and doesn't
  define the function (`Repo.all/1` from `use Ecto.Repo`) is external and
  keeps its module; so are its captures and `apply/3`, keeping their
  kinds. Component calls keep their kind and count as external.
- What a project `use` defines: the walk that applies a `use` (through
  Phoenix's `use AppWeb, :kind`, `unquote(helpers())` and nested uses,
  three levels deep) records each `def`, `defp`, `defmacro` and
  `defdelegate` it puts in the module, with the arities its defaults
  allow; those are the module's own. A definition it can't place (inside
  an `if` or `for`, a computed name, a chain past the depth limit) marks
  the module dynamic, as does a `dynamic_definition` note: bare calls in
  such a module, or in one importing it, stay unresolved rather than being
  named after a library. ExUnit.CaseTemplate `using` blocks are expanded
  the same way.
- Requests: a request's base comes from a `base_url:` in its own function:
  a string, a module attribute, the literals of a local function it calls
  (`base_url(opts)`), or a variable whose every possible value (each
  branch of a `case`/`cond`/`if`, a tuple position, `||` alternatives,
  `<>` of literals) is a literal URL, bound only by `=` at the top of the
  function. It sets `base` to `scheme://host` and joins the URL's path;
  with several hosts the first in source order is used and no path joined.
  A function with no `base_url:` takes its module's host when every
  `base_url:` in the module is a literal naming one host. An interpolated
  string, a loopback URL or anything computed sets none, and a base joined
  into the URL itself (`url: base <> path`) isn't read: such a request
  keeps its module as its base.
- `stats`: one entry, language `elixir`. `call_sites` counts `calls`;
  `in_repo` those whose target is a function in this document, `external`
  those naming a module outside it (Kernel and Erlang included) and the
  library calls above, `unresolved` the rest (dynamic calls, unattributed
  unqualified calls, a function missing from a module that is here).
  Coverage shows dynamic calls (`kind` `dynamic`) on their own and leaves
  them out of unresolved. `not_extracted` counts the
  routes and refs above that were not extracted; only the count is kept in
  the document, not which sites they were. `dispatch` and `refs` are 0. A
  second entry, `heex`, counts the event sends: `in_repo` linked,
  `unresolved` with no receiver, `not_extracted` dynamic.
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
  | `heex_parse_error` | A template the tokenizer rejected (the message has its line). |
  | `heex_unowned` | A `.heex` file no module renders (not colocated, no `embed_templates`). |

Every array is sorted, and keys are written in a fixed order, so the same
input gives byte-identical output.

## Tests

```bash
mise exec -- elixir plugins/extract-elixir/test/extract_test.exs
mise exec -- elixir plugins/extract-elixir/test/heex_test.exs
mise exec -- go test ./internal/facts -run Contract
```

`test/fixture` is the v1 shop fixture; `test/web_fixture/{admin,rosearch,sites}`
are small copies of a Phoenix admin app's routers, a client app's Req
wrapper, and a shared library whose one request is built across functions.
