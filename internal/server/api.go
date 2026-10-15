package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/shermanhuman/cartographer/internal/query"
)

// The builders below produce the JSON shapes the UI (ported from the admin
// System Map) consumes: catalog entries, module/table "shards", the file
// manifest, the schema index and the overview. Proposal, evidence and cost
// fields that only the admin build produced are kept as empty values so the
// UI code paths stay valid.

type obj = map[string]any

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func decodeJSON[T any](raw string) T {
	var v T
	json.Unmarshal([]byte(raw), &v)
	return v
}

// iconFor maps a language to one of the bundled technology icons.
func iconFor(language string) string {
	l := strings.ToLower(language)
	switch {
	case strings.HasPrefix(l, "elixir"):
		return "elixir"
	case strings.HasPrefix(l, "javascript"), strings.HasPrefix(l, "typescript"):
		return "javascript"
	case strings.HasPrefix(l, "python"):
		return "python"
	case l == "shell", l == "bash", l == "sh":
		return "bash"
	case l == "dockerfile":
		return "docker"
	}
	return ""
}

type repoIndex map[string]query.Repo

func repos(ctx context.Context, q *query.Q) (repoIndex, error) {
	list, err := q.Repos(ctx)
	if err != nil {
		return nil, err
	}
	idx := repoIndex{}
	for _, r := range list {
		idx[r.Name] = r
	}
	return idx, nil
}

func (ri repoIndex) url(repo, file string, line int) string {
	return query.BlobURL(ri[repo], file, line)
}

func moduleLabel(name, file string) string {
	if name == "" {
		return path.Base(file)
	}
	parts := strings.Split(name, ".")
	return parts[len(parts)-1]
}

type moduleRow struct {
	ID, Repo, Name, File, Language, Context string
	Line, Count                             int
}

func modules(ctx context.Context, q *query.Q) ([]moduleRow, error) {
	rows, err := q.S.DB.QueryContext(ctx, `
	  SELECT m.id, m.repo, m.name, m.file, m.line, m.language, m.context,
	         (SELECT count(*) FROM functions f WHERE f.snapshot_id = m.snapshot_id AND f.module = m.id)
	    FROM modules m WHERE m.snapshot_id = ? ORDER BY m.id`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []moduleRow
	for rows.Next() {
		var m moduleRow
		if err := rows.Scan(&m.ID, &m.Repo, &m.Name, &m.File, &m.Line, &m.Language, &m.Context, &m.Count); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func moduleEntry(m moduleRow) obj {
	return obj{
		"id": m.ID, "label": moduleLabel(m.Name, m.File), "full": m.Name, "kind": "module", "repo": m.Repo,
		"file": m.File, "line": m.Line, "language": m.Language, "context": m.Context, "shard": m.ID, "count": m.Count,
		"icon": iconFor(m.Language), "changes": []string{}, "evidence": []string{}, "cost": 0,
	}
}

type tableRow struct {
	ID, Schema, Name string
	Columns          []struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Nullable bool   `json:"nullable"`
		Default  string `json:"default,omitempty"`
	}
	Weight int // FKs + code mentions, for ordering
}

func tables(ctx context.Context, q *query.Q) ([]tableRow, error) {
	rows, err := q.S.DB.QueryContext(ctx, `
	  SELECT t.id, t.schema_name, t.name, t.columns,
	         (SELECT count(*) FROM foreign_keys k WHERE k.snapshot_id = t.snapshot_id AND (k.from_table = t.id OR k.to_table = t.id)) +
	         (SELECT count(*) FROM table_refs r WHERE r.snapshot_id = t.snapshot_id AND r.table_id = t.id)
	    FROM db_tables t WHERE t.snapshot_id = ? ORDER BY t.id`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tableRow
	for rows.Next() {
		var t tableRow
		var cols string
		if err := rows.Scan(&t.ID, &t.Schema, &t.Name, &cols, &t.Weight); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(cols), &t.Columns)
		out = append(out, t)
	}
	return out, rows.Err()
}

func domainOf(t tableRow) string {
	if t.Schema != "" {
		return t.Schema
	}
	return "tables"
}

func tableEntry(t tableRow) obj {
	return obj{
		"id": t.ID, "label": t.Name, "full": t.ID, "kind": "table", "repo": "", "domain": domainOf(t),
		"shard": t.ID, "count": len(t.Columns), "icon": "postgresql", "changes": []string{}, "evidence": []string{}, "cost": 0,
	}
}

type annRow struct {
	ID, Kind, Title, Status string
	Body                    struct {
		Summary  string            `json:"summary"`
		Body     string            `json:"body"`
		Cites    []string          `json:"cites"`
		Reviewed map[string]string `json:"reviewed"`
		Steps    []struct {
			ID       string            `json:"id"`
			Title    string            `json:"title"`
			Body     string            `json:"body"`
			Cites    []string          `json:"cites"`
			Reviewed map[string]string `json:"reviewed"`
		} `json:"steps"`
	}
	Steps []struct {
		ID      string   `json:"id"`
		Status  string   `json:"status"`
		Reasons []string `json:"reasons"`
	}
}

func annotationRows(ctx context.Context, q *query.Q) ([]annRow, error) {
	rows, err := q.S.DB.QueryContext(ctx, `SELECT id, kind, title, body, status, steps FROM annotations WHERE snapshot_id = ? ORDER BY kind, id`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []annRow
	for rows.Next() {
		var a annRow
		var body, steps string
		if err := rows.Scan(&a.ID, &a.Kind, &a.Title, &body, &a.Status, &steps); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(body), &a.Body)
		json.Unmarshal([]byte(steps), &a.Steps)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (a annRow) cites() []string {
	seen := map[string]bool{}
	var out []string
	add := func(ids []string) {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	add(a.Body.Cites)
	for _, s := range a.Body.Steps {
		add(s.Cites)
	}
	return out
}

// annotations returns journeys, investigations and notes in the generic
// annotation shape, with review status from the store per step.
func annotations(ctx context.Context, q *query.Q) (any, error) {
	rows, err := annotationRows(ctx, q)
	if err != nil {
		return nil, err
	}
	out := []obj{}
	for _, a := range rows {
		status := map[string]obj{}
		for _, s := range a.Steps {
			status[s.ID] = obj{"status": s.Status, "reasons": nonNil(s.Reasons)}
		}
		steps := []obj{}
		for _, s := range a.Body.Steps {
			st := status[s.ID]
			if st == nil {
				st = obj{"status": "unreviewed", "reasons": []string{}}
			}
			steps = append(steps, obj{
				"id": s.ID, "title": s.Title, "body": s.Body, "cites": nonNil(s.Cites), "reviewed": orEmpty(s.Reviewed),
				"status": st["status"], "reasons": st["reasons"],
			})
		}
		reasons := []string{}
		if len(a.Body.Steps) == 0 {
			if st := status[a.ID]; st != nil {
				reasons = st["reasons"].([]string)
			}
		}
		out = append(out, obj{
			"kind": a.Kind, "id": a.ID, "title": a.Title, "summary": a.Body.Summary, "body": a.Body.Body,
			"cites": nonNil(a.Body.Cites), "reviewed": orEmpty(a.Body.Reviewed), "status": a.Status,
			"reasons": reasons, "steps": steps,
		})
	}
	return out, nil
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

type contextRow struct {
	ID, Repo, Name string
	Modules        []string
	Tables         []obj
}

func contextRows(ctx context.Context, q *query.Q) ([]contextRow, error) {
	rows, err := q.S.DB.QueryContext(ctx, `SELECT id, repo, name FROM contexts WHERE snapshot_id = ? ORDER BY repo, name`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	var out []contextRow
	idx := map[string]int{}
	for rows.Next() {
		var c contextRow
		rows.Scan(&c.ID, &c.Repo, &c.Name)
		idx[c.ID] = len(out)
		out = append(out, c)
	}
	rows.Close()
	rows, err = q.S.DB.QueryContext(ctx, `SELECT context, id FROM modules WHERE snapshot_id = ? ORDER BY id`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c, id string
		rows.Scan(&c, &id)
		if i, ok := idx[c]; ok {
			out[i].Modules = append(out[i].Modules, id)
		}
	}
	rows.Close()
	rows, err = q.S.DB.QueryContext(ctx, `
	  SELECT m.context, r.table_id, count(*) FROM table_refs r
	    JOIN functions f ON f.snapshot_id = r.snapshot_id AND f.id = r.function_id
	    JOIN modules m ON m.snapshot_id = f.snapshot_id AND m.id = f.module
	   WHERE r.snapshot_id = ? GROUP BY 1, 2 ORDER BY 1, 3 DESC, 2`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c, t string
		var n int
		rows.Scan(&c, &t, &n)
		if i, ok := idx[c]; ok {
			out[i].Tables = append(out[i].Tables, obj{"id": t, "count": n})
		}
	}
	rows.Close()
	return out, nil
}

// catalog lists every browsable entity: modules, functions, tables,
// repository "services", contexts and annotations.
func catalog(ctx context.Context, q *query.Q) (any, error) {
	mods, err := modules(ctx, q)
	if err != nil {
		return nil, err
	}
	out := []obj{}
	known := map[string]bool{}
	for _, m := range mods {
		out = append(out, moduleEntry(m))
		known[m.ID] = true
	}
	rows, err := q.S.DB.QueryContext(ctx, `
	  SELECT f.id, f.repo, f.module, coalesce(m.name, ''), f.name, f.arity, f.file, f.line
	    FROM functions f LEFT JOIN modules m ON m.snapshot_id = f.snapshot_id AND m.id = f.module
	   WHERE f.snapshot_id = ? ORDER BY f.module, f.line, f.id`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, repo, module, modName, name, file string
		var arity, line int
		if err := rows.Scan(&id, &repo, &module, &modName, &name, &arity, &file, &line); err != nil {
			rows.Close()
			return nil, err
		}
		full := fmt.Sprintf("%s/%d", name, arity)
		if modName != "" {
			full = modName + "." + full
		}
		parent, shard := module, module
		if !known[module] {
			parent, shard = "", id
		}
		e := obj{"id": id, "label": fmt.Sprintf("%s/%d", name, arity), "full": full, "kind": "function", "repo": repo, "shard": shard, "file": file, "line": line, "cost": 0}
		if parent != "" {
			e["parent"] = parent
		}
		out = append(out, e)
		known[id] = true
	}
	rows.Close()
	tbls, err := tables(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, t := range tbls {
		out = append(out, tableEntry(t))
		known[t.ID] = true
	}
	svcs, err := services(ctx, q, mods, tbls)
	if err != nil {
		return nil, err
	}
	out = append(out, svcs...)
	ctxs, err := contextRows(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, c := range ctxs {
		var tids []string
		for _, t := range c.Tables {
			tids = append(tids, t["id"].(string))
		}
		out = append(out, obj{
			"id": c.ID, "label": c.Name, "full": c.Name + " · " + c.Repo, "kind": "context", "repo": c.Repo, "shard": c.ID,
			"members": nonNil(c.Modules), "modules": nonNil(c.Modules), "tables": nonNil(tids), "count": len(c.Modules),
			"changes": []string{}, "evidence": []string{}, "cost": 0,
		})
		known[c.ID] = true
	}
	anns, err := annotationRows(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, a := range anns {
		var members []string
		for _, id := range a.cites() {
			if known[id] {
				members = append(members, id)
			}
		}
		out = append(out, obj{
			"id": "ann:" + a.ID, "label": a.Title, "full": a.Title + " · " + a.Kind, "kind": "annotation", "repo": "", "shard": "ann:" + a.ID,
			"annotation": a.ID, "annotation_kind": a.Kind, "status": a.Status, "summary": a.Body.Summary,
			"members": nonNil(members), "count": len(members), "changes": []string{}, "evidence": []string{}, "cost": 0,
		})
	}
	return out, nil
}

// services gives each repository (and the database, when tables exist) a
// catalog node; the overview draws them and the calls between them.
func services(ctx context.Context, q *query.Q, mods []moduleRow, tbls []tableRow) ([]obj, error) {
	rs, err := q.Repos(ctx)
	if err != nil {
		return nil, err
	}
	byRepo := map[string][]moduleRow{}
	for _, m := range mods {
		byRepo[m.Repo] = append(byRepo[m.Repo], m)
	}
	var out []obj
	for _, r := range rs {
		ms := append([]moduleRow{}, byRepo[r.Name]...)
		sort.SliceStable(ms, func(i, j int) bool {
			if ms[i].Count != ms[j].Count {
				return ms[i].Count > ms[j].Count
			}
			return ms[i].ID < ms[j].ID
		})
		var ids []string
		for i, m := range ms {
			if i == 6 {
				break
			}
			ids = append(ids, m.ID)
		}
		icon := "github"
		if len(ms) > 0 && iconFor(ms[0].Language) != "" {
			icon = iconFor(ms[0].Language)
		}
		out = append(out, obj{
			"id": "s:" + r.Name, "label": r.Name, "full": r.Name + " repository", "kind": "service", "repo": r.Name, "shard": "s:" + r.Name,
			"icon": icon, "modules": nonNil(ids), "members": nonNil(ids), "count": len(byRepo[r.Name]),
			"summary": fmt.Sprintf("%d modules at %s", len(byRepo[r.Name]), query.Short(r.Revision)),
			"changes": []string{}, "evidence": []string{}, "cost": 0,
		})
	}
	if len(tbls) > 0 {
		ts := append([]tableRow{}, tbls...)
		sort.SliceStable(ts, func(i, j int) bool {
			if ts[i].Weight != ts[j].Weight {
				return ts[i].Weight > ts[j].Weight
			}
			return ts[i].ID < ts[j].ID
		})
		var ids []string
		for i, t := range ts {
			if i == 6 {
				break
			}
			ids = append(ids, t.ID)
		}
		out = append(out, obj{
			"id": "s:database", "label": "Database", "full": "Database schema", "kind": "service", "repo": "", "shard": "s:database",
			"icon": "postgresql", "tables": ids, "members": ids, "count": len(tbls),
			"summary": fmt.Sprintf("%d tables in the snapshot schema", len(tbls)),
			"changes": []string{}, "evidence": []string{}, "cost": 0,
		})
	}
	return out, nil
}

// overview is the landing payload: provenance, the repository graph,
// contexts and (empty) proposal/evidence lists.
func overview(ctx context.Context, q *query.Q) (any, error) {
	db := q.S.DB
	var ws, created string
	db.QueryRowContext(ctx, `SELECT workspace, created_at FROM snapshots WHERE id = ?`, q.Snapshot).Scan(&ws, &created)
	rs, err := q.Repos(ctx)
	if err != nil {
		return nil, err
	}
	ri := repoIndex{}
	sources := obj{}
	for _, r := range rs {
		sources[r.Name] = r.Revision
		ri[r.Name] = r
	}
	mods, err := modules(ctx, q)
	if err != nil {
		return nil, err
	}
	tbls, err := tables(ctx, q)
	if err != nil {
		return nil, err
	}
	svcs, err := services(ctx, q, mods, tbls)
	if err != nil {
		return nil, err
	}
	// Home view: services in a row; calls between repositories and code
	// mentions of tables as edges.
	// Repositories in rows of three; the database centred below them so
	// table-mention arrows do not cross repository nodes.
	var nodes []obj
	nRepos := len(rs)
	for i := 0; i < nRepos; i++ {
		nodes = append(nodes, obj{"id": svcs[i]["id"], "x": 30 + (i%3)*310, "y": 60 + (i/3)*220})
	}
	if len(svcs) > nRepos {
		width := min(nRepos, 3)
		nodes = append(nodes, obj{"id": "s:database", "x": 30 + max(width-1, 0)*155, "y": 60 + ((nRepos+2)/3)*220})
	}
	edges := []obj{}
	rows, err := db.QueryContext(ctx, `
	  SELECT a.repo, b.repo, count(*) FROM calls c
	    JOIN functions a ON a.snapshot_id = c.snapshot_id AND a.id = c.from_id
	    JOIN functions b ON b.snapshot_id = c.snapshot_id AND b.id = c.to_id
	   WHERE c.snapshot_id = ? AND a.repo != b.repo GROUP BY 1, 2 ORDER BY 1, 2`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var from, to string
		var n int
		rows.Scan(&from, &to, &n)
		edges = append(edges, obj{"from": "s:" + from, "to": "s:" + to, "label": fmt.Sprintf("%d call site%s", n, plural(n))})
	}
	rows.Close()
	if len(tbls) > 0 {
		rows, err = db.QueryContext(ctx, `SELECT repo, count(*) FROM table_refs WHERE snapshot_id = ? GROUP BY 1 ORDER BY 1`, q.Snapshot)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var repo string
			var n int
			rows.Scan(&repo, &n)
			edges = append(edges, obj{"from": "s:" + repo, "to": "s:database", "label": fmt.Sprintf("%d table mention%s", n, plural(n))})
		}
		rows.Close()
	}
	ctxs, err := contextRows(ctx, q)
	if err != nil {
		return nil, err
	}
	var contexts []obj
	for _, c := range ctxs {
		docs, err := docsFor(ctx, q, ri, c.Modules...)
		if err != nil {
			return nil, err
		}
		contexts = append(contexts, obj{"id": c.ID, "repo": c.Repo, "name": c.Name, "modules": nonNil(c.Modules), "tables": nonNil(c.Tables), "docs": docs})
	}
	var resolved, total int
	db.QueryRowContext(ctx, `SELECT count(to_id), count(*) FROM calls WHERE snapshot_id = ?`, q.Snapshot).Scan(&resolved, &total)
	coverage := []string{
		fmt.Sprintf("%d of %d call sites resolve to a function in the map; the rest are external, dynamic or imported and are not drawn.", resolved, total),
		"Edges are labelled precise (the extractor resolved them), heuristic (matched by module and name across repositories) or candidate (textual, such as table mentions).",
		"Table mentions are textual matches in source, not proven reads or writes.",
	}
	var notes []string
	rows, err = db.QueryContext(ctx, `SELECT kind, count(*) FROM notes WHERE snapshot_id = ? GROUP BY 1 ORDER BY 1`, q.Snapshot)
	if err == nil {
		for rows.Next() {
			var k string
			var n int
			rows.Scan(&k, &n)
			notes = append(notes, fmt.Sprintf("%d %s", n, strings.ReplaceAll(k, "_", " ")))
		}
		rows.Close()
	}
	if len(notes) > 0 {
		coverage = append(coverage, "Extractor notes: "+strings.Join(notes, ", ")+".")
	}
	home := obj{
		"title": "The system, at a glance", "subtitle": fmt.Sprintf("%d repositories in workspace %s. Select one to explore its modules and data.", len(rs), ws),
		"nodes": nonNil(nodes), "edges": edges,
		"note": "Repositories and the database. Arrows count resolved cross-repository call sites and textual table mentions; dynamic and runtime paths are not inferred.",
	}
	return obj{
		"manifest": obj{
			"snapshot": q.Snapshot, "workspace": ws, "created_at": created, "sources": sources, "repos": nonNil(rs),
			"coverage": coverage, "schema_basis": "Schema facts as captured by the snapshot's schema extractor.",
		},
		"views":    obj{"home": home},
		"services": nonNil(svcs), "contexts": nonNil(contexts),
		"changes": []obj{}, "evidence": []obj{}, "catalog_count": len(mods) + len(tbls) + len(svcs),
	}, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// docsFor lists docs that mention any of ids, with pinned links.
func docsFor(ctx context.Context, q *query.Q, ri repoIndex, ids ...string) ([]obj, error) {
	out := []obj{}
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{q.Snapshot}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := q.S.DB.QueryContext(ctx, `
	  SELECT r.repo, r.doc_path, min(r.line), d.title FROM doc_refs r
	    JOIN docs d ON d.snapshot_id = r.snapshot_id AND d.repo = r.repo AND d.path = r.doc_path
	   WHERE r.snapshot_id = ? AND r.entity_id IN (`+ph+`) GROUP BY 1, 2 ORDER BY 1, 2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var repo, p, title string
		var line int
		rows.Scan(&repo, &p, &line, &title)
		out = append(out, obj{"id": "doc:" + repo + ":" + p, "repo": repo, "path": p, "title": title, "line": line, "url": ri.url(repo, p, line)})
	}
	return out, rows.Err()
}

// annotationsCiting lists annotations whose body cites id.
func annotationsCiting(ctx context.Context, q *query.Q, id string) ([]obj, error) {
	rows, err := q.S.DB.QueryContext(ctx, `SELECT id, kind, title, status FROM annotations WHERE snapshot_id = ? AND body LIKE ? ORDER BY id`, q.Snapshot, `%"`+id+`"%`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []obj{}
	for rows.Next() {
		var aid, kind, title, status string
		rows.Scan(&aid, &kind, &title, &status)
		out = append(out, obj{"id": "ann:" + aid, "annotation": aid, "kind": kind, "title": title, "status": status})
	}
	return out, rows.Err()
}

func files(ctx context.Context, q *query.Q) (any, error) {
	ri, err := repos(ctx, q)
	if err != nil {
		return nil, err
	}
	type key struct{ repo, file string }
	mods := map[key][]string{}
	fnCount := map[key]int{}
	ms, err := modules(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		k := key{m.Repo, m.File}
		mods[k] = append(mods[k], m.ID)
	}
	rows, err := q.S.DB.QueryContext(ctx, `SELECT repo, file, count(*) FROM functions WHERE snapshot_id = ? GROUP BY 1, 2`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k key
		var n int
		rows.Scan(&k.repo, &k.file, &n)
		fnCount[k] = n
	}
	rows.Close()
	byRepo := map[string][]obj{}
	rows, err = q.S.DB.QueryContext(ctx, `SELECT repo, path, language, category FROM files WHERE snapshot_id = ? ORDER BY repo, path`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var repo, p, lang, cat string
		rows.Scan(&repo, &p, &lang, &cat)
		k := key{repo, p}
		byRepo[repo] = append(byRepo[repo], obj{
			"id": "file:" + repo + ":" + p, "repo": repo, "path": p, "name": path.Base(p), "language": lang,
			"category": categoryLabel(cat), "modules": nonNil(mods[k]), "functions": fnCount[k], "url": ri.url(repo, p, 0),
		})
	}
	rows.Close()
	names := make([]string, 0, len(ri))
	for n := range ri {
		names = append(names, n)
	}
	for n := range byRepo {
		if _, ok := ri[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	repoList := []obj{}
	for _, n := range names {
		fs := nonNil(byRepo[n])
		repoList = append(repoList, obj{"id": n, "revision": ri[n].Revision, "url": ri[n].URL, "files": fs, "count": len(fs)})
	}
	return obj{"repositories": repoList, "basis": "Files the extractors reported at the pinned repository revisions. Function details exist only for files an extractor parsed."}, nil
}

func categoryLabel(c string) string {
	switch c {
	case "source":
		return "Source"
	case "test":
		return "Tests"
	case "documentation":
		return "Documentation"
	case "config", "configuration":
		return "Configuration"
	case "":
		return "Other"
	}
	return strings.ToUpper(c[:1]) + c[1:]
}

func schemaIndex(ctx context.Context, q *query.Q) (any, error) {
	tbls, err := tables(ctx, q)
	if err != nil {
		return nil, err
	}
	out := []obj{}
	for _, t := range tbls {
		e := tableEntry(t)
		cols := []string{}
		for _, c := range t.Columns {
			cols = append(cols, c.Name)
		}
		var rel int
		q.S.DB.QueryRowContext(ctx, `SELECT count(*) FROM foreign_keys WHERE snapshot_id = ? AND (from_table = ?2 OR to_table = ?2)`, q.Snapshot, t.ID).Scan(&rel)
		e["columns"] = cols
		e["relationships"] = rel
		out = append(out, e)
	}
	return out, nil
}

// entity returns the detail "shard" for an id: a module (or a function's
// module) with its functions and call edges, a table with schema and code
// references, or a context, service or annotation summary.
func entity(ctx context.Context, q *query.Q, id string) (any, error) {
	ri, err := repos(ctx, q)
	if err != nil {
		return nil, err
	}
	db := q.S.DB
	var n int
	db.QueryRowContext(ctx, `SELECT count(*) FROM modules WHERE snapshot_id = ? AND id = ?`, q.Snapshot, id).Scan(&n)
	if n > 0 {
		return moduleShard(ctx, q, ri, id, `module = ?`)
	}
	var module string
	err = db.QueryRowContext(ctx, `SELECT module FROM functions WHERE snapshot_id = ? AND id = ?`, q.Snapshot, id).Scan(&module)
	if err == nil {
		db.QueryRowContext(ctx, `SELECT count(*) FROM modules WHERE snapshot_id = ? AND id = ?`, q.Snapshot, module).Scan(&n)
		if n > 0 {
			return moduleShard(ctx, q, ri, module, `module = ?`)
		}
		return moduleShard(ctx, q, ri, id, `id = ?`)
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	db.QueryRowContext(ctx, `SELECT count(*) FROM db_tables WHERE snapshot_id = ? AND id = ?`, q.Snapshot, id).Scan(&n)
	if n > 0 {
		return tableShard(ctx, q, ri, id)
	}
	switch {
	case strings.HasPrefix(id, "ctx:"):
		ctxs, err := contextRows(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, c := range ctxs {
			if c.ID == id {
				docs, err := docsFor(ctx, q, ri, c.Modules...)
				if err != nil {
					return nil, err
				}
				return obj{"id": c.ID, "kind": "context", "repo": c.Repo, "name": c.Name, "modules": nonNil(c.Modules), "tables": nonNil(c.Tables), "docs": docs, "annotations": []obj{}}, nil
			}
		}
	case strings.HasPrefix(id, "ann:"):
		list, err := annotations(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, a := range list.([]obj) {
			if "ann:"+a["id"].(string) == id {
				a["docs"], a["annotations"] = []obj{}, []obj{}
				return a, nil
			}
		}
	case strings.HasPrefix(id, "s:"):
		mods, err := modules(ctx, q)
		if err != nil {
			return nil, err
		}
		tbls, err := tables(ctx, q)
		if err != nil {
			return nil, err
		}
		svcs, err := services(ctx, q, mods, tbls)
		if err != nil {
			return nil, err
		}
		for _, s := range svcs {
			if s["id"] == id {
				s["url"] = strings.TrimSuffix(ri[s["repo"].(string)].URL, ".git")
				s["docs"], s["annotations"] = []obj{}, []obj{}
				return s, nil
			}
		}
	}
	return nil, errNotFound{fmt.Sprintf("no entity %q in snapshot %s", id, q.Snapshot)}
}

type neighbor struct {
	ID        string `json:"id"`
	Sites     int    `json:"sites"`
	Precision string `json:"precision"`
}

func moduleShard(ctx context.Context, q *query.Q, ri repoIndex, id, where string) (any, error) {
	db := q.S.DB
	var modName, modRepo, modFile, modCtx string
	var modLine int
	db.QueryRowContext(ctx, `SELECT name, repo, file, line, context FROM modules WHERE snapshot_id = ? AND id = ?`, q.Snapshot, id).Scan(&modName, &modRepo, &modFile, &modLine, &modCtx)
	sub := `SELECT id FROM functions WHERE snapshot_id = ?1 AND ` + strings.Replace(where, "?", "?2", 1)
	rows, err := db.QueryContext(ctx, `SELECT f.id, f.repo, f.name, f.arity, f.min_arity, f.kind, f.file, f.line, f.clauses, f.fingerprint,
	    coalesce((SELECT language FROM files x WHERE x.snapshot_id = f.snapshot_id AND x.repo = f.repo AND x.path = f.file), '')
	  FROM functions f WHERE f.snapshot_id = ?1 AND f.id IN (`+sub+`) ORDER BY f.line, f.id`, q.Snapshot, id)
	if err != nil {
		return nil, err
	}
	fns := obj{}
	var order []string
	for rows.Next() {
		var fid, repo, name, kind, file, clauses, fp, lang string
		var arity, minArity, line int
		if err := rows.Scan(&fid, &repo, &name, &arity, &minArity, &kind, &file, &line, &clauses, &fp, &lang); err != nil {
			rows.Close()
			return nil, err
		}
		order = append(order, fid)
		fns[fid] = obj{
			"id": fid, "repo": repo, "module": modName, "name": name, "arity": arity, "min_arity": minArity, "kind": kind,
			"file": file, "line": line, "clauses": decodeJSON[[]int](clauses), "fingerprint": fp, "language": lang,
			"url": ri.url(repo, file, line), "in": []neighbor{}, "out": []neighbor{}, "external_or_unresolved_sites": 0,
			"tables": []string{}, "changes": []string{}, "evidence": []string{}, "cost": nil,
		}
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, `
	  SELECT from_id, to_id, count(*), group_concat(DISTINCT precision) FROM calls
	   WHERE snapshot_id = ?1 AND to_id IS NOT NULL AND (from_id IN (`+sub+`) OR to_id IN (`+sub+`))
	   GROUP BY 1, 2`, q.Snapshot, id)
	if err != nil {
		return nil, err
	}
	in, out := map[string][]neighbor{}, map[string][]neighbor{}
	for rows.Next() {
		var from, to, precs string
		var sites int
		rows.Scan(&from, &to, &sites, &precs)
		best := ""
		for _, p := range strings.Split(precs, ",") {
			if precisionRank(p) > precisionRank(best) {
				best = p
			}
		}
		if _, ok := fns[from]; ok {
			out[from] = append(out[from], neighbor{to, sites, best})
		}
		if _, ok := fns[to]; ok {
			in[to] = append(in[to], neighbor{from, sites, best})
		}
	}
	rows.Close()
	sortN := func(ns []neighbor) []neighbor {
		sort.Slice(ns, func(i, j int) bool {
			if ns[i].Sites != ns[j].Sites {
				return ns[i].Sites > ns[j].Sites
			}
			return ns[i].ID < ns[j].ID
		})
		return nonNil(ns)
	}
	for fid, f := range fns {
		f.(obj)["in"] = sortN(in[fid])
		f.(obj)["out"] = sortN(out[fid])
	}
	rows, err = db.QueryContext(ctx, `SELECT from_id, count(*) FROM calls WHERE snapshot_id = ?1 AND to_id IS NULL AND from_id IN (`+sub+`) GROUP BY 1`, q.Snapshot, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var fid string
		var c int
		rows.Scan(&fid, &c)
		if f, ok := fns[fid]; ok {
			f.(obj)["external_or_unresolved_sites"] = c
		}
	}
	rows.Close()
	tableSet := map[string]bool{}
	rows, err = db.QueryContext(ctx, `SELECT DISTINCT function_id, table_id FROM table_refs WHERE snapshot_id = ?1 AND (function_id IN (`+sub+`) OR (repo = ?3 AND file = ?4)) ORDER BY 2`, q.Snapshot, id, modRepo, modFile)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var fid, t string
		rows.Scan(&fid, &t)
		tableSet[t] = true
		if f, ok := fns[fid]; ok {
			f.(obj)["tables"] = append(f.(obj)["tables"].([]string), t)
		}
	}
	rows.Close()
	var tids []string
	for t := range tableSet {
		tids = append(tids, t)
	}
	sort.Strings(tids)
	for _, fid := range order {
		f := fns[fid].(obj)
		if f["docs"], err = docsFor(ctx, q, ri, fid); err != nil {
			return nil, err
		}
		if f["annotations"], err = annotationsCiting(ctx, q, fid); err != nil {
			return nil, err
		}
	}
	docs, err := docsFor(ctx, q, ri, id)
	if err != nil {
		return nil, err
	}
	anns, err := annotationsCiting(ctx, q, id)
	if err != nil {
		return nil, err
	}
	shard := obj{
		"id": id, "kind": "module", "name": modName, "repo": modRepo, "file": modFile, "line": modLine, "context": modCtx,
		"url": ri.url(modRepo, modFile, modLine), "functions": fns, "tables": nonNil(tids),
		"docs": docs, "annotations": anns, "changes": []string{}, "evidence": []string{},
	}
	if modName == "" { // a function outside any module
		shard["kind"] = "function"
		for _, f := range fns {
			shard["url"] = f.(obj)["url"]
		}
	}
	return shard, nil
}

func precisionRank(p string) int {
	return map[string]int{"precise": 3, "heuristic": 2, "candidate": 1}[p]
}

func tableShard(ctx context.Context, q *query.Q, ri repoIndex, id string) (any, error) {
	db := q.S.DB
	var schema, name, cols, cons, idx string
	if err := db.QueryRowContext(ctx, `SELECT schema_name, name, columns, constraints, indexes FROM db_tables WHERE snapshot_id = ? AND id = ?`, q.Snapshot, id).Scan(&schema, &name, &cols, &cons, &idx); err != nil {
		return nil, err
	}
	columns := decodeJSON[[]obj](cols)
	constraints := decodeJSON[[]obj](cons)
	indexes := []obj{}
	for _, c := range constraints {
		if c["kind"] == "primary_key" {
			indexes = append(indexes, obj{"name": c["name"], "definition": c["definition"], "primary": true, "unique": true})
		}
	}
	for _, n := range decodeJSON[[]string](idx) {
		indexes = append(indexes, obj{"name": n, "definition": n, "primary": false, "unique": strings.Contains(n, "unique")})
	}
	rels := []obj{}
	rows, err := db.QueryContext(ctx, `SELECT name, from_table, from_columns, to_table, to_columns FROM foreign_keys WHERE snapshot_id = ?1 AND (from_table = ?2 OR to_table = ?2) ORDER BY 1, 2`, q.Snapshot, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n, ft, fc, tt, tc string
		rows.Scan(&n, &ft, &fc, &tt, &tc)
		from, to := decodeJSON[[]string](fc), decodeJSON[[]string](tc)
		rels = append(rels, obj{
			"name": n, "from": ft, "to": tt, "kind": "physical_fk", "columns": nonNil(from), "target_columns": nonNil(to),
			"definition": fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(%s)", strings.Join(from, ", "), tt, strings.Join(to, ", ")),
		})
	}
	rows.Close()
	refs := []obj{}
	rows, err = db.QueryContext(ctx, `SELECT repo, file, line, function_id, text FROM table_refs WHERE snapshot_id = ? AND table_id = ? ORDER BY repo, file, line`, q.Snapshot, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var repo, file, fn, text string
		var line int
		rows.Scan(&repo, &file, &line, &fn, &text)
		var fnv any
		if fn != "" {
			fnv = fn
		}
		refs = append(refs, obj{"repo": repo, "file": file, "line": line, "function": fnv, "kind": "candidate", "precision": "candidate", "text": text, "url": ri.url(repo, file, line)})
	}
	rows.Close()
	docs, err := docsFor(ctx, q, ri, id)
	if err != nil {
		return nil, err
	}
	anns, err := annotationsCiting(ctx, q, id)
	if err != nil {
		return nil, err
	}
	domain := schema
	if domain == "" {
		domain = "tables"
	}
	return obj{
		"id": id, "kind": "table", "schema": schema, "name": name, "domain": domain,
		"columns": nonNil(columns), "constraints": nonNil(constraints), "indexes": indexes, "triggers": []obj{},
		"relationships": rels, "references": refs, "sources": []obj{}, "evidence": []string{},
		"docs": docs, "annotations": anns, "rls": false,
	}, nil
}

// graph is a layered call neighbourhood for deep links with depth > 1:
// callers get negative depths, callees positive, the entity's functions 0.
func graph(ctx context.Context, q *query.Q, ref string, depth int) (any, error) {
	if depth <= 0 {
		depth = 1
	}
	if depth > 4 {
		depth = 4
	}
	e, err := q.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	start, note, err := q.FunctionsOf(ctx, e)
	if err != nil {
		return nil, errNotFound{err.Error()}
	}
	const maxNodes = 60
	level := map[string]int{}
	var order []string
	add := func(id string, d int) {
		if _, ok := level[id]; !ok && len(order) < maxNodes {
			level[id] = d
			order = append(order, id)
		}
	}
	for _, id := range start {
		add(id, 0)
	}
	callers, err := q.Walk(ctx, start, depth, true)
	if err != nil {
		return nil, err
	}
	callees, err := q.Walk(ctx, start, depth, false)
	if err != nil {
		return nil, err
	}
	total := len(start)
	for d := 1; d <= depth; d++ {
		for _, h := range callers {
			if h.Depth == d {
				total++
				add(h.ID, -d)
			}
		}
		for _, h := range callees {
			if h.Depth == d {
				total++
				add(h.ID, d)
			}
		}
	}
	edges := []obj{}
	es, err := q.Edges(ctx, order, false)
	if err != nil {
		return nil, err
	}
	for _, ed := range es {
		if _, ok := level[ed.To]; ok && ed.From != ed.To {
			edges = append(edges, obj{"from": ed.From, "to": ed.To, "sites": ed.Sites, "precision": ed.Precision})
		}
	}
	nodes := []obj{}
	for _, id := range order {
		nodes = append(nodes, obj{"id": id, "depth": level[id]})
	}
	return obj{"id": e.ID, "kind": e.Kind, "depth": depth, "nodes": nodes, "edges": edges, "hidden": max(0, total-len(order)), "note": note}, nil
}
