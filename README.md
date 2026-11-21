# cartographer

A system map for projects that span several repositories: a deterministic
call graph, the database schema, docs and human-reviewed journeys, in one
local SQLite file. Coding agents query it over MCP or the CLI with
token-budgeted answers; people browse it in a local web UI and share views
as Mermaid, PDF or SVG.

> Status: v0.2 in development. Design and checklist:
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

A project is a set of repositories mapped together. Each mapped repository
carries its own settings in a committed `.cartographer/` folder, and one
repository, the anchor, lists the members. The map itself is never
committed or published: every machine builds it locally from each member's
default branch and keeps it in its own SQLite store.

```text
my-app/.cartographer/repo.toml        my-worker/.cartographer/repo.toml
  project = "shop"                      project = "shop"
  members = [                           anchor = "my-org/my-app"
    {repo = "my-org/my-app"},           extract = ["go"]
    {repo = "my-org/my-worker"},        docs = ["README.md"]
  ]
  extract = ["elixir", "javascript"]
  docs = ["README.md", "docs/billing.md"]
my-app/.cartographer/annotations/     hand-written journeys and notes (optional)
```

`repo.toml` keys: `project`, `anchor` (another member's `owner/name`; omitted
in the anchor), `members` (anchor only), `extract` (`go`, `rust`, `elixir`,
`javascript`, `kubernetes`), `docs` (an audited list of Markdown to carry;
`false` for none), and where needed `source_roots`, `images` (container
images built from this repo, so Kubernetes Services resolve to it),
`[schema]` (how to build the database schema; `postgres = "managed"` runs a
throwaway PostgreSQL), `[rust] build_scripts` and `contexts_depth`.

```sh
cartographer project add ~/src/my-app     # or my-org/my-app; any member works
cartographer status                       # map id, freshness, coverage, your work
cartographer refresh                      # fetch and rebuild now
cartographer project init ~/src/my-worker # a starter .cartographer/repo.toml, plus the git commands to commit it
cartographer refresh --head --from ~/src/my-app --db /tmp/try.db   # try uncommitted settings in a scratch store
```

`project add` reads the member's settings from its fetched default branch,
follows the anchor's member list, finds each member's checkout (a
`[checkouts]` entry, a sibling, or a repository under `checkout_roots`,
default `~/github`, whose remote is that GitHub repository), shows what it
will trust and asks. Registering trusts the members' default branches:
rebuilds run the schema step, rust-analyzer's build scripts and the
commands members declare. A member with no checkout gets its `git clone`
command; nothing is cloned for you.

From then on the map keeps itself current. When you use cartographer (any
command, an MCP tool call, the web map loading its status) and the last fetch
is over 15 minutes old, a background rebuild fetches each member's default
branch into `refs/cartographer/<branch>` (your branches, files, `origin/*`
and tags are never touched), and rebuilds when anything moved. The query in
hand answers from the current map; the next one gets the new map. Each map
has an id, the hash of its contents: two machines showing the same id have
the same map. The header says what's mapped and how your checkout differs:

```text
map 7f3c9a1e · main as fetched 6 min ago · your checkout: fix/dialer, 2 ahead, 1 uncommitted, 3 files differ from the map
```

If anything fails (a member missing, settings that don't parse, an
extractor error), nothing is imported: the previous map keeps answering and
the header names the repository, the step and the log. A failed set of
inputs is retried after six hours, at once when a new commit arrives, or by
`cartographer refresh`. Turn automatic rebuilds off with `--no-rebuild`,
`CARTOGRAPHER_NO_REBUILD=1`, or `auto_rebuild = false` in the user config.

Building needs [mise](https://mise.jdx.dev): each extractor runs with the
language version its repository pins (`mise.toml`, `.mise.toml` or
`.tool-versions`), and the indexers (scip-go, rust-analyzer), PostgreSQL and
the Node used for JavaScript inventories are pinned by the cartographer
release. No other mise configuration is read. Reading a map needs only the
binary.

Extractors: Go and Rust through SCIP indexers (`cartographer extract scip`:
calls, interface and trait dispatch, `net/http` routes, HTTP requests), the
bundled Elixir extractor (AST: calls, Phoenix routes, `Req` requests),
JavaScript (inventory), Kubernetes manifests and URLs in YAML configuration
(built in), and the PostgreSQL schema. Calls between services are linked
from a request to the route it reaches, never as `precise`: see
[the facts format](docs/facts-format.md).

### Workspaces (0.1)

The 0.1 layout still works: a workspace directory holding
`cartographer.toml` that lists the repositories, registered with
`workspace add <dir>` and rebuilt from each checkout's `HEAD` with
`build --local --import`. A workspace with a registered project's name is
ignored while the project is registered.

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
local_workspaces = ["~/src/my-project-map"]   # written by workspace add <dir>; a hand-listed entry still needs it (it records the trust digest)
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
