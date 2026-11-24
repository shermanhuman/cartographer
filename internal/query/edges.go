package query

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Every reader that follows edges goes through edgeQuery: callers, callees,
// impact, path, the web graph, the module shard and the snapshot diff. It
// reads the store's edges view, so resolved calls (call, local, dispatch, …),
// route → handler ('route') and function → route HTTP links ('http') all
// count, each pair carrying its kinds and its best precision.

// edge is an aggregated from -> to relationship (every site of one pair).
type edge struct {
	From, To  string
	Sites     int
	Precision string   // best precision across sites
	Kinds     []string // distinct edge kinds, sorted: call, dispatch, http, route, …
	// Provenance says why an HTTP link was made (config key or deployment
	// env → service → repo), or which events a send carries to a
	// handle_event (phx-click "save", "delete"); empty for other kinds.
	Provenance string
}

// Has reports whether the edge has the kind.
func (e edge) Has(kind string) bool {
	for _, k := range e.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Notable kinds are drawn and printed: they are not a direct call.
var notable = map[string]bool{"dispatch": true, "event": true, "http": true, "route": true}

// Notable returns the edge's kinds that are not a direct call (dispatch,
// http, route), in order.
func (e edge) Notable() []string {
	var out []string
	for _, k := range e.Kinds {
		if notable[k] {
			out = append(out, k)
		}
	}
	return out
}

// edgeQuery aggregates the snapshot's edges; where filters the edges view
// (columns from_id, to_id, kind, precision; args follow the snapshot id).
func (q *Q) edgeQuery(ctx context.Context, where string, args ...any) ([]edge, error) {
	rows, err := q.S.DB.QueryContext(ctx, `
	  SELECT from_id, to_id, count(*), group_concat(DISTINCT kind), group_concat(DISTINCT precision)
	    FROM edges WHERE snapshot_id = ? AND (`+where+`)
	   GROUP BY from_id, to_id ORDER BY from_id, to_id`, append([]any{q.Snapshot}, args...)...)
	if err != nil {
		return nil, err
	}
	var out []edge
	for rows.Next() {
		var e edge
		var kinds, precs string
		if err := rows.Scan(&e.From, &e.To, &e.Sites, &kinds, &precs); err != nil {
			rows.Close()
			return nil, err
		}
		e.Kinds = splitSet(kinds)
		for _, p := range strings.Split(precs, ",") {
			if PrecisionRank(p) > PrecisionRank(e.Precision) {
				e.Precision = p
			}
		}
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		switch {
		case out[i].Has("http"):
			out[i].Provenance = q.provenance(ctx, out[i].From, out[i].To)
		case out[i].Has("event"):
			out[i].Provenance = q.eventProvenance(ctx, out[i].From, out[i].To)
		}
	}
	return out, nil
}

// provenance is the evidence of the strongest HTTP link from a function to
// a route.
func (q *Q) provenance(ctx context.Context, from, route string) string {
	var p string
	q.S.DB.QueryRowContext(ctx, `SELECT provenance FROM http_links WHERE snapshot_id = ? AND from_id = ? AND route_id = ?
	  ORDER BY CASE precision WHEN 'heuristic' THEN 0 ELSE 1 END, provenance LIMIT 1`, q.Snapshot, from, route).Scan(&p)
	return p
}

// eventProvenance names the events one function sends to a handle_event,
// grouped by how (phx-click "save", "delete"; pushEvent "signed" via phx-hook
// Pad (page.ex:12)), at most eight names.
func (q *Q) eventProvenance(ctx context.Context, from, to string) string {
	rows, err := q.S.DB.QueryContext(ctx, `SELECT DISTINCT via, provenance, event FROM event_links
	  WHERE snapshot_id = ? AND from_id = ? AND to_id = ? ORDER BY via, provenance, event`, q.Snapshot, from, to)
	if err != nil {
		return ""
	}
	defer rows.Close()
	type group struct{ via, prov string }
	var order []group
	names := map[group][]string{}
	total := 0
	for rows.Next() {
		var g group
		var ev string
		if rows.Scan(&g.via, &g.prov, &ev) != nil {
			return ""
		}
		if _, ok := names[g]; !ok {
			order = append(order, g)
		}
		total++
		if total <= 8 {
			names[g] = append(names[g], fmt.Sprintf("%q", ev))
		} else if names[g] == nil {
			names[g] = []string{}
		}
	}
	var parts []string
	for _, g := range order {
		if len(names[g]) == 0 {
			continue
		}
		p := g.via + " " + strings.Join(names[g], ", ")
		if g.prov != "" {
			p += " via " + g.prov
		}
		parts = append(parts, p)
	}
	s := strings.Join(parts, "; ")
	if total > 8 {
		s += fmt.Sprintf(" +%d more", total-8)
	}
	return s
}

// splitSet splits a group_concat list into its sorted non-empty values.
func splitSet(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v != "" {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// edges returns aggregated edges into (reverse) or out of ids.
func (q *Q) edges(ctx context.Context, ids []string, reverse bool) ([]edge, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	col := "from_id"
	if reverse {
		col = "to_id"
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	es, err := q.edgeQuery(ctx, fmt.Sprintf("%s IN (%s)", col, ph), args...)
	if err != nil {
		return nil, err
	}
	if reverse { // callers grouped by the node they reach, as before
		sort.SliceStable(es, func(i, j int) bool {
			if es[i].To != es[j].To {
				return es[i].To < es[j].To
			}
			return es[i].From < es[j].From
		})
	}
	return es, nil
}

// AllEdges returns every aggregated edge of the snapshot.
func (q *Q) AllEdges(ctx context.Context) ([]Edge, error) { return q.edgeQuery(ctx, "1") }

// RepoEdge counts the edges from one repository into another, by kind.
type RepoEdge struct {
	From, To  string
	Sites     map[string]int // edge kind -> sites
	Precision string         // best precision
}

// RepoEdges aggregates the edges that cross repositories. The caller's repo
// is the edge's own (a call, route or link belongs to its from end); the
// callee's comes from the nodes view, so routes count as well as functions.
func (q *Q) RepoEdges(ctx context.Context) ([]RepoEdge, error) {
	rows, err := q.S.DB.QueryContext(ctx, `
	  SELECT e.repo, n.repo, e.kind, count(*), group_concat(DISTINCT e.precision) FROM edges e
	    JOIN nodes n ON n.snapshot_id = e.snapshot_id AND n.id = e.to_id
	   WHERE e.snapshot_id = ? AND e.repo != n.repo GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`, q.Snapshot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoEdge
	for rows.Next() {
		var from, to, kind, precs string
		var n int
		if err := rows.Scan(&from, &to, &kind, &n, &precs); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].From != from || out[len(out)-1].To != to {
			out = append(out, RepoEdge{From: from, To: to, Sites: map[string]int{}})
		}
		re := &out[len(out)-1]
		re.Sites[kind] += n
		for _, p := range strings.Split(precs, ",") {
			if PrecisionRank(p) > PrecisionRank(re.Precision) {
				re.Precision = p
			}
		}
	}
	return out, rows.Err()
}

// ShortLabel names a node the way a chain reads: Module.fun for Elixir
// (last module segment), Type.Method or fun for Go, METHOD /path for a route.
func ShortLabel(e Entity) string {
	if e.Kind != "function" {
		return e.Label
	}
	rest := strings.TrimPrefix(e.ID, e.Repo+":")
	if i := strings.LastIndexByte(rest, '/'); i > 0 && !strings.ContainsAny(rest[i+1:], "./:") {
		rest = rest[:i] // the arity
	}
	mod := ""
	if i := strings.LastIndexByte(e.Label, '.'); i >= 0 {
		// e.Label is Module.name/arity; the module name is what precedes the
		// function name.
		mod = strings.TrimSuffix(e.Label[:i+1], ".")
	}
	if mod == "" || !strings.HasPrefix(rest, mod+".") {
		return rest
	}
	tail := rest[len(mod)+1:]
	if strings.Contains(mod, "/") || strings.Contains(tail, ".") {
		return tail // a Go package path, or a method already qualified by its type
	}
	seg := mod
	if i := strings.LastIndexAny(seg, ".:"); i >= 0 {
		seg = seg[i+1:]
	}
	return seg + "." + tail
}
