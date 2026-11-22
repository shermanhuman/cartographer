# Troubleshooting

| Symptom | Fix |
|---|---|
| `map_*` tools missing | The plugin's MCP server couldn't start: install the binary (`mise use -g github:shermanhuman/cartographer`) and start a new session. |
| Map tools work in some repositories only | With only a project-level pin and mise shims on PATH, the plugin resolves the global latest `github:shermanhuman/cartographer` (installing it if needed); offline it falls back to the shim, which works only in repositories whose mise config is trusted. Pin it globally: `mise use -g github:shermanhuman/cartographer`. |
| "no map snapshot is available yet" / "no map of project … yet" | Register the project once: `cartographer project add <member checkout>`; the next command builds it in the background, or `cartographer refresh` now. A member without a checkout prints its `git clone` command. For a 0.1 workspace: `cartographer build --local --import` in it, then `cartographer workspace add <dir>`, or `cartographer pull <owner/name>` for a published one. |
| Answers come from another project | `cartographer status` shows the snapshot in use; the map is chosen by the repository you're in. |
| Pull fails with 401/404 | The workspace repository is private: `gh auth login` (or set `GH_TOKEN`) with access to it. |
| Many ⚠ marks, "N files differ from the map" | The map is each member's default branch as last fetched; ⚠ marks files your checkout has changed relative to it (a branch, commits ahead, uncommitted edits). It refreshes itself on use at most every 15 minutes; `cartographer refresh` fetches now. |
| "map not updated: … has no .cartographer/repo.toml" or "names another project" | A member doesn't line up with the anchor's list. The message offers `cartographer project init <checkout>`; commit the file it writes, or remove the repo from the anchor's `members`. The previous map keeps answering meanwhile. |
| "behind the fetched commits: <repo> failed at <step>" in the header | Read the log it names. Usually a language pin is missing from the repo's own mise config, or an extractor failed. Fix it on main; the same inputs are retried after 6 hours, at the next fetch that finds a new commit, or by `cartographer refresh`. A 0.1 workspace's "last rebuild failed" works as before: fix it and run `cartographer build --local --import`. |
| Codex `exec` refuses map tools ("approval policy is never") | Allow the server's tools in Codex config (per-server tool approval) or answer with grep in non-interactive runs. |
| Codex hooks don't fire | Codex runs plugin hooks only after you trust them in `/hooks`; `codex exec` skips them. The map tools still work. |
