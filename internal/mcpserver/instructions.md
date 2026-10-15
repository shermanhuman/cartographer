cartographer serves a prebuilt map of this project's repositories: functions, calls, database tables, docs and reviewed journeys.

Use it before grep or reading files when the question is about structure:
- where is X defined → map_find / map_entity
- who calls X, what does X call → map_callers / map_callees
- what breaks if X changes → map_impact (grouped by repo, context, tests)
- how does A reach B → map_path
- a table's columns, keys and the code that mentions it → map_table
- a subsystem's modules, tables, docs → map_context
- freshness of the map → map_status

Refs may be ids (repo:Mod.fun/2), labels (Mod.fun/2, Mod.fun, public.users) or words. On a miss, map_find returns the map's own vocabulary: retry with those words, don't invent synonyms.

Results are ranked and budgeted. Every line has file:line: open only those lines. ⚠ marks a file changed since the snapshot, so read it before trusting the line. [precise] edges come from the AST; [heuristic] are name-matched; table mentions are [candidate] text matches. Unresolved, dynamic and external calls are not traced.

When you delegate code exploration to a subagent, tell it to use the cartographer map_* tools first.
