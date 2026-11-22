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
   needs mise (every extractor runs on the language version its repository
   pins); reading a map needs only the binary.
2. **Settings in each member:** `.cartographer/repo.toml`. One member, the
   anchor, lists the project:

   ```toml
   project = "shop"
   members = [{ repo = "my-org/my-app" }, { repo = "my-org/my-worker" }]
   extract = ["elixir", "javascript"]
   docs = ["README.md"]          # an audited list; false for none
   ```

   Every other member names `project`, `anchor = "my-org/my-app"`,
   `extract` (`go`, `rust`, `elixir`, `javascript`, `kubernetes`) and
   `docs`. Optional keys: `source_roots`, `images` (container images built
   from this repo, so Kubernetes Services resolve to it), `[schema]` (the
   repo whose migrations define the database: `prepare`, `env`, `capture`,
   `cache_key`, `postgres = "managed"` for a throwaway PostgreSQL),
   `[rust] build_scripts`, `contexts_depth`. `cartographer project init
   <checkout>` writes a starter file and prints the git commands to commit
   it. Each extractor needs its language pinned in the repository's own
   `mise.toml`, `.mise.toml` or `.tool-versions`.
3. **Try it before merging:** `cartographer refresh --head --from
   <member checkout> --db /tmp/try.db` builds from working copies (settings
   included) into a scratch store; query it with the same `--db`.
4. **Register once per machine:** `cartographer project add <member
   checkout or owner/name>`. It reads the settings from the fetched default
   branch, finds each member's checkout by its GitHub remote (under
   `checkout_roots`, default `~/github`, or `[checkouts]` in the user
   config), and asks before trusting: rebuilds run the members' schema
   step, Rust build scripts and declared commands from their fetched main.
   Missing checkouts get their `git clone` command; nothing is cloned.
5. **Fresh by itself:** on use, at most every 15 minutes, a background
   rebuild fetches each member into `refs/cartographer/<branch>` (branches,
   files, `origin/*` and tags untouched) and rebuilds when anything moved.
   The header shows the map id, "main as fetched N min ago" and how the
   current checkout differs. A failure keeps the previous map and names the
   repo, step and log; `cartographer refresh` retries now. Off switches:
   `--no-rebuild`, `CARTOGRAPHER_NO_REBUILD=1`, `auto_rebuild = false`.
6. **Annotations:** journeys and notes live in `.cartographer/annotations/`
   of the repo they describe and cite entity ids. After reviewing one, run
   `cartographer review <id>` in that checkout and commit the file; it
   counts once merged. Changed or missing cited code marks it *needs
   review*.
7. **Older layout (0.1):** a workspace repository with `cartographer.toml`
   (`cartographer init`), rebuilt from each checkout's HEAD with `build
   --local --import` and registered with `workspace add <dir>`, still
   works, as does CI publishing (`build-snapshot.yml`, `pr-impact.yml`, see
   the README). A workspace with a registered project's name is ignored
   while the project is registered.
