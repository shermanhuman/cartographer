// Package query answers map questions from the store. The CLI and the MCP
// server both call it, so agents, humans and CI see identical output.
package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/shermanhuman/cartographer/internal/ident"
	"github.com/shermanhuman/cartographer/internal/output"
	"github.com/shermanhuman/cartographer/internal/store"
)

// Changed reports whether a repository file changed since the snapshot
// (supplied by the freshness checker; nil means unknown).
type Changed func(repo, file string) bool

// Q runs queries against one snapshot.
type Q struct {
	S        *store.Store
	Snapshot string
	Changed  Changed
	// Staleness is a one-line summary prepended to every header, e.g.
	// "shop-app 3 commits behind". Empty means current or unknown.
	Staleness string
}

// New opens a query context on the newest snapshot (or the given id).
func New(ctx context.Context, s *store.Store, snapshot string) (*Q, error) {
	if snapshot == "" {
		id, err := s.Latest(ctx)
		if err != nil {
			return nil, err
		}
		snapshot = id
	}
	return &Q{S: s, Snapshot: snapshot}, nil
}

// Entity is a resolved node of any kind.
type Entity struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"` // function, module, table, context, file, doc, annotation
	Label string `json:"label"`
	Repo  string `json:"repo,omitempty"`
	File  string `json:"file,omitempty"`
	Line  int    `json:"line,omitempty"`
}

// Loc renders file:line, marked ⚠ when the file changed since the snapshot.
func (q *Q) Loc(repo, file string, line int) string {
	if file == "" {
		return ""
	}
	loc := file
	if line > 0 {
		loc = fmt.Sprintf("%s:%d", file, line)
	}
	if q.Changed != nil && q.Changed(repo, file) {
		loc += " ⚠"
	}
	return loc
}

// ErrAmbiguous carries candidates when a reference matches several entities.
type ErrAmbiguous struct {
	Ref        string
	Candidates []Entity
}

func (e *ErrAmbiguous) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d entities; use an id:", e.Ref, len(e.Candidates))
	for _, c := range e.Candidates {
		fmt.Fprintf(&b, "\n  %s %s", strings.ToUpper(c.Kind[:2]), c.ID)
	}
	return b.String()
}

// ErrNotFound carries vocabulary suggestions for a reference with no match.
type ErrNotFound struct {
	Ref         string
	Suggestions []string
}

func (e *ErrNotFound) Error() string {
	msg := fmt.Sprintf("no entity matches %q", e.Ref)
	if len(e.Suggestions) > 0 {
		msg += "; words in this map: " + strings.Join(e.Suggestions, " ")
	}
	return msg
}

func (q *Q) get(ctx context.Context, id string) (*Entity, error) {
	sid := q.Snapshot
	row := q.S.DB.QueryRowContext(ctx, `
	  SELECT f.id, 'function', coalesce(m.name || '.', '') || f.name || '/' || f.arity, f.repo, f.file, f.line
	    FROM functions f LEFT JOIN modules m ON m.snapshot_id = f.snapshot_id AND m.id = f.module
	   WHERE f.snapshot_id = ?1 AND f.id = ?2
	  UNION ALL SELECT id, 'module', name, repo, file, line FROM modules WHERE snapshot_id = ?1 AND id = ?2
	  UNION ALL SELECT id, 'table', id, '', '', 0 FROM db_tables WHERE snapshot_id = ?1 AND id = ?2
	  UNION ALL SELECT id, 'context', name, repo, '', 0 FROM contexts WHERE snapshot_id = ?1 AND id = ?2
	  UNION ALL SELECT 'file:' || repo || ':' || path, 'file', path, repo, path, 0 FROM files WHERE snapshot_id = ?1 AND 'file:' || repo || ':' || path = ?2
	  UNION ALL SELECT 'doc:' || repo || ':' || path, 'doc', title, repo, path, 0 FROM docs WHERE snapshot_id = ?1 AND 'doc:' || repo || ':' || path = ?2
	  UNION ALL SELECT 'ann:' || id, 'annotation', title, '', '', 0 FROM annotations WHERE snapshot_id = ?1 AND 'ann:' || id = ?2
	  LIMIT 1`, sid, id)
	var e Entity
	if err := row.Scan(&e.ID, &e.Kind, &e.Label, &e.Repo, &e.File, &e.Line); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// Resolve turns a user reference into one entity: an exact id, an exact
// label ("Shop.Orders.create/2", "public.orders"), a label without arity or
// repo ("Orders.create"), or a unique search hit.
func (q *Q) Resolve(ctx context.Context, ref string) (*Entity, error) {
	ref = strings.TrimSpace(ref)
	if e, err := q.get(ctx, ref); err != nil || e != nil {
		return e, err
	}
	for _, prefix := range []string{"ann:", "doc:", "file:"} {
		if !strings.HasPrefix(ref, prefix) {
			if e, err := q.get(ctx, prefix+ref); err != nil || e != nil {
				return e, err
			}
		}
	}
	// Label matches: functions by Module.name[/arity] suffix, modules by name
	// suffix, tables by name, contexts by name.
	rows, err := q.S.DB.QueryContext(ctx, `
	  SELECT f.id, 'function', coalesce(m.name || '.', '') || f.name || '/' || f.arity AS label, f.repo, f.file, f.line
	    FROM functions f LEFT JOIN modules m ON m.snapshot_id = f.snapshot_id AND m.id = f.module
	   WHERE f.snapshot_id = ?1
	     AND (label = ?2 OR label GLOB '*.' || ?2 OR label GLOB ?2 || '/*' OR label GLOB '*.' || ?2 || '/*')
	  UNION ALL SELECT id, 'module', name, repo, file, line FROM modules
	   WHERE snapshot_id = ?1 AND (name = ?2 OR name GLOB '*.' || ?2)
	  UNION ALL SELECT id, 'table', id, '', '', 0 FROM db_tables WHERE snapshot_id = ?1 AND (name = ?2)
	  UNION ALL SELECT id, 'context', name, repo, '', 0 FROM contexts WHERE snapshot_id = ?1 AND name = ?2
	  LIMIT 50`, q.Snapshot, ref)
	if err != nil {
		return nil, err
	}
	cands, err := scanEntities(rows)
	if err != nil {
		return nil, err
	}
	cands = preferModules(cands)
	// Prefer exact label matches over suffix matches.
	var exact []Entity
	for _, c := range cands {
		if c.Label == ref {
			exact = append(exact, c)
		}
	}
	if len(exact) > 0 {
		cands = exact
	}
	switch {
	case len(cands) == 1:
		return &cands[0], nil
	case len(cands) > 1:
		return nil, &ErrAmbiguous{Ref: ref, Candidates: cands}
	}
	hits, err := q.search(ctx, ident.Words(ref), 10, 0)
	if err != nil {
		return nil, err
	}
	if len(hits) == 1 {
		return &hits[0], nil
	}
	if len(hits) > 1 {
		return nil, &ErrAmbiguous{Ref: ref, Candidates: hits}
	}
	sugg, err := q.suggest(ctx, ident.Words(ref))
	if err != nil {
		return nil, err
	}
	return nil, &ErrNotFound{Ref: ref, Suggestions: sugg}
}

// preferModules drops a context whose name equals a candidate module's name:
// the module is the more specific answer and leads to its context anyway.
func preferModules(cands []Entity) []Entity {
	mods := map[string]bool{}
	for _, c := range cands {
		if c.Kind == "module" {
			mods[c.Repo+"|"+c.Label] = true
		}
	}
	out := cands[:0:0]
	for _, c := range cands {
		if c.Kind == "context" && mods[c.Repo+"|"+c.Label] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ResolveKind is Resolve, narrowed to one kind when a reference is ambiguous.
func (q *Q) ResolveKind(ctx context.Context, ref, kind string) (*Entity, error) {
	e, err := q.Resolve(ctx, ref)
	var amb *ErrAmbiguous
	if errors.As(err, &amb) {
		var keep []Entity
		for _, c := range amb.Candidates {
			if c.Kind == kind {
				keep = append(keep, c)
			}
		}
		if len(keep) == 1 {
			return &keep[0], nil
		}
		if len(keep) > 1 {
			amb.Candidates = keep
		}
	}
	return e, err
}

func scanEntities(rows *sql.Rows) ([]Entity, error) {
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.ID, &e.Kind, &e.Label, &e.Repo, &e.File, &e.Line); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// search runs an FTS query: every word must match (prefix), ranked by bm25.
func (q *Q) search(ctx context.Context, words []string, limit, offset int) ([]Entity, error) {
	if len(words) == 0 {
		return nil, nil
	}
	terms := make([]string, len(words))
	for i, w := range words {
		terms[i] = `"` + strings.ReplaceAll(w, `"`, ``) + `"*`
	}
	rows, err := q.S.DB.QueryContext(ctx, `
	  SELECT e.id, e.kind, e.label FROM entities e
	   WHERE e.snapshot_id = ? AND entities MATCH ?
	   ORDER BY bm25(entities, 0, 0, 0, 2.0, 1.0), length(e.label)
	   LIMIT ? OFFSET ?`, q.Snapshot, "words : ("+strings.Join(terms, " AND ")+")", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.ID, &e.Kind, &e.Label); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if full, err := q.get(ctx, out[i].ID); err == nil && full != nil {
			out[i] = *full
		}
	}
	return out, nil
}

// suggest returns map vocabulary words close to the query words: shared
// prefix (>=3 letters) or edit distance <= 2, most frequent first.
func (q *Q) suggest(ctx context.Context, words []string) ([]string, error) {
	rows, err := q.S.DB.QueryContext(ctx, `SELECT word, count FROM vocab WHERE snapshot_id = ? ORDER BY count DESC, word`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var w string
		var n int
		if err := rows.Scan(&w, &n); err != nil {
			return nil, err
		}
		for _, qw := range words {
			if near(qw, w) {
				out = append(out, w)
				break
			}
		}
		if len(out) == 20 {
			break
		}
	}
	return out, rows.Err()
}

func near(a, b string) bool {
	if a == b {
		return false
	}
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	if p >= 3 {
		return true
	}
	return len(a) >= 4 && levenshtein(a, b) <= 2
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// header is the first line of every result.
func (q *Q) header(ctx context.Context, d *output.Doc) {
	rows, err := q.S.DB.QueryContext(ctx, `SELECT name, revision FROM repos WHERE snapshot_id = ? ORDER BY name`, q.Snapshot)
	var repos []string
	if err == nil {
		for rows.Next() {
			var n, r string
			rows.Scan(&n, &r)
			if len(r) > 7 {
				r = r[:7]
			}
			repos = append(repos, n+"@"+r)
		}
		rows.Close()
	}
	line := "map " + q.Snapshot
	if len(repos) > 0 {
		line += " · " + strings.Join(repos, " ")
	}
	if q.Staleness != "" {
		line += " · " + q.Staleness
	}
	d.H("%s", line)
}

func abbrev(kind string) string {
	switch kind {
	case "function":
		return "FN"
	case "module":
		return "MOD"
	case "table":
		return "TBL"
	case "context":
		return "CTX"
	case "file":
		return "FILE"
	case "doc":
		return "DOC"
	case "annotation":
		return "ANN"
	}
	return strings.ToUpper(kind)
}

func (q *Q) entityLine(e Entity) string {
	loc := q.Loc(e.Repo, e.File, e.Line)
	if e.Kind == "file" || e.Kind == "doc" {
		loc = ""
	}
	s := abbrev(e.Kind) + " " + e.ID
	if loc != "" {
		s += "  " + loc
	}
	if e.Kind == "doc" || e.Kind == "annotation" {
		s += "  \"" + e.Label + "\""
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func decode[T any](raw string) T {
	var v T
	json.Unmarshal([]byte(raw), &v)
	return v
}
