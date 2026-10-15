package query

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/shermanhuman/cartographer/internal/ident"
	"github.com/shermanhuman/cartographer/internal/output"
)

// edge is an aggregated caller->callee relationship.
type edge struct {
	From, To  string
	Sites     int
	Precision string // best precision across sites
}

func better(a, b string) string {
	rank := map[string]int{"precise": 3, "heuristic": 2, "candidate": 1}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// edges returns aggregated resolved calls into (reverse) or out of ids.
func (q *Q) edges(ctx context.Context, ids []string, reverse bool) ([]edge, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	col, other := "from_id", "to_id"
	if reverse {
		col, other = "to_id", "from_id"
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{q.Snapshot}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := q.S.DB.QueryContext(ctx, fmt.Sprintf(`
	  SELECT from_id, to_id, count(*), group_concat(DISTINCT precision)
	    FROM calls WHERE snapshot_id = ? AND to_id IS NOT NULL AND %s IN (%s)
	   GROUP BY from_id, to_id ORDER BY %s, %s`, col, ph, col, other), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []edge
	for rows.Next() {
		var e edge
		var precs string
		if err := rows.Scan(&e.From, &e.To, &e.Sites, &precs); err != nil {
			return nil, err
		}
		for _, p := range strings.Split(precs, ",") {
			e.Precision = better(e.Precision, p)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type hop struct {
	Entity
	Depth     int    `json:"depth"`
	Via       string `json:"via"` // the node it calls / is called by
	Sites     int    `json:"sites"`
	Precision string `json:"precision"`
	Context   string `json:"context,omitempty"`
	Test      bool   `json:"test,omitempty"`
}

// walk does a BFS over calls from the start ids, up to depth levels.
func (q *Q) walk(ctx context.Context, start []string, depth int, reverse bool) ([]hop, error) {
	seen := map[string]bool{}
	for _, s := range start {
		seen[s] = true
	}
	frontier := start
	var out []hop
	for d := 1; d <= depth && len(frontier) > 0; d++ {
		es, err := q.edges(ctx, frontier, reverse)
		if err != nil {
			return nil, err
		}
		var next []string
		for _, e := range es {
			node, via := e.To, e.From
			if reverse {
				node, via = e.From, e.To
			}
			if seen[node] {
				continue
			}
			seen[node] = true
			ent, err := q.get(ctx, node)
			if err != nil {
				return nil, err
			}
			if ent == nil {
				continue
			}
			h := hop{Entity: *ent, Depth: d, Via: via, Sites: e.Sites, Precision: e.Precision}
			q.S.DB.QueryRowContext(ctx, `SELECT m.context FROM functions f JOIN modules m ON m.snapshot_id = f.snapshot_id AND m.id = f.module WHERE f.snapshot_id = ? AND f.id = ?`, q.Snapshot, node).Scan(&h.Context)
			var cat string
			q.S.DB.QueryRowContext(ctx, `SELECT category FROM files WHERE snapshot_id = ? AND repo = ? AND path = ?`, q.Snapshot, ent.Repo, ent.File).Scan(&cat)
			h.Test = cat == "test" || strings.HasPrefix(ent.File, "test/")
			out = append(out, h)
			next = append(next, node)
		}
		frontier = next
	}
	return out, nil
}

func (q *Q) hopLine(h hop, rootRepo string) string {
	s := fmt.Sprintf("d%d FN %s  %s  [%s]", h.Depth, h.ID, q.Loc(h.Repo, h.File, h.Line), h.Precision)
	if h.Sites > 1 {
		s += fmt.Sprintf(" ×%d", h.Sites)
	}
	if h.Depth > 1 {
		s += "  via " + h.Via
	}
	if rootRepo != "" && h.Repo != rootRepo {
		s += "  ⇄ cross-repo"
	}
	return s
}

// functionsOf expands an entity into the function ids it stands for.
func (q *Q) functionsOf(ctx context.Context, e *Entity) ([]string, string, error) {
	var query string
	switch e.Kind {
	case "function":
		return []string{e.ID}, "", nil
	case "module":
		query = `SELECT id FROM functions WHERE snapshot_id = ? AND module = ? ORDER BY id`
	case "table":
		query = `SELECT DISTINCT function_id FROM table_refs WHERE snapshot_id = ? AND table_id = ? AND function_id != '' ORDER BY 1`
	case "context":
		query = `SELECT f.id FROM functions f JOIN modules m ON m.snapshot_id = f.snapshot_id AND m.id = f.module WHERE f.snapshot_id = ? AND m.context = ? ORDER BY f.id`
	case "file":
		query = `SELECT id FROM functions WHERE snapshot_id = ? AND 'file:' || repo || ':' || file = ? ORDER BY id`
	default:
		return nil, "", fmt.Errorf("%s %s has no functions to trace", e.Kind, e.ID)
	}
	rows, err := q.S.DB.QueryContext(ctx, query, q.Snapshot, e.ID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	note := ""
	if e.Kind == "table" {
		note = "starting from functions whose source mentions the table (candidate)"
	}
	return ids, note, rows.Err()
}

// Find searches entities by words; on a miss it returns vocabulary suggestions.
func (q *Q) Find(ctx context.Context, terms string, limit, offset int) (*output.Doc, error) {
	d := &output.Doc{}
	q.header(ctx, d)
	if limit <= 0 {
		limit = 25
	}
	offset = max(offset, 0)
	words := ident.Words(terms)
	hits, err := q.search(ctx, words, limit+1, offset)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		sugg, err := q.suggest(ctx, words)
		if err != nil {
			return nil, err
		}
		d.H("no match for %q", terms)
		if len(sugg) > 0 {
			d.L("map vocabulary near your words (retry with these): %s", strings.Join(sugg, " "))
		} else {
			d.L("no similar words in this map; it may not cover this topic")
		}
		return d, nil
	}
	more := len(hits) > limit
	if more {
		hits = hits[:limit]
	}
	d.H("find %q → %d result(s), best first", terms, len(hits))
	for _, h := range hits {
		d.L("%s", q.entityLine(h))
	}
	if more {
		d.F("more results: offset=%d", offset+limit)
	}
	return d, nil
}

// Show explains one entity and its nearest neighbours.
func (q *Q) Show(ctx context.Context, ref string) (*output.Doc, error) {
	e, err := q.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	d := &output.Doc{}
	q.header(ctx, d)
	d.H("%s", q.entityLine(*e))
	switch e.Kind {
	case "function":
		var kind, clauses, fp, module string
		var arity, minArity int
		q.S.DB.QueryRowContext(ctx, `SELECT kind, arity, min_arity, clauses, fingerprint, module FROM functions WHERE snapshot_id = ? AND id = ?`, q.Snapshot, e.ID).
			Scan(&kind, &arity, &minArity, &clauses, &fp, &module)
		ar := fmt.Sprint(arity)
		if minArity != arity {
			ar = fmt.Sprintf("%d..%d", minArity, arity)
		}
		d.L("kind %s · arity %s · clauses %s · module %s", kind, ar, clauses, module)
		for _, rev := range []bool{true, false} {
			hops, err := q.walk(ctx, []string{e.ID}, 1, rev)
			if err != nil {
				return nil, err
			}
			label := "callees"
			if rev {
				label = "callers"
			}
			d.L("%s (%d):", label, len(hops))
			for i, h := range hops {
				if i == 8 {
					d.L("  …+%d more (map_%s)", len(hops)-8, label)
					break
				}
				d.L("  %s", q.hopLine(h, e.Repo))
			}
		}
		var unresolved int
		q.S.DB.QueryRowContext(ctx, `SELECT count(*) FROM calls WHERE snapshot_id = ? AND from_id = ? AND to_id IS NULL`, q.Snapshot, e.ID).Scan(&unresolved)
		if unresolved > 0 {
			d.L("unresolved/external call sites: %d (dynamic, imported or outside the map)", unresolved)
		}
		q.tableRefsOf(ctx, d, `function_id = ?`, e.ID)
	case "module":
		rows, err := q.S.DB.QueryContext(ctx, `SELECT id, line FROM functions WHERE snapshot_id = ? AND module = ? ORDER BY line`, q.Snapshot, e.ID)
		if err != nil {
			return nil, err
		}
		var fns []string
		for rows.Next() {
			var id string
			var line int
			rows.Scan(&id, &line)
			fns = append(fns, fmt.Sprintf("  FN %s :%d", id, line))
		}
		rows.Close()
		var ctxID string
		q.S.DB.QueryRowContext(ctx, `SELECT context FROM modules WHERE snapshot_id = ? AND id = ?`, q.Snapshot, e.ID).Scan(&ctxID)
		d.L("context %s · %d functions", ctxID, len(fns))
		d.Body = append(d.Body, fns...)
	case "table":
		return q.Table(ctx, e.ID)
	case "context":
		return q.Context(ctx, e.ID)
	case "annotation":
		var status, steps string
		q.S.DB.QueryRowContext(ctx, `SELECT status, steps FROM annotations WHERE snapshot_id = ? AND 'ann:' || id = ?`, q.Snapshot, e.ID).Scan(&status, &steps)
		d.L("review status: %s", status)
		for _, s := range decode[[]struct {
			ID, Status string
			Reasons    []string
		}](steps) {
			d.L("  step %s: %s %s", s.ID, s.Status, strings.Join(s.Reasons, "; "))
		}
	}
	q.docsAbout(ctx, d, e.ID)
	return d, nil
}

func (q *Q) tableRefsOf(ctx context.Context, d *output.Doc, where string, arg string) {
	rows, err := q.S.DB.QueryContext(ctx, `SELECT table_id, repo, file, line, text FROM table_refs WHERE snapshot_id = ? AND `+where+` ORDER BY table_id, file, line`, q.Snapshot, arg)
	if err != nil {
		return
	}
	defer rows.Close()
	first := true
	for rows.Next() {
		var t, repo, file, text string
		var line int
		rows.Scan(&t, &repo, &file, &line, &text)
		if first {
			d.L("table mentions [candidate]:")
			first = false
		}
		d.L("  TBL %s  %s  %q", t, q.Loc(repo, file, line), strings.TrimSpace(text))
	}
}

func (q *Q) docsAbout(ctx context.Context, d *output.Doc, id string) {
	rows, err := q.S.DB.QueryContext(ctx, `SELECT DISTINCT r.repo, r.doc_path, r.line, d.title FROM doc_refs r JOIN docs d ON d.snapshot_id = r.snapshot_id AND d.repo = r.repo AND d.path = r.doc_path WHERE r.snapshot_id = ? AND r.entity_id = ? ORDER BY 1, 2, 3`, q.Snapshot, id)
	if err == nil {
		var lines []string
		for rows.Next() {
			var repo, path, title string
			var line int
			rows.Scan(&repo, &path, &line, &title)
			lines = append(lines, fmt.Sprintf("  DOC %s:%s:%d  %q", repo, path, line, title))
		}
		rows.Close()
		if len(lines) > 0 {
			d.L("docs mentioning it:")
			d.Body = append(d.Body, lines...)
		}
	}
	// The annotation body is JSON; a citation holds the id as a JSON string.
	rows, err = q.S.DB.QueryContext(ctx, `SELECT id, title, status FROM annotations WHERE snapshot_id = ? AND instr(body, ?) > 0 ORDER BY id`, q.Snapshot, `"`+id+`"`)
	if err == nil {
		for rows.Next() {
			var aid, title, status string
			rows.Scan(&aid, &title, &status)
			d.L("  ANN ann:%s  %q  (%s)", aid, title, status)
		}
		rows.Close()
	}
}

// Calls lists callers (reverse) or callees of an entity up to depth.
func (q *Q) Calls(ctx context.Context, ref string, depth int, reverse bool, limit, offset int) (*output.Doc, error) {
	e, err := q.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	if depth <= 0 {
		depth = 1
	}
	start, note, err := q.functionsOf(ctx, e)
	if err != nil {
		return nil, err
	}
	hops, err := q.walk(ctx, start, depth, reverse)
	if err != nil {
		return nil, err
	}
	d := &output.Doc{}
	q.header(ctx, d)
	what := "callees"
	if reverse {
		what = "callers"
	}
	d.H("%s of %s (depth ≤%d): %d", what, q.entityLine(*e), depth, len(hops))
	if note != "" {
		d.H("%s", note)
	}
	sort.SliceStable(hops, func(i, j int) bool {
		if hops[i].Depth != hops[j].Depth {
			return hops[i].Depth < hops[j].Depth
		}
		return hops[i].ID < hops[j].ID
	})
	page(d, hops, limit, offset, func(h hop) string { return q.hopLine(h, e.Repo) })
	if !reverse {
		var unresolved int
		q.S.DB.QueryRowContext(ctx, `SELECT count(*) FROM calls WHERE snapshot_id = ? AND from_id = ? AND to_id IS NULL`, q.Snapshot, e.ID).Scan(&unresolved)
		if unresolved > 0 {
			d.F("+%d unresolved/external call sites from %s (not traced)", unresolved, e.ID)
		}
	}
	return d, nil
}

// page appends items[offset:offset+limit]; limit <= 0 means all, and a
// negative offset counts as 0.
func page[T any](d *output.Doc, items []T, limit, offset int, line func(T) string) {
	offset = min(max(offset, 0), len(items))
	items = items[offset:]
	more := limit > 0 && len(items) > limit
	if more {
		items = items[:limit]
	}
	for _, it := range items {
		d.Body = append(d.Body, line(it))
	}
	if more {
		d.F("more: offset=%d", offset+limit)
	}
}

// Impact is the blast radius: transitive callers grouped by repo and context,
// with tests listed separately.
func (q *Q) Impact(ctx context.Context, ref string, depth int) (*output.Doc, error) {
	e, err := q.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	if depth <= 0 {
		depth = 3
	}
	start, note, err := q.functionsOf(ctx, e)
	if err != nil {
		return nil, err
	}
	hops, err := q.walk(ctx, start, depth, true)
	if err != nil {
		return nil, err
	}
	var tests, code []hop
	repos := map[string]bool{}
	cross := 0
	for _, h := range hops {
		repos[h.Repo] = true
		if e.Repo != "" && h.Repo != e.Repo {
			cross++
		}
		if h.Test {
			tests = append(tests, h)
		} else {
			code = append(code, h)
		}
	}
	d := &output.Doc{}
	q.header(ctx, d)
	d.H("impact of %s (depth ≤%d): %d callers in %d repo(s) · %d cross-repo · %d tests", q.entityLine(*e), depth, len(hops), len(repos), cross, len(tests))
	if note != "" {
		d.H("%s", note)
	}
	sort.SliceStable(code, func(i, j int) bool {
		a, b := code[i], code[j]
		if (a.Repo == e.Repo) != (b.Repo == e.Repo) {
			return a.Repo == e.Repo
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		if a.Context != b.Context {
			return a.Context < b.Context
		}
		if a.Depth != b.Depth {
			return a.Depth < b.Depth
		}
		return a.ID < b.ID
	})
	repo, ctxName := "", ""
	for _, h := range code {
		if h.Repo != repo {
			repo, ctxName = h.Repo, ""
			suffix := ""
			if e.Repo != "" && repo != e.Repo {
				suffix = " ⇄ cross-repo"
			}
			d.L("%s%s", repo, suffix)
		}
		if h.Context != ctxName {
			ctxName = h.Context
			d.L("  %s", strings.TrimPrefix(ctxName, "ctx:"+repo+":"))
		}
		d.L("    %s", q.hopLine(h, ""))
	}
	if len(tests) > 0 {
		d.L("tests")
		for _, h := range tests {
			d.L("    %s", q.hopLine(h, ""))
		}
	}
	return d, nil
}

// Path finds the shortest call chain from a to b (or b to a).
func (q *Q) Path(ctx context.Context, from, to string) (*output.Doc, error) {
	a, err := q.Resolve(ctx, from)
	if err != nil {
		return nil, err
	}
	b, err := q.Resolve(ctx, to)
	if err != nil {
		return nil, err
	}
	d := &output.Doc{}
	q.header(ctx, d)
	for _, dir := range []struct {
		src, dst *Entity
		label    string
	}{{a, b, "calls"}, {b, a, "calls (reverse direction)"}} {
		chain, err := q.bfsPath(ctx, dir.src.ID, dir.dst.ID, 8)
		if err != nil {
			return nil, err
		}
		if chain != nil {
			d.H("path %s → %s: %d hop(s), %s", dir.src.ID, dir.dst.ID, len(chain)-1, dir.label)
			for i, id := range chain {
				ent, _ := q.get(ctx, id)
				if ent == nil {
					continue
				}
				d.L("%d %s", i, q.entityLine(*ent))
			}
			return d, nil
		}
	}
	d.H("no call path between %s and %s within 8 hops (unresolved/dynamic calls are not traced)", a.ID, b.ID)
	return d, nil
}

func (q *Q) bfsPath(ctx context.Context, src, dst string, maxDepth int) ([]string, error) {
	prev := map[string]string{src: ""}
	frontier := []string{src}
	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		es, err := q.edges(ctx, frontier, false)
		if err != nil {
			return nil, err
		}
		var next []string
		for _, e := range es {
			if _, ok := prev[e.To]; ok {
				continue
			}
			prev[e.To] = e.From
			if e.To == dst {
				var chain []string
				for n := dst; n != ""; n = prev[n] {
					chain = append([]string{n}, chain...)
				}
				return chain, nil
			}
			next = append(next, e.To)
		}
		frontier = next
	}
	return nil, nil
}

// Table shows columns, keys and the code that mentions a table.
func (q *Q) Table(ctx context.Context, ref string) (*output.Doc, error) {
	e, err := q.ResolveKind(ctx, ref, "table")
	if err != nil || e.Kind != "table" {
		// Fall back to table names containing the reference.
		rows, qerr := q.S.DB.QueryContext(ctx, `SELECT id, 'table', id, '', '', 0 FROM db_tables WHERE snapshot_id = ? AND instr(name, ?) > 0 ORDER BY length(name), id LIMIT 20`, q.Snapshot, strings.ToLower(ref))
		if qerr != nil {
			return nil, qerr
		}
		tables, qerr := scanEntities(rows)
		if qerr != nil {
			return nil, qerr
		}
		switch {
		case len(tables) == 1:
			e, err = &tables[0], nil
		case len(tables) > 1:
			return nil, &ErrAmbiguous{Ref: ref, Candidates: tables}
		case err == nil:
			return nil, fmt.Errorf("%s is a %s, not a table", e.ID, e.Kind)
		default:
			return nil, err
		}
	}
	if e.Kind != "table" {
		return nil, fmt.Errorf("%s is a %s, not a table", e.ID, e.Kind)
	}
	var cols, cons, idx string
	q.S.DB.QueryRowContext(ctx, `SELECT columns, constraints, indexes FROM db_tables WHERE snapshot_id = ? AND id = ?`, q.Snapshot, e.ID).Scan(&cols, &cons, &idx)
	d := &output.Doc{}
	q.header(ctx, d)
	d.H("TBL %s", e.ID)
	for _, c := range decode[[]struct {
		Name, Type, Default string
		Nullable            bool
	}](cols) {
		null := "not null"
		if c.Nullable {
			null = "null"
		}
		def := ""
		if c.Default != "" {
			def = " default " + c.Default
		}
		d.L("  %s %s %s%s", c.Name, c.Type, null, def)
	}
	for _, c := range decode[[]struct{ Name, Kind, Definition string }](cons) {
		d.L("  constraint %s %s %s", c.Name, c.Kind, c.Definition)
	}
	if ix := decode[[]string](idx); len(ix) > 0 {
		d.L("  indexes: %s", strings.Join(ix, ", "))
	}
	rows, err := q.S.DB.QueryContext(ctx, `SELECT name, from_table, from_columns, to_table, to_columns FROM foreign_keys WHERE snapshot_id = ? AND (from_table = ?2 OR to_table = ?2) ORDER BY 1`, q.Snapshot, e.ID)
	if err == nil {
		for rows.Next() {
			var name, ft, fc, tt, tc string
			rows.Scan(&name, &ft, &fc, &tt, &tc)
			d.L("  FK %s.%s → %s.%s (%s)", ft, strings.Join(decode[[]string](fc), ","), tt, strings.Join(decode[[]string](tc), ","), name)
		}
		rows.Close()
	}
	q.tableRefsOf(ctx, d, `table_id = ?`, e.ID)
	q.docsAbout(ctx, d, e.ID)
	return d, nil
}

// Context lists a cluster's modules, the tables its code mentions and docs.
func (q *Q) Context(ctx context.Context, ref string) (*output.Doc, error) {
	e, err := q.ResolveKind(ctx, ref, "context")
	if err != nil {
		return nil, err
	}
	if e.Kind != "context" {
		var c string
		q.S.DB.QueryRowContext(ctx, `SELECT context FROM modules WHERE snapshot_id = ? AND id = ?`, q.Snapshot, e.ID).Scan(&c)
		if c == "" {
			return nil, fmt.Errorf("%s is a %s, not a context", e.ID, e.Kind)
		}
		if e, err = q.Resolve(ctx, c); err != nil {
			return nil, err
		}
	}
	d := &output.Doc{}
	q.header(ctx, d)
	d.H("CTX %s", e.ID)
	rows, err := q.S.DB.QueryContext(ctx, `
	  SELECT m.id, m.file, m.repo, (SELECT count(*) FROM functions f WHERE f.snapshot_id = m.snapshot_id AND f.module = m.id)
	    FROM modules m WHERE m.snapshot_id = ? AND m.context = ? ORDER BY m.id`, q.Snapshot, e.ID)
	if err != nil {
		return nil, err
	}
	var modIDs []string
	for rows.Next() {
		var id, file, repo string
		var n int
		rows.Scan(&id, &file, &repo, &n)
		modIDs = append(modIDs, id)
		d.L("MOD %s  %s  %d fn", id, q.Loc(repo, file, 0), n)
	}
	rows.Close()
	rows, err = q.S.DB.QueryContext(ctx, `
	  SELECT r.table_id, count(*) FROM table_refs r JOIN functions f ON f.snapshot_id = r.snapshot_id AND f.id = r.function_id
	    JOIN modules m ON m.snapshot_id = f.snapshot_id AND m.id = f.module
	   WHERE r.snapshot_id = ? AND m.context = ? GROUP BY 1 ORDER BY 2 DESC, 1`, q.Snapshot, e.ID)
	if err == nil {
		first := true
		for rows.Next() {
			var t string
			var n int
			rows.Scan(&t, &n)
			if first {
				d.L("tables mentioned [candidate]:")
				first = false
			}
			d.L("  TBL %s ×%d", t, n)
		}
		rows.Close()
	}
	for _, id := range modIDs {
		q.docsAbout(ctx, d, id)
	}
	return d, nil
}

// Status summarises the snapshot and its coverage.
func (q *Q) Status(ctx context.Context) (*output.Doc, error) {
	d := &output.Doc{}
	q.header(ctx, d)
	var created, ws string
	q.S.DB.QueryRowContext(ctx, `SELECT created_at, workspace FROM snapshots WHERE id = ?`, q.Snapshot).Scan(&created, &ws)
	d.H("workspace %s · built %s", ws, created)
	counts := []struct{ label, table string }{
		{"modules", "modules"}, {"functions", "functions"}, {"call sites", "calls"},
		{"tables", "db_tables"}, {"docs", "docs"}, {"annotations", "annotations"},
	}
	var parts []string
	for _, c := range counts {
		var n int
		q.S.DB.QueryRowContext(ctx, `SELECT count(*) FROM `+c.table+` WHERE snapshot_id = ?`, q.Snapshot).Scan(&n)
		parts = append(parts, fmt.Sprintf("%d %s", n, c.label))
	}
	d.L("%s", strings.Join(parts, " · "))
	var resolved, total int
	q.S.DB.QueryRowContext(ctx, `SELECT count(to_id), count(*) FROM calls WHERE snapshot_id = ?`, q.Snapshot).Scan(&resolved, &total)
	if total > 0 {
		d.L("call sites resolved: %d of %d (%.0f%%); the rest are external, dynamic or imported", resolved, total, 100*float64(resolved)/float64(total))
	}
	rows, err := q.S.DB.QueryContext(ctx, `SELECT status, count(*) FROM annotations WHERE snapshot_id = ? GROUP BY 1 ORDER BY 1`, q.Snapshot)
	if err == nil {
		var st []string
		for rows.Next() {
			var s string
			var n int
			rows.Scan(&s, &n)
			st = append(st, fmt.Sprintf("%d %s", n, s))
		}
		rows.Close()
		if len(st) > 0 {
			d.L("annotations: %s", strings.Join(st, ", "))
		}
	}
	return d, nil
}
