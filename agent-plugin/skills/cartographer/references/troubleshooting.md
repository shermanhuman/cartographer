# Troubleshooting

| Symptom | Fix |
|---|---|
| `map_*` tools missing | The plugin's MCP server couldn't start: install the binary (`mise use -g github:shermanhuman/cartographer`) and start a new session. |
| Map tools work in some repositories only | With only a project-level pin and mise shims on PATH, the plugin resolves the global latest `github:shermanhuman/cartographer` (installing it if needed); offline it falls back to the shim, which works only in repositories whose mise config is trusted. Pin it globally: `mise use -g github:shermanhuman/cartographer`. |
| "no map snapshot is available yet" | With the workspace cloned beside the repositories: `cartographer build --local --import` in it, then `cartographer workspace add <dir>` to keep it fresh. Otherwise `cartographer pull <owner/name>` with the workspace repository that publishes this project's snapshots. |
| Answers come from another project | `cartographer status` shows the snapshot in use; the map is chosen by the repository you're in. |
| Pull fails with 401/404 | The workspace repository is private: `gh auth login` (or set `GH_TOKEN`) with access to it. |
| Many ⚠ marks, "N commits behind" | Committed work past the snapshot rebuilds the map by itself once the workspace is registered (`cartographer workspace add <dir>`, the trust step; `status` suggests it for a workspace beside the repo): the header says "rebuilding since …" and a later call answers from the new snapshot. Rebuilds map the checkouts the workspace lists; from a second clone or a worktree, `status` names the checkout it builds. Otherwise `cartographer pull`, or `cartographer build --local --import` in the workspace. Uncommitted edits stay ⚠ until committed. |
| "cartographer.toml changed since you registered it" | The workspace's config changed (e.g. a pull of the map repository); rebuilds are paused until it is trusted again. Review the change, then `cartographer workspace add <dir>`. |
| "last rebuild failed …" in the header | Read the log it names (`<data dir>/rebuild/<workspace>-<hash>/log`); a plugin runtime is usually missing in the workspace's `mise.toml`. Fix and run `cartographer build --local --import`, or rebuild again by querying after 5 minutes (6 hours after "automatic rebuild stopped after 15m": build by hand). `--no-rebuild`, `CARTOGRAPHER_NO_REBUILD=1` or `auto_rebuild = false` in the user config turn rebuilds off. |
| Codex `exec` refuses map tools ("approval policy is never") | Allow the server's tools in Codex config (per-server tool approval) or answer with grep in non-interactive runs. |
| Codex hooks don't fire | Codex runs plugin hooks only after you trust them in `/hooks`; `codex exec` skips them. The map tools still work. |
