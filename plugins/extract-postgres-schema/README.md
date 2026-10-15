# extract-postgres-schema

Captures table, column, constraint, index and foreign-key metadata from a
**disposable, loopback** PostgreSQL database. It writes one
[facts v1](../../docs/facts-format.md) schema document to stdout. It reads
catalog metadata only, never table data. Needs Elixir 1.19+ and `psql`, or
`docker` when `--container` is given.

```bash
mise exec -- elixir plugins/extract-postgres-schema/capture.exs \
  --database cartographer_shop [--host 127.0.0.1] [--port 5432] [--user postgres] \
  [--container NAME] [--db-prefix system_map_,shop_test] [--schemas public]
```

| Argument | Default | Meaning |
|---|---|---|
| `--database` | required | Database name. It must start with an allowed prefix. |
| `--host` / `--port` | `127.0.0.1` / `5432` | Must be loopback (`127.0.0.1`, `localhost`, `::1`). |
| `--user` | `postgres` | Role. Uses `--no-password`, so set up trust auth or `PGPASSWORD`/`.pgpass`. |
| `--container` | none | Run `psql` inside this Docker container (`docker exec`) and use its local socket. |
| `--db-prefix` | none | Comma-separated extra allowed prefixes; repeatable. `cartographer_` is always allowed. |
| `--schemas` | `public` | Comma-separated schemas to capture. |

## Guards

These run in order, and any failure stops the capture before the catalog
query runs.

1. The host is loopback.
2. The database name is a lowercase identifier starting with an allowed
   prefix. Blank or non-identifier prefixes are refused.
3. Every `psql` session runs with
   `PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=30000`.
   `SHOW default_transaction_read_only` must return `on` before the catalog
   query runs.
4. `server_version_num` must be PostgreSQL 15 or newer. 18 is the tested
   version, so other versions print a warning on stderr.

Exit status 2 means a bad argument or failed guard, and 1 means a failed
capture. stdout carries only the JSON document.

## Output

- `tables`: tables, partitioned tables, views and materialized views.
  `id` is `<schema>.<name>`.
  - `columns` carries `name`, `type` (`format_type`), `nullable` and
    `default`.
  - `constraints` holds `primary_key`, `unique`, `foreign_key`, `check`,
    `exclusion` and `trigger`. NOT NULL catalog constraints are left out
    because `nullable` already covers them.
  - `indexes` lists index names.
- `foreign_keys`: physical foreign keys between captured tables. A key that
  points outside the captured schemas becomes an
  `uncaptured_foreign_key_target` note.

Schema caching and provenance (which migrations produced the database) are
the core's job. This plugin records none.

## Tests

The tests need no database:

```bash
mise exec -- elixir plugins/extract-postgres-schema/test/capture_test.exs
```

`test/sample.json` is the conversion of `test/raw_catalog.json`. The tests
keep the two in sync, and the Go contract test validates it.

Manual capture against a real disposable database:

```bash
createdb -h 127.0.0.1 cartographer_shop && (cd /path/to/app && \
  MIX_ENV=test DATABASE_URL=ecto://postgres@127.0.0.1/cartographer_shop mix ecto.migrate)
mise exec -- elixir plugins/extract-postgres-schema/capture.exs --database cartographer_shop > schema.json
mise exec -- elixir plugins/extract-postgres-schema/capture.exs \
  --container my-postgres --database app_test --db-prefix app_test > schema.json
```
