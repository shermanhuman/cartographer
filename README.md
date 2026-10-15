# cartographer

A system map for multi-repository projects: a deterministic call graph,
database schema and reviewed explanations. Agents query it over MCP or the
CLI, and people browse it in a local web UI.

> Status: early development. The design and checklist live in the
> [cartographer v1 issue](https://github.com/shermanhuman/cartographer/issues/1).

- **Extractors are plugins.** Any executable that emits
  [facts v1](docs/facts-format.md) JSON. v1 ships Elixir (AST, precise),
  JavaScript inventory and PostgreSQL schema extractors.
- **One binary.** `cartographer` builds, publishes and pulls snapshots, serves
  the UI on `127.0.0.1`, answers queries (`callers`, `impact`, `table`, ...)
  and runs a stdio MCP server (`cartographer mcp`).
- **Token-budgeted output.** Results are ranked, compact and cut at a budget,
  so agents spend fewer tokens than grepping.

## License

MIT
