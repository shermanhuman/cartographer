package query

import (
	"context"
	"database/sql"
	"errors"
)

// RouteInfo is one HTTP route with its handler and the functions that reach
// it through HTTP links.
type RouteInfo struct {
	ID        string        `json:"id"`
	Method    string        `json:"method"` // "" for any
	Path      string        `json:"path"`
	Repo      string        `json:"repo"`
	File      string        `json:"file"`
	Line      int           `json:"line"`
	Handler   string        `json:"handler"`   // function id, "" when not named
	Precision string        `json:"precision"` // of route -> handler
	Callers   []RouteCaller `json:"callers"`
}

// RouteCaller is a function linked to a route by an HTTP reference.
type RouteCaller struct {
	Entity
	Precision  string `json:"precision"`  // heuristic | candidate (the best of its links)
	Provenance string `json:"provenance"` // why the link was made
	Sites      int    `json:"sites"`
	LinkFile   string `json:"link_file"` // where the request is built
	LinkLine   int    `json:"link_line"`
}

// Route returns a route by id, or an error when there is none.
func (q *Q) Route(ctx context.Context, id string) (*RouteInfo, error) {
	r := &RouteInfo{ID: id}
	err := q.S.DB.QueryRowContext(ctx, `SELECT method, path, repo, file, line, handler_id, precision FROM routes WHERE snapshot_id = ? AND id = ?`, q.Snapshot, id).
		Scan(&r.Method, &r.Path, &r.Repo, &r.File, &r.Line, &r.Handler, &r.Precision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ErrNotFound{Ref: id}
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.S.DB.QueryContext(ctx, `SELECT from_id, precision, provenance, file, line FROM http_links WHERE snapshot_id = ? AND route_id = ?
	  ORDER BY from_id, CASE precision WHEN 'heuristic' THEN 0 ELSE 1 END, file, line`, q.Snapshot, id)
	if err != nil {
		return nil, err
	}
	var callers []RouteCaller
	for rows.Next() {
		var c RouteCaller
		if err := rows.Scan(&c.ID, &c.Precision, &c.Provenance, &c.LinkFile, &c.LinkLine); err != nil {
			rows.Close()
			return nil, err
		}
		if n := len(callers); n > 0 && callers[n-1].ID == c.ID {
			callers[n-1].Sites++ // the first row per caller is its best link
			continue
		}
		c.Sites = 1
		callers = append(callers, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range callers {
		ent, err := q.get(ctx, callers[i].ID)
		if err != nil {
			return nil, err
		}
		if ent == nil {
			ent = &Entity{ID: callers[i].ID, Kind: "function", Label: callers[i].ID}
		}
		callers[i].Entity = *ent
	}
	r.Callers = callers
	return r, nil
}
