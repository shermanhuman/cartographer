
CREATE TABLE IF NOT EXISTS snapshots (
  id TEXT PRIMARY KEY,
  workspace TEXT NOT NULL,
  created_at TEXT NOT NULL,
  imported_at TEXT NOT NULL,
  manifest TEXT NOT NULL,
  -- main | head | published: what the build mapped (manifest source; '' is published)
  source TEXT NOT NULL DEFAULT 'published',
  map_id TEXT NOT NULL DEFAULT ''  -- bundle.MapID, '' when unknown
);

CREATE TABLE IF NOT EXISTS repos (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  url TEXT NOT NULL DEFAULT '',
  revision TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (snapshot_id, name)
);

CREATE TABLE IF NOT EXISTS files (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  repo TEXT NOT NULL,
  path TEXT NOT NULL,
  language TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (snapshot_id, repo, path)
);

CREATE TABLE IF NOT EXISTS modules (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
  repo TEXT NOT NULL,
  name TEXT NOT NULL,
  file TEXT NOT NULL,
  line INTEGER NOT NULL DEFAULT 0,
  language TEXT NOT NULL DEFAULT '',
  context TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (snapshot_id, id)
);

CREATE TABLE IF NOT EXISTS functions (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
  repo TEXT NOT NULL,
  module TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  arity INTEGER NOT NULL,
  min_arity INTEGER NOT NULL,
  kind TEXT NOT NULL DEFAULT '',
  file TEXT NOT NULL,
  line INTEGER NOT NULL DEFAULT 0,
  clauses TEXT NOT NULL DEFAULT '[]',
  fingerprint TEXT NOT NULL DEFAULT '',
  test INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (snapshot_id, id)
);

-- One row per call site. to_id is NULL when the target is unresolved.
CREATE TABLE IF NOT EXISTS calls (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  from_id TEXT NOT NULL,
  to_id TEXT,
  target TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '',
  repo TEXT NOT NULL,
  file TEXT NOT NULL DEFAULT '',
  line INTEGER NOT NULL DEFAULT 0,
  precision TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS calls_from ON calls(snapshot_id, from_id);
CREATE INDEX IF NOT EXISTS calls_to ON calls(snapshot_id, to_id);

CREATE TABLE IF NOT EXISTS db_tables (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
  schema_name TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'table', -- table | partitioned_table | view | materialized_view
  columns TEXT NOT NULL DEFAULT '[]',
  constraints TEXT NOT NULL DEFAULT '[]',
  indexes TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (snapshot_id, id)
);

CREATE TABLE IF NOT EXISTS foreign_keys (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  name TEXT NOT NULL DEFAULT '',
  from_table TEXT NOT NULL,
  from_columns TEXT NOT NULL,
  to_table TEXT NOT NULL,
  to_columns TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS table_refs (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  repo TEXT NOT NULL,
  table_id TEXT NOT NULL,
  file TEXT NOT NULL,
  line INTEGER NOT NULL,
  function_id TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS table_refs_table ON table_refs(snapshot_id, table_id);

CREATE TABLE IF NOT EXISTS contexts (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
  repo TEXT NOT NULL,
  name TEXT NOT NULL,
  PRIMARY KEY (snapshot_id, id)
);

CREATE TABLE IF NOT EXISTS docs (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  repo TEXT NOT NULL,
  path TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (snapshot_id, repo, path)
);

CREATE TABLE IF NOT EXISTS doc_refs (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  repo TEXT NOT NULL,
  doc_path TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  line INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS doc_refs_entity ON doc_refs(snapshot_id, entity_id);

CREATE TABLE IF NOT EXISTS annotations (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
  kind TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL,          -- the annotation JSON as authored
  status TEXT NOT NULL,        -- reviewed | needs_review | unreviewed
  steps TEXT NOT NULL DEFAULT '[]', -- [{id,status,reasons}]
  PRIMARY KEY (snapshot_id, id)
);

CREATE TABLE IF NOT EXISTS notes (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  repo TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL,
  file TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS vocab (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  word TEXT NOT NULL,
  count INTEGER NOT NULL,
  PRIMARY KEY (snapshot_id, word)
);

CREATE VIRTUAL TABLE IF NOT EXISTS entities USING fts5(
  snapshot_id UNINDEXED,
  id UNINDEXED,
  kind UNINDEXED,
  label,
  words,
  tokenize = 'unicode61'
);

-- Facts version 2.
CREATE TABLE IF NOT EXISTS routes (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  id TEXT NOT NULL,
  repo TEXT NOT NULL,
  method TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL,
  handler_id TEXT NOT NULL DEFAULT '',
  file TEXT NOT NULL DEFAULT '',
  line INTEGER NOT NULL DEFAULT 0,
  precision TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (snapshot_id, id)
);

CREATE TABLE IF NOT EXISTS http_refs (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  repo TEXT NOT NULL,
  from_id TEXT NOT NULL,
  method TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL,
  base TEXT NOT NULL DEFAULT '',
  file TEXT NOT NULL DEFAULT '',
  line INTEGER NOT NULL DEFAULT 0
);

-- function -> route edges the linker found for http_refs; never precise.
CREATE TABLE IF NOT EXISTS http_links (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  from_id TEXT NOT NULL,
  route_id TEXT NOT NULL,
  repo TEXT NOT NULL,          -- the caller's
  precision TEXT NOT NULL,     -- heuristic | candidate
  provenance TEXT NOT NULL DEFAULT '', -- why: config key or env value -> service
  file TEXT NOT NULL DEFAULT '',
  line INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS http_links_route ON http_links(snapshot_id, route_id);

CREATE TABLE IF NOT EXISTS config_urls (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  repo TEXT NOT NULL,
  key TEXT NOT NULL,
  value TEXT NOT NULL,
  file TEXT NOT NULL DEFAULT '',
  line INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS services (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  repo TEXT NOT NULL,          -- where the manifest lives
  kind TEXT NOT NULL,          -- deployment | service
  name TEXT NOT NULL,
  namespace TEXT NOT NULL DEFAULT '',
  image TEXT NOT NULL DEFAULT '',
  target_repo TEXT NOT NULL DEFAULT '', -- the repo a deployment's image builds from
  selector TEXT NOT NULL DEFAULT '{}',
  labels TEXT NOT NULL DEFAULT '{}',
  env TEXT NOT NULL DEFAULT '{}',
  file TEXT NOT NULL DEFAULT '',
  line INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS stats (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  repo TEXT NOT NULL,
  language TEXT NOT NULL,
  call_sites INTEGER NOT NULL DEFAULT 0,
  in_repo INTEGER NOT NULL DEFAULT 0,
  dispatch INTEGER NOT NULL DEFAULT 0,
  external INTEGER NOT NULL DEFAULT 0,
  unresolved INTEGER NOT NULL DEFAULT 0,
  refs INTEGER NOT NULL DEFAULT 0,
  not_extracted INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (snapshot_id, repo, language)
);

CREATE TABLE IF NOT EXISTS inventory (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
  repo TEXT NOT NULL,
  ext TEXT NOT NULL,
  files INTEGER NOT NULL,
  PRIMARY KEY (snapshot_id, repo, ext)
);

-- Every resolved edge, one shape: calls (call, dispatch, ...), route -> handler
-- ('route') and function -> route ('http'). Readers that follow edges use this.
-- +to_id keeps the planner on calls_from for a from_id lookup (to_id IS NOT
-- NULL would otherwise pick calls_to and scan the snapshot's calls).
CREATE VIEW IF NOT EXISTS edges AS
  SELECT snapshot_id, from_id, to_id, kind, precision, repo, file, line FROM calls WHERE +to_id IS NOT NULL
  UNION ALL
  SELECT snapshot_id, id, handler_id, 'route', precision, repo, file, line FROM routes WHERE handler_id != ''
  UNION ALL
  SELECT snapshot_id, from_id, route_id, 'http', precision, repo, file, line FROM http_links;

-- Every edge endpoint: functions and routes.
CREATE VIEW IF NOT EXISTS nodes AS
  SELECT snapshot_id, id, repo, module, name, file, line, 'function' AS kind FROM functions
  UNION ALL
  SELECT snapshot_id, id, repo, '', CASE method WHEN '' THEN path ELSE method || ' ' || path END, file, line, 'route' FROM routes;

