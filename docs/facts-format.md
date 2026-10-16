# Facts format v1

An extractor plugin is any executable that writes one JSON document to stdout
describing a repository (or a database schema). cartographer merges the
documents from every plugin in a workspace into one snapshot.

Schema: [`schema/facts.v1.json`](../schema/facts.v1.json). Go types and the
validator: `internal/facts`. Check a plugin's output with
`cartographer validate-facts <file>`.

## Rules

- `facts_version` is `1`. Unknown fields are rejected.
- Ids are opaque strings. Module, function and table ids are unique across
  the whole workspace (every document in a snapshot), not just within one
  document; a duplicate fails the snapshot with both file names. Convention:
  `<repo>:<Module>` for modules and `<repo>:<Module>.<name>/<arity>` for
  functions, `<schema>.<table>` for tables. Annotation ids are likewise unique
  across all annotation files.
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
lexical binding is not enough. See each plugin's README for its call kinds.

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
