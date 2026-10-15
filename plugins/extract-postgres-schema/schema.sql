-- Catalog metadata only: no table data is read. __SCHEMAS__ is replaced by a
-- validated, quoted list of schema names (default 'public').
WITH tables AS (
 SELECT c.oid,c.relname,c.relkind,n.nspname
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname IN (__SCHEMAS__) AND c.relkind IN ('r','p','v','m')
), exported AS (
 SELECT jsonb_build_object(
  'id',t.nspname||'.'||t.relname,'name',t.relname,'schema',t.nspname,'kind',t.relkind,
  'columns',COALESCE((SELECT jsonb_agg(jsonb_build_object('name',a.attname,'type',format_type(a.atttypid,a.atttypmod),'nullable',NOT a.attnotnull,'default',pg_get_expr(d.adbin,d.adrelid)) ORDER BY a.attnum) FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=t.oid AND a.attnum>0 AND NOT a.attisdropped),'[]'),
  'constraints',COALESCE((SELECT jsonb_agg(jsonb_build_object('name',co.conname,'type',co.contype,'definition',pg_get_constraintdef(co.oid)) ORDER BY co.conname) FROM pg_constraint co WHERE co.conrelid=t.oid),'[]'),
  'indexes',COALESCE((SELECT jsonb_agg(jsonb_build_object('name',ci.relname,'definition',pg_get_indexdef(i.indexrelid)) ORDER BY ci.relname) FROM pg_index i JOIN pg_class ci ON ci.oid=i.indexrelid WHERE i.indrelid=t.oid),'[]')
 ) value FROM tables t
), fks AS (
 SELECT jsonb_build_object('name',co.conname,'from',ns.nspname||'.'||s.relname,'to',nt.nspname||'.'||target.relname,
 'columns',(SELECT jsonb_agg(a.attname ORDER BY k.ord) FROM unnest(co.conkey) WITH ORDINALITY k(attnum,ord) JOIN pg_attribute a ON a.attrelid=co.conrelid AND a.attnum=k.attnum),
 'target_columns',(SELECT jsonb_agg(a.attname ORDER BY k.ord) FROM unnest(co.confkey) WITH ORDINALITY k(attnum,ord) JOIN pg_attribute a ON a.attrelid=co.confrelid AND a.attnum=k.attnum)) value
 FROM pg_constraint co JOIN pg_class s ON s.oid=co.conrelid JOIN pg_namespace ns ON ns.oid=s.relnamespace JOIN pg_class target ON target.oid=co.confrelid JOIN pg_namespace nt ON nt.oid=target.relnamespace
 WHERE co.contype='f' AND ns.nspname IN (__SCHEMAS__)
)
SELECT jsonb_build_object('tables',COALESCE((SELECT jsonb_agg(value ORDER BY value->>'id') FROM exported),'[]'),'foreign_keys',COALESCE((SELECT jsonb_agg(value ORDER BY value->>'from',value->>'name') FROM fks),'[]'));
