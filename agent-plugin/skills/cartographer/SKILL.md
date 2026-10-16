---
name: cartographer
description: Use the cartographer system map (map_* MCP tools or the cartographer CLI) for where-defined, who-calls, what-breaks, schema and cross-repo questions before grepping, and to embed call graphs in GitHub issues/PRs.
---

# cartographer

A prebuilt, deterministic map of this project's repositories: functions,
resolved calls (cross-repo), database tables, docs and reviewed journeys.
Answer from the map first; read source only where the map points.

## Fast path

1. Structural question → call a `map_*` tool (or `cartographer <cmd>` in a
   shell). Don't grep first, don't read whole files.
2. Read only the `file:line` ranges the result cites.
3. A line marked **⚠** is in a file changed since the snapshot: read that file
   before trusting the line. The header names how far behind each repo is.
4. On a miss, `map_find` returns the map's own vocabulary. Retry with those
   words; never invent synonyms.

| Question | Tool | CLI |
|---|---|---|
| Where is X defined? | `map_find` / `map_entity` | `find`, `show` |
| Who calls X? What does X call? | `map_callers` / `map_callees` (`depth`) | `callers`, `callees` |
| What breaks if X changes? | `map_impact` (default depth 3) | `impact` |
| How does A reach B? | `map_path` | `path` |
| Table columns, keys, code mentioning it | `map_table` | `table` |
| A subsystem's modules, tables, docs | `map_context` | `context` |
| Is the map current? | `map_status` | `status` |
| Diagram for an issue/PR | `map_embed` | `embed`, `mermaid` |

Refs are ids (`repo:Mod.fun/2`), labels (`Mod.fun/2`, `Mod.fun`,
`public.users`) or search words. An ambiguous ref returns candidates: pick an
id and call again.

## Budget and paging

Results are ranked and cut at `budget` tokens (default 2000) with a
"+N more" note. Narrow the ref or `depth` before raising the budget; page
lists with `limit`/`offset`.

## Trust

- `[precise]`: resolved from the AST. `[heuristic]`: name-matched (e.g. a
  cross-repo call). Table mentions are `[candidate]` text matches, not proven
  reads/writes.
- Unresolved, dynamic and external calls are not traced: an empty caller
  list is not proof that nothing calls X through `apply`, behaviours or HTTP.
  Say so when it matters. Details: `references/precision.md`.

## Delegating

When you hand code exploration to a subagent, tell it: "use the cartographer
map_* tools first; read only the lines they cite."

## Sharing

`map_embed` returns ready-to-post Markdown (summary, Mermaid diagram of at
most 40 nodes, source links pinned to the snapshot). Post it with
`gh issue comment N --body-file FILE` (or `gh pr comment`). PDFs and SVGs:
`references/sharing.md`.

## When the map can't answer

No snapshot, not a mapped repo, or the tools are missing:
`references/troubleshooting.md`. Fall back to grep and say the map was
unavailable.
