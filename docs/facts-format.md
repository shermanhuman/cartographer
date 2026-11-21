# Facts format

Version 2 is current; version 1 documents are still read. Everything below
applies to both unless a section says "version 2".

An extractor plugin is any executable that writes one JSON document to stdout
describing a repository (or a database schema). cartographer merges the
documents from every plugin in a workspace into one snapshot.

Schemas: [`schema/facts.v1.json`](../schema/facts.v1.json) and
[`schema/facts.v2.json`](../schema/facts.v2.json). Go types and the
validator: `internal/facts`. Check a plugin's output with
`cartographer validate-facts <file>`.

## Rules

- `facts_version` is `1` or `2`. Unknown fields are rejected, and a version 1
  document may not carry a version 2 section (`routes`, `http_refs`,
  `config_urls`, `services`, `stats`, `functions[].test`).
- A version 2 document needs snapshot format 2; a format 1 snapshot holding
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
| Go function | `<repo>:<package dir>.<Type>.<name>/<arity>` (no `.<Type>` for a function) | `api:internal/app.Server.run/1`, `api:internal/app.Keys/1` |
| Go route | `<repo>:route:<package dir>:<METHOD> <path>` (`*` for any method) | `api:route:internal/app:GET /items/{id}` |
| Rust module | `<repo>:<crate dir>::<module path>` | `svc:crates/shapes::geo` |
| Rust function | `<repo>:<crate dir>::<module path>.<Type>[impl <Trait>].<name>/<arity>` | `svc:crates/shapes::geo.Circle[impl Shape].area/0` |

The **package dir** of a Go package is its directory relative to the
repository root, with these forms:

- `./` for the module root package. Its module id is `<repo>:./`, and
  `./` already ends the directory part, so its functions read
  `<repo>:./<Type>.<name>/<arity>` (`<repo>:./config.Load/0`, apart from
  `config/`'s `<repo>:config.Load/0`) and its routes
  `<repo>:route:./:GET /healthz`. No directory starts with `./`.
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
local call); they never match elsewhere. Calls with `kind` `dynamic`, or any
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
writes interpolations as `:p1`, `:p2`, …, Go writes the parameter's name,
`/people/:id`), `base` (what supplies the base URL, when the extractor
knows), `file`, `line`.

The core links a reference to routes in **other** repositories after all
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
arguments). `status` reports coverage from these and from the manifest's
file inventory.

### Tests

`functions[].test` marks a test function where the file can't say so (a Rust
`#[test]`, or a function inside a `#[cfg(test)]` module). Files keep
`category: "test"` for whole test files.
