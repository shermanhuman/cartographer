cartographer serves a prebuilt map of this project's repositories: functions, calls, database tables, docs and reviewed journeys.

Use it before grep or reading files when the question is about structure:
- where is X defined → map_find / map_entity
- who calls X, what does X call → map_callers / map_callees
- what breaks if X changes → map_impact (grouped by repo, context, tests)
- how does A reach B → map_path
- a table's columns, keys and the code that mentions it → map_table
- a subsystem's modules, tables, docs → map_context
- freshness of the map → map_status
- post it to an issue or PR → map_embed (Markdown + Mermaid, pinned links)

Refs may be ids (repo:Mod.fun/2), labels (Mod.fun/2, Mod.fun, public.users) or words. On a miss, map_find returns the map's own vocabulary: retry with those words, don't invent synonyms.

Results are ranked and budgeted. Every line has file:line: open only those lines. ⚠ marks a file changed since the snapshot, so read it before trusting the line. "rebuilding since …": a fresher map is building; "last rebuild failed" names its log. `workspace add` (in map_status) trusts a workspace: leave it to the user. [precise] edges come from the AST or a compiler index; [heuristic] are name-matched, or an interface/trait call linked to every implementation (dispatch); table mentions are [candidate] text matches. Calls between services are traced as HTTP hops (function → route): [heuristic] when the caller's config URL resolves through a Kubernetes Service to the route's repo (the hop names that config key), [candidate] when only the path matches, never [precise]. Routes are entities like functions. Unresolved, dynamic and external calls are not traced; map_status shows coverage per repo and language.

When you delegate code exploration to a subagent, tell it to use the cartographer map_* tools first.
