---
name: cartographer-setup
description: Set up cartographer for a project - workspace repo, cartographer.toml, extractor plugins, local rebuilds, optional CI publishing and PR impact comments.
disable-model-invocation: true
---

# Set up cartographer

1. **Install:** `mise use -g github:shermanhuman/cartographer`.
2. **Workspace repository** (private if the code is private), cloned beside
   the product repositories: it holds `cartographer.toml`, `annotations/`
   and, if you publish, the build workflow. `cartographer init` writes a
   commented template.
3. **Repos and plugins:** list each repository (`name`, `url`, sibling
   `path`, `ref = "origin/main"` for CI) and its plugins. Bundled plugins
   (`{plugins}`): `extract-elixir` (precise), `extract-javascript`
   (inventory), `extract-postgres-schema`. Pin their runtimes in the
   workspace's `mise.toml`. Any executable printing
   [facts v1](https://github.com/shermanhuman/cartographer/blob/main/docs/facts-format.md)
   is a plugin; check output with `cartographer validate-facts`.
4. **Schema (optional):** `[schema]` with a `prepare` command that migrates a
   disposable database, the capture command, and `cache_key` paths
   (migrations) so it reruns only when they change.
5. **Build locally:** `cartographer build --local --import` in the
   workspace maps every sibling checkout at its HEAD (no fetch, no token),
   then `cartographer status` and `cartographer serve --open`. To keep it
   fresh, register it once: `cartographer workspace add <dir>`. That is the
   trust step (like `mise trust`): rebuilds run the workspace's plugin
   commands, so only registered workspaces are rebuilt. Then a query from a
   checkout whose workspace checkout has commits past the snapshot
   rebuilds in the background ("rebuilding since …" in the header).
   Uncommitted edits stay ⚠. Off switches: `--no-rebuild`,
   `CARTOGRAPHER_NO_REBUILD=1`, `auto_rebuild = false` in
   `~/.config/cartographer/config.toml`.
6. **Publish from CI (optional,** for teammates without the checkouts and
   for PR impact comments): set `publish = "owner/map-repo"`, then call
   `shermanhuman/cartographer/.github/workflows/build-snapshot.yml@v0.1.2`
   with `cartographer-version: v0.1.2` on `repository_dispatch`,
   `workflow_dispatch` and a weekly schedule. Credentials, simplest first:
   a `repo-token` secret holding a fine-grained PAT with contents read on
   each repository, or a GitHub App (`app-id`, `app-private-key`) that can
   read them all. Keep the workflow ref and `cartographer-version` equal;
   the workflow installs exactly that binary.
7. **Product repositories (with publishing):** on push to `main`, send a
   `product-main-push` dispatch to the workspace with a token that can
   write contents there: a PAT, or an App token scoped to the workspace
   repository (App permissions apply installation-wide; the reusable
   workflows mint theirs with `permission-contents: read`):

   ```yaml
   - id: app
     uses: actions/create-github-app-token@64ab729a6294000f5aa4a4bc0b73d1f32bc45510 # v3.3.0
     with:
       app-id: ${{ secrets.MAP_APP_ID }}
       private-key: ${{ secrets.MAP_APP_PRIVATE_KEY }}
       owner: ${{ github.repository_owner }}
       repositories: my-project-map
       permission-contents: write
   - env:
       GH_TOKEN: ${{ steps.app.outputs.token }}
     run: gh api repos/${{ github.repository_owner }}/my-project-map/dispatches -f event_type=product-main-push
   ```

   On pull requests, call
   `shermanhuman/cartographer/.github/workflows/pr-impact.yml@v0.1.2` with
   `cartographer-version: v0.1.2` (same release as the ref), `workspace`,
   `repo` and the `repo-token` or App secrets for a sticky impact comment.
8. **Teammates:** with the checkouts and the workspace cloned side by side,
   `cartographer build --local --import` and `cartographer workspace add
   <dir>` once; the map then rebuilds itself. Without them, `cartographer pull <owner/workspace>` once; queries
   and the MCP server then pull newer snapshots at most hourly. Both can
   coexist: the newest snapshot of the workspace answers.
9. **Annotations:** journeys cite entity ids; after reviewing one, run
   `cartographer review <id>` and commit the file. Changed or missing cited
   code marks it *needs review*.
