# Settings examples

## `[schema]`: the database from one repo's migrations

Only one member may declare `[schema]`: the repo whose migrations define
the database. With `postgres = "managed"`, cartographer starts a throwaway
PostgreSQL (pinned by the release) on a free local port, runs `prepare`
against it, runs `capture` to read the catalog, and removes it. The result
is cached: the step runs again only when a `cache_key` path, the commands,
`env` or the tool versions change.

```toml
[schema]
postgres = "managed"
# a shell command, run in the repo's archived copy with its own tool pins
prepare = "mix deps.get && mix ecto.create --repo MyApp.Repo && mix ecto.migrate --repo MyApp.Repo"
# the repo's config reads these; {pg_host} and {pg_port} are the throwaway server
env = { MIX_ENV = "test", TEST_DB_HOST = "{pg_host}", TEST_DB_PORT = "{pg_port}" }
# an argument list; {plugins} is cartographer's bundled plugin directory
capture = ["elixir", "{plugins}/extract-postgres-schema/capture.exs",
  "--host", "{pg_host}", "--port", "{pg_port}",
  "--database", "my_app_test", "--db-prefix", "my_app_"]
cache_key = ["priv/repo/migrations", "mix.lock", "config/config.exs", "config/test.exs"]
```

## Annotations: `.cartographer/annotations/*.json`

Hand-written journeys and notes that cite map entities by id. Ids are
unique across the project. `cartographer review <id>` stamps the cited
code's current fingerprints into the file; when that code changes, the
annotation reads *needs review*.

```json
{
  "annotations": [
    {
      "kind": "journey",
      "id": "cold-check",
      "title": "A cold campaign checks the app before sending",
      "summary": "The worker asks the app whether a contact may be cold-emailed.",
      "steps": [
        {"id": "enroll", "title": "Enroll contacts", "cites": ["my-worker:internal/campaigns.Server.enroll/2"]},
        {"id": "check", "title": "Admin decides", "cites": ["my-app:route:MyAppWeb.Router:POST /api/v1/cold/check"]}
      ]
    }
  ]
}
```
