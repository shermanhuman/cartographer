# Troubleshooting

| Symptom | Fix |
|---|---|
| `map_*` tools missing | The plugin's MCP server couldn't start: install the binary (`mise use -g github:shermanhuman/cartographer`) and start a new session. |
| Map tools work in some repositories only | With only a project-level pin and mise shims on PATH, the plugin resolves the global latest `github:shermanhuman/cartographer` (installing it if needed); offline it falls back to the shim, which works only in repositories whose mise config is trusted. Pin it globally: `mise use -g github:shermanhuman/cartographer`. |
| "no map snapshot is available yet" | `cartographer pull <owner/name>` with the workspace repository that publishes this project's snapshots. |
| Answers come from another project | `cartographer status` shows the snapshot in use; the map is chosen by the repository you're in. |
| Pull fails with 401/404 | The workspace repository is private: `gh auth login` (or set `GH_TOKEN`) with access to it. |
| Many ⚠ marks | Your checkout is far from the snapshot; `cartographer pull`, or rebuild the workspace (`cartographer build --import`). |
| Codex `exec` refuses map tools ("approval policy is never") | Allow the server's tools in Codex config (per-server tool approval) or answer with grep in non-interactive runs. |
| Codex hooks don't fire | Codex runs plugin hooks only after you trust them in `/hooks`; `codex exec` skips them. The map tools still work. |
