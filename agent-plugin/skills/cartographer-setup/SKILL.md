---
name: cartographer-setup
description: Set up cartographer for a project - .cartographer/repo.toml in each mapped repository, the anchor's member list, project add, local rebuilds from fetched main, annotations; the older workspace repo and CI publishing.
disable-model-invocation: true
---

# Set up cartographer

A project is the repositories one map covers. Each machine builds the map
locally from every member's default branch; only hand-written settings and
annotations are committed, in a `.cartographer/` folder in each member.

1. **Install:** `mise use -g github:shermanhuman/cartographer`. Building
   needs mise; reading a map needs only the binary. The `go`, `rust` and
   `elixir` extractors (and the schema step) run on the language version the
   repository itself pins in `mise.toml`, `.mise.toml` or `.tool-versions`
   (`elixir` needs `erlang` too); `javascript` and `kubernetes` need no pin.
2. **Settings in each member:** `.cartographer/repo.toml`. One member, the
   anchor, lists the project:

   ```toml
   project = "shop"
   members = [{ repo = "my-org/my-app" }, { repo = "my-org/my-worker" }]
   extract = ["elixir", "javascript"]
   docs = ["README.md"]          # an audited list; false for none
   ```

   A member entry may add `branch = "release"` to map that branch instead
   of the remote's default. `project add` still reads each member's
   settings from its default branch, so keep `.cartographer/` on both.

   Every other member names `project`, `anchor = "my-org/my-app"`,
   `extract` (`go`, `rust`, `elixir`, `javascript`, `kubernetes`) and
   `docs`. Optional keys: `source_roots`, `images` (container images built
   from this repo, so Kubernetes Services resolve to it), `[schema]` (the
   repo whose migrations define the database: `prepare`, `env`, `capture`,
   `cache_key`, `postgres = "managed"` for a throwaway PostgreSQL),
   `[rust] build_scripts`, `contexts_depth`. A worked `[schema]` and the
   annotation format are in `references/settings.md`. `cartographer project
   init <checkout> --project NAME [--anchor owner/name]` writes a starter
   file (`--anchor` for every member but the anchor) and prints the git
   commands to commit it; its `docs = []` carries every Markdown file until
   you replace it with the audited list.
3. **Try it before merging:** `cartographer refresh --head --from
   <member checkout> --db /tmp/try.db` builds from working copies (settings
   included) into a scratch store; query it with the same `--db`.
4. **Merge the settings:** commit each member's `.cartographer/` on a
   branch and open one pull request per repository; the user merges them.
   Registration reads the settings from each member's default branch, so
   register only once every member's settings are there.
5. **Register once per machine (the user's decision; only this skill runs
   it, other sessions give the user the command):** run `cartographer
   project add <member checkout or owner/name>` without `--yes`. It reads
   the settings from the fetched default branches, finds each member's
   checkout by its GitHub remote (under `checkout_roots`, default
   `~/github`, or `[checkouts]`; both live in the user config,
   `~/.config/cartographer/config.toml`), prints the members and what
   registering trusts (rebuilds run the members' schema step, Rust build
   scripts and declared commands from their fetched main), and asks. Show
   the user that list and trust text, and run it again with `--yes` only
   after they say yes. Missing checkouts get their `git clone` command;
   nothing is cloned.
6. **Fresh by itself:** on use, at most every 15 minutes, a background
   rebuild fetches each member into `refs/cartographer/<branch>` (branches,
   files, `origin/*` and tags untouched) and rebuilds when anything moved.
   The header shows the map id, "main as fetched N min ago" and how the
   current checkout differs. A failure keeps the previous map and names the
   repo, step and log; `cartographer refresh` retries now. Off switches:
   `--no-rebuild`, `CARTOGRAPHER_NO_REBUILD=1`, `auto_rebuild = false` in
   the user config.
7. **Annotations:** journeys and notes live in `.cartographer/annotations/`
   of the repo they describe and cite entity ids. After reviewing one, run
   `cartographer review <id>` in that checkout and commit the file; it
   counts once merged. Changed or missing cited code marks it *needs
   review*.
8. **Older layout (0.1):** a workspace repository with `cartographer.toml`
   (`cartographer init`), rebuilt from each checkout's HEAD with `build
   --local --import` and registered with `workspace add <dir>`, still
   works, as does CI publishing (`build-snapshot.yml`, `pr-impact.yml`, see
   the README). A workspace with a registered project's name is ignored
   while the project is registered.
