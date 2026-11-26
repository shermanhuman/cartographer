# Precision and limits

| Tag | Source | Use it for |
|---|---|---|
| `precise` | resolution inside one repository: the Elixir AST (`Mod.fun/arity`, default arguments, pipes, captures, `defdelegate`), a Go/Rust compiler index (scip-go, rust-analyzer) on a concrete receiver, the TypeScript compiler for a JavaScript or TypeScript identifier or import, a template component call, or a LiveView event from the LiveView's own template (or `phx-target={@myself}`) to a clause that names it | definite call edges |
| `heuristic` | an interface or trait call linked to every implementation in the repo (`dispatch`); an HTTP request linked to another repo's route because the caller's config URL resolves through a Kubernetes Service to it (`http`, with the config key as provenance), or a browser request with no host linked to its own repo's route (`same-origin`); a JavaScript member call resolved through an inferred type; a LiveView event that reaches its LiveView through components, a delegating catch-all or a `phx-hook` (`event`, with the mount as provenance); a call whose target module is defined in exactly one *other* repository | leads to verify |
| `candidate` | text: a source line naming a table; an HTTP request whose path matches a route in exactly one other repo, with no config evidence | where to look, not proof |

Routes (`GET /path` served by a Phoenix router or Go `net/http`) are
entities: `map_entity` shows a route's handler and its HTTP callers, and
`map_path` walks through them, naming each hop's kind and precision.

Not traced: dynamic dispatch in Elixir (`apply/3` with variables,
`var.fun()`), behaviour callbacks invoked by a framework, macro-generated
functions (including handlers injected by `__using__` and `attach_hook`),
calls into dependencies and the functions a library's `use` generates
(`Repo.all/1`; counted as external only with evidence: the locked Hex
packages' verified source from the Hex cache, or the runtime's exports,
and unresolved otherwise; a `dependency_missing` note in `status` names a
package the cache lacks), requests to outside services (counted
as external on the `http` row), requests whose path is built across
functions or piped, JavaScript calls on untyped values (`this.el`), and
LiveView event names built at runtime (counted as dynamic, never guessed).
Elixir dynamic calls are counted as `dynamic` on their own, not as
unresolved; `show` and `callees` still give one "unresolved or external"
figure per function. A calls target that names a module for an external
call names the module the function arrives through, not where it is
defined.
`map_status` shows coverage per repository and language (files, call sites
resolved in the repo, functions linked), plus an `events` row (sends linked,
`handle_event` clauses reached) and an `http` row (requests linked to a
route), so you can tell how much is missing.

For a registered project the map is each member's default branch as last
fetched, at the revisions in its header; your branch, commits ahead and
uncommitted edits show only as ⚠ marks on changed files. A `--head` map
(header: "your checkouts") is built from working copies, and a 0.1
workspace map from each checkout's HEAD.
