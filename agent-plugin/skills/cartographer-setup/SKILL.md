---
name: cartographer-setup
description: Set up cartographer for a project - workspace repo, cartographer.toml, extractor plugins, CI publishing and PR impact comments.
disable-model-invocation: true
---

# Set up cartographer

1. **Install:** `mise use -g github:shermanhuman/cartographer`.
2. **Workspace repository** (private if the code is private): it holds
   `cartographer.toml`, `annotations/` and the build workflow; its releases
   hold snapshots. `cartographer init` writes a commented template.
3. **Repos and plugins:** list each repository (`name`, `url`, sibling
   `path`, `ref = "origin/main"`) and its plugins. Bundled plugins
   (`{plugins}`): `extract-elixir` (precise), `extract-javascript`
   (inventory), `extract-postgres-schema`. Pin their runtimes in the
   workspace's `mise.toml`. Any executable printing
   [facts v1](https://github.com/shermanhuman/cartographer/blob/main/docs/facts-format.md)
   is a plugin; check output with `cartographer validate-facts`.
4. **Schema (optional):** `[schema]` with a `prepare` command that migrates a
   disposable database, the capture command, and `cache_key` paths
   (migrations) so it reruns only when they change.
5. **Build locally:** `cartographer build --import`, then
   `cartographer status` and `cartographer serve --open`.
6. **Publish from CI:** call
   `shermanhuman/cartographer/.github/workflows/build-snapshot.yml@v0.1.0`
   with `cartographer-version: v0.1.0` on `repository_dispatch`,
   `workflow_dispatch` and a weekly schedule, with a GitHub App (`app-id`,
   `app-private-key`) that can read every repository. Keep the workflow ref
   and `cartographer-version` equal; the workflow installs exactly that binary.
7. **Product repositories:** on push to `main`, send a `product-main-push`
   dispatch to the workspace with an App token scoped to the workspace
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
   `shermanhuman/cartographer/.github/workflows/pr-impact.yml@v0.1.0` with
   `cartographer-version: v0.1.0` (same release as the ref), `workspace`,
   `repo` and the App secrets for a sticky impact comment.
8. **Teammates:** `cartographer pull <owner/workspace>` once; queries and the
   MCP server then pull newer snapshots at most hourly.
9. **Annotations:** journeys cite entity ids; after reviewing one, run
   `cartographer review <id>` and commit the file. Changed or missing cited
   code marks it *needs review*.
