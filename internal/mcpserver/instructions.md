cartographer serves a prebuilt map of this project's repositories: functions, calls, database tables, docs and reviewed journeys.

Use it before grep or reading files when the question is about structure:
- where is X defined → map_find / map_entity
- who calls X, what does X call → map_callers / map_callees
- what breaks if X changes → map_impact
- how does A reach B → map_path
- a table's columns, keys and the code that mentions it → map_table
- a subsystem's modules, tables, docs → map_context
- freshness of the map → map_status
- post it to an issue or PR → map_embed (Markdown + Mermaid, pinned links)

Refs may be ids (repo:Mod.fun/2), labels (Mod.fun/2, Mod.fun, public.users) or words. On a miss, map_find returns the map's own vocabulary: retry with those words, don't invent synonyms.

Results are ranked and budgeted. Every line has file:line: open only those lines. ⚠ marks a file changed relative to the map: read it before trusting the line. The header says when a fresher map is building or a rebuild failed, with its log. `project add` and `workspace add` trust code to run here: ask the user first. [precise] edges are statically resolved; [heuristic] are name-matched, or dispatch to every implementation; [candidate] are text matches. Service-to-service calls are HTTP hops to routes. Unresolved, dynamic and external calls aren't traced.

When you delegate code exploration to a subagent, tell it to use the cartographer map_* tools first.
