
CREATE TABLE IF NOT EXISTS snapshots (
  id TEXT PRIMARY KEY,
  workspace TEXT NOT NULL,
  created_at TEXT NOT NULL,
  imported_at TEXT NOT NULL,
  manifest TEXT NOT NULL
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
