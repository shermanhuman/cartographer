# cartographer

A system map for projects that span several repositories: a deterministic
call graph, the database schema, docs and human-reviewed journeys, in one
local SQLite file. Coding agents query it over MCP or the CLI with
token-budgeted answers; people browse it in a local web UI and share views
as Mermaid, PDF or SVG.

> Status: v0.1 in development. Design and checklist:
> [cartographer v1](https://github.com/shermanhuman/cartographer/issues/1).

## Why

Grepping across repositories to answer "who calls this?" or "what breaks if
I change it?" burns tokens and misses cross-repository links. cartographer
answers from a prebuilt map:

```text
$ cartographer impact Shop.Orders.total/1
map snapshot-20261015T120000Z · shop-app@aaaaaaa shop-worker@bbbbbbb
impact of FN shop-app:Shop.Orders.total/1  lib/shop/orders.ex:20 (depth ≤3): 4 callers in 2 repo(s) · 1 cross-repo · 1 tests
shop-app
  Shop.Billing
    d1 FN shop-app:Shop.Billing.charge/2  lib/shop/billing.ex:5  [precise]
  ShopWeb.OrderController
    d2 FN shop-app:ShopWeb.OrderController.create/2  lib/shop_web/order_controller.ex:8  [precise]  via shop-app:Shop.Billing.charge/2
shop-worker ⇄ cross-repo
  Worker.Nightly
    d2 FN shop-worker:Worker.Nightly.run/0  lib/worker/nightly.ex:3  [heuristic]  via shop-app:Shop.Billing.charge/2
tests
    d1 FN shop-app:Shop.OrdersTest.test_total/1  test/shop/orders_test.exs:5  [precise]
```

Every line carries `file:line` and how the edge was established
(`precise`, `heuristic`, `candidate`). Lines in files changed since the
snapshot are marked ⚠.

## Install

```sh
mise use -g github:shermanhuman/cartographer
```

Agents (Claude Code and Codex) get the MCP tools, a skill and a grep nudge
from the plugin:

```text
/plugin marketplace add shermanhuman/cartographer      # Claude Code
/plugin install cartographer@cartographer
codex plugin marketplace add shermanhuman/cartographer  # Codex
```

## Use a published map

```sh
cartographer pull my-org/my-project-map   # once; later pulls are automatic (hourly)
cartographer status                        # snapshot, freshness against your checkouts
cartographer find billing charge
cartographer callers Shop.Orders.total/1 --depth 2
cartographer impact public.orders          # code that mentions a table, and its callers
cartographer serve --open                  # the map in your browser (127.0.0.1)
cartographer embed Shop.Billing.charge/2   # Markdown + Mermaid for an issue
```

Private workspaces need a GitHub token: `GH_TOKEN`/`GITHUB_TOKEN`, else
`gh auth token` (a `gh` on PATH, or one installed globally with mise).

`cartographer help` lists every command and `cartographer <command> --help`
its flags. The queries (`status`, `find`, `show`, `callers`, `callees`,
`impact`, `path`, `table`, `context`) take `--budget` and `--format json`;
`find`, `callers`, `callees` and `impact` also page with `--limit`/`--offset`.

`pull` and `workspace add` record workspaces in `~/.config/cartographer/config.toml`
(`$CARTOGRAPHER_CONFIG`, or under `$XDG_CONFIG_HOME`). Freshness looks for each
repository's checkout next to the one you are in; list checkouts that live
elsewhere under `[checkouts]`:

```toml
workspaces = ["my-org/my-project-map"]

[checkouts]
shop-worker = "~/src/shop-worker"   # repository name = local path
```

## Map your project

A workspace repository holds `cartographer.toml` (the repositories and their
extractor plugins), `annotations/` (reviewed journeys) and a workflow that
publishes snapshots as releases:

```sh
cartographer init               # writes a commented cartographer.toml
cartographer build --import     # snapshot sibling checkouts, run plugins, import
cartographer publish .cartographer/snapshots/build-1234
```

Bundled extractor plugins: `extract-elixir` (AST, precise calls with default
arguments, pipes, captures, delegates), `extract-javascript` (inventory) and
`extract-postgres-schema` (catalog capture from a disposable database). Any
executable that prints [facts v1](docs/facts-format.md) JSON is a plugin.

In CI, call the reusable workflows: `build-snapshot.yml` from the workspace
repository (on dispatch and weekly), and `pr-impact.yml` from each product
repository's pull requests for a sticky impact comment. Pin both the
workflow ref (`@v0.1.0`) and its `cartographer-version` input to the same
release; the workflows install exactly that binary. In your own CI, keep
the token away from plugin code: `cartographer build --fetch-only` clones or
fetches the repositories without running plugins or schema `prepare`, so
only that step needs `GH_TOKEN`; build in a later step without it.

## How it works

```text
repos ──git archive──▶ plugins ──facts v1──▶ snapshot (tar.gz release)
                                                   │ pull
                                     SQLite store ◀┘  (calls resolved, contexts,
                                          │            doc links, annotation freshness)
             CLI · MCP server · web UI ◀──┘
```

## License

MIT
