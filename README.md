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
map snapshot-20261015T120000Z-43385991 · shop-app@aaaaaaa shop-worker@bbbbbbb
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

## Map your project

A workspace is a directory (usually its own repository) holding
`cartographer.toml`, which lists the repositories one map covers and their
extractor plugins, and `annotations/` (reviewed journeys). Clone it next to
the repositories it maps and build from what you have checked out:

```sh
cd ~/src && git clone git@github.com:my-org/my-project-map.git   # beside my-app, my-worker…
cd my-project-map
cartographer init                  # first time: writes a commented cartographer.toml
cartographer build --local --import   # every sibling checkout at its HEAD, plugins, import
cartographer workspace add .       # trust it: queries rebuild it when checkouts move on
cd ../my-app                       # query from a mapped repository
cartographer status                # snapshot, freshness against your checkouts
```

`build --local` needs no network and no token: each repository is taken at
the `HEAD` of its checkout (a sibling of the workspace, or a path under
`[checkouts]` in the user config). With `--import` it keeps the newest 10
snapshots per workspace in the store (as `import` and `pull` do).

`workspace add <dir>` registers the workspace once, so the map keeps itself
fresh. Registering is the trust step, like `mise trust`: a rebuild runs the
workspace's plugin commands, so cartographer records a digest of
`cartographer.toml` and rebuilds only workspaces you added, and only while
that file is unchanged. After it changes (a pull of the map repository),
`status` says `cartographer.toml changed since you registered it`: review
it and run `workspace add` again. A workspace merely found beside your
repository is never run (`status` suggests the command). From then on, when you query from a checkout
whose workspace checkout has commits past the snapshot, cartographer
rebuilds in the background (a detached `build --local --import`; the MCP
server builds in-process) and the header says `rebuilding since 10:42` until
the next query answers from the new snapshot. Rebuilds map the checkouts the
workspace lists, so a second clone or a worktree that is ahead is reported
(`status` names the checkout the workspace builds) but rebuilds nothing.

Uncommitted edits are never built; their files stay marked ⚠. A failed
rebuild shows `last rebuild failed 10:42: … (log: …)` and is retried after
five minutes. Turn rebuilds off with `--no-rebuild`,
`CARTOGRAPHER_NO_REBUILD=1`, or `auto_rebuild = false` in the user config.

Bundled extractor plugins: `extract-elixir` (AST, precise calls with default
arguments, pipes, captures, delegates), `extract-javascript` (inventory) and
`extract-postgres-schema` (catalog capture from a disposable database). Any
executable that prints [facts v1](docs/facts-format.md) JSON is a plugin.

## Query it

```sh
cartographer find billing charge
cartographer callers Shop.Orders.total/1 --depth 2
cartographer impact public.orders          # code that mentions a table, and its callers
cartographer serve --open                  # the map in your browser (127.0.0.1)
cartographer embed Shop.Billing.charge/2   # Markdown + Mermaid for an issue
```

`cartographer help` lists every command and `cartographer <command> --help`
its flags. The queries (`status`, `find`, `show`, `callers`, `callees`,
`impact`, `path`, `table`, `context`) take `--budget` and `--format json`;
`find`, `callers`, `callees` and `impact` also page with `--limit`/`--offset`.

The user config, `~/.config/cartographer/config.toml` (`$CARTOGRAPHER_CONFIG`,
or under `$XDG_CONFIG_HOME`), records local workspaces and published ones,
and where checkouts live when they aren't siblings of the repository you
are in:

```toml
local_workspaces = ["~/src/my-project-map"]   # workspace add <dir>: rebuilt automatically
workspaces = ["my-org/my-project-map"]        # workspace add <owner/name>, pull
# auto_rebuild = false

[checkouts]
shop-worker = "~/src/shop-worker"   # repository name = local path
```

A checkout whose `origin` is a fork or mirror (a URL no workspace lists)
maps only through its `[checkouts]` entry; otherwise cartographer never
guesses across projects by directory name.

## Publish snapshots from CI (optional)

Teammates without every checkout, and pull-request impact comments, use
snapshots published as releases of the workspace repository. Set
`publish = "my-org/my-project-map"` in `cartographer.toml` and call the
reusable workflows: `build-snapshot.yml` from the workspace repository (on
dispatch and weekly), and `pr-impact.yml` from each product repository's
pull requests for a sticky impact comment. Pin both the workflow ref
(`@v0.1.2`) and its `cartographer-version` input to the same release; the
workflows install exactly that binary.

The workflows need a token that can read every repository in the workspace.
Simplest: a `repo-token` secret holding a fine-grained personal access token
with *Contents: read* on each repository. Or a GitHub App (`app-id`,
`app-private-key`); its permissions apply to the whole installation, so
scope each token: the reusable workflows mint theirs with
`permission-contents: read`, and a product repository that dispatches a
rebuild on push mints its token with `repositories: <workspace repo>` and
`permission-contents: write`. To publish by hand:

```sh
cartographer build --fetch --import       # origin's revisions, needs GH_TOKEN for private repos
cartographer publish .cartographer/snapshots/build-1234
```

In your own CI, keep the token away from plugin code: `cartographer build
--fetch-only` clones or fetches the repositories without running plugins or
schema `prepare`, so only that step needs `GH_TOKEN`; build in a later step
without it.

## Use a published map

```sh
cartographer pull my-org/my-project-map   # once; later pulls are automatic (hourly)
cartographer status                        # snapshot, freshness against your checkouts
```

Private workspaces need a GitHub token: `GH_TOKEN`/`GITHUB_TOKEN`, else
`gh auth token` (a `gh` on PATH, or one installed globally with mise).
Pulled and locally built snapshots coexist: the newest one of the
workspace answers.

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
