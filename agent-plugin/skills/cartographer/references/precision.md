# Precision and limits

| Tag | Source | Use it for |
|---|---|---|
| `precise` | AST resolution inside one repository (e.g. Elixir `Mod.fun/arity`, default arguments, pipes, captures, `defdelegate`) | definite call edges |
| `heuristic` | name match: a call whose target module is defined in exactly one *other* repository, or a JavaScript name | leads to verify |
| `candidate` | text: a source line naming a table | where to look, not proof of a read or write |

Not traced: dynamic dispatch (`apply/3` with variables, `var.fun()`),
behaviour callbacks invoked by a framework, macro-generated functions,
calls into dependencies (Ecto, Phoenix, Oban, ...), HTTP between services,
TypeScript and templates. `map_callees` and `map_status` count unresolved
call sites so you can tell how much is missing.

The map is a snapshot of committed code at the revisions in its header.
Uncommitted and newer work shows only as ⚠ marks on changed files.
