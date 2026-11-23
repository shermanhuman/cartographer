# Facts format

Version 3 is current; version 1 and 2 documents are still read. Everything
below applies to all three unless a section says "version 2" or "version 3".

An extractor plugin is any executable that writes one JSON document to stdout
describing a repository (or a database schema). cartographer merges the
documents from every plugin in a workspace into one snapshot.

Schemas: [`schema/facts.v1.json`](../schema/facts.v1.json),
[`schema/facts.v2.json`](../schema/facts.v2.json) and
[`schema/facts.v3.json`](../schema/facts.v3.json) (which reads all three
versions). Go types and the
validator: `internal/facts`. Check a plugin's output with
`cartographer validate-facts <file>`.

## Rules

- `facts_version` is `1`, `2` or `3`. `Decode` rejects unknown fields. A
  version 1 document may not carry a version 2 section (`routes`,
  `http_refs`, `config_urls`, `services`, `stats`, `functions[].test`), and
  a version 1 or 2 document may not carry a version 3 one (`events`,
  `event_handlers`, `hook_mounts`, `http_refs[].origin`).
- A version 3 document needs snapshot format 3, and a format 2 snapshot
  holding one is rejected. A version 2 document needs snapshot format 2; a format 1 snapshot holding
  one is rejected, and so is a format 1 manifest with format 2 fields.
- Ids are opaque strings. Module, function and table ids are unique across
  the whole workspace (every document in a snapshot), not just within one
  document; a duplicate fails the snapshot with both file names. Convention:
  `<repo>:<Module>` for modules and `<repo>:<Module>.<name>/<arity>` for
  functions, `<schema>.<table>` for tables; route ids share the function
  namespace (both are edge endpoints). Go and Rust ids follow the grammar
  in [Ids](#ids). Annotation ids are likewise unique across all annotation
  files.
- A repository document's `repo` must name a repository in the snapshot
  manifest.
- Paths are repository-relative with `/` separators. When `files` is present,
  every `file` must be listed in it.
- `functions[].min_arity` is the smallest arity the definition accepts
  (default arguments); the core resolves calls of any arity in
  `min_arity..arity`. Both satisfy `0 <= min_arity <= arity <= 255`
  (Erlang's maximum arity); documents outside that range are rejected.
  Extractors for languages without that limit clamp to 255.
- `functions[].fingerprint` is an opaque hash of the function's source.
  Annotations that cite a function go stale when its fingerprint changes.

## Ids

Ids are a compatibility surface: annotations and links cite them. Each
starts with `<repo>:`.

| Kind | Grammar | Example |
|---|---|---|
| Elixir module | `<repo>:<Module>` | `shop:Shop.Orders` |
| Elixir function | `<repo>:<Module>.<name>/<arity>` | `shop:Shop.Orders.create/2` |
| Elixir route | `<repo>:route:<router module>:<METHOD> <path>` | `shop:route:ShopWeb.Router:POST /orders` |
| Go module (a package) | `<repo>:<package dir>` | `api:internal/app` |
| Go function | `<repo>:<package dir>.<Type>.<name>/<arity>` (no `.<Type>` for a function); methods are in their package's module | `api:internal/app.Server.run/1`, `api:internal/app.Keys/1` |
| Go route | `<repo>:route:<package dir>:<METHOD> <path>` (`*` for any method) | `api:route:internal/app:GET /items/{id}` |
| Rust module (a module) | `<repo>:<crate dir>::<module path>` | `svc:crates/shapes::geo` |
| Rust module (a type, trait or impl) | `<repo>:<crate dir>::<module path>.<Type>[impl <Trait>]` | `svc:crates/shapes::geo.Circle`, `svc:crates/shapes.Shape`, `svc:crates/shapes::a.A[impl Service]` |
| Rust function | `<repo>:<crate dir>::<module path>.<Type>[impl <Trait>].<name>/<arity>`, in the module of its type, trait or impl | `svc:crates/shapes::geo.Circle[impl Shape].area/0` |
| Elixir template function | a colocated `name.html.heex` is its module's `render/1`; an `embed_templates` file is `<repo>:<Module>.<file name>/1` (the names Phoenix compiles) | `shop:ShopWeb.PageHTML.home/1` |
| JavaScript or TypeScript module | `<repo>:<path>` | `shop:assets/js/app.js` |
| JavaScript or TypeScript function | `<repo>:<path>:<name>/<arity>`: `name` qualified by its class or object binding, `anonymous@<line>:<column>` when it has none, `#2`, `#3`, … for a repeated name in a file | `shop:assets/js/app.js:Hooks.Chart.mounted/0`, `shop:assets/js/app.js:anonymous@4:23/1` |
| JavaScript or TypeScript top level | `<repo>:<path>:(top)/0`, the file's top-level code; listed after every real function, so a real function named `(top)` keeps its id and this one becomes `(top)/0#2` | `shop:assets/js/app.js:(top)/0` |

The **package dir** of a Go package is its directory relative to the
repository root, with these forms:

- `./` for the package at the repository root. A module nested in a
  subdirectory has its root package at that directory (`app_test`), like
  any other package. The root package's module id is `<repo>:./`, and its
  routes are `<repo>:route:./:GET /healthz`. No directory starts with `./`.
- Its function ids are the one decided exception to "module id, a dot,
  then the rest": `./` already ends the directory part, so they are
  `<repo>:./<name>/<arity>` and `<repo>:./<Type>.<name>/<arity>`
  (`<repo>:./main/0`, `<repo>:./config.Load/0`, apart from `config/`'s
  `<repo>:config.Load/0`), not `<repo>:./.main/0`. They are kept as they
  are.
- `<dir>[test]` for an external test package (`package p_test` beside
  `package p` in `<dir>`), and `./[test]` at the module root:
  `<repo>:app[test].helper/0`, `<repo>:./[test].TestRoot/1`. No package
  directory has `[` in it (the go tool rejects it in an import path), so
  these never meet a real `app_test/` package's `<repo>:app_test.helper/0`.
  An external test package is in its package's context.

The **crate dir** of a Rust crate is its package directory for the library
and `src/main.rs`, `./` for a package at the repository root
(`<repo>:./.top/0`, `<repo>:./::a.call_a/1`), else the target's root file
without `.rs` (`crates/shapes/tests/api`, `src/bin/tool`). `<Type>`,
`[impl <Trait>]` and the module path are left out when empty; lifetimes are
dropped from type arguments (`Prepared<'a>` is `Prepared`).

`[impl <Trait>]` uses the trait's short name, without its path
(`[impl Service]` for `impl a::Service for A`). rust-analyzer gives
`impl v1::Service for Both` and `impl v2::Service for Both` one symbol, so
when one type implements two same-named traits, one of the two impls'
same-named functions is lost. Calls dispatch by the trait itself, not its
short name, wherever the impl's header names a trait the index saw. An impl
written by a macro (no trait in its header) falls back to the short name,
and only while one trait in the repository has that name.

Go and Rust ids share one directory namespace and carry no language: a
directory holding both a Go package and a Cargo package isn't supported.

## Calls

A call site names its caller (`from`) and either its callee id (`to`) or a
`target` (`module` name, `name`, `arity`). The core resolves targets after all
documents are merged:

1. same repository, unique match → resolved with the producer's
   `call_precision` (default `heuristic`);
2. otherwise a unique match anywhere in the workspace → resolved as
   `heuristic` (the dependency between repositories is not proven);
3. otherwise the call stays unresolved and is reported as such.

Targets without a `module` resolve only against the caller's own module (a
local call); they never match elsewhere. `kind` `component` is a template's
component call (`<.button>`, `<Mod.card>`, `<.live_component module={X}>`,
which calls `X.render/1`); it resolves like any other call. Calls with `kind` `dynamic`, or any
kind starting with `js_`, are never resolved: name-only evidence without
lexical binding is not enough. Nor are calls with `kind` `unresolved`: the
producer says it found no target, so a same-named function in the caller's
module is not one. See each plugin's README for its call kinds.

## Precision

| Value | Meaning |
|---|---|
| `precise` | compiler or AST-grade resolution |
| `heuristic` | name-matched; may be wrong |
| `candidate` | textual co-occurrence only (e.g. `table_refs`) |

## Schema documents

A schema plugin omits `repo` and emits `tables` and `foreign_keys`.
`table_refs` (in repository documents) are text matches and always
`candidate`; each carries a 1-based `line`.

### Table kind

A table may carry an optional `kind`: `table`, `partitioned_table`, `view`
or `materialized_view`. It is omitted when the plugin cannot tell. Readers
treat a missing `kind` as `table`.

## Version 2 sections

### Calls through interfaces and traits

`calls[].kind` `dispatch` marks a call to an interface or trait method that
the extractor links to each implementation in the repository, `heuristic`:
which implementation runs isn't proven. A Rust call through a trait links to
the implementations of that trait only, matched by the trait's own symbol
(the path in each `impl` header), not its name: `a::Service` and
`b::Service` stay apart. A call on a concrete receiver keeps the
compiler's resolution, `precise`. A reference to a callable that isn't
invoked (a method value, a registration) is never a call; it is counted in
`stats.refs`.

### Routes

`routes[]` are HTTP endpoints the repository serves: `id`, `method` (upper
case; empty for any), `path` (as registered: `/leads/:id`, `/dial/{id}`),
`handler` (a function id in the same document, when the router names one),
`file`, `line`, and `precision` of the route → handler edge. Routes are
entities: they can be searched, shown and walked like functions.

### HTTP references and links

`http_refs[]` are requests a function builds to a path: `from` (the calling
function), `method`, `path` (each parameter segment starts with `:`: Elixir
writes interpolations as `:p1`, `:p2`, …, Go and JavaScript write the
parameter's name, `/people/:id`), `base` (what supplies the base URL, when
the extractor knows), `file`, `line`, and, in version 3, `origin`.

`origin` `"same"` marks a browser request with no host (`fetch("/dial/me")`):
it goes to the server that served the page. The core links it to a matching
route in the caller's **own** repository only, `heuristic`, provenance
`same-origin`. A same-origin reference has no `base`.

Every other reference links to routes in **other** repositories, after all
documents are merged. A link is never `precise`:

- `heuristic` when a URL in the caller's own configuration (its
  `config_urls`, or the literal env values of a Deployment built from the
  caller's repository) resolves through a Kubernetes Service to the route's
  repository, and the method and path match (the URL's path is tried as a
  base: `/api/v1` + `/leads`);
- otherwise `candidate` when exactly one other repository has a matching
  route;
- otherwise no link.

Path matching: a route's `:x` and `{x}` segments match any one segment;
`*x` (Phoenix, axum), `{*x}` and `{x...}` match the rest, which may be empty;
a Go pattern ending in `/` (other than `/` itself) matches its subtree; and
Go's `{$}` anchors the end; a reference's `:pN`
segment (any `:` segment of a reference) matches only a route parameter. The query string is ignored. Each
link records its provenance (`config.yaml cold.admin_url -> breakdown-admin`).

### Config URLs

`config_urls[]` are http(s) URLs in a repository's configuration: `key` (the
dotted path, `cold.admin_url`), `value`, `file`, `line`. The built-in
`cartographer extract urls` reads YAML files; Kubernetes manifests are left to
the Kubernetes extractor.

### Services

`services[]` come from manifests (`cartographer extract kubernetes`):
`kind` `deployment` (a Deployment or a StatefulSet, with `image`, pod `labels` and the literal `env` values that are http(s) URLs; nothing else from a manifest is kept) or
`service` (with its `selector`), plus `name`, `namespace` (empty means
`default`), `file`, `line`. A Deployment's image resolves to the repository
that builds it: the manifest's `images` map (image name → repo), else a repo
with the image's name. A Service reaches the repositories of the Deployments
its selector matches in its namespace. Service hosts are recognised as
`name`, `name.ns`, `name.ns.svc` and `name.ns.svc.cluster.local`, with or
without a port.

### Stats

`stats[]` has one entry per language the document covers: `call_sites` (each
invocation once, however many edges it gets), `in_repo`, `dispatch`,
`external` (into dependencies; counted, not stored), `unresolved`, `refs`
(references that aren't invocations) and `not_extracted` (routes or requests
the extractor saw but couldn't read: closures, computed paths, piped
arguments). Two entries aren't languages: `heex` counts template event sends
(`in_repo` linked, `unresolved` with no receiver, `not_extracted` dynamic),
and `http` counts JavaScript requests (`refs` with a path, `not_extracted`
with none). `status` reports coverage from these, from the event and HTTP
links, and from the manifest's file inventory.

### Tests

`functions[].test` marks a test function where the file can't say so (a Rust
`#[test]`, or a function inside a `#[cfg(test)]` module). Files keep
`category: "test"` for whole test files.

## Version 3 sections

### Events

`events[]` are places that send a LiveView event to the server: `from` (the
function that renders the template or holds the hook), `event` (or `dynamic`
`true` when the name is built at runtime), `via` (`phx-click`, `phx-submit`,
… for an attribute; `JS.push`; `pushEvent` or `pushEventTo` for a hook),
`target` (`myself` for `phx-target={@myself}`, `dynamic` for any other
target, omitted for none), `file`, `line`, `precision`, and either:

- `to`: the `handle_event` functions the producer resolved the send to
  (a template's send), or
- `hooks`: the names a hook object is registered under in
  `new LiveSocket(..., {hooks})` (a JavaScript `pushEvent`), for the core to
  resolve through `hook_mounts`.

A send with a literal name and no `to` is unlinked; a dynamic one is never
guessed. LiveView's own events (`lv:clear-flash`) aren't sends.

### Event handlers

`event_handlers[]` are the names a `handle_event` clause takes: `function`,
`event`, `source` (`head` for `def handle_event("save", ...)`, `guard` for
`when event in ~w(...)` or `== "..."`, `attribute` for `when event in
@events`), and `line` (the clause). A catch-all that hands the event to
another module's `handle_event` is `source` `delegate`, with that function as
`delegate` and no `event`.

### Hook mounts

`hook_mounts[]` are `phx-hook` attributes: `hook` (the name; a colocated
`.Name` is `<Module>.Name`), `from` (the rendering function), `receivers`
(the `handle_event` functions a `pushEvent` from that element reaches),
`file`, `line`.

### Event links

The core links a send to each of its `to` functions, and a hook's
`pushEvent` to each receiver of each mount, in the same repository, whose
hook is one of the send's `hooks` and that has a clause for the event (or a
delegating catch-all to a module that has one). Hook links are `heuristic`,
with the mount as provenance (`phx-hook DocumentSignature
(lib/admin_web/live/finance_live/certificate.ex:193)`). Links are edges of
kind `event`; `callers` names the events on each.

