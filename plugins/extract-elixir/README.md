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
  [--hex-cache DIR]
```

| Argument | Default | Meaning |
|---|---|---|
| `--repo` | required | Repository name; ids are `<repo>:<Module>` and `<repo>:<Module>.<fun>/<arity>`. |
| `--root` | required | Directory to read. No git operations; the core passes a `git archive` snapshot. |
| `--revision` | none | Copied to `revision`. |
| `--source-roots` | `lib` | Comma-separated directories (relative to root) whose `.ex`/`.exs` files are parsed. Paths are literal (no globbing); symlinks are never followed; dot-files and dot-directories are skipped. |
| `--schema-facts` | none | A facts document from `extract-postgres-schema`. Enables `table_refs`. |
| `--ref-roots` | `lib,priv/repo/migrations,scripts` | Directories scanned for table names (`.ex .exs .sql .sh`). |
| `--hex-cache` | `$HEX_HOME/packages/hexpm`, else `~/.hex/packages/hexpm` | Hex's package cache: the evidence for library calls (see below). Read only. |

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
  (`map.field`), `super(...)` and `quote` bodies are not calls. A pipe adds one to the
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
  - `use X, :kind` (and `use X`) applies X's helper quote, as Phoenix's
    `html_helpers` does: its `import` and `alias` forms, and the functions
    it defines (see library calls below). Up to three levels are followed,
    through `unquote(helper())` and nested `use`s. So `<.button>` resolves
    to the CoreComponents of its own app, and other calls through those
    imports resolve too.
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
- **Evidence for library calls** (0.6.0). A call counts as external only
  when the evidence defines it and the call reaches it; otherwise it is
  unresolved, never a guess. The evidence:
  - the Hex packages the nearest `mix.lock` pins, read from the Hex cache
    (`--hex-cache`, default `$HEX_HOME/packages/hexpm` or
    `~/.hex/packages/hexpm`). A tarball counts only when its SHA-256
    equals the lock's outer checksum; nothing is fetched, installed or
    evaluated, and the lock is parsed as data. A missing or mismatched
    package gives a `dependency_missing` note naming it (`file` is
    `name-version`). A build with no Hex cache (a CI runner, a fresh
    machine) has no dependency evidence: those calls are unresolved, with a
    note per package. Running `mix deps.get` once in any checkout fills
    the cache, and cartographer then rebuilds the map: the cache's state is
    part of its inputs digest;
  - the running Elixir and OTP: each module on the code path and its
    exports, and Elixir's own `use` quotes from its bundled sources (a
    runtime without them gives a `stdlib_sources_missing` note);
  - a library module's exports: its definitions (under a module-level
    `if` too), a top-level `for` over literal atoms, `defexception`,
    `defprotocol`, and what its own top-level `use`s generate;
  - what a library `use` applies (a superset reader): every definition in
    any quote of each library module the `use` reaches through `use`,
    `import`, `@before_compile` and module aliases passed as options
    (`adapter: Ecto.Adapters.Postgres`), and the exports of the modules
    those quotes import. A superset is sound here because the project's
    own definitions are checked first.
- **The rules**, in order:
  1. query vocabulary: a bare call whose name `Ecto.Query.API` or
     `WindowAPI` exports, inside a proven `Ecto.Query` macro's expression
     arguments (those after the first; for `dynamic`, its last), reached
     only through calls that are themselves API names (`subquery`,
     `exists`, `all`, `any` and `values` evaluate their arguments as code
     and break the chain), and with no `^` inside the macro covering it;
  2. the project's own definitions: the caller's module, a project module
     it imports, and what a project `use` put there;
  3. a bare call is external when an admitting import of a module that
     exports it, a project module a library `use` generated it into, or a
     library `use` in scope defines it. One candidate names the owner,
     several none, none leaves it unresolved. A hole in scope (functions
     the inventory can't list, an unexpandable project `use`, an
     unreadable import, a library with no evidence) leaves it unresolved;
  4. a remote call (or capture, or `apply/3`) is external when the
     module's evidence holds the function, including a function a library
     `use` generated into a project module (`Repo.all/1`, keeping its
     module). Component calls keep their kind.
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
  a string, a module attribute, or the literals of a local function it
  calls (`base_url(opts)`). A variable gives no base (0.6.0). It sets
  `base` to `scheme://host` and joins the URL's path;
  with several hosts the first in source order is used and no path joined.
  A function with no `base_url:` takes its module's host when every
  `base_url:` in the module is a literal naming one host. An interpolated
  string, a loopback URL or a variable sets none
  (an expression written in `base_url:` itself, such as
  `Application.get_env(:app, :api, "https://…")`, takes the literal
  default inside it), and a base joined
  into the URL itself (`url: base <> path`) isn't read: such a request
  keeps its module as its base.
- `stats`: one entry, language `elixir`. `call_sites` counts `calls`;
  `in_repo` those whose target is a function in this document, `external`
  the calls the evidence above proves, `unresolved` the rest (dynamic calls, unattributed
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
  | `dependency_missing` | A locked package gives no evidence (not in the Hex cache, a checksum that differs from the lock, another repository, no outer checksum) or a lock can't be read; `file` is `name-version` (or `mix.lock`). |
  | `stdlib_sources_missing` | The runtime has no bundled sources for an Elixir app (`ex_unit`, `elixir`): its `use` quotes give no evidence. |
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
