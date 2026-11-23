# Precision and limits

| Tag | Source | Use it for |
|---|---|---|
| `precise` | resolution inside one repository: the Elixir AST (`Mod.fun/arity`, default arguments, pipes, captures, `defdelegate`) or a Go/Rust compiler index (scip-go, rust-analyzer) on a concrete receiver | definite call edges |
| `heuristic` | an interface or trait call linked to every implementation in the repo (`dispatch`); an HTTP request linked to another repo's route because the caller's config URL resolves through a Kubernetes Service to it (`http`, with the config key as provenance); a call whose target module is defined in exactly one *other* repository | leads to verify |
| `candidate` | text: a source line naming a table; an HTTP request whose path matches a route in exactly one other repo, with no config evidence | where to look, not proof |

Routes (`GET /path` served by a Phoenix router or Go `net/http`) are
entities: `map_entity` shows a route's handler and its HTTP callers, and
`map_path` walks through them, naming each hop's kind and precision.

Not traced: dynamic dispatch in Elixir (`apply/3` with variables,
`var.fun()`), behaviour callbacks invoked by a framework, macro-generated
functions, calls into dependencies (counted as external), requests whose
path is built across functions or piped, JavaScript calls (functions are
listed, calls aren't resolved), TypeScript and templates.
`map_status` shows coverage per repository and language (files, call sites
resolved in the repo, functions linked), so you can tell how much is
missing.

For a registered project the map is each member's default branch as last
fetched, at the revisions in its header; your branch, commits ahead and
uncommitted edits show only as ⚠ marks on changed files. A `--head` map
(header: "your checkouts") is built from working copies, and a 0.1
workspace map from each checkout's HEAD.
